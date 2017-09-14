// Copyright 2014 The go-phpchain Authors
// This file is part of the go-phpchain library.
//
// The go-phpchain library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-phpchain library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-phpchain library. If not, see <http://www.gnu.org/licenses/>.

// Package eth implements the PHPChain protocol.
package eth

import (
	"errors"
	"fmt"
	"math/big"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/phpchain/go-phpchain/accounts"
	"github.com/phpchain/go-phpchain/common"
	"github.com/phpchain/go-phpchain/common/hexutil"
	"github.com/phpchain/go-phpchain/consensus"
	"github.com/phpchain/go-phpchain/consensus/clique"
	"github.com/phpchain/go-phpchain/consensus/ethash"
	"github.com/phpchain/go-phpchain/core"
	"github.com/phpchain/go-phpchain/core/types"
	"github.com/phpchain/go-phpchain/core/vm"
	"github.com/phpchain/go-phpchain/eth/downloader"
	"github.com/phpchain/go-phpchain/eth/filters"
	"github.com/phpchain/go-phpchain/eth/gasprice"
	"github.com/phpchain/go-phpchain/ethdb"
	"github.com/phpchain/go-phpchain/event"
	"github.com/phpchain/go-phpchain/internal/ethapi"
	"github.com/phpchain/go-phpchain/log"
	"github.com/phpchain/go-phpchain/miner"
	"github.com/phpchain/go-phpchain/node"
	"github.com/phpchain/go-phpchain/p2p"
	"github.com/phpchain/go-phpchain/params"
	"github.com/phpchain/go-phpchain/rlp"
	"github.com/phpchain/go-phpchain/rpc"
)

type LesServer interface {
	Start(srvr *p2p.Server)
	Stop()
	Protocols() []p2p.Protocol
}

// PHPChain implements the PHPChain full node service.
type PHPChain struct {
	chainConfig *params.ChainConfig
	// Channel for shutting down the service
	shutdownChan  chan bool // Channel for shutting down the phpchain
	stopDbUpgrade func()    // stop chain db sequential key upgrade
	// Handlers
	txPool          *core.TxPool
	blockchain      *core.BlockChain
	protocolManager *ProtocolManager
	lesServer       LesServer
	// DB interfaces
	chainDb ethdb.Database // Block chain database

	eventMux       *event.TypeMux
	engine         consensus.Engine
	accountManager *accounts.Manager

	ApiBackend *EthApiBackend

	miner     *miner.Miner
	gasPrice  *big.Int
	phpbase common.Address

	networkId     uint64
	netRPCService *ethapi.PublicNetAPI

	lock sync.RWMutex // Protects the variadic fields (e.g. gas price and phpbase)
}

func (s *PHPChain) AddLesServer(ls LesServer) {
	s.lesServer = ls
}

// New creates a new PHPChain object (including the
// initialisation of the common PHPChain object)
func New(ctx *node.ServiceContext, config *Config) (*PHPChain, error) {
	if config.SyncMode == downloader.LightSync {
		return nil, errors.New("can't run eth.PHPChain in light sync mode, use les.LightPHPChain")
	}
	if !config.SyncMode.IsValid() {
		return nil, fmt.Errorf("invalid sync mode %d", config.SyncMode)
	}

	chainDb, err := CreateDB(ctx, config, "chaindata")
	if err != nil {
		return nil, err
	}
	stopDbUpgrade := upgradeSequentialKeys(chainDb)
	chainConfig, genesisHash, genesisErr := core.SetupGenesisBlock(chainDb, config.Genesis)
	if _, ok := genesisErr.(*params.ConfigCompatError); genesisErr != nil && !ok {
		return nil, genesisErr
	}
	log.Info("Initialised chain configuration", "config", chainConfig)

	eth := &PHPChain{
		chainDb:        chainDb,
		chainConfig:    chainConfig,
		eventMux:       ctx.EventMux,
		accountManager: ctx.AccountManager,
		engine:         CreateConsensusEngine(ctx, config, chainConfig, chainDb),
		shutdownChan:   make(chan bool),
		stopDbUpgrade:  stopDbUpgrade,
		networkId:      config.NetworkId,
		gasPrice:       config.GasPrice,
		phpbase:      config.PHPbase,
	}

	if err := addMipmapBloomBins(chainDb); err != nil {
		return nil, err
	}
	log.Info("Initialising PHPChain protocol", "versions", ProtocolVersions, "network", config.NetworkId)

	if !config.SkipBcVersionCheck {
		bcVersion := core.GetBlockChainVersion(chainDb)
		if bcVersion != core.BlockChainVersion && bcVersion != 0 {
			return nil, fmt.Errorf("Blockchain DB version mismatch (%d / %d). Run geth upgradedb.\n", bcVersion, core.BlockChainVersion)
		}
		core.WriteBlockChainVersion(chainDb, core.BlockChainVersion)
	}

	vmConfig := vm.Config{EnablePreimageRecording: config.EnablePreimageRecording}
	eth.blockchain, err = core.NewBlockChain(chainDb, eth.chainConfig, eth.engine, eth.eventMux, vmConfig)
	if err != nil {
		return nil, err
	}
	// Rewind the chain in case of an incompatible config upgrade.
	if compat, ok := genesisErr.(*params.ConfigCompatError); ok {
		log.Warn("Rewinding chain to upgrade configuration", "err", compat)
		eth.blockchain.SetHead(compat.RewindTo)
		core.WriteChainConfig(chainDb, genesisHash, chainConfig)
	}

	newPool := core.NewTxPool(config.TxPool, eth.chainConfig, eth.EventMux(), eth.blockchain.State, eth.blockchain.GasLimit)
	eth.txPool = newPool

	maxPeers := config.MaxPeers
	if config.LightServ > 0 {
		// if we are running a light server, limit the number of ETH peers so that we reserve some space for incoming LES connections
		// temporary solution until the new peer connectivity API is finished
		halfPeers := maxPeers / 2
		maxPeers -= config.LightPeers
		if maxPeers < halfPeers {
			maxPeers = halfPeers
		}
	}

	if eth.protocolManager, err = NewProtocolManager(eth.chainConfig, config.SyncMode, config.NetworkId, maxPeers, eth.eventMux, eth.txPool, eth.engine, eth.blockchain, chainDb); err != nil {
		return nil, err
	}

	eth.miner = miner.New(eth, eth.chainConfig, eth.EventMux(), eth.engine)
	eth.miner.SetExtra(makeExtraData(config.ExtraData))

	eth.ApiBackend = &EthApiBackend{eth, nil}
	gpoParams := config.GPO
	if gpoParams.Default == nil {
		gpoParams.Default = config.GasPrice
	}
	eth.ApiBackend.gpo = gasprice.NewOracle(eth.ApiBackend, gpoParams)

	return eth, nil
}

func makeExtraData(extra []byte) []byte {
	if len(extra) == 0 {
		// create default extradata
		extra, _ = rlp.EncodeToBytes([]interface{}{
			uint(params.VersionMajor<<16 | params.VersionMinor<<8 | params.VersionPatch),
			"geth",
			runtime.Version(),
			runtime.GOOS,
		})
	}
	if uint64(len(extra)) > params.MaximumExtraDataSize {
		log.Warn("Miner extra data exceed limit", "extra", hexutil.Bytes(extra), "limit", params.MaximumExtraDataSize)
		extra = nil
	}
	return extra
}

// CreateDB creates the chain database.
func CreateDB(ctx *node.ServiceContext, config *Config, name string) (ethdb.Database, error) {
	db, err := ctx.OpenDatabase(name, config.DatabaseCache, config.DatabaseHandles)
	if err != nil {
		return nil, err
	}
	if db, ok := db.(*ethdb.LDBDatabase); ok {
		db.Meter("eth/db/chaindata/")
	}
	return db, nil
}

// CreateConsensusEngine creates the required type of consensus engine instance for an PHPChain service
func CreateConsensusEngine(ctx *node.ServiceContext, config *Config, chainConfig *params.ChainConfig, db ethdb.Database) consensus.Engine {
	// If proof-of-authority is requested, set it up
	if chainConfig.Clique != nil {
		return clique.New(chainConfig.Clique, db)
	}
	// Otherwise assume proof-of-work
	switch {
	case config.PowFake:
		log.Warn("Ethash used in fake mode")
		return ethash.NewFaker()
	case config.PowTest:
		log.Warn("Ethash used in test mode")
		return ethash.NewTester()
	case config.PowShared:
		log.Warn("Ethash used in shared mode")
		return ethash.NewShared()
	default:
		engine := ethash.New(ctx.ResolvePath(config.EthashCacheDir), config.EthashCachesInMem, config.EthashCachesOnDisk,
			config.EthashDatasetDir, config.EthashDatasetsInMem, config.EthashDatasetsOnDisk)
		engine.SetThreads(-1) // Disable CPU mining
		return engine
	}
}

// APIs returns the collection of RPC services the phpchain package offers.
// NOTE, some of these services probably need to be moved to somewhere else.
func (s *PHPChain) APIs() []rpc.API {
	apis := ethapi.GetAPIs(s.ApiBackend)

	// Append any APIs exposed explicitly by the consensus engine
	apis = append(apis, s.engine.APIs(s.BlockChain())...)

	// Append all the local APIs and return
	return append(apis, []rpc.API{
		{
			Namespace: "eth",
			Version:   "1.0",
			Service:   NewPublicPHPChainAPI(s),
			Public:    true,
		}, {
			Namespace: "eth",
			Version:   "1.0",
			Service:   NewPublicMinerAPI(s),
			Public:    true,
		}, {
			Namespace: "eth",
			Version:   "1.0",
			Service:   downloader.NewPublicDownloaderAPI(s.protocolManager.downloader, s.eventMux),
			Public:    true,
		}, {
			Namespace: "miner",
			Version:   "1.0",
			Service:   NewPrivateMinerAPI(s),
			Public:    false,
		}, {
			Namespace: "eth",
			Version:   "1.0",
			Service:   filters.NewPublicFilterAPI(s.ApiBackend, false),
			Public:    true,
		}, {
			Namespace: "admin",
			Version:   "1.0",
			Service:   NewPrivateAdminAPI(s),
		}, {
			Namespace: "debug",
			Version:   "1.0",
			Service:   NewPublicDebugAPI(s),
			Public:    true,
		}, {
			Namespace: "debug",
			Version:   "1.0",
			Service:   NewPrivateDebugAPI(s.chainConfig, s),
		}, {
			Namespace: "net",
			Version:   "1.0",
			Service:   s.netRPCService,
			Public:    true,
		},
	}...)
}

func (s *PHPChain) ResetWithGenesisBlock(gb *types.Block) {
	s.blockchain.ResetWithGenesisBlock(gb)
}

func (s *PHPChain) PHPbase() (eb common.Address, err error) {
	s.lock.RLock()
	phpbase := s.phpbase
	s.lock.RUnlock()

	if phpbase != (common.Address{}) {
		return phpbase, nil
	}
	if wallets := s.AccountManager().Wallets(); len(wallets) > 0 {
		if accounts := wallets[0].Accounts(); len(accounts) > 0 {
			return accounts[0].Address, nil
		}
	}
	return common.Address{}, fmt.Errorf("phpbase address must be explicitly specified")
}

// set in js console via admin interface or wrapper from cli flags
func (self *PHPChain) SetPHPbase(phpbase common.Address) {
	self.lock.Lock()
	self.phpbase = phpbase
	self.lock.Unlock()

	self.miner.SetPHPbase(phpbase)
}

func (s *PHPChain) StartMining(local bool) error {
	eb, err := s.PHPbase()
	if err != nil {
		log.Error("Cannot start mining without phpbase", "err", err)
		return fmt.Errorf("phpbase missing: %v", err)
	}
	if clique, ok := s.engine.(*clique.Clique); ok {
		wallet, err := s.accountManager.Find(accounts.Account{Address: eb})
		if wallet == nil || err != nil {
			log.Error("PHPbase account unavailable locally", "err", err)
			return fmt.Errorf("singer missing: %v", err)
		}
		clique.Authorize(eb, wallet.SignHash)
	}
	if local {
		// If local (CPU) mining is started, we can disable the transaction rejection
		// mechanism introduced to speed sync times. CPU mining on mainnet is ludicrous
		// so noone will ever hit this path, whereas marking sync done on CPU mining
		// will ensure that private networks work in single miner mode too.
		atomic.StoreUint32(&s.protocolManager.acceptTxs, 1)
	}
	go s.miner.Start(eb)
	return nil
}

func (s *PHPChain) StopMining()         { s.miner.Stop() }
func (s *PHPChain) IsMining() bool      { return s.miner.Mining() }
func (s *PHPChain) Miner() *miner.Miner { return s.miner }

func (s *PHPChain) AccountManager() *accounts.Manager  { return s.accountManager }
func (s *PHPChain) BlockChain() *core.BlockChain       { return s.blockchain }
func (s *PHPChain) TxPool() *core.TxPool               { return s.txPool }
func (s *PHPChain) EventMux() *event.TypeMux           { return s.eventMux }
func (s *PHPChain) Engine() consensus.Engine           { return s.engine }
func (s *PHPChain) ChainDb() ethdb.Database            { return s.chainDb }
func (s *PHPChain) IsListening() bool                  { return true } // Always listening
func (s *PHPChain) EthVersion() int                    { return int(s.protocolManager.SubProtocols[0].Version) }
func (s *PHPChain) NetVersion() uint64                 { return s.networkId }
func (s *PHPChain) Downloader() *downloader.Downloader { return s.protocolManager.downloader }

// Protocols implements node.Service, returning all the currently configured
// network protocols to start.
func (s *PHPChain) Protocols() []p2p.Protocol {
	if s.lesServer == nil {
		return s.protocolManager.SubProtocols
	} else {
		return append(s.protocolManager.SubProtocols, s.lesServer.Protocols()...)
	}
}

// Start implements node.Service, starting all internal goroutines needed by the
// PHPChain protocol implementation.
func (s *PHPChain) Start(srvr *p2p.Server) error {
	s.netRPCService = ethapi.NewPublicNetAPI(srvr, s.NetVersion())

	s.protocolManager.Start()
	if s.lesServer != nil {
		s.lesServer.Start(srvr)
	}
	return nil
}

// Stop implements node.Service, terminating all internal goroutines used by the
// PHPChain protocol.
func (s *PHPChain) Stop() error {
	if s.stopDbUpgrade != nil {
		s.stopDbUpgrade()
	}
	s.blockchain.Stop()
	s.protocolManager.Stop()
	if s.lesServer != nil {
		s.lesServer.Stop()
	}
	s.txPool.Stop()
	s.miner.Stop()
	s.eventMux.Stop()

	s.chainDb.Close()
	close(s.shutdownChan)

	return nil
}

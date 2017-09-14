// Copyright 2016 The go-phpchain Authors
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

package ethclient

import "github.com/phpchain/go-phpchain"

// Verify that Client implements the phpchain interfaces.
var (
	_ = phpchain.ChainReader(&Client{})
	_ = phpchain.TransactionReader(&Client{})
	_ = phpchain.ChainStateReader(&Client{})
	_ = phpchain.ChainSyncReader(&Client{})
	_ = phpchain.ContractCaller(&Client{})
	_ = phpchain.GasEstimator(&Client{})
	_ = phpchain.GasPricer(&Client{})
	_ = phpchain.LogFilterer(&Client{})
	_ = phpchain.PendingStateReader(&Client{})
	// _ = phpchain.PendingStateEventer(&Client{})
	_ = phpchain.PendingContractCaller(&Client{})
)

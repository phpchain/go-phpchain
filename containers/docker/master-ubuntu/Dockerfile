FROM ubuntu:xenial

RUN \
  apt-get update && apt-get upgrade -q -y && \
  apt-get install -y --no-install-recommends golang git make gcc libc-dev ca-certificates && \
  git clone --depth 1 --branch release/1.5 https://github.com/phpchain/go-phpchain && \
  (cd go-phpchain && make geth) && \
  cp go-phpchain/build/bin/geth /geth && \
  apt-get remove -y golang git make gcc libc-dev && apt autoremove -y && apt-get clean && \
  rm -rf /go-phpchain

EXPOSE 8484
EXPOSE 39595

ENTRYPOINT ["/geth"]

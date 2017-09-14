FROM alpine:3.5

RUN \
  apk add --update go git make gcc musl-dev linux-headers ca-certificates && \
  git clone --depth 1 --branch release/1.6 https://github.com/phpchain/go-phpchain && \
  (cd go-phpchain && make geth) && \
  cp go-phpchain/build/bin/geth /geth && \
  apk del go git make gcc musl-dev linux-headers && \
  rm -rf /go-phpchain && rm -rf /var/cache/apk/*

EXPOSE 8484
EXPOSE 39595

ENTRYPOINT ["/geth"]

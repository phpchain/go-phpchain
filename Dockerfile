FROM alpine:3.5

ADD . /go-phpchain
RUN \
  apk add --update git go make gcc musl-dev linux-headers && \
  (cd go-phpchain && make geth)                           && \
  cp go-phpchain/build/bin/geth /usr/local/bin/           && \
  apk del git go make gcc musl-dev linux-headers          && \
  rm -rf /go-phpchain && rm -rf /var/cache/apk/*

EXPOSE 8484
EXPOSE 38484
EXPOSE 38484/udp

ENTRYPOINT ["geth"]

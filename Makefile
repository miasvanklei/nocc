RELEASE = v2.0.0
BUILD_COMMIT := $(shell git rev-parse --short HEAD)
DATE := $(shell date -u '+%F %X UTC')
VERSION := ${RELEASE}, rev ${BUILD_COMMIT}, compiled at ${DATE}
GOPATH := $(shell go env GOPATH)

PREFIX ?= ${DESTDIR}/usr/local
ETCDIR ?= ${DESTDIR}/etc

.EXPORT_ALL_VARIABLES:
PATH := ${PATH}:${GOPATH}/bin

all: protogen client server

define build_client
	go build -o $(1)/nocc -trimpath -ldflags '-s -w -X "nocc/internal/common.version=${VERSION}"' cmd/nocc/main.go
endef

define build_daemon
    go build -o $(1)/nocc-daemon -trimpath -ldflags '-s -w -X "nocc/internal/common.version=${VERSION}"' cmd/nocc-daemon/*.go
endef

define build_server
	go build -o $(1)/nocc-server -trimpath -ldflags '-s -w -X "nocc/internal/common.version=${VERSION}"' cmd/nocc-server/*.go
endef

.PHONY: protogen
protogen:
	protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative pb/nocc-protobuf.proto

.PHONY: lint
lint:
	golangci-lint run

.PHONY: client
client: protogen
	$(call build_daemon,bin)
	$(call build_client,bin)

.PHONY: server
server: protogen
	$(call build_server,bin)

.PHONY: install
install: install.bin install.systemd install.config

.PHONY: install.systemd
install.systemd:
	install -D -m 644 data/nocc-daemon.service $(PREFIX)/lib/systemd/system/nocc-daemon.service
	install -D -m 644 data/nocc-server.service $(PREFIX)/lib/systemd/system/nocc-server.service
	install -D -m 644 data/nocc-daemon.socket $(PREFIX)/lib/systemd/system/nocc-daemon.socket

.PHONY: install.bin
install.bin:
	install -D -m 755 bin/nocc $(PREFIX)/bin/nocc
	install -D -m 755 bin/nocc-daemon $(PREFIX)/bin/nocc-daemon
	install -D -m 755 bin/nocc-server $(PREFIX)/bin/nocc-server

.PHONY: install.config
install.config:
	install -D -m 644 data/nocc-daemon.conf.example $(ETCDIR)/nocc/daemon.conf.example
	install -D -m 644 data/nocc-server.conf.example $(ETCDIR)/nocc/server.conf.example

clean:
	rm -f bin/nocc bin/nocc-daemon bin/nocc-server

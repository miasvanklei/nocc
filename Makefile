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
    go build -o $(1)/nocc-daemon -trimpath -ldflags '-s -w -X "nocc/internal/common.version=${VERSION}"' cmd/nocc-daemon/main.go
endef

define build_server
	go build -o $(1)/nocc-server -trimpath -ldflags '-s -w -X "nocc/internal/common.version=${VERSION}"' cmd/nocc-server/main.go
endef

.PHONY: protogen
protogen:
	protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative pb/nocc-protobuf.proto

.PHONY: lint
lint:
	golangci-lint run

.PHONY: client
client:
	$(call build_daemon,bin)
	$(call build_client,bin)

.PHONY: server
server:
	$(call build_server,bin)

.PHONY: install
install: install.bin install.systemd

.PHONY: install.systemd
install.systemd:
	install -D -m 644 data/nocc-daemon.service $(PREFIX)/lib/systemd/nocc-daemon.service
	install -D -m 644 data/nocc-server.service $(PREFIX)/lib/systemd/nocc-server.service
	install -D -m 644 data/nocc-daemon.socket $(PREFIX)/lib/systemd/nocc-daemon.socket

.PHONY: install.bin
install.bin:
	install -D -m 755 bin/nocc $(PREFIX)/usr/lib/nocc/bin
	install -D -m 755 bin/nocc-daemon $(PREFIX)/usr/lib/nocc/bin
	install -D -m 755 bin/nocc-server $(PREFIX)/usr/lib/nocc/bin

clean:
	rm -f bin/nocc bin/nocc-daemon bin/nocc-server

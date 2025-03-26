RELEASE = v2.0.0
BUILD_COMMIT := $(shell git rev-parse --short HEAD)
DATE := $(shell date -u '+%F %X UTC')
VERSION := ${RELEASE}, rev ${BUILD_COMMIT}, compiled at ${DATE}
GOPATH := $(shell go env GOPATH)

.EXPORT_ALL_VARIABLES:
PATH := ${PATH}:${GOPATH}/bin

define build_client
	go build -o $(1)/nocc -trimpath -ldflags '-s -w -X "nocc/internal/common.version=${VERSION}"' cmd/nocc/main.go
endef

define build_daemon
    go build -o $(1)/nocc-daemon -trimpath -ldflags '-s -w -X "nocc/internal/common.version=${VERSION}"' cmd/nocc-daemon/main.go
endef

define build_server
	go build -o $(1)/nocc-server -trimpath -ldflags '-s -w -X "nocc/internal/common.version=${VERSION}"' cmd/nocc-server/main.go
endef

protogen:
	protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative pb/nocc-protobuf.proto

lint:
	golangci-lint run

client:
	$(call build_daemon,bin)
	$(call build_client,bin)

server:
	$(call build_server,bin)

.DEFAULT_GOAL := all
all: protogen lint client server
.PHONY : all

clean:
	rm -f bin/nocc bin/nocc-daemon bin/nocc-server

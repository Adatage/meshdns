.PHONY: build build-server build-cli run-server proto proto-tools deps test clean lint help

GO_BIN := $(shell go env GOBIN)
ifeq ($(GO_BIN),)
    GO_BIN := $(shell go env GOPATH)/bin
endif

PROTOC      := $(HOME)/.local/bin/protoc
PROTO_INC   := $(HOME)/.local/include
PROTO_SRC   := api/proto
PROTO_OUT   := pkg/proto
PROTO_FILES := $(shell find $(PROTO_SRC) -name "*.proto")

BIN_DIR     := bin
SERVER_BIN  := $(BIN_DIR)/meshdns
CLI_BIN     := $(BIN_DIR)/dnsctl

ifneq (,$(wildcard .env))
    include .env
    export
endif

build: build-server build-cli

build-server:
	@mkdir -p $(BIN_DIR)
	go build -o $(SERVER_BIN) ./cmd/server

build-cli:
	@mkdir -p $(BIN_DIR)
	go build -o $(CLI_BIN) ./cmd/cli

run-server:
	go run ./cmd/server

proto: $(PROTO_FILES)
	@mkdir -p $(PROTO_OUT)
	$(PROTOC) \
		-I $(PROTO_SRC) \
		-I $(PROTO_INC) \
		--plugin=protoc-gen-go=$(GO_BIN)/protoc-gen-go \
		--plugin=protoc-gen-go-grpc=$(GO_BIN)/protoc-gen-go-grpc \
		--go_out=$(PROTO_OUT)      --go_opt=paths=source_relative \
		--go-grpc_out=$(PROTO_OUT) --go-grpc_opt=paths=source_relative \
		$(PROTO_FILES)

proto-tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

deps:
	go mod download
	go mod tidy

test:
	go test -v ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf $(BIN_DIR)
	go clean

help:
	@grep -E '^[a-zA-Z_-]+:' Makefile | grep -v '^\.' | sed 's/:.*//' | sort
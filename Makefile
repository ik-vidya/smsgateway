BINARY      := smsgateway
PKG         := ./cmd/$(BINARY)
BIN_DIR     := bin
GOFLAGS     := -trimpath
LDFLAGS     := -s -w
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_TIME  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     += -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)

.PHONY: all build run test test-race vet fmt tidy lint clean dist migrate diagnostics help

all: build

build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) $(PKG)

run: build
	./$(BIN_DIR)/$(BINARY) -config .env

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

tidy:
	go mod tidy

lint: vet
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || \
	    echo "golangci-lint not installed, skipping"

migrate: build
	./$(BIN_DIR)/$(BINARY) -config .env -migrate

diagnostics: build
	./$(BIN_DIR)/$(BINARY) -config .env -diagnostics

clean:
	rm -rf $(BIN_DIR)

dist: clean
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY)-linux-amd64   $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY)-linux-arm64   $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm   GOARM=7 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY)-linux-armv7 $(PKG)

help:
	@echo "Targets: build run test test-race vet fmt tidy lint migrate diagnostics clean dist"

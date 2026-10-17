BINARY      := smsgateway
PKG         := ./cmd/$(BINARY)
BIN_DIR     := bin
GOFLAGS     := -trimpath
LDFLAGS     := -s -w
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_TIME  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     += -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)

.PHONY: all build run test test-race vet fmt tidy lint clean dist \
        fetch ci migrate diagnostics help

all: build

# build: tidy, fmt, vet выполняются как пререквизиты.
build: tidy fmt vet
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) $(PKG)

run: build
	./$(BIN_DIR)/$(BINARY) -config .env

test:
	go test ./... -timeout 60s

test-race:
	go test -race -timeout 120s \
		./internal/logging/... \
		./internal/webui/... \
		./internal/api/... \
		./internal/modem/... \
		./internal/service/... \
		./internal/storage/...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

tidy:
	go mod tidy

lint: vet
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || \
	    echo "golangci-lint not installed, skipping"

# ci: полный прогон, как в GitHub Actions.
ci: tidy fmt vet
	@UNFORMATTED=$$(gofmt -l cmd/ internal/ || true); \
	if [ -n "$$UNFORMATTED" ]; then \
		echo "gofmt found unformatted files:"; echo "$$UNFORMATTED"; exit 1; \
	fi
	@$(MAKE) --no-print-directory vendor-check
	go test ./... -timeout 60s
	@$(MAKE) --no-print-directory test-race
	CGO_ENABLED=0 go build ./...

# vendor-check: убедиться, что вендорные ассеты на месте.
# Если нет — вызвать make fetch.
vendor-check:
	@missing=0; \
	for f in \
		internal/webui/static/vendor/bootstrap/bootstrap.min.css \
		internal/webui/static/vendor/bootstrap/bootstrap.bundle.min.js \
		internal/webui/static/vendor/htmx/htmx.min.js; do \
		if [ ! -s "$$f" ]; then \
			echo "missing or empty: $$f"; missing=1; \
		fi; \
	done; \
	if [ "$$missing" = "1" ]; then \
		echo "Fetching vendor assets..."; \
		$(MAKE) --no-print-directory fetch; \
	fi

# fetch: скачать вендорные Bootstrap 5.2.3 и htmx 4.0.0 в
# internal/webui/static/vendor/. Требуется curl или wget.
fetch:
	@mkdir -p internal/webui/static/vendor/bootstrap
	@mkdir -p internal/webui/static/vendor/htmx
	@echo "Fetching Bootstrap 5.2.3..."
	@curl -fsSL -o internal/webui/static/vendor/bootstrap/bootstrap.min.css \
	    https://cdn.jsdelivr.net/npm/bootstrap@5.2.3/dist/css/bootstrap.min.css
	@curl -fsSL -o internal/webui/static/vendor/bootstrap/bootstrap.bundle.min.js \
	    https://cdn.jsdelivr.net/npm/bootstrap@5.2.3/dist/js/bootstrap.bundle.min.js
	@echo "Fetching htmx 4.0.0..."
	@curl -fsSL -o internal/webui/static/vendor/htmx/htmx.min.js \
	    https://cdn.jsdelivr.net/npm/htmx.org@4.0.0/dist/htmx.min.js
	@echo "Done. Files:"
	@ls -la internal/webui/static/vendor/bootstrap/ internal/webui/static/vendor/htmx/

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
	@echo "Targets: build run test test-race vet fmt tidy lint fetch ci migrate diagnostics clean dist"

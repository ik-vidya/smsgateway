BINARY      := smsgateway
PKG         := ./cmd/$(BINARY)
BIN_DIR     := bin
GOFLAGS     := -trimpath
LDFLAGS     := -s -w
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_TIME  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     += -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)

# Параметры для деплоя
DEPLOY_HOST  := rsdt@rsmodule-tallin1.nebula
DEPLOY_BIN   := /home/rsmodule-common/bin
DEPLOY_UNIT  := smsgateway@smsgw-akawinsrv.service
DEPLOY_KEEP  := 5

.PHONY: all build run test test-race vet fmt tidy lint clean dist \
	fetch ci migrate diagnostics help deploy rollback rollback-to bump

all: build

# build: tidy, fmt, vet выполняются как пререквизиты.
build: tidy fmt vet
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) $(PKG)

run: build
	sudo ./$(BIN_DIR)/$(BINARY) --config .env

test:
	go test ./... --timeout 60s

test-race:
	go test --race --timeout 120s \
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
	go test ./... --timeout 60s
	@$(MAKE) --no-print-directory test-race
	CGO_ENABLED=0 go build ./...

# vendor-check: убедиться, что вендорные ассеты на месте.
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

# fetch: скачать вендорные Bootstrap 5.2.3 и htmx 4.0.0.
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

# ==================== DEPLOY ====================

deploy: build
	@echo "=== Деплой на $(DEPLOY_HOST) ==="
	@set -e ; \
	TS=$$(date +%Y%m%d-%H%M%S) ; \
	echo "  [1/6] rsync бинарника" ; \
	rsync -av $(BIN_DIR)/smsgateway $(DEPLOY_HOST):$(DEPLOY_BIN)/smsgateway.new ; \
	echo "  [2/6] бэкап старого бинарника" ; \
	ssh $(DEPLOY_HOST) "cp $(DEPLOY_BIN)/smsgateway $(DEPLOY_BIN)/smsgateway.$$TS 2>/dev/null || true" ; \
	echo "  [3/6] остановка сервиса" ; \
	ssh $(DEPLOY_HOST) "sudo systemctl stop $(DEPLOY_UNIT) || true" ; \
	echo "  [4/6] активация нового бинарника" ; \
	ssh $(DEPLOY_HOST) "mv $(DEPLOY_BIN)/smsgateway.new $(DEPLOY_BIN)/smsgateway" ; \
	echo "  [5/6] запуск сервиса" ; \
	ssh $(DEPLOY_HOST) "sudo systemctl start $(DEPLOY_UNIT)" ; \
	echo "  [6/6] проверка через 2с" ; \
	sleep 2 ; \
	if ! ssh $(DEPLOY_HOST) "sudo systemctl is-active --quiet $(DEPLOY_UNIT)" ; then \
	    echo "!!! Сервис не запустился, откат" ; \
	    ssh $(DEPLOY_HOST) "mv $(DEPLOY_BIN)/smsgateway $(DEPLOY_BIN)/smsgateway.failed && cp $(DEPLOY_BIN)/smsgateway.$$TS $(DEPLOY_BIN)/smsgateway" ; \
	    ssh $(DEPLOY_HOST) "sudo systemctl start $(DEPLOY_UNIT)" ; \
	    exit 1 ; \
	fi ; \
	echo "  очистка старых бэкапов (оставляем $(DEPLOY_KEEP))" ; \
	ssh $(DEPLOY_HOST) "ls -t $(DEPLOY_BIN)/smsgateway.20* 2>/dev/null | tail -n +$$(($(DEPLOY_KEEP)+1)) | xargs -r rm" ; \
	echo "=== OK ==="

# Показать доступные бэкапы для отката
rollback:
	@ssh $(DEPLOY_HOST) "ls -t $(DEPLOY_BIN)/smsgateway.20* | head -3"
	@echo "Выберите: make rollback-to VERSION=20261020-193807"

# Откатиться на конкретную версию
rollback-to:
	@test -n "$(VERSION)" || (echo "Укажите VERSION=YYYYMMDD-HHMMSS"; exit 1)
	@echo "Откат к $(VERSION)"
	@ssh $(DEPLOY_HOST) "sudo systemctl stop $(DEPLOY_UNIT) || true"
	@ssh $(DEPLOY_HOST) "cp $(DEPLOY_BIN)/smsgateway.$(VERSION) $(DEPLOY_BIN)/smsgateway"
	@ssh $(DEPLOY_HOST) "sudo systemctl start $(DEPLOY_UNIT)"
	@echo "Готово."

# ==================== DIST ====================

dist: clean
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY)-linux-amd64   $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY)-linux-arm64   $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm   GOARM=7 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY)-linux-armv7 $(PKG)

help:
	@echo "Targets: build run test test-race vet fmt tidy lint fetch ci migrate diagnostics clean deploy rollback dist"

# make bump                    # patch: v0.1.0 → v0.1.1
# make bump bump_part=minor    # minor: v0.1.1 → v0.2.0
# make bump bump_part=major    # major: v0.2.0 → v1.0.0
bump:
	@CURRENT=$$(git describe --tags --abbrev=0 2>/dev/null || echo v0.0.0); \
	PART="$${bump_part:-patch}"; \
	BASE=$${CURRENT#v}; \
	MAJOR=$$(echo $$BASE | cut -d. -f1); \
	MINOR=$$(echo $$BASE | cut -d. -f2); \
	PATCH=$$(echo $$BASE | cut -d. -f3); \
	case "$$PART" in \
	    major) MAJOR=$$((MAJOR+1)); MINOR=0; PATCH=0 ;; \
	    minor) MINOR=$$((MINOR+1)); PATCH=0 ;; \
	    patch) PATCH=$$((PATCH+1)) ;; \
	    *) echo "bump_part: major|minor|patch"; exit 1 ;; \
	esac; \
	NEXT="v$$MAJOR.$$MINOR.$$PATCH"; \
	echo "Tag: $$NEXT"; \
	git tag -a "$$NEXT" -m "Release $$NEXT"; \
	git push origin "$$NEXT"

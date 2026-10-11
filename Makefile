GO ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
# Ubuntu 24.04+ / Debian 13+ only ship webkit2gtk-4.1: make desktop WAILS_TAGS=webkit2_41
# (comma-separate extra tags: go build rejects a -tags list that mixes commas and spaces)
WAILS_TAGS ?=
comma := ,
DESKTOP_TAGS = desktop,production$(addprefix $(comma),$(WAILS_TAGS))
# Minimum Go statement coverage enforced by `make cover` (and CI).
COVER_MIN ?= 75

.PHONY: all frontend server app desktop desktop-windows test cover dist dist-desktop-linux dist-desktop-windows dist-desktop-macos deploy-server dev-server dev-app demo docker clean

all: server app

frontend/node_modules: frontend/package.json
	cd frontend && bun install
	@touch $@

## frontend: build the UI into frontend/dist (embedded by the Go binaries)
frontend: frontend/node_modules
	cd frontend && bun run build

## server: the self-hosted team server (no UI, no CGO)
server:
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o bin/braid-server ./cmd/braid-server

## app: the local core + UI served to your browser
app: frontend
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/braid ./cmd/braid

## desktop: the native Wails app (needs gtk3 + webkit2gtk headers on Linux)
desktop: frontend
	CGO_ENABLED=1 $(GO) build -tags $(DESKTOP_TAGS) -ldflags "$(LDFLAGS)" -o bin/braid-desktop ./desktop

## desktop-windows: cross-compile the native app for Windows (WebView2, no CGO)
desktop-windows: frontend
	GOOS=windows GOARCH=amd64 $(GO) build -tags desktop,production -ldflags "$(LDFLAGS) -H windowsgui" -o bin/Braid.exe ./desktop

test: frontend/node_modules
	$(GO) vet ./...
	$(GO) test ./...
	cd frontend && bun run typecheck && bun test

## cover: Go tests with the race detector, failing below COVER_MIN% statement coverage
cover:
	$(GO) test -race -coverpkg=./internal/... -coverprofile=coverage.out ./...
	@$(GO) tool cover -func=coverage.out | awk -v min=$(COVER_MIN) '/^total:/ { sub("%", "", $$3); printf "Go statement coverage: %s%% (minimum %s%%)\n", $$3, min; if ($$3 + 0 < min) exit 1 }'

## dist: release archives of braid-server and braid for every platform (CGO-free cross-compiles)
dist: frontend
	VERSION=$(VERSION) scripts/dist.sh

## dist-desktop-*: the packaged desktop app for one platform (linux needs the gtk/webkit headers, macos runs on a Mac)
dist-desktop-linux: desktop
	mkdir -p dist
	tar -C bin -czf dist/braid-desktop_$(VERSION)_linux_$(shell $(GO) env GOARCH).tar.gz braid-desktop

dist-desktop-windows: desktop-windows
	mkdir -p dist
	cd bin && if command -v zip >/dev/null; then zip -q ../dist/braid-desktop_$(VERSION)_windows_amd64.zip Braid.exe; \
		else python3 -m zipfile -c ../dist/braid-desktop_$(VERSION)_windows_amd64.zip Braid.exe; fi

dist-desktop-macos: frontend
	VERSION=$(VERSION) scripts/package-macos.sh

## deploy-server: build braid-server for DEPLOY_HOST (user@host) and install or update it there as a systemd service
deploy-server:
	VERSION=$(VERSION) scripts/deploy-server.sh $(DEPLOY_HOST)

## dev-server: run a local team server on :8080 (prints a setup code on first run)
dev-server:
	$(GO) run ./cmd/braid-server serve --addr 127.0.0.1:8080 --db ./data/dev.db

## dev-app: rebuild the UI on change and serve it from disk
dev-app: frontend/node_modules
	cd frontend && bun run dev & $(GO) run ./cmd/braid --ui-dir frontend/dist

## demo: like dev-app, but with the offline demo agent (no model calls)
demo: frontend
	BRAID_FAKE_AGENT=1 BRAID_FAKE_DELAY_MS=120 $(GO) run ./cmd/braid --home ./data/demo-home

docker:
	docker build -t braid-server:$(VERSION) .

clean:
	rm -rf bin dist coverage.out
	find frontend/dist -mindepth 1 ! -name .gitkeep -delete

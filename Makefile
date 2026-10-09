GO ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
# Ubuntu 24.04+ / Debian 13+ only ship webkit2gtk-4.1: make desktop WAILS_TAGS=webkit2_41
WAILS_TAGS ?=

.PHONY: all frontend server app desktop desktop-windows test lint dev-server dev-app demo docker clean

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
	CGO_ENABLED=1 $(GO) build -tags "desktop,production $(WAILS_TAGS)" -ldflags "$(LDFLAGS)" -o bin/braid-desktop ./desktop

## desktop-windows: cross-compile the native app for Windows (WebView2, no CGO)
desktop-windows: frontend
	GOOS=windows GOARCH=amd64 $(GO) build -tags desktop,production -ldflags "$(LDFLAGS) -H windowsgui" -o bin/Braid.exe ./desktop

test: frontend/node_modules
	$(GO) vet ./...
	$(GO) test ./...
	cd frontend && bun run typecheck

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
	rm -rf bin
	find frontend/dist -mindepth 1 ! -name .gitkeep -delete

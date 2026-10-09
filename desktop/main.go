// Command braid-desktop is the native Braid app: a Wails window around the
// same local core that `braid` serves to a browser.
//
// Build with the Wails tags (see the Makefile):
//
//	go build -tags desktop,production -o bin/braid-desktop ./desktop
//
// Linux needs gtk3 and webkit2gtk development headers (add -tags webkit2_41
// on distributions that only ship webkit2gtk-4.1, e.g. Ubuntu 24.04+).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"github.com/sam-jeston/braid/frontend"
	"github.com/sam-jeston/braid/internal/local"
)

var version = "dev"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	core, err := local.New(local.Options{Version: version, Logger: log})
	if err != nil {
		log.Error("start core", "err", err)
		os.Exit(1)
	}

	// The UI talks to the core over a token-protected loopback API, exactly
	// as in browser mode; the webview only needs to learn the address.
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		panic(err)
	}
	token := hex.EncodeToString(tok)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Error("listen", "err", err)
		os.Exit(1)
	}
	api := &http.Server{Handler: core.NewAPI(token, nil), ReadHeaderTimeout: 10 * time.Second}
	go api.Serve(ln)
	apiURL := "http://" + ln.Addr().String()

	// Only the embedded webview can reach the asset server, so this is how
	// the UI receives the token without it ever touching disk or the URL.
	bootstrap := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/__braid/bootstrap" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{"api": apiURL, "token": token})
	})

	err = wails.Run(&options.App{
		Title:            "Braid",
		Width:            1440,
		Height:           920,
		MinWidth:         1000,
		MinHeight:        640,
		BackgroundColour: &options.RGBA{R: 246, G: 246, B: 248, A: 255},
		AssetServer:      &assetserver.Options{Assets: frontend.Dist(), Handler: bootstrap},
		OnShutdown: func(ctx context.Context) {
			core.Shutdown()
			sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = api.Shutdown(sctx)
		},
		SingleInstanceLock: &options.SingleInstanceLock{UniqueId: "dev.braid.desktop"},
		Linux:              &linux.Options{ProgramName: "braid", WebviewGpuPolicy: linux.WebviewGpuPolicyOnDemand},
		Mac:                &mac.Options{About: &mac.AboutInfo{Title: "Braid", Message: "Product, design and engineering agents on one thread. " + version}},
	})
	if err != nil {
		log.Error("desktop app", "err", err)
		os.Exit(1)
	}
}

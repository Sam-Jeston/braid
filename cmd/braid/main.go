// Command braid runs the Braid desktop core and serves its UI in your
// browser. It is the same core the native desktop app (./desktop) embeds,
// useful on machines without a webview toolkit, over SSH tunnels, and for
// development.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sam-jeston/braid/frontend"
	"github.com/sam-jeston/braid/internal/local"
)

var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "ui":
			args = args[1:]
		case "version", "--version", "-v":
			fmt.Println("braid", version)
			return
		case "help", "-h", "--help":
			fmt.Fprintln(os.Stderr, "usage: braid [ui] [--port N] [--no-open] [--ui-dir DIR] [--home DIR]")
			return
		}
	}
	flags := flag.NewFlagSet("ui", flag.ExitOnError)
	port := flags.Int("port", 0, "loopback port for the UI (0 = random)")
	noOpen := flags.Bool("no-open", false, "don't open a browser")
	uiDir := flags.String("ui-dir", "", "serve the UI from this directory instead of the embedded build (development)")
	home := flags.String("home", "", "Braid data directory (default $BRAID_HOME or the user config dir)")
	verbose := flags.Bool("v", false, "verbose logging")
	flags.Parse(args)

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	core, err := local.New(local.Options{Home: *home, Version: version, Logger: log})
	if err != nil {
		log.Error("start core", "err", err)
		os.Exit(1)
	}
	var static = frontend.Dist()
	if *uiDir != "" {
		static = os.DirFS(*uiDir)
	}
	if _, err := fs.Stat(static, "index.html"); err != nil {
		log.Warn("UI not built; run `make frontend` (API still available)")
	}

	token := randomToken()
	api := core.NewAPI(token, static)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		log.Error("listen", "err", err)
		os.Exit(1)
	}
	srv := &http.Server{Handler: api, ReadHeaderTimeout: 10 * time.Second}
	url := fmt.Sprintf("http://%s/#token=%s", ln.Addr(), token)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		core.Shutdown()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()

	fmt.Fprintf(os.Stderr, "\n  Braid is running. Open this link (it contains a session token):\n\n    %s\n\n  Data: %s\n\n", url, core.Home())
	if !*noOpen {
		openBrowser(url)
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func openBrowser(url string) {
	if cmd, err := local.OpenCommand(url); err == nil {
		_ = cmd.Start()
	}
}

// Command braid-server is the self-hosted Braid backend: one binary plus a
// SQLite file. It stores team state and relays realtime events; agents never
// run here.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sam-jeston/braid/internal/server"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "serve":
			os.Args = append(os.Args[:1], os.Args[2:]...)
		case "setup-code":
			setupCode(os.Args[2:])
			return
		case "recovery-code":
			recoveryCode(os.Args[2:])
			return
		case "version", "--version", "-v":
			fmt.Println("braid-server", version)
			return
		case "help", "--help", "-h":
			usage()
			return
		}
	}
	serve()
}

func usage() {
	fmt.Fprint(os.Stderr, `braid-server — self-hosted state server for Braid

Usage:
  braid-server [serve] [flags]        run the server (default)
  braid-server setup-code [flags]     mint a single-use code for creating a team
  braid-server recovery-code [flags]  mint a single-use code that lets a member
                                      who lost their devices sign back in
  braid-server version

Run "braid-server serve -h" for flags. Every flag can also be set with an
environment variable (BRAID_ADDR, BRAID_DB, BRAID_TLS_CERT, BRAID_TLS_KEY,
BRAID_TRUST_PROXY, BRAID_LOG_LEVEL).
`)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func serve() {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", env("BRAID_ADDR", ":8080"), "listen address")
	dbPath := fs.String("db", env("BRAID_DB", "./data/braid.db"), "SQLite database path")
	cert := fs.String("tls-cert", env("BRAID_TLS_CERT", ""), "TLS certificate (PEM); omit when behind a TLS-terminating proxy")
	key := fs.String("tls-key", env("BRAID_TLS_KEY", ""), "TLS private key (PEM)")
	trustProxy := fs.Bool("trust-proxy", env("BRAID_TRUST_PROXY", "") == "true", "trust X-Forwarded-For for client IPs")
	logLevel := fs.String("log-level", env("BRAID_LOG_LEVEL", "info"), "debug, info, warn or error")
	fs.Parse(os.Args[1:])

	log := newLogger(*logLevel)
	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o755); err != nil {
		log.Error("create data dir", "err", err)
		os.Exit(1)
	}
	srv, err := server.New(server.Config{DBPath: *dbPath, Version: version, Logger: log, TrustProxy: *trustProxy})
	if err != nil {
		log.Error("start", "err", err)
		os.Exit(1)
	}
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if code, err := srv.BootstrapCode(ctx); err != nil {
		log.Error("bootstrap", "err", err)
	} else if code != "" {
		fmt.Fprintf(os.Stderr, `
  ┌──────────────────────────────────────────────────────────────┐
  │  No teams yet. Use this setup code in the Braid app to       │
  │  create the first team (single use, valid for 7 days):       │
  │                                                              │
  │      %-56s│
  │                                                              │
  │  Mint more with: braid-server setup-code                     │
  └──────────────────────────────────────────────────────────────┘

`, code)
	}

	go srv.Run(ctx)
	hs := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()

	log.Info("braid-server listening", "addr", *addr, "db", *dbPath, "tls", *cert != "", "version", version, "server_id", srv.ServerID())
	if *cert == "" {
		log.Warn("serving plain HTTP: put braid-server behind a TLS-terminating proxy, or pass --tls-cert/--tls-key. The app refuses non-HTTPS servers except on localhost.")
		err = hs.ListenAndServe()
	} else {
		err = hs.ListenAndServeTLS(*cert, *key)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}

func setupCode(args []string) {
	fs := flag.NewFlagSet("setup-code", flag.ExitOnError)
	dbPath := fs.String("db", env("BRAID_DB", "./data/braid.db"), "SQLite database path")
	ttl := fs.Duration("ttl", 7*24*time.Hour, "how long the code stays valid")
	fs.Parse(args)
	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	srv, err := server.New(server.Config{DBPath: *dbPath, Version: version, Logger: newLogger("warn")})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer srv.Close()
	code, err := srv.CreateSetupCode(context.Background(), *ttl)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(code)
}

func recoveryCode(args []string) {
	fs := flag.NewFlagSet("recovery-code", flag.ExitOnError)
	dbPath := fs.String("db", env("BRAID_DB", "./data/braid.db"), "SQLite database path")
	team := fs.String("team", "", "team name or ID (needed when the server has several teams)")
	member := fs.String("member", "", "member name or ID (default: the team's admin)")
	ttl := fs.Duration("ttl", 24*time.Hour, "how long the code stays valid")
	fs.Parse(args)
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err := os.Stat(*dbPath); err != nil {
		fail(fmt.Errorf("there's no Braid database at %s. Pass the server's database with --db or BRAID_DB", *dbPath))
	}
	if *ttl <= 0 {
		fail(errors.New("--ttl must be positive"))
	}
	srv, err := server.New(server.Config{DBPath: *dbPath, Version: version, Logger: newLogger("warn")})
	if err != nil {
		fail(err)
	}
	defer srv.Close()
	rc, err := srv.CreateRecoveryCode(context.Background(), *team, *member, *ttl)
	if err != nil {
		fail(err)
	}
	fmt.Println(rc.Code)
	fmt.Fprintf(os.Stderr, "\nThis code signs %s back in to %s. In Braid, choose Join a team and enter\n"+
		"the server URL and this code. It works once, until %s, and signs out\n"+
		"%s's other devices.\n", rc.Member.DisplayName, rc.Team.Name, rc.ExpiresAt.Local().Format("2 Jan 15:04"), rc.Member.DisplayName)
}

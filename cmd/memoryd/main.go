// Package main implements memoryd, the bishop-memory HTTP daemon.
//
// It loads configuration, opens/bootstraps the SQLite database, wires
// the Gin router, and serves HTTP until either the listener fails or
// the process receives SIGINT/SIGTERM — at which point it shuts down
// gracefully (stop accepting new connections, let in-flight requests
// finish, close the database) rather than being killed outright.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	dbschema "bishop-memory/db"
	"bishop-memory/internal/api"
	"bishop-memory/internal/auth"
	"bishop-memory/internal/config"
	"bishop-memory/internal/store"
)

// shutdownTimeout bounds how long graceful shutdown waits for
// in-flight requests to finish before forcing the listener closed.
const shutdownTimeout = 10 * time.Second

func main() {
	cfg := config.MustLoad()

	// memoryd keys … and memoryd backup … are operator commands that run
	// and exit; with no arguments memoryd serves.
	if len(os.Args) > 1 {
		os.Exit(runCommand(cfg, os.Args[1:]))
	}

	keys, err := auth.NewStore(cfg.APIKeysFile, cfg.APIKey)
	if err != nil {
		log.Fatal(err)
	}

	// Authentication is off only while no key exists. That is the
	// single-machine default, and safe only on loopback: on any other
	// address memoryd refuses to start without a key unless the operator
	// opts out explicitly (docs/plans/NETWORK-DEPLOYMENT-PLAN.md §3.2).
	tls := cfg.TLSCertFile != "" || cfg.TLSKeyFile != ""
	if tls && (cfg.TLSCertFile == "" || cfg.TLSKeyFile == "") {
		log.Fatal("bishop-memory: set both TLS_CERT_FILE and TLS_KEY_FILE, or neither")
	}
	if !config.IsLoopbackHost(cfg.HTTPHost) {
		switch {
		case keys.Empty() && !cfg.AllowNoAuth:
			log.Fatalf("bishop-memory: HTTP_HOST=%q is not loopback and no API key is configured. "+
				"Create one with `memoryd keys add <name>` (keys file %s), or set "+
				"BISHOP_ALLOW_NO_AUTH=1 to run open on the network.", cfg.HTTPHost, keys.Path())
		case keys.Empty():
			log.Printf("bishop-memory: WARNING — BISHOP_ALLOW_NO_AUTH=1: %s is open to anything that can reach it, with no API key.", cfg.HTTPAddr)
		case !tls:
			log.Printf("bishop-memory: WARNING — serving plain HTTP on %s. API keys cross the network in clear "+
				"unless the network itself is encrypted (Tailscale, WireGuard, an SSH tunnel). "+
				"Set TLS_CERT_FILE and TLS_KEY_FILE to serve HTTPS.", cfg.HTTPAddr)
		}
	}
	if keys.Empty() {
		log.Printf("bishop-memory: no API keys configured; /v1 is open (loopback only)")
	}

	db, err := store.Open(cfg.DatabasePath)
	if err != nil {
		log.Fatal(err)
	}

	// Step 4 review fix (Review C, CRITICAL 2): the original code had
	// `defer db.Close()` immediately after a successful Open, then
	// called log.Fatal on every subsequent failure path. log.Fatal
	// calls os.Exit, which does NOT run deferred functions — so the
	// defer was dead code on every real exit this program could take
	// (ApplySchema failure, a listener bind failure, and — because
	// there was no signal handling at all — the SIGTERM/SIGINT sent by
	// launchctl/systemctl on every normal stop). We close db
	// explicitly on the schema-failure path below, and via the
	// graceful-shutdown path once request serving is wired up.
	if err := store.ApplySchemaSQL(db, dbschema.Schema); err != nil {
		_ = db.Close()
		log.Fatal(err)
	}

	// ApplySchema's CREATE TABLE IF NOT EXISTS statements cannot add a
	// column to a table that already exists, so columns introduced after
	// a table's first release are applied separately. No-op once the
	// database is current.
	if err := store.EnsureColumns(db); err != nil {
		_ = db.Close()
		log.Fatal(err)
	}

	// EnsureMissionStepsIndex creates the unique index on (mission_id, step)
	// after pre-checking for duplicates. Must run after ApplySchema and EnsureColumns
	// to ensure the mission_steps table exists and is fully populated. No-op once
	// the index is present. On duplicate detection, logs an actionable error and exits.
	if err := store.EnsureMissionStepsIndex(db); err != nil {
		_ = db.Close()
		log.Fatal(err)
	}

	router := api.NewRouterWithKeys(cfg, db, keys)

	srv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: router,
	}

	// ctx is cancelled the moment SIGINT or SIGTERM arrives — the exact
	// signals launchctl (Step 5's launchd plist) and systemctl (Step 6's
	// systemd unit) send on a normal `stop`/`unload`/`restart`. stop()
	// releases the underlying signal.Notify registration on every exit
	// path from main, including the srv.ListenAndServe() error branch
	// below (CONV-040: what is acquired is released on every exit path).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		scheme := "http"
		if tls {
			scheme = "https"
		}
		log.Printf(
			"bishop-memory listening on %s://%s in %s mode",
			scheme,
			cfg.HTTPAddr,
			cfg.AppEnv,
		)
		if tls {
			serveErr <- srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
			return
		}
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		// The listener itself failed (e.g. the port is already in
		// use) before any shutdown was requested. Close db explicitly
		// — there is no server to shut down, so the graceful path
		// below never runs.
		_ = db.Close()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}

	case <-ctx.Done():
		// A shutdown signal arrived. Stop accepting new connections
		// and let in-flight requests finish (bounded by
		// shutdownTimeout), THEN close the database — this is the
		// exit path that was previously unreachable entirely.
		log.Printf("bishop-memory: shutdown signal received, shutting down gracefully")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("bishop-memory: graceful shutdown did not complete cleanly: %v", err)
		}

		if err := db.Close(); err != nil {
			log.Printf("bishop-memory: error closing database: %v", err)
		}

		log.Printf("bishop-memory: shutdown complete")
	}
}

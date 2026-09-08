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
	"os/signal"
	"syscall"
	"time"

	"bishop-memory/internal/api"
	"bishop-memory/internal/config"
	"bishop-memory/internal/store"
)

// shutdownTimeout bounds how long graceful shutdown waits for
// in-flight requests to finish before forcing the listener closed.
const shutdownTimeout = 10 * time.Second

func main() {
	cfg := config.MustLoad()

	// Job 2 (task-20260821-02 step 6): the default bind is
	// loopback-only (see config.Config.HTTPHost's docblock). An
	// operator can still override HTTP_HOST to bind elsewhere, but
	// that is a deliberate exposure decision this service cannot make
	// safe on its own — there is no authentication anywhere in this
	// codebase, so warn loudly, at the moment the choice takes effect,
	// naming exactly what becomes reachable.
	if !config.IsLoopbackHost(cfg.HTTPHost) {
		log.Printf(
			"bishop-memory: WARNING — HTTP_HOST=%q is not loopback-only; "+
				"binding %s exposes this service to anything that can reach "+
				"it. bishop-memory has NO authentication: every task and "+
				"event is readable and writable by any caller, and "+
				"POST /v1/documents/sync will walk any filesystem root a "+
				"caller supplies. Loopback (127.0.0.1) plus an SSH tunnel "+
				"is the supported deployment model — override only if you "+
				"understand and accept this exposure.",
			cfg.HTTPHost, cfg.HTTPAddr,
		)
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
	if err := store.ApplySchema(db, "db/schema.sql"); err != nil {
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

	router := api.NewRouter(cfg, db)

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
		log.Printf(
			"bishop-memory listening on http://%s in %s mode",
			cfg.HTTPAddr,
			cfg.AppEnv,
		)
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

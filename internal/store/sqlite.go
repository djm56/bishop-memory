// Package store wraps SQLite access for bishop-memory. The store layer is
// the single point that talks to the database; handlers and importers go
// through it so the storage engine can change without rippling through
// the API.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" driver via init()
)

// Open returns a *sql.DB connected to the SQLite file at path. The caller
// is responsible for closing it (cmd/memoryd/main.go defers db.Close).
// ApplySchema is called separately at boot to bring the database to the
// latest schema.
//
// The DSN carries the connection-scoped pragmas so they apply to every
// pooled connection (Step 2 review warning #1): foreign_keys=ON (without
// it ON DELETE CASCADE silently no-ops), journal_mode=WAL (readers do not
// block the writer), and busy_timeout=5000ms (avoid spuriously failing on
// a contended write). With SetMaxOpenConns(1) the local single-writer
// pattern serialises writers so concurrent HTTP handlers never hit
// "database is locked".
func Open(path string) (*sql.DB, error) {
	// Ensure the parent directory exists so a fresh checkout boots
	// without running `make init-db` first.
	//
	// Step 4 review fix (Review A, SUGGESTION 4): 0o700 rather than the
	// previous 0o755 — the database holds task/event content that may
	// be worth restricting to the owner on a shared machine. The SQLite
	// file itself (created by the driver on first write, not by this
	// function) still inherits the process umask; there is no portable
	// os.MkdirAll-style knob for that from this call site, so the
	// directory permission is the practical control point here.
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create db directory %s: %w", dir, err)
		}
	}

	// Each _pragma value is run as "PRAGMA <value>" on every connection
	// the pool opens, so FK enforcement + WAL + busy_timeout apply
	// connection-wide (modernc.org/sqlite applyQueryParams).
	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}

	// Single-writer pattern: local SQLite serialises writes through one
	// connection to avoid "database is locked" under concurrent writers.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	// No rotation: the local service keeps its single connection for the
	// process lifetime.
	db.SetConnMaxLifetime(0)

	// Ping before returning so an unreachable/unopenable database is
	// surfaced as a boot failure (fatal in cmd/memoryd/main.go). Ping
	// also forces the driver to actually create the file on a fresh
	// path, which is why the permission tightening below happens after
	// it, not before.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}

	// Step 4 review fix (Review A, SUGGESTION 4): the SQLite file
	// inherits the process umask (typically world-readable 0644) with
	// no DSN/driver knob to control it at creation time, so we tighten
	// it explicitly to owner-only after the fact. Best-effort: a
	// Chmod failure (e.g. a filesystem or platform that does not
	// support Unix permission bits) does not block boot — the
	// directory-level 0o700 above is the primary control, this is a
	// belt-and-suspenders extra for the file itself.
	_ = os.Chmod(path, 0o600)

	return db, nil
}

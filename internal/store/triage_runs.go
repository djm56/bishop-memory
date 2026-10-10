// Package store — widening triage_runs.kind to admit mission-grader runs.
//
// triage_runs was created with CHECK (kind IN ('classify', 'process')). The
// mission grader records its runs in the same table, so the CHECK has to
// admit 'grade'. SQLite cannot alter a CHECK in place, and db/schema.sql's
// CREATE TABLE IF NOT EXISTS skips a table that already exists, so a database
// created before the grader keeps the narrow CHECK until this rebuilds it.
//
// The rebuild follows SQLite's documented procedure for a schema change ALTER
// TABLE cannot make (https://sqlite.org/lang_altertable.html, "Making Other
// Kinds Of Table Schema Changes"): with foreign keys off, inside one
// transaction, create the new table, copy every row with its id, drop the old
// table, rename the new one into place, and confirm foreign_key_check is
// clean before committing. Other tables reference triage_runs by name, so
// their references resolve to the rebuilt table unchanged.
//
// The rebuilt table is a strict superset of the old one — same columns, same
// rows, a wider CHECK — so an older memoryd started against it still works.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// triageRunsTable is the current definition of triage_runs. It must match the
// CREATE TABLE in db/schema.sql; TestTriageRunsDefinitionMatchesSchema checks.
const triageRunsTable = `CREATE TABLE triage_runs_rebuild (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    kind        TEXT    NOT NULL,
    category    TEXT,
    model       TEXT,
    status      TEXT    NOT NULL DEFAULT 'running',
    considered  INTEGER NOT NULL DEFAULT 0,
    written     INTEGER NOT NULL DEFAULT 0,
    notes       TEXT,
    started_at  TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),
    finished_at TEXT,

    CHECK (kind IN ('classify', 'process', 'grade')),
    CHECK (status IN ('running', 'done', 'failed'))
)`

// triageRunsColumns lists the columns copied across, in table order.
const triageRunsColumns = `id, kind, category, model, status, considered, written, notes, started_at, finished_at`

// EnsureTriageRunKinds rebuilds triage_runs when its CHECK does not yet admit
// kind 'grade'. A no-op once it does, so it is safe on every boot. Must run
// after ApplySchemaSQL.
func EnsureTriageRunKinds(db *sql.DB) error {
	ctx := context.Background()

	var definition string
	err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'triage_runs'`).Scan(&definition)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // ApplySchemaSQL creates it; nothing to widen
	}
	if err != nil {
		return fmt.Errorf("read triage_runs definition: %w", err)
	}
	if strings.Contains(definition, "'grade'") {
		return nil
	}

	// PRAGMA foreign_keys is a no-op inside a transaction and applies per
	// connection, so the whole rebuild runs on one dedicated connection.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("triage_runs rebuild: get connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("triage_runs rebuild: foreign keys off: %w", err)
	}
	// Whatever happens below, the connection goes back to the pool with
	// foreign keys enforced again.
	defer conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`) //nolint:errcheck

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("triage_runs rebuild: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var before int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM triage_runs`).Scan(&before); err != nil {
		return fmt.Errorf("triage_runs rebuild: count rows: %w", err)
	}
	// A database may already hold foreign-key violations that have nothing
	// to do with triage_runs; only new ones would be this rebuild's doing.
	violationsBefore, err := countForeignKeyViolations(ctx, tx)
	if err != nil {
		return err
	}
	statements := []string{
		triageRunsTable,
		`INSERT INTO triage_runs_rebuild (` + triageRunsColumns + `) SELECT ` + triageRunsColumns + ` FROM triage_runs`,
		`DROP TABLE triage_runs`,
		`ALTER TABLE triage_runs_rebuild RENAME TO triage_runs`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("triage_runs rebuild: %w", err)
		}
	}
	var after int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM triage_runs`).Scan(&after); err != nil {
		return fmt.Errorf("triage_runs rebuild: recount rows: %w", err)
	}
	if after != before {
		return fmt.Errorf("triage_runs rebuild: copied %d of %d rows; rolled back", after, before)
	}
	violationsAfter, err := countForeignKeyViolations(ctx, tx)
	if err != nil {
		return err
	}
	if violationsAfter > violationsBefore {
		return fmt.Errorf("triage_runs rebuild: foreign_key_check rose from %d to %d violations; rolled back",
			violationsBefore, violationsAfter)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("triage_runs rebuild: commit: %w", err)
	}
	return nil
}

func countForeignKeyViolations(ctx context.Context, tx *sql.Tx) (int, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return 0, fmt.Errorf("triage_runs rebuild: foreign_key_check: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	return n, rows.Err()
}

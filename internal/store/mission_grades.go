// Package store — widening mission_grades.state to admit 'excluded'.
//
// mission_grades was created with CHECK (state IN ('pending', 'graded',
// 'insufficient', 'skipped')). The operator's N/A grade adds 'excluded', and
// SQLite cannot alter a CHECK in place. db/schema.sql's CREATE TABLE IF NOT
// EXISTS skips a table that already exists, so a database created before this
// change keeps the narrow CHECK until this rebuilds the table.
//
// The rebuild is the one EnsureTriageRunKinds performs for triage_runs, with
// the same procedure (triage_runs.go describes it, and why it is safe): foreign
// keys off, one transaction, copy every row with its columns, drop, rename,
// and check the row count and the foreign-key violations before committing.
// One step is added. mission_grades carries idx_mission_grades_state, which
// DROP TABLE takes with the table, so the index is created again after the
// rename.
//
// Nothing declares mission_grades as a parent, so dropping it removes no
// dependent rows. Every row the old table holds satisfies the rebuilt CHECKS,
// which admit state 'excluded' and add CHECK (state <> 'excluded' OR grade IS NULL): no old row holds 'excluded'.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
)

// missionGradesTable is the current definition of mission_grades under the
// rebuild's temporary name. It must match the CREATE TABLE in db/schema.sql;
// TestMissionGradesDefinitionMatchesSchema checks.
const missionGradesTable = `CREATE TABLE mission_grades_rebuild (
    mission_id   TEXT    PRIMARY KEY REFERENCES missions(id) ON DELETE CASCADE,
    state        TEXT    NOT NULL DEFAULT 'pending',
    grade        TEXT,
    summary      TEXT,
    suggestions  TEXT,
    signals      TEXT,
    graded_by    TEXT,
    run_id       INTEGER REFERENCES triage_runs(id),
    attempts     INTEGER NOT NULL DEFAULT 0,
    claimed_at   TEXT,
    graded_at    TEXT,
    created_at   TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),

    CHECK (state IN ('pending', 'graded', 'insufficient', 'skipped', 'excluded')),
    CHECK (grade IS NULL OR grade IN ('A', 'B', 'C', 'D', 'E', 'F')),
    CHECK (state <> 'graded' OR grade IS NOT NULL),
    CHECK (state <> 'excluded' OR grade IS NULL)
)`

// missionGradesColumns lists the columns copied across, in table order.
const missionGradesColumns = `mission_id, state, grade, summary, suggestions, signals, graded_by, run_id, attempts, claimed_at, graded_at, created_at`

// missionGradesIndex is the index db/schema.sql declares on mission_grades.
// The rebuild recreates it, since the DROP takes the original with the table.
const missionGradesIndex = `CREATE INDEX IF NOT EXISTS idx_mission_grades_state ON mission_grades(state)`

// EnsureMissionGradeExcluded rebuilds mission_grades when its CHECK does not
// yet admit state 'excluded'. It is a no-op once the table admits it, so it is
// safe on every boot. It must run after ApplySchemaSQL.
//
// The rebuild runs in one transaction and rolls back on any error, leaving the
// old table as it was. It returns an error when the copied row count differs
// from the original, or when the foreign-key violations rise.
//
// Side effects: on a database whose table lacks 'excluded', it replaces the
// table and its index and commits the change. Nothing else is written.
func EnsureMissionGradeExcluded(db *sql.DB) error {
	ctx := context.Background()

	var definition string
	err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'mission_grades'`).Scan(&definition)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // ApplySchemaSQL creates it; nothing to widen
	}
	if err != nil {
		return fmt.Errorf("read mission_grades definition: %w", err)
	}
	if strings.Contains(definition, "'excluded'") {
		return nil
	}

	// PRAGMA foreign_keys is a no-op inside a transaction and applies per
	// connection, so the whole rebuild runs on one dedicated connection.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("mission_grades rebuild: get connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("mission_grades rebuild: foreign keys off: %w", err)
	}
	// Whatever happens below, the connection is handed back with foreign keys
	// enforced again; if that restore fails, restoreForeignKeys logs it.
	defer restoreForeignKeys(ctx, conn)

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mission_grades rebuild: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var before int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_grades`).Scan(&before); err != nil {
		return fmt.Errorf("mission_grades rebuild: count rows: %w", err)
	}
	// A database may already hold foreign-key violations that have nothing
	// to do with mission_grades; only new ones would be this rebuild's doing.
	violationsBefore, err := countForeignKeyViolations(ctx, tx)
	if err != nil {
		return err
	}
	statements := []string{
		missionGradesTable,
		`INSERT INTO mission_grades_rebuild (` + missionGradesColumns + `) SELECT ` + missionGradesColumns + ` FROM mission_grades`,
		`DROP TABLE mission_grades`,
		`ALTER TABLE mission_grades_rebuild RENAME TO mission_grades`,
		missionGradesIndex,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("mission_grades rebuild: %w", err)
		}
	}
	var after int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_grades`).Scan(&after); err != nil {
		return fmt.Errorf("mission_grades rebuild: recount rows: %w", err)
	}
	if after != before {
		return fmt.Errorf("mission_grades rebuild: copied %d of %d rows; rolled back", after, before)
	}
	violationsAfter, err := countForeignKeyViolations(ctx, tx)
	if err != nil {
		return err
	}
	if violationsAfter > violationsBefore {
		return fmt.Errorf("mission_grades rebuild: foreign_key_check rose from %d to %d violations; rolled back",
			violationsBefore, violationsAfter)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mission_grades rebuild: commit: %w", err)
	}
	return nil
}

// restoreForeignKeys turns foreign key enforcement back on for the pooled
// connection the rebuild used. The rebuild has already answered when it runs,
// so a failure cannot be returned and is logged instead. A connection left with
// foreign keys off would silently stop every ON DELETE CASCADE on the app's
// only connection.
func restoreForeignKeys(ctx context.Context, conn *sql.Conn) {
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		log.Printf("mission_grades rebuild: restoring foreign keys: %v", err)
	}
}

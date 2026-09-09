// Package store — additive column migrations.
//
// ApplySchema brings a database up to db/schema.sql, but every statement
// there is CREATE TABLE IF NOT EXISTS / CREATE INDEX IF NOT EXISTS. That
// makes repeated boots idempotent, and it also means a column ADDED to an
// existing CREATE TABLE never reaches a database that already has that
// table: SQLite sees the table exists and skips the statement entirely,
// silently leaving the old shape in place.
//
// EnsureColumns closes that gap for additive, nullable columns — the only
// kind of change that can be applied to a populated table without a
// rewrite or a data decision. Anything else (dropping a column, adding a
// NOT NULL without a default, changing a CHECK) needs a real migration
// with a considered backfill, and deliberately is not handled here.
package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// columnMigration is one additive column: the table it belongs to, the
// column name, and the type/constraint text used in the ALTER.
type columnMigration struct {
	table  string
	column string
	// definition is everything after the column name in
	// "ALTER TABLE <table> ADD COLUMN <column> <definition>". It must be
	// valid for a populated table, which in practice means nullable with
	// no default, or with a constant default.
	definition string
}

// additiveColumns is the ordered list of columns added to tables after
// their first release. Each entry stays here permanently: it is what
// upgrades a database created before that column existed. Removing an
// entry once shipped would strand any database that never ran it.
var additiveColumns = []columnMigration{
	// missions.harness records which harness allocated a mission, so a
	// single bishop-memory can serve several harnesses and still attribute
	// every mission. Nullable because missions created before central
	// allocation — and any created by a harness in standalone mode — have
	// no harness to record.
	{table: "missions", column: "harness", definition: "TEXT"},
}

// EnsureColumns applies every additive column that the database does not
// already have. It is safe to call on every boot: existing columns are
// detected via PRAGMA table_info and skipped, so the function is a no-op
// on an up-to-date database.
//
// A missing TABLE is not an error here — ApplySchema runs first and
// creates the tables, so a table absent at this point means the schema
// file no longer defines it, and a migration for a table that does not
// exist is simply not applicable.
func EnsureColumns(db *sql.DB) error {
	for _, migration := range additiveColumns {
		exists, err := tableExists(db, migration.table)
		if err != nil {
			return fmt.Errorf("check table %s: %w", migration.table, err)
		}
		if !exists {
			continue
		}

		has, err := columnExists(db, migration.table, migration.column)
		if err != nil {
			return fmt.Errorf("check column %s.%s: %w", migration.table, migration.column, err)
		}
		if has {
			continue
		}

		// Identifiers are compile-time constants from additiveColumns, not
		// caller input, so interpolation here cannot be influenced
		// externally. SQLite has no parameter binding for DDL identifiers.
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s",
			migration.table, migration.column, migration.definition)
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("add column %s.%s: %w", migration.table, migration.column, err)
		}
	}
	return nil
}

// tableExists reports whether a table of that name is present.
func tableExists(db *sql.DB, table string) (bool, error) {
	var name string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`,
		table,
	).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// columnExists reports whether a column is present on a table, using
// PRAGMA table_info. PRAGMA does not accept a bound parameter for its
// argument, so the table name — a constant from additiveColumns — is
// interpolated.
func columnExists(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			name      string
			colType   sql.NullString
			notNull   sql.NullInt64
			dfltValue sql.NullString
			pk        sql.NullInt64
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, rows.Err()
		}
	}
	return false, rows.Err()
}

// EnsureMissionStepsIndex applies the unique index on (mission_id, step)
// to the mission_steps table. It must run after ApplySchema but before
// the API layer begins accepting upsert calls on the mission_steps endpoint.
// It is safe to call on every boot: the index is skipped if it already exists.
//
// Why this function exists in the migration path instead of db/schema.sql:
//
// cmd/memoryd/main.go calls ApplySchema before EnsureMissionStepsIndex and
// uses log.Fatal on either failure. A unique index declared in schema.sql
// would fail inside ApplySchema at boot with a raw SQLite constraint violation
// error (no guidance for the operator). Putting the index in the migration path
// allows this function to pre-check for duplicates and produce an actionable
// error message before the constraint is applied.
//
// The index is idempotent: safe to call on every boot, a no-op once it exists.
// The duplicate check runs every time; after the operator resolves duplicates
// and restarts the service, this function creates the index and moves on.
//
// Note: SQLite treats NULLs as distinct in a unique index — rows with
// step IS NULL never collide even if mission_id is identical. Multiple NULL-step
// rows for one mission are permitted. This is a consequence of SQL NULL semantics,
// not a constraint violation. Legacy NULL-step rows from before this change
// remain readable and unaffected by the index; they can never be upserted
// through the API (step is now required), and are thus immutable.
func EnsureMissionStepsIndex(db *sql.DB) error {
	// Check if the index already exists. SQLite has no direct way to check
	// for an existing index by name, so we query sqlite_master.
	var indexName string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master
		 WHERE type = 'index' AND name = 'idx_mission_steps_unique_key'`,
	).Scan(&indexName)
	if err == nil {
		// Index already exists; no-op.
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check index existence: %w", err)
	}

	// Index does not exist. Check for duplicate (mission_id, step) pairs.
	// Exclude NULL-step rows: they cannot violate a unique index (SQLite treats
	// NULL as distinct), so they must never block index creation. Only non-NULL
	// duplicate pairs are the problem here.
	rows, err := db.Query(`
		SELECT mission_id, step, COUNT(*) as cnt
		FROM mission_steps
		WHERE step IS NOT NULL
		GROUP BY mission_id, step
		HAVING COUNT(*) > 1
		ORDER BY cnt DESC
	`)
	if err != nil {
		return fmt.Errorf("query duplicates: %w", err)
	}
	defer rows.Close()

	type duplicate struct {
		missionID string
		step      string
	}
	var duplicates []duplicate
	for rows.Next() {
		var missionID string
		var step string
		var cnt int
		if err := rows.Scan(&missionID, &step, &cnt); err != nil {
			return fmt.Errorf("scan duplicate: %w", err)
		}
		duplicates = append(duplicates, duplicate{missionID, step})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate duplicates: %w", err)
	}

	if len(duplicates) > 0 {
		// Format an actionable error showing the operator the duplicates
		// and how to fix them. Show up to 5 samples to keep the error readable.
		msg := fmt.Sprintf("cannot create unique index on mission_steps(mission_id, step): "+
			"found %d duplicate (mission_id, step) pairs. "+
			"Examples:\n", len(duplicates))
		shown := 0
		for _, dup := range duplicates {
			if shown >= 5 {
				msg += fmt.Sprintf("... and %d more.\n", len(duplicates)-5)
				break
			}
			msg += fmt.Sprintf("  mission_id=%q, step=%q\n", dup.missionID, dup.step)
			shown++
		}
		msg += "To fix: identify the duplicate rows in the database (use " +
			"`SELECT * FROM mission_steps WHERE mission_id = ? AND step = ?`) " +
			"and delete the obsolete ones (e.g., keeping the one with the lowest id). " +
			"Restart the service to create the unique index and enable upsert semantics."
		return errors.New(msg)
	}

	// No duplicates found. Create the unique index.
	if _, err := db.Exec(`
		CREATE UNIQUE INDEX idx_mission_steps_unique_key
		ON mission_steps(mission_id, step)
	`); err != nil {
		return fmt.Errorf("create unique index: %w", err)
	}

	return nil
}

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
	if err == sql.ErrNoRows {
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

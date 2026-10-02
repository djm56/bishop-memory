package store

import (
	"path/filepath"
	"testing"
)

// TestUpgradeFromPreTriageDatabase reproduces the boot path on a database
// created before findings.harness and the triage tables existed: the
// findings table is present without the column, ApplySchema runs first (so
// nothing in db/schema.sql may reference the missing column), then
// EnsureColumns adds it and its index. The first cut of the triage schema
// declared the index in schema.sql and failed exactly here.
func TestUpgradeFromPreTriageDatabase(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE findings (
		id INTEGER PRIMARY KEY AUTOINCREMENT, finding_date TEXT, target TEXT,
		suggestion TEXT NOT NULL, rationale TEXT, status TEXT NOT NULL DEFAULT 'proposed',
		approver TEXT, date_approved TEXT, mission_id TEXT,
		created_at TEXT NOT NULL DEFAULT (CURRENT_TIMESTAMP),
		CHECK (status IN ('proposed','approved','applied','rejected','retired','superseded')))`); err != nil {
		t.Fatalf("create legacy findings: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO findings (suggestion) VALUES ('legacy row')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := ApplySchema(db, filepath.Join("..", "..", "db", "schema.sql")); err != nil {
		t.Fatalf("ApplySchema on a pre-triage database must succeed: %v", err)
	}
	if err := EnsureColumns(db); err != nil {
		t.Fatalf("EnsureColumns: %v", err)
	}
	if err := EnsureColumns(db); err != nil {
		t.Fatalf("EnsureColumns second run must be a no-op: %v", err)
	}

	for _, column := range []string{"harness", "decision_note"} {
		has, err := columnExists(db, "findings", column)
		if err != nil || !has {
			t.Fatalf("findings.%s after upgrade: has=%v err=%v", column, has, err)
		}
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_findings_harness'`).Scan(&name); err != nil {
		t.Fatalf("idx_findings_harness missing after upgrade: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM findings`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("legacy row lost: n=%d err=%v", n, err)
	}
}

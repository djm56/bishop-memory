package store

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEnsureTriageRunKindsWidensOldTable boots a database whose triage_runs
// predates the mission grader (CHECK admits classify and process only), with a
// gap in the run ids and a classification pointing at one run, and checks the
// rebuild keeps every row, id and reference, then admits 'grade'.
func TestEnsureTriageRunKindsWidensOldTable(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE triage_runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, category TEXT, model TEXT,
		status TEXT NOT NULL DEFAULT 'running', considered INTEGER NOT NULL DEFAULT 0,
		written INTEGER NOT NULL DEFAULT 0, notes TEXT,
		started_at TEXT NOT NULL DEFAULT (CURRENT_TIMESTAMP), finished_at TEXT,
		CHECK (kind IN ('classify', 'process')),
		CHECK (status IN ('running', 'done', 'failed')))`); err != nil {
		t.Fatalf("create legacy triage_runs: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO triage_runs (id, kind, status, notes) VALUES (1, 'classify', 'done', 'first')`,
		`INSERT INTO triage_runs (id, kind, category, status) VALUES (7, 'process', 'brief-writing', 'done')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO triage_runs (kind) VALUES ('grade')`); err == nil {
		t.Fatal("legacy table accepted kind 'grade'; the test is not exercising the old CHECK")
	}

	if err := ApplySchema(db, filepath.Join("..", "..", "db", "schema.sql")); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if err := EnsureColumns(db); err != nil {
		t.Fatalf("EnsureColumns: %v", err)
	}
	// A classification referencing run 7 must still resolve after the rebuild.
	for _, statement := range []string{
		`INSERT INTO finding_categories (slug, name, description) VALUES ('brief-writing', 'Brief writing', 'd')`,
		`INSERT INTO findings (suggestion) VALUES ('s')`,
		`INSERT INTO finding_triage (finding_id, category, classified_by, run_id) VALUES (1, 'brief-writing', 'm', 7)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed child rows: %v", err)
		}
	}

	if err := EnsureTriageRunKinds(db); err != nil {
		t.Fatalf("EnsureTriageRunKinds: %v", err)
	}
	if err := EnsureTriageRunKinds(db); err != nil {
		t.Fatalf("second EnsureTriageRunKinds must be a no-op: %v", err)
	}

	var ids string
	if err := db.QueryRow(`SELECT group_concat(id || ':' || kind || ':' || COALESCE(notes, category), ',') FROM triage_runs ORDER BY id`).Scan(&ids); err != nil {
		t.Fatalf("read rebuilt rows: %v", err)
	}
	if ids != "1:classify:first,7:process:brief-writing" {
		t.Fatalf("rows after rebuild = %q", ids)
	}
	result, err := db.Exec(`INSERT INTO triage_runs (kind) VALUES ('grade')`)
	if err != nil {
		t.Fatalf("rebuilt table refuses kind 'grade': %v", err)
	}
	if id, _ := result.LastInsertId(); id != 8 {
		t.Fatalf("next run id = %d, want 8 (AUTOINCREMENT must continue from the old rows)", id)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	dangling := rows.Next()
	rows.Close()
	if dangling {
		t.Fatal("foreign_key_check reports violations after the rebuild")
	}
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys after rebuild = %d (err %v), want 1", fk, err)
	}
	if _, err := db.Exec(`INSERT INTO finding_triage (finding_id, category, classified_by, run_id) VALUES (1, 'brief-writing', 'm', 999)
		ON CONFLICT(finding_id) DO UPDATE SET run_id = excluded.run_id`); err == nil {
		t.Fatal("a reference to a missing run was accepted: foreign keys are not enforced after the rebuild")
	}
}

// TestEnsureTriageRunKindsFreshDatabase checks a database created from the
// current schema is left alone.
func TestEnsureTriageRunKindsFreshDatabase(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "new.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := ApplySchema(db, filepath.Join("..", "..", "db", "schema.sql")); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	var before string
	_ = db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'triage_runs'`).Scan(&before)
	if err := EnsureTriageRunKinds(db); err != nil {
		t.Fatalf("EnsureTriageRunKinds: %v", err)
	}
	var after string
	_ = db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'triage_runs'`).Scan(&after)
	if before != after {
		t.Fatal("a current triage_runs was rebuilt")
	}
}

// TestTriageRunsDefinitionMatchesSchema keeps the rebuild's table definition
// identical to db/schema.sql's, so a rebuilt database and a fresh one agree.
func TestTriageRunsDefinitionMatchesSchema(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "db", "schema.sql"))
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	match := regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS triage_runs (\(.*?\n\));`).FindStringSubmatch(string(raw))
	if match == nil {
		t.Fatal("triage_runs not found in db/schema.sql")
	}
	normalise := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	want := normalise(match[1])
	got := normalise(strings.TrimPrefix(triageRunsTable, "CREATE TABLE triage_runs_rebuild "))
	if got != want {
		t.Fatalf("rebuild definition differs from db/schema.sql:\n got %s\nwant %s", got, want)
	}
}

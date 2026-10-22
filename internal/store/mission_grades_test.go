package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// legacyMissionGradesTable is mission_grades as db/schema.sql defined it before
// the 'excluded' state: four states admitted and no exclusion CHECK. The
// migration test stands the table back at this definition.
const legacyMissionGradesTable = `CREATE TABLE mission_grades (
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

    CHECK (state IN ('pending', 'graded', 'insufficient', 'skipped')),
    CHECK (grade IS NULL OR grade IN ('A', 'B', 'C', 'D', 'E', 'F')),
    CHECK (state <> 'graded' OR grade IS NOT NULL)
)`

// schemaPath is db/schema.sql, relative to this package's directory.
var schemaPath = filepath.Join("..", "..", "db", "schema.sql")

// dumpMissionGrades renders every mission_grades row, one per line in
// mission_id order, each column through SQL's quote() so a NULL and the text
// 'NULL' render differently. Two dumps are equal only when the rows are.
func dumpMissionGrades(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT quote(mission_id), quote(state), quote(grade), quote(summary),
		quote(suggestions), quote(signals), quote(graded_by), quote(run_id), quote(attempts),
		quote(claimed_at), quote(graded_at), quote(created_at)
		FROM mission_grades ORDER BY mission_id`)
	if err != nil {
		t.Fatalf("dump mission_grades: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		cols := make([]string, 12)
		dest := make([]any, len(cols))
		for i := range cols {
			dest[i] = &cols[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatalf("scan mission_grades row: %v", err)
		}
		lines = append(lines, strings.Join(cols, " | "))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate mission_grades: %v", err)
	}
	return strings.Join(lines, "\n")
}

// scalarInt runs a query that returns one integer.
func scalarInt(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

// missionGradesTableSQL returns the stored CREATE TABLE text of mission_grades.
func missionGradesTableSQL(t *testing.T, db *sql.DB) string {
	t.Helper()
	var definition string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'mission_grades'`).Scan(&definition); err != nil {
		t.Fatalf("read mission_grades definition: %v", err)
	}
	return definition
}

// TestEnsureMissionGradeExcludedWidensOldTable boots a database whose
// mission_grades predates the exclusion, with one row in each state the old
// table admits, and checks the rebuild keeps every row and column, recreates the
// index, admits 'excluded' with no grade, and refuses 'excluded' with one.
func TestEnsureMissionGradeExcludedWidensOldTable(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := ApplySchema(db, schemaPath); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if err := EnsureColumns(db); err != nil {
		t.Fatalf("EnsureColumns: %v", err)
	}
	// Stand the table back at its pre-exclusion definition, with the index it
	// had. Everything else is current, so only this table is under test.
	for _, statement := range []string{
		`DROP TABLE mission_grades`,
		legacyMissionGradesTable,
		`CREATE INDEX idx_mission_grades_state ON mission_grades(state)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("stand back the legacy table: %v", err)
		}
	}

	// Seed a run, the missions, and one row per state the old table admits.
	// Every column carries a value, so a column the copy drops shows up.
	seed := []string{
		`INSERT INTO triage_runs (id, kind, status) VALUES (3, 'grade', 'done')`,
		`INSERT INTO missions (id, title, status, outcome) VALUES ('mission-20261001-01', 'a', 'complete', 'done')`,
		`INSERT INTO missions (id, title, status, outcome) VALUES ('mission-20261001-02', 'b', 'complete', 'done')`,
		`INSERT INTO missions (id, title, status, outcome) VALUES ('mission-20261001-03', 'c', 'complete', 'done')`,
		`INSERT INTO missions (id, title, status, outcome) VALUES ('mission-20261001-04', 'd', 'complete', 'done')`,
		`INSERT INTO mission_grades (mission_id, state, grade, summary, suggestions, signals, graded_by, run_id, attempts, claimed_at, graded_at)
			VALUES ('mission-20261001-01', 'graded', 'B', 'Two of three.', 'Name the owner.', '{"steps":4}', 'model-x', 3, 1, '2026-10-01 12:00:00', '2026-10-01 12:05:00')`,
		`INSERT INTO mission_grades (mission_id, state, summary, graded_by, run_id, attempts, claimed_at, graded_at)
			VALUES ('mission-20261001-02', 'insufficient', 'no debrief', 'model-x', 3, 1, '2026-10-01 12:00:00', '2026-10-01 12:05:00')`,
		`INSERT INTO mission_grades (mission_id, state, run_id, attempts, claimed_at)
			VALUES ('mission-20261001-03', 'pending', 3, 1, '2026-10-01 12:00:00')`,
		`INSERT INTO mission_grades (mission_id, state, attempts, claimed_at)
			VALUES ('mission-20261001-04', 'skipped', 2, '2026-10-01 12:00:00')`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	before := dumpMissionGrades(t, db)
	if n := scalarInt(t, db, `SELECT COUNT(*) FROM mission_grades`); n != 4 {
		t.Fatalf("seeded %d rows, want 4", n)
	}
	// Control: the legacy table refuses 'excluded', so the test is exercising the old CHECK.
	if _, err := db.Exec(`INSERT INTO missions (id, title, status, outcome) VALUES ('mission-20261001-05', 'e', 'complete', 'done')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO mission_grades (mission_id, state) VALUES ('mission-20261001-05', 'excluded')`); err == nil {
		t.Fatal("legacy table accepted state 'excluded'; the test is not exercising the old CHECK")
	}

	if err := EnsureMissionGradeExcluded(db); err != nil {
		t.Fatalf("EnsureMissionGradeExcluded: %v", err)
	}
	if after := dumpMissionGrades(t, db); after != before {
		t.Fatalf("rows changed in the rebuild:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// A second run finds the table current: no rebuild, the same definition, the same rows.
	definition := missionGradesTableSQL(t, db)
	if err := EnsureMissionGradeExcluded(db); err != nil {
		t.Fatalf("second EnsureMissionGradeExcluded must be a no-op: %v", err)
	}
	if missionGradesTableSQL(t, db) != definition || dumpMissionGrades(t, db) != before {
		t.Fatal("a second run changed the table or its rows")
	}

	// The index was dropped with the old table and must be back.
	if n := scalarInt(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_mission_grades_state'`); n != 1 {
		t.Fatalf("idx_mission_grades_state count = %d after the rebuild, want 1", n)
	}

	// The widened CHECK. Excluded with no grade is admitted; with a grade it is
	// refused; a graded row is still admitted (control).
	for _, statement := range []string{
		`INSERT INTO missions (id, title, status, outcome) VALUES ('mission-20261001-06', 'f', 'complete', 'done')`,
		`INSERT INTO missions (id, title, status, outcome) VALUES ('mission-20261001-07', 'g', 'complete', 'done')`,
		`INSERT INTO missions (id, title, status, outcome) VALUES ('mission-20261001-08', 'h', 'complete', 'done')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO mission_grades (mission_id, state) VALUES ('mission-20261001-05', 'excluded')`); err != nil {
		t.Fatalf("rebuilt table refuses excluded with no grade: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO mission_grades (mission_id, state, grade) VALUES ('mission-20261001-06', 'excluded', 'A')`); err == nil {
		t.Fatal("rebuilt table admits excluded with a grade; the exclusion CHECK is missing")
	}
	if _, err := db.Exec(`INSERT INTO mission_grades (mission_id, state, grade) VALUES ('mission-20261001-07', 'graded', 'C')`); err != nil {
		t.Fatalf("rebuilt table refuses graded C (control): %v", err)
	}

	// Foreign keys stay enforced on the rebuilt table: a missing run is refused.
	if _, err := db.Exec(`INSERT INTO mission_grades (mission_id, state, run_id) VALUES ('mission-20261001-08', 'pending', 999)`); err == nil {
		t.Fatal("a reference to a missing run was accepted: foreign keys are not enforced after the rebuild")
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
}

// TestEnsureMissionGradeExcludedFreshDatabase checks that a database created
// from the current schema is left alone, and that its table admits 'excluded'
// with no grade (the positive control for the rebuilt table above).
func TestEnsureMissionGradeExcludedFreshDatabase(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "new.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := ApplySchema(db, schemaPath); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	before := missionGradesTableSQL(t, db)
	if err := EnsureMissionGradeExcluded(db); err != nil {
		t.Fatalf("EnsureMissionGradeExcluded: %v", err)
	}
	if missionGradesTableSQL(t, db) != before {
		t.Fatal("a current mission_grades was rebuilt")
	}
	if _, err := db.Exec(`INSERT INTO missions (id, title, status, outcome) VALUES ('mission-20261001-01', 'a', 'complete', 'done')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO mission_grades (mission_id, state) VALUES ('mission-20261001-01', 'excluded')`); err != nil {
		t.Fatalf("fresh database refuses state 'excluded' with no grade: %v", err)
	}
}

// TestMissionGradesDefinitionMatchesSchema keeps the rebuild's table definition
// identical to db/schema.sql's, so a rebuilt database and a fresh one agree.
func TestMissionGradesDefinitionMatchesSchema(t *testing.T) {
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	match := regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS mission_grades (\(.*?\n\));`).FindStringSubmatch(string(raw))
	if match == nil {
		t.Fatal("mission_grades not found in db/schema.sql")
	}
	normalise := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	want := normalise(match[1])
	got := normalise(strings.TrimPrefix(missionGradesTable, "CREATE TABLE mission_grades_rebuild "))
	if got != want {
		t.Fatalf("rebuild definition differs from db/schema.sql:\n got %s\nwant %s", got, want)
	}
}

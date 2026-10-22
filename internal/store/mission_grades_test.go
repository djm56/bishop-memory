package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// legacyMissionGradesTable is mission_grades as db/schema.sql defined it before
// the 'excluded' state: four states admitted and no exclusion CHECK. The
// migration tests stand the table back at this definition.
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

// missionGradeColumns names the columns missionGradeRows reads, in table order.
var missionGradeColumns = []string{"mission_id", "state", "grade", "summary", "suggestions",
	"signals", "graded_by", "run_id", "attempts", "claimed_at", "graded_at", "created_at"}

// missionGradeRows reads every mission_grades row, each cell through SQL's
// quote() so that NULL and the text 'NULL' differ, in mission_id order.
func missionGradeRows(t *testing.T, db *sql.DB) [][]string {
	t.Helper()
	quoted := make([]string, len(missionGradeColumns))
	for i, column := range missionGradeColumns {
		quoted[i] = "quote(" + column + ")"
	}
	rows, err := db.Query(`SELECT ` + strings.Join(quoted, ", ") + ` FROM mission_grades ORDER BY mission_id`)
	if err != nil {
		t.Fatalf("read mission_grades: %v", err)
	}
	defer rows.Close()
	var out [][]string
	for rows.Next() {
		cells := make([]string, len(missionGradeColumns))
		dest := make([]any, len(cells))
		for i := range cells {
			dest[i] = &cells[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatalf("scan mission_grades row: %v", err)
		}
		out = append(out, cells)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate mission_grades: %v", err)
	}
	return out
}

// firstRowDifference names the first cell at which two reads of mission_grades
// differ, by row index and column, or returns "" when they are equal.
func firstRowDifference(before, after [][]string) string {
	if len(before) != len(after) {
		return fmt.Sprintf("row count %d before, %d after", len(before), len(after))
	}
	for i := range before {
		for j := range before[i] {
			if before[i][j] != after[i][j] {
				return fmt.Sprintf("row %d column %s: before %s, after %s",
					i, missionGradeColumns[j], before[i][j], after[i][j])
			}
		}
	}
	return ""
}

// scalarInt runs a query that returns one integer and fails the test on error.
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

// seedMissions inserts finished missions with the given ids, so that a grade
// row may be written for each.
func seedMissions(t *testing.T, db *sql.DB, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := db.Exec(`INSERT INTO missions (id, title, status, outcome) VALUES (?, 'm', 'complete', 'done')`, id); err != nil {
			t.Fatalf("seed mission %s: %v", id, err)
		}
	}
}

// legacyGradesDB opens a database whose mission_grades predates the exclusion,
// with one row in each state the old table admits. Every other table is
// current, so only mission_grades is under test. Every column of every row
// holds a value, so a column the rebuild drops shows up in the comparison.
func legacyGradesDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := ApplySchema(db, schemaPath); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if err := EnsureColumns(db); err != nil {
		t.Fatalf("EnsureColumns: %v", err)
	}
	// Stand the table back at its pre-exclusion definition, with the index it
	// had. The DROP takes the index with it, so both are made again here.
	for _, statement := range []string{
		`DROP TABLE mission_grades`,
		legacyMissionGradesTable,
		`CREATE INDEX idx_mission_grades_state ON mission_grades(state)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("stand back the legacy table: %v", err)
		}
	}
	seed := []string{
		`INSERT INTO triage_runs (id, kind, status) VALUES (3, 'grade', 'done')`,
		`INSERT INTO mission_grades (mission_id, state, grade, summary, suggestions, signals, graded_by, run_id, attempts, claimed_at, graded_at)
			VALUES ('mission-20261001-01', 'graded', 'B', 'Two of three.', 'Name the owner.', '{"steps":4}', 'model-x', 3, 1, '2026-10-01 12:00:00', '2026-10-01 12:05:00')`,
		`INSERT INTO mission_grades (mission_id, state, summary, graded_by, run_id, attempts, claimed_at, graded_at)
			VALUES ('mission-20261001-02', 'insufficient', 'no debrief', 'model-x', 3, 1, '2026-10-01 12:00:00', '2026-10-01 12:05:00')`,
		`INSERT INTO mission_grades (mission_id, state, run_id, attempts, claimed_at)
			VALUES ('mission-20261001-03', 'pending', 3, 1, '2026-10-01 12:00:00')`,
		`INSERT INTO mission_grades (mission_id, state, attempts, claimed_at)
			VALUES ('mission-20261001-04', 'skipped', 2, '2026-10-01 12:00:00')`,
	}
	seedMissions(t, db, "mission-20261001-01", "mission-20261001-02", "mission-20261001-03",
		"mission-20261001-04", "mission-20261001-05", "mission-20261001-06",
		"mission-20261001-07", "mission-20261001-08")
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if n := scalarInt(t, db, `SELECT COUNT(*) FROM mission_grades`); n != 4 {
		t.Fatalf("seeded %d rows, want 4", n)
	}
	return db
}

// TestEnsureMissionGradeExcludedWidensOldTable runs the rebuild over a legacy
// database, one property per subtest, each on its own database.
func TestEnsureMissionGradeExcludedWidensOldTable(t *testing.T) {
	t.Run("legacy_table_refuses_excluded", func(t *testing.T) {
		db := legacyGradesDB(t)
		// Control: the old CHECK refuses 'excluded', so the other subtests exercise the rebuild.
		if _, err := db.Exec(`INSERT INTO mission_grades (mission_id, state) VALUES ('mission-20261001-05', 'excluded')`); err == nil {
			t.Fatal("legacy table accepted state 'excluded'; the test is not exercising the old CHECK")
		}
	})

	t.Run("rows_preserved", func(t *testing.T) {
		db := legacyGradesDB(t)
		before := missionGradeRows(t, db)
		if err := EnsureMissionGradeExcluded(db); err != nil {
			t.Fatalf("EnsureMissionGradeExcluded: %v", err)
		}
		if diff := firstRowDifference(before, missionGradeRows(t, db)); diff != "" {
			t.Fatalf("rows changed in the rebuild: %s", diff)
		}
	})

	t.Run("second_run_is_noop", func(t *testing.T) {
		db := legacyGradesDB(t)
		if err := EnsureMissionGradeExcluded(db); err != nil {
			t.Fatalf("EnsureMissionGradeExcluded: %v", err)
		}
		definition := missionGradesTableSQL(t, db)
		before := missionGradeRows(t, db)
		if err := EnsureMissionGradeExcluded(db); err != nil {
			t.Fatalf("second EnsureMissionGradeExcluded must be a no-op: %v", err)
		}
		if missionGradesTableSQL(t, db) != definition {
			t.Fatal("a second run rebuilt the table")
		}
		if diff := firstRowDifference(before, missionGradeRows(t, db)); diff != "" {
			t.Fatalf("a second run changed rows: %s", diff)
		}
	})

	t.Run("index_recreated_on_state", func(t *testing.T) {
		db := legacyGradesDB(t)
		if err := EnsureMissionGradeExcluded(db); err != nil {
			t.Fatalf("EnsureMissionGradeExcluded: %v", err)
		}
		if n := scalarInt(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_mission_grades_state'`); n != 1 {
			t.Fatalf("idx_mission_grades_state count = %d after the rebuild, want 1", n)
		}
		var column string
		if err := db.QueryRow(`SELECT name FROM pragma_index_info('idx_mission_grades_state')`).Scan(&column); err != nil {
			t.Fatalf("read idx_mission_grades_state columns: %v", err)
		}
		if column != "state" {
			t.Fatalf("idx_mission_grades_state covers column %s after the rebuild, want state", column)
		}
	})

	t.Run("admits_excluded_without_grade", func(t *testing.T) {
		db := legacyGradesDB(t)
		if err := EnsureMissionGradeExcluded(db); err != nil {
			t.Fatalf("EnsureMissionGradeExcluded: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO mission_grades (mission_id, state) VALUES ('mission-20261001-05', 'excluded')`); err != nil {
			t.Fatalf("rebuilt table refuses excluded with no grade: %v", err)
		}
	})

	t.Run("refuses_excluded_with_grade", func(t *testing.T) {
		db := legacyGradesDB(t)
		if err := EnsureMissionGradeExcluded(db); err != nil {
			t.Fatalf("EnsureMissionGradeExcluded: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO mission_grades (mission_id, state, grade) VALUES ('mission-20261001-05', 'excluded', 'A')`); err == nil {
			t.Fatal("rebuilt table admits excluded with a grade; the exclusion CHECK is missing")
		}
	})

	t.Run("keeps_graded_admitted", func(t *testing.T) {
		// Control for the refusal above: the rebuilt CHECK still admits a verdict.
		db := legacyGradesDB(t)
		if err := EnsureMissionGradeExcluded(db); err != nil {
			t.Fatalf("EnsureMissionGradeExcluded: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO mission_grades (mission_id, state, grade) VALUES ('mission-20261001-05', 'graded', 'C')`); err != nil {
			t.Fatalf("rebuilt table refuses graded C (control): %v", err)
		}
	})

	t.Run("foreign_keys_enforced", func(t *testing.T) {
		db := legacyGradesDB(t)
		if err := EnsureMissionGradeExcluded(db); err != nil {
			t.Fatalf("EnsureMissionGradeExcluded: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO mission_grades (mission_id, state, run_id) VALUES ('mission-20261001-05', 'pending', 999)`); err == nil {
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
	})
}

// TestEnsureMissionGradeExcludedFreshDatabase checks a database created from
// the current schema. Its CHECK is read from db/schema.sql, so these subtests
// also guard the schema text, which the rebuild does not touch.
func TestEnsureMissionGradeExcludedFreshDatabase(t *testing.T) {
	fresh := func(t *testing.T) *sql.DB {
		t.Helper()
		db, err := Open(filepath.Join(t.TempDir(), "new.db"))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if err := ApplySchema(db, schemaPath); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}
		return db
	}

	t.Run("table_left_alone", func(t *testing.T) {
		db := fresh(t)
		before := missionGradesTableSQL(t, db)
		if err := EnsureMissionGradeExcluded(db); err != nil {
			t.Fatalf("EnsureMissionGradeExcluded: %v", err)
		}
		if missionGradesTableSQL(t, db) != before {
			t.Fatal("a current mission_grades was rebuilt")
		}
	})

	t.Run("admits_excluded_without_grade", func(t *testing.T) {
		db := fresh(t)
		seedMissions(t, db, "mission-20261001-01")
		if _, err := db.Exec(`INSERT INTO mission_grades (mission_id, state) VALUES ('mission-20261001-01', 'excluded')`); err != nil {
			t.Fatalf("fresh database refuses state 'excluded' with no grade: %v", err)
		}
	})

	t.Run("refuses_excluded_with_grade", func(t *testing.T) {
		db := fresh(t)
		seedMissions(t, db, "mission-20261001-01")
		if _, err := db.Exec(`INSERT INTO mission_grades (mission_id, state, grade) VALUES ('mission-20261001-01', 'excluded', 'A')`); err == nil {
			t.Fatal("fresh database admits state 'excluded' with a grade; the schema CHECK is missing")
		}
	})
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

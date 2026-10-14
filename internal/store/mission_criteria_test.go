package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// openSchemaDB opens a temporary database with db/schema.sql applied.
func openSchemaDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "criteria.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := ApplySchema(db, filepath.Join("..", "..", "db", "schema.sql")); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	return db
}

// TestApplySchemaAddsMissionCriteriaToExistingDatabase removes mission_criteria
// from a database that already holds a mission, as a database created before the
// table existed would, then reapplies the schema: the table must appear, accept
// rows, reject an unknown result, and the mission must be untouched.
func TestApplySchemaAddsMissionCriteriaToExistingDatabase(t *testing.T) {
	db := openSchemaDB(t)
	if _, err := db.Exec(`INSERT INTO missions (id, title, status) VALUES ('m-old', 'Old', 'complete')`); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE mission_criteria`); err != nil {
		t.Fatalf("drop mission_criteria: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='mission_criteria'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("precondition: mission_criteria present=%d err=%v, want absent", n, err)
	}

	if err := ApplySchema(db, filepath.Join("..", "..", "db", "schema.sql")); err != nil {
		t.Fatalf("ApplySchema on a database without mission_criteria: %v", err)
	}

	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='mission_criteria'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("after ApplySchema: mission_criteria present=%d err=%v, want 1", n, err)
	}
	if _, err := db.Exec(
		`INSERT INTO mission_criteria (mission_id, ord, criterion, result, evidence) VALUES ('m-old', 1, 'c', 'met', 'e')`,
	); err != nil {
		t.Fatalf("insert into recreated table: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO mission_criteria (mission_id, ord, criterion, result) VALUES ('m-old', 2, 'c', 'bogus')`,
	); err == nil {
		t.Errorf("insert with result 'bogus' succeeded, want CHECK constraint failure")
	}
	var title string
	if err := db.QueryRow(`SELECT title FROM missions WHERE id = 'm-old'`).Scan(&title); err != nil || title != "Old" {
		t.Errorf("existing mission after ApplySchema: title=%q err=%v", title, err)
	}
}

// TestMissionCriteriaCascadeOnMissionDelete deletes a mission that has criteria
// rows: its rows must go with it, and another mission's rows must stay.
func TestMissionCriteriaCascadeOnMissionDelete(t *testing.T) {
	db := openSchemaDB(t)
	for _, id := range []string{"m-a", "m-b"} {
		if _, err := db.Exec(`INSERT INTO missions (id, title, status) VALUES (?, 'T', 'in-progress')`, id); err != nil {
			t.Fatalf("seed mission %s: %v", id, err)
		}
		for ord := 1; ord <= 2; ord++ {
			if _, err := db.Exec(`INSERT INTO mission_criteria (mission_id, ord, criterion) VALUES (?, ?, 'c')`, id, ord); err != nil {
				t.Fatalf("seed criteria %s/%d: %v", id, ord, err)
			}
		}
	}

	if _, err := db.Exec(`DELETE FROM missions WHERE id = 'm-a'`); err != nil {
		t.Fatalf("delete mission m-a: %v", err)
	}

	count := func(id string) int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM mission_criteria WHERE mission_id = ?`, id).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", id, err)
		}
		return n
	}
	if got := count("m-a"); got != 0 {
		t.Errorf("m-a criteria after mission delete: got %d, want 0 (cascade)", got)
	}
	if got := count("m-b"); got != 2 {
		t.Errorf("m-b criteria after deleting m-a: got %d, want 2", got)
	}
}

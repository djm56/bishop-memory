package store

import (
	"path/filepath"
	"testing"
)

// TestMissionLessonsSchema applies the schema twice to one database: rows in
// mission_lessons survive the second application, a kind outside the three
// allowed is refused, and deleting the mission deletes its lessons.
func TestMissionLessonsSchema(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "lessons.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema := filepath.Join("..", "..", "db", "schema.sql")
	if err := ApplySchema(db, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO missions (id, title, status) VALUES ('m-1', 'One', 'complete')`); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO mission_lessons (mission_id, kind, ord, text, why, correction) VALUES
		('m-1', 'wrong-assumption', 1, 'a', 'b', 'c'),
		('m-1', 'similar-mission', 1, 't', NULL, NULL),
		('m-1', 'reuse-step', 1, 's', NULL, NULL)`); err != nil {
		t.Fatalf("insert the three allowed kinds: %v", err)
	}
	if err := ApplySchema(db, schema); err != nil {
		t.Fatalf("second ApplySchema: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_lessons WHERE mission_id = 'm-1'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("rows after second ApplySchema: n=%d err=%v, want 3", n, err)
	}
	if _, err := db.Exec(`INSERT INTO mission_lessons (mission_id, kind, ord, text) VALUES ('m-1', 'other', 1, 'x')`); err == nil {
		t.Errorf("kind 'other' was accepted, want the CHECK constraint to refuse it")
	}
	if _, err := db.Exec(`DELETE FROM missions WHERE id = 'm-1'`); err != nil {
		t.Fatalf("delete mission: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_lessons WHERE mission_id = 'm-1'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("rows after deleting the mission: n=%d err=%v, want 0", n, err)
	}
}

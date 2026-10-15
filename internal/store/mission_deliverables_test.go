package store

import (
	"path/filepath"
	"testing"
)

// TestApplySchemaAddsMissionDeliverablesTableToExistingDatabase removes
// mission_deliverables from a database that already holds a mission, as a database
// created before the table existed would, then reapplies the schema: the table must
// appear, accept rows, and the mission must be untouched.
func TestApplySchemaAddsMissionDeliverablesTableToExistingDatabase(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "deliverables.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := ApplySchema(db, filepath.Join("..", "..", "db", "schema.sql")); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO missions (id, title, status) VALUES ('m-existing', 'Existing', 'complete')`); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE mission_deliverables`); err != nil {
		t.Fatalf("drop mission_deliverables: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='mission_deliverables'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("precondition: mission_deliverables present=%d err=%v, want absent", n, err)
	}

	if err := ApplySchema(db, filepath.Join("..", "..", "db", "schema.sql")); err != nil {
		t.Fatalf("ApplySchema on a database without mission_deliverables: %v", err)
	}

	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='mission_deliverables'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("after ApplySchema: mission_deliverables present=%d err=%v, want 1", n, err)
	}

	if _, err := db.Exec(
		`INSERT INTO mission_deliverables (mission_id, ord, path, note) VALUES ('m-existing', 1, 'file1.go', 'Added new function')`,
	); err != nil {
		t.Fatalf("insert with path: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO mission_deliverables (mission_id, ord, path, note) VALUES ('m-existing', 2, NULL, 'Updated documentation')`,
	); err != nil {
		t.Fatalf("insert with NULL path: %v", err)
	}

	var title string
	if err := db.QueryRow(`SELECT title FROM missions WHERE id = 'm-existing'`).Scan(&title); err != nil || title != "Existing" {
		t.Errorf("existing mission after ApplySchema: title=%q err=%v", title, err)
	}
}

// TestMissionDeliverablesSchemaIdempotence applies schema, inserts rows, applies
// schema again without dropping, and asserts the rows survive.
func TestMissionDeliverablesSchemaIdempotence(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "deliverables_idempotence.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := ApplySchema(db, filepath.Join("..", "..", "db", "schema.sql")); err != nil {
		t.Fatalf("ApplySchema first time: %v", err)
	}

	// Insert a mission and deliverable rows.
	if _, err := db.Exec(`INSERT INTO missions (id, title, status) VALUES ('m-test', 'Test', 'in-progress')`); err != nil {
		t.Fatalf("insert mission: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO mission_deliverables (mission_id, ord, path, note) VALUES (?, ?, ?, ?)`,
		"m-test", 1, "file1.go", "note1",
	); err != nil {
		t.Fatalf("insert deliverable 1: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO mission_deliverables (mission_id, ord, path, note) VALUES (?, ?, ?, ?)`,
		"m-test", 2, nil, "note2",
	); err != nil {
		t.Fatalf("insert deliverable 2: %v", err)
	}

	// Reapply schema without dropping.
	if err := ApplySchema(db, filepath.Join("..", "..", "db", "schema.sql")); err != nil {
		t.Fatalf("ApplySchema second time: %v", err)
	}

	// Check that deliverables survive.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_deliverables WHERE mission_id = 'm-test'`).Scan(&count); err != nil {
		t.Fatalf("count deliverables: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 deliverables after schema reapply, got %d", count)
	}

	// Check specific rows.
	var path1, note1, note2 string
	var path2 interface{}
	if err := db.QueryRow(
		`SELECT path, note FROM mission_deliverables WHERE mission_id = 'm-test' AND ord = 1`,
	).Scan(&path1, &note1); err != nil {
		t.Fatalf("query deliverable 1: %v", err)
	}
	if path1 != "file1.go" || note1 != "note1" {
		t.Errorf("deliverable 1 mismatch: path=%q note=%q", path1, note1)
	}
	if err := db.QueryRow(
		`SELECT path, note FROM mission_deliverables WHERE mission_id = 'm-test' AND ord = 2`,
	).Scan(&path2, &note2); err != nil {
		t.Fatalf("query deliverable 2: %v", err)
	}
	if path2 != nil || note2 != "note2" {
		t.Errorf("deliverable 2 mismatch: path=%v note=%q", path2, note2)
	}
}

// TestMissionDeliverablesCascadeOnMissionDelete deletes a mission that has
// deliverable rows: its rows must go with it, and another mission's rows must stay.
func TestMissionDeliverablesCascadeOnMissionDelete(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "deliverables_cascade.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := ApplySchema(db, filepath.Join("..", "..", "db", "schema.sql")); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	for _, id := range []string{"m-a", "m-b"} {
		if _, err := db.Exec(`INSERT INTO missions (id, title, status) VALUES (?, 'T', 'in-progress')`, id); err != nil {
			t.Fatalf("seed mission %s: %v", id, err)
		}
		for ord := 1; ord <= 2; ord++ {
			path := "file.go"
			if ord == 2 {
				path = ""
			}
			var pathVal interface{} = path
			if path == "" {
				pathVal = nil
			}
			if _, err := db.Exec(
				`INSERT INTO mission_deliverables (mission_id, ord, path, note) VALUES (?, ?, ?, ?)`,
				id, ord, pathVal, "note",
			); err != nil {
				t.Fatalf("seed deliverable %s/%d: %v", id, ord, err)
			}
		}
	}

	if _, err := db.Exec(`DELETE FROM missions WHERE id = 'm-a'`); err != nil {
		t.Fatalf("delete mission m-a: %v", err)
	}

	count := func(id string) int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM mission_deliverables WHERE mission_id = ?`, id).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", id, err)
		}
		return n
	}
	if got := count("m-a"); got != 0 {
		t.Errorf("m-a deliverables after mission delete: got %d, want 0 (cascade)", got)
	}
	if got := count("m-b"); got != 2 {
		t.Errorf("m-b deliverables after deleting m-a: got %d, want 2", got)
	}
}

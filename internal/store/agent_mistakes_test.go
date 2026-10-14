package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestApplySchemaAddsAgentMistakesToExistingDatabase removes agent_mistakes
// from a database that already holds a mission, as a database created before the
// table existed would, then reapplies the schema: the table must appear, accept
// rows, reject an invalid impact value, and the mission must be untouched.
func TestApplySchemaAddsAgentMistakesToExistingDatabase(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "mistakes.db"))
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
	if _, err := db.Exec(`DROP TABLE agent_mistakes`); err != nil {
		t.Fatalf("drop agent_mistakes: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='agent_mistakes'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("precondition: agent_mistakes present=%d err=%v, want absent", n, err)
	}

	if err := ApplySchema(db, filepath.Join("..", "..", "db", "schema.sql")); err != nil {
		t.Fatalf("ApplySchema on a database without agent_mistakes: %v", err)
	}

	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='agent_mistakes'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("after ApplySchema: agent_mistakes present=%d err=%v, want 1", n, err)
	}
	if _, err := db.Exec(
		`INSERT INTO agent_mistakes (mission_id, ord, agent, agent_raw, mistake, impact, correction, prevention) VALUES ('m-existing', 1, 'hicks', '@hicks', 'test', 'low', 'fix', 'prevent')`,
	); err != nil {
		t.Fatalf("insert with impact 'low': %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO agent_mistakes (mission_id, ord, agent, agent_raw, mistake, impact) VALUES ('m-existing', 2, 'apone', '@apone', 'test', 'med')`,
	); err != nil {
		t.Fatalf("insert with impact 'med': %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO agent_mistakes (mission_id, ord, agent, agent_raw, mistake, impact) VALUES ('m-existing', 3, 'ripley', '@ripley', 'test', 'high')`,
	); err != nil {
		t.Fatalf("insert with impact 'high': %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO agent_mistakes (mission_id, ord, agent, agent_raw, mistake, impact) VALUES ('m-existing', 4, 'bishop', '@bishop', 'test', NULL)`,
	); err != nil {
		t.Fatalf("insert with impact NULL: %v", err)
	}
	// 'medium' is not a stored value (the parser normalises it to 'med'). The insert must
	// fail because of the CHECK constraint, not for some other reason: the error text
	// must name CHECK, and the identical row with impact 'low' must then be accepted,
	// so the impact value is the only difference between the rejected and accepted row.
	_, err = db.Exec(
		`INSERT INTO agent_mistakes (mission_id, ord, agent, agent_raw, mistake, impact) VALUES ('m-existing', 5, 'lambert', '@lambert', 'test', 'medium')`,
	)
	if err == nil {
		t.Errorf("insert with impact 'medium' succeeded, want CHECK constraint failure")
	} else if !strings.Contains(err.Error(), "CHECK") {
		t.Errorf("insert with impact 'medium' failed, but not on the CHECK constraint: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO agent_mistakes (mission_id, ord, agent, agent_raw, mistake, impact) VALUES ('m-existing', 5, 'lambert', '@lambert', 'test', 'low')`,
	); err != nil {
		t.Errorf("control: the same row with impact 'low' was rejected: %v", err)
	}
	var title string
	if err := db.QueryRow(`SELECT title FROM missions WHERE id = 'm-existing'`).Scan(&title); err != nil || title != "Existing" {
		t.Errorf("existing mission after ApplySchema: title=%q err=%v", title, err)
	}
}

// TestAgentMistakesCascadeOnMissionDelete deletes a mission that has mistake
// rows: its rows must go with it, and another mission's rows must stay.
func TestAgentMistakesCascadeOnMissionDelete(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "mistakes_cascade.db"))
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
			if _, err := db.Exec(
				`INSERT INTO agent_mistakes (mission_id, ord, agent, agent_raw, mistake, impact) VALUES (?, ?, 'test', '@test', 'mistake', 'low')`,
				id, ord,
			); err != nil {
				t.Fatalf("seed mistake %s/%d: %v", id, ord, err)
			}
		}
	}

	if _, err := db.Exec(`DELETE FROM missions WHERE id = 'm-a'`); err != nil {
		t.Fatalf("delete mission m-a: %v", err)
	}

	count := func(id string) int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM agent_mistakes WHERE mission_id = ?`, id).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", id, err)
		}
		return n
	}
	if got := count("m-a"); got != 0 {
		t.Errorf("m-a mistakes after mission delete: got %d, want 0 (cascade)", got)
	}
	if got := count("m-b"); got != 2 {
		t.Errorf("m-b mistakes after deleting m-a: got %d, want 2", got)
	}
}

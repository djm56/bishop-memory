package api

import (
	"testing"
)

// TestHUDMissionDetailAgentMistakes drives GET /v1/hud/missions/:id through the
// router with three agent_mistakes rows inserted in the order ord 3, 1, 2,
// whose mistake texts sort alphabetically as ord 2, 3, 1. Each subtest asserts
// one property of the "agent_mistakes" key:
//
//   - OrderedByOrd: the ord sequence is exactly 1, 2, 3 and each mistake text
//     is the one inserted with that ord. The query's `ORDER BY ord` is pinned
//     against `ORDER BY ord DESC` (gives 3,2,1), `ORDER BY mistake`
//     (gives 2,3,1) and `ORDER BY rowid` (insertion order, gives 3,1,2).
//     Removing ORDER BY ord does not change the result here: with
//     WHERE mission_id = ? SQLite currently returns rows in PRIMARY KEY
//     (mission_id, ord) order, so no test in this file can tell the two apart.
//   - FieldsAndNulls: one row has impact NULL and impact_note NULL → both are
//     JSON null in the object; one row has impact 'med' and impact_note "note text"
//     → both are strings; the agent and agent_raw fields are both returned for
//     all rows.
//   - OnlyThisMission: rows from a second mission must not appear in the results.
func TestHUDMissionDetailAgentMistakes(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-mistakes", "test", "in-progress")
	insertMission(t, db, "m-other-mission", "test", "in-progress")

	seed := []struct {
		ord      int
		agent    string
		agentRaw string
		mistake  string
		impact   any
		note     any
	}{
		{3, "hicks", "@hicks", "Mid", nil, nil},
		{1, "bishop", "@Bishop", "Zeta", "med", "note text"},
		{2, "lambert", "@lambert", "Alpha", "low", nil},
	}
	for _, s := range seed {
		if _, err := db.Exec(
			`INSERT INTO agent_mistakes (mission_id, ord, agent, agent_raw, mistake, impact, impact_note, correction, prevention)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"m-mistakes", s.ord, s.agent, s.agentRaw, s.mistake, s.impact, s.note, "correction", "prevention",
		); err != nil {
			t.Fatalf("insert agent_mistake ord %d: %v", s.ord, err)
		}
	}

	// Seed a row for the other mission to verify filtering.
	if _, err := db.Exec(
		`INSERT INTO agent_mistakes (mission_id, ord, agent, agent_raw, mistake, impact, impact_note, correction, prevention)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"m-other-mission", 1, "ripley", "@ripley", "Other", "high", nil, "correction", "prevention",
	); err != nil {
		t.Fatalf("insert other mission's agent_mistake: %v", err)
	}

	detail := hudGet(t, router, "m-mistakes")
	mistakes := hudDetailArray(t, detail, "agent_mistakes")

	t.Run("OrderedByOrd", func(t *testing.T) {
		if len(mistakes) != 3 {
			t.Fatalf("agent_mistakes has %d rows, want 3; value: %v", len(mistakes), mistakes)
		}
		wantMistake := []string{"Zeta", "Alpha", "Mid"}
		for i, wantOrd := range []float64{1, 2, 3} {
			row := hudRow(t, "agent_mistakes", mistakes, i)
			gotOrd, ok := row["ord"].(float64)
			if !ok {
				t.Fatalf("agent_mistakes[%d].ord is %T, want JSON number (float64); value: %v", i, row["ord"], row["ord"])
			}
			if gotOrd != wantOrd {
				t.Errorf("agent_mistakes order wrong: agent_mistakes[%d].ord = %v, want %v", i, gotOrd, wantOrd)
			}
			if row["mistake"] != wantMistake[i] {
				t.Errorf("agent_mistakes order wrong: agent_mistakes[%d].mistake = %v, want %q", i, row["mistake"], wantMistake[i])
			}
		}
	})

	t.Run("FieldsAndNulls", func(t *testing.T) {
		// Row with ord 1 should have impact 'med' and a note.
		row := hudRowByOrd(t, "agent_mistakes", mistakes, 1)
		if row["agent"] != "bishop" {
			t.Errorf("ord 1 agent = %v, want %q", row["agent"], "bishop")
		}
		if row["agent_raw"] != "@Bishop" {
			t.Errorf("ord 1 agent_raw = %v, want %q", row["agent_raw"], "@Bishop")
		}
		if row["impact"] != "med" {
			t.Errorf("ord 1 impact = %v, want %q", row["impact"], "med")
		}
		if row["impact_note"] != "note text" {
			t.Errorf("ord 1 impact_note = %v, want %q", row["impact_note"], "note text")
		}

		// Row with ord 3 should have NULL impact and impact_note.
		row = hudRowByOrd(t, "agent_mistakes", mistakes, 3)
		if row["agent"] != "hicks" {
			t.Errorf("ord 3 agent = %v, want %q", row["agent"], "hicks")
		}
		if row["agent_raw"] != "@hicks" {
			t.Errorf("ord 3 agent_raw = %v, want %q", row["agent_raw"], "@hicks")
		}
		for _, key := range []string{"impact", "impact_note"} {
			v, ok := row[key]
			if !ok {
				t.Errorf("ord 3 row has no %q key; row: %v", key, row)
			} else if v != nil {
				t.Errorf("ord 3 row %s = %v (%T), want JSON null", key, v, v)
			}
		}
	})

	t.Run("OnlyThisMission", func(t *testing.T) {
		// Verify no rows from other missions appear.
		hasMid := false
		for i := range mistakes {
			row := hudRow(t, "agent_mistakes", mistakes, i)
			if row["mistake"] == "Other" {
				t.Errorf("agent_mistakes contains row from other mission: %v", row)
			}
			if row["mistake"] == "Mid" {
				hasMid = true
			}
		}
		if !hasMid {
			t.Errorf("agent_mistakes missing expected row with mistake %q", "Mid")
		}
	})
}

// TestHUDMissionDetailAgentMistakesEmptyArray drives the same endpoint for a
// mission with no agent_mistakes rows and asserts that the decoded
// "agent_mistakes" value is a JSON array (not null, not absent) of length 0.
// The positive control through the same path is TestHUDMissionDetailAgentMistakes,
// where the key carries rows.
func TestHUDMissionDetailAgentMistakesEmptyArray(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-no-mistakes", "test", "in-progress")

	detail := hudGet(t, router, "m-no-mistakes")
	mistakes := hudDetailArray(t, detail, "agent_mistakes")
	if len(mistakes) != 0 {
		t.Fatalf("agent_mistakes has %d rows, want 0; value: %v", len(mistakes), mistakes)
	}
}

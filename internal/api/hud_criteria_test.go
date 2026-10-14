package api

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

// hudDetailMap returns detail[key] as a JSON object, failing the test with the
// actual Go type when the value is absent or is not an object.
func hudDetailMap(t *testing.T, detail map[string]any, key string) map[string]any {
	t.Helper()
	raw, ok := detail[key]
	if !ok {
		t.Fatalf("detail has no %q key", key)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("detail[%q] is %T, want JSON object (map[string]any); value: %v", key, raw, raw)
	}
	return m
}

// hudDetailArray returns detail[key] as a JSON array, failing the test with
// the actual Go type when the value is absent or is not an array.
func hudDetailArray(t *testing.T, detail map[string]any, key string) []any {
	t.Helper()
	raw, ok := detail[key]
	if !ok {
		t.Fatalf("detail has no %q key", key)
	}
	a, ok := raw.([]any)
	if !ok {
		t.Fatalf("detail[%q] is %T, want JSON array ([]any); value: %v", key, raw, raw)
	}
	return a
}

// hudRow returns arr[i] as a JSON object, failing the test with the actual Go
// type when the element is not an object.
func hudRow(t *testing.T, name string, arr []any, i int) map[string]any {
	t.Helper()
	m, ok := arr[i].(map[string]any)
	if !ok {
		t.Fatalf("%s[%d] is %T, want JSON object (map[string]any); value: %v", name, i, arr[i], arr[i])
	}
	return m
}

// hudRowByOrd returns the element from rows whose ord is want, so a subtest
// asserts the content of one seeded row independently of where ordering put
// it. It fails the test if no such element exists.
func hudRowByOrd(t *testing.T, name string, rows []any, want float64) map[string]any {
	t.Helper()
	for i := range rows {
		row := hudRow(t, name, rows, i)
		if got, ok := row["ord"].(float64); ok && got == want {
			return row
		}
	}
	t.Fatalf("no %s row with ord %v; %s: %v", name, want, name, rows)
	return nil
}

// hudGet performs GET /v1/hud/missions/<id> and fails unless the status is 200.
func hudGet(t *testing.T, router *gin.Engine, id string) map[string]any {
	t.Helper()
	code, detail := do(t, router, http.MethodGet, "/v1/hud/missions/"+id, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/hud/missions/%s: status %d, want 200; body: %v", id, code, detail)
	}
	return detail
}

// TestHUDMissionDetailCriteria drives GET /v1/hud/missions/:id through the
// router (the same entry point production uses) with three criteria rows
// inserted in the order ord 3, 1, 2, whose criterion texts sort alphabetically
// as ord 2, 3, 1. Each subtest asserts one property of the "criteria" key:
//
//   - OrderedByOrd: the ord sequence is exactly 1, 2, 3 and each criterion text
//     is the one inserted with that ord. The query's `ORDER BY ord` is pinned
//     against `ORDER BY ord DESC` (gives 3,2,1), `ORDER BY criterion`
//     (gives 2,3,1) and `ORDER BY rowid` (insertion order, gives 3,1,2). It
//     does NOT pin removal of ORDER BY: under PRIMARY KEY (mission_id, ord)
//     SQLite returns this lookup in ord order anyway, so that mutant is
//     equivalent and no test can kill it.
//   - NullResultAndEvidence: the row with ord 1 (located by ord, not by
//     position) has criterion "Zeta"; its "result" and "evidence" keys are both
//     present in the object and both JSON null (an absent key fails with
//     distinct text from a non-null value).
//   - GradedRow: the row with ord 3 (located by ord) has criterion "Mid",
//     result "met" and evidence "All integration tests pass".
func TestHUDMissionDetailCriteria(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-criteria", "test", "in-progress")

	seed := []struct {
		ord       int
		criterion string
		result    any
		evidence  any
	}{
		{3, "Mid", "met", "All integration tests pass"},
		{1, "Zeta", nil, nil},
		{2, "Alpha", nil, nil},
	}
	for _, s := range seed {
		if _, err := db.Exec(
			`INSERT INTO mission_criteria (mission_id, ord, criterion, result, evidence) VALUES (?, ?, ?, ?, ?)`,
			"m-criteria", s.ord, s.criterion, s.result, s.evidence,
		); err != nil {
			t.Fatalf("insert criterion ord %d: %v", s.ord, err)
		}
	}

	detail := hudGet(t, router, "m-criteria")
	criteria := hudDetailArray(t, detail, "criteria")
	if len(criteria) != 3 {
		t.Fatalf("criteria has %d rows, want 3; value: %v", len(criteria), criteria)
	}

	t.Run("OrderedByOrd", func(t *testing.T) {
		wantCriterion := []string{"Zeta", "Alpha", "Mid"}
		for i, wantOrd := range []float64{1, 2, 3} {
			row := hudRow(t, "criteria", criteria, i)
			gotOrd, ok := row["ord"].(float64)
			if !ok {
				t.Fatalf("criteria[%d].ord is %T, want JSON number (float64); value: %v", i, row["ord"], row["ord"])
			}
			if gotOrd != wantOrd {
				t.Errorf("criteria order wrong: criteria[%d].ord = %v, want %v", i, gotOrd, wantOrd)
			}
			if row["criterion"] != wantCriterion[i] {
				t.Errorf("criteria order wrong: criteria[%d].criterion = %v, want %q", i, row["criterion"], wantCriterion[i])
			}
		}
	})

	t.Run("NullResultAndEvidence", func(t *testing.T) {
		row := hudRowByOrd(t, "criteria", criteria, 1)
		if row["criterion"] != "Zeta" {
			t.Errorf("null-row criterion = %v, want %q", row["criterion"], "Zeta")
		}
		for _, key := range []string{"result", "evidence"} {
			v, ok := row[key]
			if !ok {
				t.Errorf("null-row has no %q key; row: %v", key, row)
			} else if v != nil {
				t.Errorf("null-row %s = %v (%T), want JSON null", key, v, v)
			}
		}
	})

	t.Run("GradedRow", func(t *testing.T) {
		row := hudRowByOrd(t, "criteria", criteria, 3)
		if row["criterion"] != "Mid" {
			t.Errorf("graded-row criterion = %v, want %q", row["criterion"], "Mid")
		}
		if row["result"] != "met" {
			t.Errorf("graded-row result = %v, want %q", row["result"], "met")
		}
		if row["evidence"] != "All integration tests pass" {
			t.Errorf("graded-row evidence = %v, want %q", row["evidence"], "All integration tests pass")
		}
	})
}

// TestHUDMissionDetailCriteriaIsEmptyArrayWhenNone drives the same endpoint
// for a mission with no mission_criteria rows and asserts that the decoded
// "criteria" value is a JSON array (not null, not absent) of length 0. The
// positive control through the same path is TestHUDMissionDetailCriteria,
// where the key carries rows.
func TestHUDMissionDetailCriteriaIsEmptyArrayWhenNone(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-no-criteria", "test", "in-progress")

	detail := hudGet(t, router, "m-no-criteria")
	criteria := hudDetailArray(t, detail, "criteria")
	if len(criteria) != 0 {
		t.Fatalf("criteria has %d rows, want 0; value: %v", len(criteria), criteria)
	}
}

// TestHUDMissionDetailExistingKeysUnchanged drives GET /v1/hud/missions/:id
// for an ungraded mission "m-existing" (one step "1"/"@hicks"/"done", one crew
// row "hicks") and a graded mission "m-graded", and asserts one property per
// subtest. Every subtest guards the keys that existed before "criteria" was
// added:
//
//   - mission: an object whose id is "m-existing".
//   - steps: an array of one object with step "1", agent "@hicks", status "done".
//   - crew: an array of one object with name "hicks".
//   - documents, events, patterns, directives, service_records: each a JSON array
//     (the fixture seeds no rows for them, so only the type is asserted).
//   - grade_null: grade is JSON null for the ungraded mission.
//   - grade_object: grade is an object with grade "B" for the graded mission.
func TestHUDMissionDetailExistingKeysUnchanged(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-existing", "test", "in-progress")
	insertMission(t, db, "m-graded", "test", "complete")
	if _, err := db.Exec(
		`INSERT INTO mission_steps (mission_id, step, agent, status) VALUES (?, ?, ?, ?)`,
		"m-existing", "1", "@hicks", "done",
	); err != nil {
		t.Fatalf("insert step: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO crew (name, role, description) VALUES (?, ?, ?)`,
		"hicks", "junior developer", "builds",
	); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO mission_grades (mission_id, state, grade, summary) VALUES (?, 'graded', 'B', 's')`,
		"m-graded",
	); err != nil {
		t.Fatalf("insert grade: %v", err)
	}

	detail := hudGet(t, router, "m-existing")
	gradedDetail := hudGet(t, router, "m-graded")

	t.Run("mission", func(t *testing.T) {
		m := hudDetailMap(t, detail, "mission")
		if m["id"] != "m-existing" {
			t.Errorf("mission.id = %v, want %q", m["id"], "m-existing")
		}
	})

	t.Run("steps", func(t *testing.T) {
		steps := hudDetailArray(t, detail, "steps")
		if len(steps) != 1 {
			t.Fatalf("steps has %d rows, want 1; value: %v", len(steps), steps)
		}
		row := hudRow(t, "steps", steps, 0)
		if row["step"] != "1" || row["agent"] != "@hicks" || row["status"] != "done" {
			t.Errorf("steps[0] = %v, want step=1 agent=@hicks status=done", row)
		}
	})

	t.Run("crew", func(t *testing.T) {
		crew := hudDetailArray(t, detail, "crew")
		if len(crew) != 1 {
			t.Fatalf("crew has %d rows, want 1; value: %v", len(crew), crew)
		}
		if row := hudRow(t, "crew", crew, 0); row["name"] != "hicks" {
			t.Errorf("crew[0].name = %v, want %q", row["name"], "hicks")
		}
	})

	for _, key := range []string{"documents", "events", "patterns", "directives", "service_records"} {
		t.Run(key, func(t *testing.T) {
			_ = hudDetailArray(t, detail, key)
		})
	}

	t.Run("grade_null", func(t *testing.T) {
		raw, ok := detail["grade"]
		if !ok {
			t.Fatalf("detail has no %q key", "grade")
		}
		if raw != nil {
			t.Errorf("ungraded mission: grade = %v (%T), want JSON null", raw, raw)
		}
	})

	t.Run("grade_object", func(t *testing.T) {
		g := hudDetailMap(t, gradedDetail, "grade")
		if g["grade"] != "B" {
			t.Errorf("graded mission: grade.grade = %v, want %q", g["grade"], "B")
		}
	})
}

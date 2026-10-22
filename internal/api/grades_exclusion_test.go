package api

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// Tests for the operator's exclusion (N/A) grade: the PUT and DELETE routes at
// /v1/mission-grades/:missionID/exclusion, and how the claim, the write, the
// list, the HUD and the performance figures treat an excluded row. Every
// request goes through the router with do(), the entry point the operator's
// client and mcpd use, so routing, validation and refusals are all exercised.

// exclusionPath is the operator's exclusion route for one mission.
func exclusionPath(id string) string {
	return "/v1/mission-grades/" + id + "/exclusion"
}

// excludeMission sets a mission aside through the operator route. It fails the
// test unless the route answers 200, and returns the response body.
func excludeMission(t *testing.T, router *gin.Engine, id, note string) map[string]any {
	t.Helper()
	code, out := do(t, router, http.MethodPut, exclusionPath(id), map[string]any{"note": note})
	if code != http.StatusOK {
		t.Fatalf("exclude %s: %d %v", id, code, out)
	}
	return out
}

// gradeRowOf returns the mission_grades row for id, keyed by column name, or
// nil when the mission has no row.
func gradeRowOf(t *testing.T, db *sql.DB, id string) map[string]any {
	t.Helper()
	rows, err := queryMaps(context.Background(), db,
		`SELECT state, grade, summary, suggestions, signals, graded_by, graded_at
		   FROM mission_grades WHERE mission_id = ?`, id)
	if err != nil {
		t.Fatalf("read grade row %s: %v", id, err)
	}
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

// hasID reports whether ids holds id.
func hasID(ids []string, id string) bool {
	for _, have := range ids {
		if have == id {
			return true
		}
	}
	return false
}

// seedOpenMission inserts a mission still in progress. The grader may not grade
// it, so the exclusion route must refuse it.
func seedOpenMission(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO missions (id, title, status) VALUES (?, 'open', 'in-progress')`, id); err != nil {
		t.Fatalf("seed open mission %s: %v", id, err)
	}
}

// performanceOf reads GET /v1/mission-grades/performance. It returns each
// harness's line keyed by harness name, and the overall line.
func performanceOf(t *testing.T, router *gin.Engine) (map[string]map[string]any, map[string]any) {
	t.Helper()
	code, out := do(t, router, http.MethodGet, "/v1/mission-grades/performance", nil)
	if code != http.StatusOK {
		t.Fatalf("performance: %d %v", code, out)
	}
	byName := map[string]map[string]any{}
	for _, h := range out["harnesses"].([]any) {
		row := h.(map[string]any)
		byName[row["harness"].(string)] = row
	}
	return byName, out["overall"].(map[string]any)
}

// TestExclusionRouteRefusals checks every way the exclusion routes refuse. Each
// refusal is followed by a request the same route accepts, so a refusal cannot
// pass merely because the route never answers at all.
func TestExclusionRouteRefusals(t *testing.T) {
	router, db := newTriageTestRouter(t)
	finished := "mission-20261022-01"
	open := "mission-20261022-02"
	unknown := "mission-20261022-99"
	seedGradableMission(t, db, finished, "kirsch", "complete", "done")
	seedOpenMission(t, db, open)

	// Each refusal states its error text as well as its status. gin answers any
	// unmatched route with a 404 of its own ("route not found"), so the status
	// alone cannot tell a refusal from a route that is not registered.
	refusals := []struct {
		name      string
		method    string
		id        string
		body      any
		want      int
		wantError string
	}{
		{"PUT_unknown_mission_404", http.MethodPut, unknown, map[string]any{"note": "n"}, http.StatusNotFound, "mission not found"},
		{"PUT_unfinished_mission_409", http.MethodPut, open, map[string]any{"note": "n"}, http.StatusConflict, "mission is not finished; only a finished mission can be excluded"},
		{"PUT_no_body_400", http.MethodPut, finished, nil, http.StatusBadRequest, "invalid request"},
		{"PUT_empty_note_400", http.MethodPut, finished, map[string]any{"note": ""}, http.StatusBadRequest, "invalid request"},
		{"PUT_whitespace_note_400", http.MethodPut, finished, map[string]any{"note": " \t "}, http.StatusBadRequest, "invalid request"},
		{"PUT_701_char_note_400", http.MethodPut, finished, map[string]any{"note": strings.Repeat("n", 701)}, http.StatusBadRequest, "invalid request"},
		{"DELETE_unknown_mission_404", http.MethodDelete, unknown, nil, http.StatusNotFound, "mission is not excluded"},
		{"DELETE_not_excluded_404", http.MethodDelete, finished, nil, http.StatusNotFound, "mission is not excluded"},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			code, out := do(t, router, tc.method, exclusionPath(tc.id), tc.body)
			if code != tc.want || out["error"] != tc.wantError {
				t.Errorf("%s %s = %d %v, want %d with error %q",
					tc.method, exclusionPath(tc.id), code, out, tc.want, tc.wantError)
			}
		})
	}
	for _, id := range []string{finished, open, unknown} {
		if row := gradeRowOf(t, db, id); row != nil {
			t.Errorf("a refused request wrote a row for %s: %v", id, row)
		}
	}

	// Controls: the same routes admit what the refusals turned away.
	if code, out := do(t, router, http.MethodPut, exclusionPath(finished),
		map[string]any{"note": strings.Repeat("n", 700)}); code != http.StatusOK {
		t.Fatalf("a 700-character note was refused: %d %v", code, out)
	}
	if code, out := do(t, router, http.MethodDelete, exclusionPath(finished), nil); code != http.StatusOK {
		t.Fatalf("lifting the exclusion: %d %v", code, out)
	}
	if code, out := do(t, router, http.MethodDelete, exclusionPath(finished), nil); code != http.StatusNotFound {
		t.Fatalf("lifting an exclusion twice = %d %v, want 404", code, out)
	}
}

// TestExclusionRouteReportsWhatItReplaced sets a mission aside over each state
// it can hold before. Each subtest starts from a row written directly, which is
// how the grader and the claim leave one. The response must name the state and
// grade the row held, and the row afterwards must carry the note and nothing
// of the old verdict.
func TestExclusionRouteReportsWhatItReplaced(t *testing.T) {
	prior := []struct {
		name  string
		setup string // run after the mission is seeded; "" leaves no row at all
		state any    // previous_state the response must name
		grade any    // previous_grade the response must name
	}{
		{"no_row", "", nil, nil},
		{"pending", `INSERT INTO mission_grades (mission_id, state, attempts) VALUES (?, 'pending', 1)`, "pending", nil},
		{"graded", `INSERT INTO mission_grades (mission_id, state, grade, summary, suggestions, signals, graded_by, graded_at)
			VALUES (?, 'graded', 'B', 'old summary', 'old advice', '{"steps":4}', 'model', CURRENT_TIMESTAMP)`, "graded", "B"},
		{"insufficient", `INSERT INTO mission_grades (mission_id, state, summary, graded_by, graded_at)
			VALUES (?, 'insufficient', 'old summary', 'model', CURRENT_TIMESTAMP)`, "insufficient", nil},
		{"skipped", `INSERT INTO mission_grades (mission_id, state, attempts) VALUES (?, 'skipped', 2)`, "skipped", nil},
	}
	for _, tc := range prior {
		t.Run(tc.name, func(t *testing.T) {
			router, db := newTriageTestRouter(t)
			id := "mission-20261022-21"
			seedGradableMission(t, db, id, "kirsch", "complete", "done")
			if tc.setup != "" {
				if _, err := db.Exec(tc.setup, id); err != nil {
					t.Fatalf("set up prior row: %v", err)
				}
			}

			out := excludeMission(t, router, id, "  test mission  ")
			if out["state"] != "excluded" || out["previous_state"] != tc.state || out["previous_grade"] != tc.grade {
				t.Fatalf("response = %v; want state excluded, previous_state %v, previous_grade %v",
					out, tc.state, tc.grade)
			}
			row := gradeRowOf(t, db, id)
			if row == nil {
				t.Fatal("no row after exclusion")
			}
			if row["state"] != "excluded" || row["grade"] != nil || row["suggestions"] != nil || row["signals"] != nil {
				t.Errorf("row = %v; an excluded row must hold no grade, suggestions or signals", row)
			}
			if row["summary"] != "test mission" || row["graded_by"] != "operator" || row["graded_at"] == nil {
				t.Errorf("row = %v; want the trimmed note in summary, graded_by operator, graded_at set", row)
			}

			if code, out := do(t, router, http.MethodDelete, exclusionPath(id), nil); code != http.StatusOK {
				t.Fatalf("lifting the exclusion: %d %v", code, out)
			}
			if row := gradeRowOf(t, db, id); row != nil {
				t.Errorf("lifting the exclusion left a row: %v; the mission must be ungraded", row)
			}
		})
	}
}

// TestExcludedMissionIsNeverClaimed checks that the grader's claim skips an
// excluded mission on the sweep, refuses it when it is named, and leaves it
// alone when a pending row with spent attempts is excluded.
func TestExcludedMissionIsNeverClaimed(t *testing.T) {
	router, db := newTriageTestRouter(t)
	excluded := "mission-20261022-11"
	control := "mission-20261022-12"
	control2 := "mission-20261022-13"
	spent := "mission-20261022-14"
	for _, id := range []string{excluded, control, control2, spent} {
		seedGradableMission(t, db, id, "kirsch", "complete", "done")
	}
	// A claim that was never answered and has used up its attempts. The sweep
	// on the next claim would mark it skipped; an exclusion must win that race.
	if _, err := db.Exec(`INSERT INTO mission_grades (mission_id, state, attempts) VALUES (?, 'pending', 2)`, spent); err != nil {
		t.Fatal(err)
	}
	excludeMission(t, router, excluded, "never claimed")
	excludeMission(t, router, spent, "spent attempts")

	// Presence control: the two unclaimed missions are waiting. Absence: neither excluded one is.
	if n := waiting(t, router); n != 2 {
		t.Fatalf("waiting = %d, want 2 (the excluded missions must not count)", n)
	}
	ids := claimIDs(t, router, map[string]any{"run_id": startGradeRun(t, router)})
	if len(ids) != 2 || !hasID(ids, control) || !hasID(ids, control2) {
		t.Fatalf("sweep claimed %v, want exactly %s and %s", ids, control, control2)
	}

	// Named explicitly, an excluded mission is refused with a reason that names the exclusion.
	for _, id := range []string{excluded, spent} {
		code, out := do(t, router, http.MethodPost, "/v1/mission-grades/claim", map[string]any{"mission_ids": []string{id}})
		if code != http.StatusConflict {
			t.Fatalf("explicit claim of excluded %s = %d %v, want 409", id, code, out)
		}
		refused, _ := out["refused"].([]any)
		if len(refused) != 1 {
			t.Fatalf("refused = %v; want one refusal for %s", refused, id)
		}
		reason, _ := refused[0].(map[string]any)["reason"].(string)
		if !strings.Contains(reason, "excluded") || !strings.Contains(reason, exclusionPath(id)) {
			t.Fatalf("reason = %q; want it to say the mission is excluded and name %s", reason, exclusionPath(id))
		}
	}

	// A refusal is all or nothing: the unexcluded mission named beside it is not claimed.
	unclaimed := "mission-20261022-15"
	seedGradableMission(t, db, unclaimed, "kirsch", "complete", "done")
	code, out := do(t, router, http.MethodPost, "/v1/mission-grades/claim",
		map[string]any{"mission_ids": []string{excluded, unclaimed}})
	if code != http.StatusConflict {
		t.Fatalf("mixed explicit claim = %d %v, want 409", code, out)
	}
	refused, _ := out["refused"].([]any)
	if len(refused) != 1 || refused[0].(map[string]any)["mission_id"] != excluded {
		t.Fatalf("mixed explicit claim refused %v, want only %s", refused, excluded)
	}
	if row := gradeRowOf(t, db, unclaimed); row != nil {
		t.Fatalf("a refused claim left a row for %s: %v", unclaimed, row)
	}

	// The exclusion held through the sweep: neither row was rewritten.
	for _, id := range []string{excluded, spent} {
		if row := gradeRowOf(t, db, id); row == nil || row["state"] != "excluded" {
			t.Fatalf("row for %s = %v after claims; want state excluded", id, row)
		}
	}

	// Lifting the exclusion makes the mission claimable again.
	if code, out := do(t, router, http.MethodDelete, exclusionPath(excluded), nil); code != http.StatusOK {
		t.Fatalf("lifting the exclusion: %d %v", code, out)
	}
	if ids := claimIDs(t, router, map[string]any{"mission_ids": []string{excluded}}); len(ids) != 1 || ids[0] != excluded {
		t.Fatalf("claim after the exclusion was lifted = %v, want %s", ids, excluded)
	}
}

// TestWriteNeverOverwritesExcludedRow checks that a verdict written for a
// mission that has been excluded is refused and the row keeps its exclusion,
// while a verdict for a mission still pending in the same batch is applied.
func TestWriteNeverOverwritesExcludedRow(t *testing.T) {
	router, db := newTriageTestRouter(t)
	excluded := "mission-20261022-31"
	graded := "mission-20261022-32"
	seedGradableMission(t, db, excluded, "kirsch", "complete", "done")
	seedGradableMission(t, db, graded, "kirsch", "complete", "done")
	claimIDs(t, router, map[string]any{"mission_ids": []string{excluded, graded}})
	excludeMission(t, router, excluded, "set aside while claimed")

	code, out := do(t, router, http.MethodPost, "/v1/mission-grades", map[string]any{
		"graded_by": "test",
		"items": []any{
			map[string]any{"mission_id": excluded, "grade": "A", "summary": "must not land"},
			map[string]any{"mission_id": graded, "grade": "C", "summary": "lands"},
		},
	})
	if code != http.StatusOK || out["written"].(float64) != 1 {
		t.Fatalf("write: %d %v, want exactly one written", code, out)
	}
	if skipped := out["skipped"].([]any); len(skipped) != 1 || skipped[0].(map[string]any)["mission_id"] != excluded {
		t.Fatalf("skipped = %v, want only %s", skipped, excluded)
	}
	code, out = do(t, router, http.MethodPost, "/v1/mission-grades", map[string]any{
		"graded_by": "test",
		"items":     []any{map[string]any{"mission_id": excluded, "insufficient": true, "summary": "must not land"}},
	})
	if code != http.StatusOK || out["written"].(float64) != 0 {
		t.Fatalf("insufficient verdict for an excluded mission: %d %v, want none written", code, out)
	}

	row := gradeRowOf(t, db, excluded)
	if row == nil || row["state"] != "excluded" || row["grade"] != nil {
		t.Fatalf("excluded row after writes = %v; want state excluded, grade NULL", row)
	}
	if row["summary"] == "must not land" {
		t.Fatalf("a refused verdict overwrote the exclusion note: %v", row)
	}
	if row := gradeRowOf(t, db, graded); row == nil || row["state"] != "graded" || row["grade"] != "C" {
		t.Fatalf("control row = %v; want graded C", row)
	}
}

// TestPerformanceCountsExcludedApartFromAverages checks that an excluded mission
// leaves the averages and the distribution, and is counted under excluded. It
// reads the figures before and after excluding a graded F, so the F's effect
// on the average is visible and then gone.
func TestPerformanceCountsExcludedApartFromAverages(t *testing.T) {
	router, db := newTriageTestRouter(t)
	kirschA := "mission-20261022-41"
	kirschB := "mission-20261022-42"
	kirschF := "mission-20261022-43"
	kirschNone := "mission-20261022-44"
	liteNone := "mission-20261022-45"
	liteWaiting := "mission-20261022-46"
	for _, id := range []string{kirschA, kirschB, kirschF, kirschNone, liteNone, liteWaiting} {
		harness := "kirsch"
		if id == liteNone || id == liteWaiting {
			harness = "lite"
		}
		seedGradableMission(t, db, id, harness, "complete", "done")
	}
	for id, grade := range map[string]string{kirschA: "A", kirschB: "B", kirschF: "F"} {
		if _, err := db.Exec(`INSERT INTO mission_grades (mission_id, state, grade, summary) VALUES (?, 'graded', ?, 's')`, id, grade); err != nil {
			t.Fatal(err)
		}
	}

	// Before any exclusion: the F drags kirsch down to a C.
	byName, overall := performanceOf(t, router)
	if k := byName["kirsch"]; k["avg_grade"] != "C" || k["graded"] != 3.0 {
		t.Fatalf("kirsch before exclusion = %v, want C over 3 graded", k)
	}

	excludeMission(t, router, kirschF, "graded F, set aside")
	excludeMission(t, router, kirschNone, "no row, set aside")
	excludeMission(t, router, liteNone, "no row, set aside")

	byName, overall = performanceOf(t, router)
	k := byName["kirsch"]
	if k["graded"] != 2.0 || k["avg_grade"] != "A" || k["avg_points"] != 4.5 {
		t.Errorf("kirsch after exclusion = %v, want A (4.5) over 2 graded", k)
	}
	if k["excluded"] != 2.0 || k["waiting"] != 0.0 {
		t.Errorf("kirsch excluded/waiting = %v/%v, want 2/0", k["excluded"], k["waiting"])
	}
	dist := k["distribution"].(map[string]any)
	if len(dist) != 2 || dist["A"] != 1.0 || dist["B"] != 1.0 {
		t.Errorf("kirsch distribution = %v, want exactly A:1 B:1 (an excluded F must not appear)", dist)
	}
	if recent := k["recent"].([]any); len(recent) != 2 {
		t.Errorf("kirsch recent = %v, want the two graded missions only", recent)
	}

	l := byName["lite"]
	if l["graded"] != 0.0 || l["excluded"] != 1.0 || l["waiting"] != 1.0 || l["avg_points"] != nil {
		t.Errorf("lite = %v, want 0 graded, 1 excluded, 1 waiting, no average", l)
	}
	if overall["graded"] != 2.0 || overall["avg_grade"] != "A" || overall["excluded"] != 3.0 || overall["waiting"] != 1.0 {
		t.Errorf("overall = %v, want 2 graded at A, 3 excluded, 1 waiting", overall)
	}
	if d := overall["distribution"].(map[string]any); len(d) != 2 || d["A"] != 1.0 || d["B"] != 1.0 {
		t.Errorf("overall distribution = %v, want exactly A:1 B:1", d)
	}
}

// TestExcludedRowsInListAndHUD checks that an excluded row comes out of the
// grade list and the HUD with its state and its note, and that the state
// filters separate it from graded rows.
func TestExcludedRowsInListAndHUD(t *testing.T) {
	router, db := newTriageTestRouter(t)
	excluded := "mission-20261022-51"
	graded := "mission-20261022-52"
	seedGradableMission(t, db, excluded, "kirsch", "complete", "done")
	seedGradableMission(t, db, graded, "kirsch", "complete", "done")
	if _, err := db.Exec(`INSERT INTO mission_grades (mission_id, state, grade, summary) VALUES (?, 'graded', 'B', 's')`, graded); err != nil {
		t.Fatal(err)
	}
	excludeMission(t, router, excluded, "test mission")

	missionIDs := func(path string) []string {
		t.Helper()
		code, out := do(t, router, http.MethodGet, path, nil)
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d %v", path, code, out)
		}
		ids := []string{}
		for _, g := range out["grades"].([]any) {
			ids = append(ids, g.(map[string]any)["mission_id"].(string))
		}
		return ids
	}
	if ids := missionIDs("/v1/mission-grades"); !hasID(ids, excluded) || !hasID(ids, graded) {
		t.Fatalf("default list = %v, want both the excluded and the graded mission", ids)
	}
	if ids := missionIDs("/v1/mission-grades?state=excluded"); len(ids) != 1 || ids[0] != excluded {
		t.Fatalf("state=excluded list = %v, want only %s", ids, excluded)
	}
	if ids := missionIDs("/v1/mission-grades?state=graded"); len(ids) != 1 || ids[0] != graded {
		t.Fatalf("state=graded list = %v, want only %s", ids, graded)
	}

	_, list := do(t, router, http.MethodGet, "/v1/mission-grades?state=excluded", nil)
	row := list["grades"].([]any)[0].(map[string]any)
	if row["state"] != "excluded" || row["grade"] != nil || row["summary"] != "test mission" || row["graded_by"] != "operator" {
		t.Errorf("list row = %v; want state excluded, grade null, the note, graded_by operator", row)
	}

	_, hud := do(t, router, http.MethodGet, "/v1/hud/missions", nil)
	for _, m := range hud["missions"].([]any) {
		m := m.(map[string]any)
		switch m["id"] {
		case excluded:
			if m["grade_state"] != "excluded" || m["grade"] != nil {
				t.Errorf("HUD row for the excluded mission = %v; want grade_state excluded, grade null", m)
			}
		case graded:
			if m["grade"] != "B" || m["grade_state"] != "graded" {
				t.Errorf("HUD row for the graded mission = %v; want grade B", m)
			}
		}
	}
	_, detail := do(t, router, http.MethodGet, "/v1/hud/missions/"+excluded, nil)
	if g, ok := detail["grade"].(map[string]any); !ok || g["state"] != "excluded" || g["grade"] != nil || g["summary"] != "test mission" {
		t.Errorf("HUD detail grade = %v; want state excluded, grade null, the note", detail["grade"])
	}
}

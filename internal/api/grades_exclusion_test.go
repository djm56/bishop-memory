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
// Each subtest builds its own database, so a mutant that breaks one path
// fails that subtest alone.

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

// finishedFixture returns a router over a fresh database that holds the given
// finished missions, none of them graded.
func finishedFixture(t *testing.T, ids ...string) (*gin.Engine, *sql.DB) {
	t.Helper()
	router, db := newTriageTestRouter(t)
	for _, id := range ids {
		seedGradableMission(t, db, id, "kirsch", "complete", "done")
	}
	return router, db
}

// seedOpenMission inserts a mission still in progress. The grader may not grade
// it, so the exclusion route must refuse it.
func seedOpenMission(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO missions (id, title, status) VALUES (?, 'open', 'in-progress')`, id); err != nil {
		t.Fatalf("seed open mission %s: %v", id, err)
	}
}

// seedGradeRow writes a grade row directly, the way the grader and the claim
// leave one, for a subtest that starts from a given state.
func seedGradeRow(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("seed grade row: %v", err)
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

// TestExclusionRouteRefusals checks every way the exclusion routes refuse, and
// the requests they must still accept beside those refusals. Each subtest
// builds its own database and states its error text as well as its status:
// gin answers any unmatched route with a 404 of its own ("route not found"), so
// the status alone cannot tell a refusal from a route that is not registered.
func TestExclusionRouteRefusals(t *testing.T) {
	const finished, open, unknown = "mission-20261022-01", "mission-20261022-02", "mission-20261022-99"

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
			router, db := finishedFixture(t, finished)
			seedOpenMission(t, db, open)
			code, out := do(t, router, tc.method, exclusionPath(tc.id), tc.body)
			if code != tc.want || out["error"] != tc.wantError {
				t.Errorf("%s %s = %d %v, want %d with error %q",
					tc.method, exclusionPath(tc.id), code, out, tc.want, tc.wantError)
			}
			for _, id := range []string{finished, open, unknown} {
				if row := gradeRowOf(t, db, id); row != nil {
					t.Errorf("a refused request wrote a row for %s: %v", id, row)
				}
			}
		})
	}

	// Controls: the same routes admit what the refusals turn away.
	t.Run("accepts_700_char_note", func(t *testing.T) {
		router, db := finishedFixture(t, finished)
		code, out := do(t, router, http.MethodPut, exclusionPath(finished),
			map[string]any{"note": strings.Repeat("n", 700)})
		if code != http.StatusOK {
			t.Fatalf("a 700-character note was refused: %d %v", code, out)
		}
		if row := gradeRowOf(t, db, finished); row == nil || row["state"] != "excluded" {
			t.Fatalf("row after a 700-character note = %v, want state excluded", row)
		}
	})

	t.Run("lifts_exclusion", func(t *testing.T) {
		router, db := finishedFixture(t, finished)
		excludeMission(t, router, finished, "set aside")
		if code, out := do(t, router, http.MethodDelete, exclusionPath(finished), nil); code != http.StatusOK {
			t.Fatalf("lifting the exclusion: %d %v", code, out)
		}
		if row := gradeRowOf(t, db, finished); row != nil {
			t.Fatalf("lifting the exclusion left a row: %v", row)
		}
	})

	t.Run("second_lift_refused", func(t *testing.T) {
		router, _ := finishedFixture(t, finished)
		excludeMission(t, router, finished, "set aside")
		do(t, router, http.MethodDelete, exclusionPath(finished), nil)
		code, out := do(t, router, http.MethodDelete, exclusionPath(finished), nil)
		if code != http.StatusNotFound || out["error"] != "mission is not excluded" {
			t.Fatalf("lifting an exclusion twice = %d %v, want 404 mission is not excluded", code, out)
		}
	})

	t.Run("DELETE_on_graded_row_404_and_row_kept", func(t *testing.T) {
		// The DELETE must touch only an excluded row, never a verdict.
		router, db := finishedFixture(t, finished)
		seedGradeRow(t, db, `INSERT INTO mission_grades (mission_id, state, grade, summary) VALUES (?, 'graded', 'B', 's')`, finished)
		code, out := do(t, router, http.MethodDelete, exclusionPath(finished), nil)
		if code != http.StatusNotFound || out["error"] != "mission is not excluded" {
			t.Fatalf("DELETE on a graded row = %d %v, want 404", code, out)
		}
		if row := gradeRowOf(t, db, finished); row == nil || row["state"] != "graded" || row["grade"] != "B" {
			t.Fatalf("graded row after a refused DELETE = %v, want state graded, grade B", row)
		}
	})
}

// TestExclusionRouteReportsWhatItReplaced sets a mission aside over each state
// it can hold before. Each subtest starts from a row written directly, which is
// how the grader and the claim leave one. The response must name the state and
// grade the row held, and the row afterwards must carry the trimmed note and
// nothing of the old verdict.
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
			id := "mission-20261022-21"
			router, db := finishedFixture(t, id)
			if tc.setup != "" {
				seedGradeRow(t, db, tc.setup, id)
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

// TestExcludedMissionIsNeverClaimed checks the claim and the waiting count
// against an excluded mission, and that a verdict-less pending retry is still
// claimed. Every subtest builds its own database.
func TestExcludedMissionIsNeverClaimed(t *testing.T) {
	const excluded, control, retry, spent, unclaimed = "mission-20261022-11", "mission-20261022-12",
		"mission-20261022-13", "mission-20261022-14", "mission-20261022-15"

	t.Run("sweep_skips_excluded", func(t *testing.T) {
		router, db := finishedFixture(t, excluded, control)
		excludeMission(t, router, excluded, "never claimed")
		if n := waiting(t, router); n != 1 {
			t.Fatalf("waiting = %d, want 1 (the excluded mission must not count)", n)
		}
		ids := claimIDs(t, router, map[string]any{"run_id": startGradeRun(t, router)})
		if len(ids) != 1 || ids[0] != control {
			t.Fatalf("sweep claimed %v, want only %s", ids, control)
		}
		if row := gradeRowOf(t, db, excluded); row == nil || row["state"] != "excluded" {
			t.Fatalf("excluded row after the sweep = %v, want state excluded", row)
		}
	})

	t.Run("sweep_still_claims_pending_retry", func(t *testing.T) {
		// Control: a pending row with attempts to spare is waiting and is claimed.
		router, db := finishedFixture(t, retry)
		seedGradeRow(t, db, `INSERT INTO mission_grades (mission_id, state, attempts) VALUES (?, 'pending', 1)`, retry)
		if n := waiting(t, router); n != 1 {
			t.Fatalf("waiting = %d, want 1 (a pending retry is waiting)", n)
		}
		ids := claimIDs(t, router, map[string]any{})
		if len(ids) != 1 || ids[0] != retry {
			t.Fatalf("sweep claimed %v, want %s", ids, retry)
		}
	})

	t.Run("spent_exclusion_survives_sweep", func(t *testing.T) {
		// A claim never answered with its attempts spent: the sweep marks such a
		// row skipped, and an exclusion must not be rewritten by it.
		router, db := finishedFixture(t, spent)
		seedGradeRow(t, db, `INSERT INTO mission_grades (mission_id, state, attempts) VALUES (?, 'pending', 2)`, spent)
		excludeMission(t, router, spent, "spent attempts")
		claimIDs(t, router, map[string]any{})
		if row := gradeRowOf(t, db, spent); row == nil || row["state"] != "excluded" {
			t.Fatalf("row after the sweep = %v, want state excluded", row)
		}
	})

	t.Run("explicit_claim_of_excluded_refused", func(t *testing.T) {
		router, db := finishedFixture(t, excluded)
		excludeMission(t, router, excluded, "never claimed")
		code, out := do(t, router, http.MethodPost, "/v1/mission-grades/claim", map[string]any{"mission_ids": []string{excluded}})
		if code != http.StatusConflict {
			t.Fatalf("explicit claim of excluded %s = %d %v, want 409", excluded, code, out)
		}
		refused, _ := out["refused"].([]any)
		if len(refused) != 1 {
			t.Fatalf("refused = %v; want one refusal for %s", refused, excluded)
		}
		reason, _ := refused[0].(map[string]any)["reason"].(string)
		if !strings.Contains(reason, "excluded") || !strings.Contains(reason, exclusionPath(excluded)) {
			t.Fatalf("reason = %q; want it to say the mission is excluded and name %s", reason, exclusionPath(excluded))
		}
		if row := gradeRowOf(t, db, excluded); row == nil || row["state"] != "excluded" {
			t.Fatalf("row after the refused claim = %v, want state excluded", row)
		}
	})

	t.Run("explicit_claim_of_pending_retry_allowed", func(t *testing.T) {
		// Control for the refusal above: a named pending retry is still claimed.
		router, db := finishedFixture(t, retry)
		seedGradeRow(t, db, `INSERT INTO mission_grades (mission_id, state, attempts) VALUES (?, 'pending', 1)`, retry)
		ids := claimIDs(t, router, map[string]any{"mission_ids": []string{retry}})
		if len(ids) != 1 || ids[0] != retry {
			t.Fatalf("explicit claim of a pending retry = %v, want %s", ids, retry)
		}
	})

	t.Run("mixed_refusal_is_all_or_nothing", func(t *testing.T) {
		router, db := finishedFixture(t, excluded, unclaimed)
		excludeMission(t, router, excluded, "never claimed")
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
	})

	t.Run("lifting_makes_claimable", func(t *testing.T) {
		router, _ := finishedFixture(t, excluded)
		excludeMission(t, router, excluded, "never claimed")
		if code, out := do(t, router, http.MethodDelete, exclusionPath(excluded), nil); code != http.StatusOK {
			t.Fatalf("lifting the exclusion: %d %v", code, out)
		}
		if ids := claimIDs(t, router, map[string]any{"mission_ids": []string{excluded}}); len(ids) != 1 || ids[0] != excluded {
			t.Fatalf("claim after the exclusion was lifted = %v, want %s", ids, excluded)
		}
	})
}

// TestWriteNeverOverwritesExcludedRow checks that a verdict for a mission that
// was excluded while claimed is refused and the row keeps its exclusion, while
// a verdict for a pending mission is still applied.
func TestWriteNeverOverwritesExcludedRow(t *testing.T) {
	const excluded, graded = "mission-20261022-31", "mission-20261022-32"

	t.Run("verdict_refused_for_excluded", func(t *testing.T) {
		router, db := finishedFixture(t, excluded, graded)
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
		row := gradeRowOf(t, db, excluded)
		if row == nil || row["state"] != "excluded" || row["grade"] != nil {
			t.Fatalf("excluded row after the write = %v; want state excluded, grade NULL", row)
		}
		if row["summary"] == "must not land" {
			t.Fatalf("a refused verdict overwrote the exclusion note: %v", row)
		}
	})

	t.Run("insufficient_refused_for_excluded", func(t *testing.T) {
		router, db := finishedFixture(t, excluded)
		claimIDs(t, router, map[string]any{"mission_ids": []string{excluded}})
		excludeMission(t, router, excluded, "set aside while claimed")
		code, out := do(t, router, http.MethodPost, "/v1/mission-grades", map[string]any{
			"graded_by": "test",
			"items":     []any{map[string]any{"mission_id": excluded, "insufficient": true, "summary": "must not land"}},
		})
		if code != http.StatusOK || out["written"].(float64) != 0 {
			t.Fatalf("insufficient verdict for an excluded mission: %d %v, want none written", code, out)
		}
		if row := gradeRowOf(t, db, excluded); row == nil || row["state"] != "excluded" {
			t.Fatalf("excluded row after the write = %v, want state excluded", row)
		}
	})

	t.Run("pending_control_takes_verdict", func(t *testing.T) {
		// Control: a claimed mission that is not excluded takes its verdict.
		router, db := finishedFixture(t, graded)
		claimIDs(t, router, map[string]any{"mission_ids": []string{graded}})
		code, out := do(t, router, http.MethodPost, "/v1/mission-grades", map[string]any{
			"graded_by": "test",
			"items":     []any{map[string]any{"mission_id": graded, "grade": "C", "summary": "lands"}},
		})
		if code != http.StatusOK || out["written"].(float64) != 1 {
			t.Fatalf("pending control took no verdict: %d %v, want one written", code, out)
		}
		if row := gradeRowOf(t, db, graded); row == nil || row["state"] != "graded" || row["grade"] != "C" {
			t.Fatalf("control row = %v; want graded C", row)
		}
	})
}

// performanceFixture seeds three harness-kirsch missions with grades A, B and
// F, and three ungraded missions: two kirsch, one lite. Returns the router and
// the ids in the order kirsch A, kirsch B, kirsch F, kirsch unrated, lite
// unrated, lite waiting.
func performanceFixture(t *testing.T) (*gin.Engine, []string) {
	t.Helper()
	router, db := newTriageTestRouter(t)
	ids := []string{
		"mission-20261022-41", "mission-20261022-42", "mission-20261022-43",
		"mission-20261022-44", "mission-20261022-45", "mission-20261022-46",
	}
	harness := []string{"kirsch", "kirsch", "kirsch", "kirsch", "lite", "lite"}
	for i, id := range ids {
		seedGradableMission(t, db, id, harness[i], "complete", "done")
	}
	for i, grade := range []string{"A", "B", "F"} {
		seedGradeRow(t, db, `INSERT INTO mission_grades (mission_id, state, grade, summary) VALUES (?, 'graded', ?, 's')`, ids[i], grade)
	}
	return router, ids
}

// setAsideUnrated excludes the graded F and the two unrated missions of the
// fixture, the set every excluded-figure subtest starts from.
func setAsideUnrated(t *testing.T, router *gin.Engine, ids []string) {
	t.Helper()
	excludeMission(t, router, ids[2], "graded F, set aside")
	excludeMission(t, router, ids[3], "unrated, set aside")
	excludeMission(t, router, ids[4], "unrated, set aside")
}

// TestPerformanceCountsExcludedApartFromAverages checks that an excluded mission
// leaves the averages and the distribution, and is counted under excluded. The
// first subtest reads the graded F's effect before any exclusion, so the later
// subtests have something to show it gone.
func TestPerformanceCountsExcludedApartFromAverages(t *testing.T) {
	t.Run("graded_F_counts_before_exclusion", func(t *testing.T) {
		router, _ := performanceFixture(t)
		byName, _ := performanceOf(t, router)
		if k := byName["kirsch"]; k["avg_grade"] != "C" || k["graded"] != 3.0 {
			t.Fatalf("kirsch before exclusion = %v, want C over 3 graded", k)
		}
	})

	t.Run("average_ignores_excluded_F", func(t *testing.T) {
		router, ids := performanceFixture(t)
		setAsideUnrated(t, router, ids)
		byName, _ := performanceOf(t, router)
		if k := byName["kirsch"]; k["graded"] != 2.0 || k["avg_grade"] != "A" || k["avg_points"] != 4.5 {
			t.Fatalf("kirsch after exclusion = %v, want A (4.5) over 2 graded", k)
		}
	})

	t.Run("excluded_counted_apart", func(t *testing.T) {
		router, ids := performanceFixture(t)
		setAsideUnrated(t, router, ids)
		byName, _ := performanceOf(t, router)
		if k := byName["kirsch"]; k["excluded"] != 2.0 || k["waiting"] != 0.0 {
			t.Errorf("kirsch excluded/waiting = %v/%v, want 2/0", k["excluded"], k["waiting"])
		}
		if l := byName["lite"]; l["graded"] != 0.0 || l["excluded"] != 1.0 || l["waiting"] != 1.0 || l["avg_points"] != nil {
			t.Errorf("lite = %v, want 0 graded, 1 excluded, 1 waiting, no average", l)
		}
	})

	t.Run("distribution_has_no_excluded", func(t *testing.T) {
		router, ids := performanceFixture(t)
		setAsideUnrated(t, router, ids)
		byName, overall := performanceOf(t, router)
		for name, line := range map[string]map[string]any{"kirsch": byName["kirsch"], "overall": overall} {
			dist := line["distribution"].(map[string]any)
			if len(dist) != 2 || dist["A"] != 1.0 || dist["B"] != 1.0 {
				t.Errorf("%s distribution = %v, want exactly A:1 B:1 (an excluded F must not appear)", name, dist)
			}
		}
	})

	t.Run("recent_lists_graded_only", func(t *testing.T) {
		router, ids := performanceFixture(t)
		setAsideUnrated(t, router, ids)
		byName, _ := performanceOf(t, router)
		if recent := byName["kirsch"]["recent"].([]any); len(recent) != 2 {
			t.Errorf("kirsch recent = %v, want the two graded missions only", recent)
		}
	})

	t.Run("overall_figures", func(t *testing.T) {
		router, ids := performanceFixture(t)
		setAsideUnrated(t, router, ids)
		_, overall := performanceOf(t, router)
		if overall["graded"] != 2.0 || overall["avg_grade"] != "A" || overall["excluded"] != 3.0 || overall["waiting"] != 1.0 {
			t.Errorf("overall = %v, want 2 graded at A, 3 excluded, 1 waiting", overall)
		}
	})
}

// listFixture seeds an excluded mission, a graded mission and a pending
// mission, and returns the router and the database with those three ids.
func listFixture(t *testing.T) (*gin.Engine, *sql.DB, string, string, string) {
	t.Helper()
	excluded, graded, pending := "mission-20261022-51", "mission-20261022-52", "mission-20261022-53"
	router, db := finishedFixture(t, excluded, graded, pending)
	seedGradeRow(t, db, `INSERT INTO mission_grades (mission_id, state, grade, summary) VALUES (?, 'graded', 'B', 's')`, graded)
	seedGradeRow(t, db, `INSERT INTO mission_grades (mission_id, state, attempts) VALUES (?, 'pending', 1)`, pending)
	excludeMission(t, router, excluded, "test mission")
	return router, db, excluded, graded, pending
}

// listedMissionIDs runs GET path against the grade list and returns the mission
// ids it lists, in order.
func listedMissionIDs(t *testing.T, router *gin.Engine, path string) []string {
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

// TestExcludedRowsInListAndHUD checks that an excluded row comes out of the
// grade list and the HUD with its state and its note, and that the state
// filters separate it from graded rows and keep pending rows out of the default.
func TestExcludedRowsInListAndHUD(t *testing.T) {
	t.Run("default_list_includes_excluded", func(t *testing.T) {
		router, _, excluded, graded, _ := listFixture(t)
		if ids := listedMissionIDs(t, router, "/v1/mission-grades"); !hasID(ids, excluded) || !hasID(ids, graded) {
			t.Fatalf("default list = %v, want both the excluded and the graded mission", ids)
		}
	})

	t.Run("default_list_omits_pending", func(t *testing.T) {
		// Control: a pending claim is not a grade and stays out of the default list.
		router, _, _, _, pending := listFixture(t)
		if ids := listedMissionIDs(t, router, "/v1/mission-grades"); hasID(ids, pending) {
			t.Fatalf("default list includes pending mission %s: %v", pending, ids)
		}
	})

	t.Run("state_filter_excluded_only", func(t *testing.T) {
		router, _, excluded, _, _ := listFixture(t)
		if ids := listedMissionIDs(t, router, "/v1/mission-grades?state=excluded"); len(ids) != 1 || ids[0] != excluded {
			t.Fatalf("state=excluded list = %v, want only %s", ids, excluded)
		}
	})

	t.Run("state_filter_graded_only", func(t *testing.T) {
		router, _, _, graded, _ := listFixture(t)
		if ids := listedMissionIDs(t, router, "/v1/mission-grades?state=graded"); len(ids) != 1 || ids[0] != graded {
			t.Fatalf("state=graded list = %v, want only %s", ids, graded)
		}
	})

	t.Run("list_row_fields", func(t *testing.T) {
		router, _, excluded, _, _ := listFixture(t)
		_, list := do(t, router, http.MethodGet, "/v1/mission-grades?state=excluded", nil)
		row := list["grades"].([]any)[0].(map[string]any)
		if row["mission_id"] != excluded || row["state"] != "excluded" || row["grade"] != nil ||
			row["summary"] != "test mission" || row["graded_by"] != "operator" {
			t.Errorf("list row = %v; want state excluded, grade null, the note, graded_by operator", row)
		}
	})

	t.Run("hud_row_state", func(t *testing.T) {
		router, _, excluded, graded, _ := listFixture(t)
		_, hud := do(t, router, http.MethodGet, "/v1/hud/missions", nil)
		seen := map[string]bool{}
		for _, m := range hud["missions"].([]any) {
			m := m.(map[string]any)
			switch m["id"] {
			case excluded:
				seen[excluded] = true
				if m["grade_state"] != "excluded" || m["grade"] != nil {
					t.Errorf("HUD row for the excluded mission = %v; want grade_state excluded, grade null", m)
				}
			case graded:
				seen[graded] = true
				if m["grade"] != "B" || m["grade_state"] != "graded" {
					t.Errorf("HUD row for the graded mission = %v; want grade B", m)
				}
			}
		}
		if !seen[excluded] || !seen[graded] {
			t.Fatalf("HUD list lacks the excluded or the graded mission: %v", seen)
		}
	})

	t.Run("hud_detail_state", func(t *testing.T) {
		router, _, excluded, _, _ := listFixture(t)
		_, detail := do(t, router, http.MethodGet, "/v1/hud/missions/"+excluded, nil)
		if g, ok := detail["grade"].(map[string]any); !ok || g["state"] != "excluded" || g["grade"] != nil || g["summary"] != "test mission" {
			t.Errorf("HUD detail grade = %v; want state excluded, grade null, the note", detail["grade"])
		}
	})
}

package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

// seedGradableMission inserts a mission with its brief, debrief, steps and a
// finding, the way the reconciler leaves them.
func seedGradableMission(t *testing.T, db *sql.DB, id, harness, status, outcome string) {
	t.Helper()
	var outcomeArg any
	if outcome != "" {
		outcomeArg = outcome
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO missions (id, title, status, outcome, harness, opened_at, closed_at)
		  VALUES (?, ?, ?, ?, ?, '2026-10-01 09:00:00', '2026-10-01 11:30:00')`,
			[]any{id, "Mission " + id, status, outcomeArg, harness}},
		{`INSERT INTO documents (source_path, kind, body, sha256, mission_id) VALUES (?, 'brief', ?, 'x', ?)`,
			[]any{id + "/BRIEF.md", "# Brief\n\n## Goal\n\nShip the thing.\n\n## Acceptance Criteria\n\n- one\n- two\n- three\n", id}},
		{`INSERT INTO documents (source_path, kind, body, sha256, mission_id) VALUES (?, 'debrief', ?, 'x', ?)`,
			[]any{id + "/DEBRIEF.md", "# Debrief\n\n## Acceptance Criteria Outcome\n\n- [x] one — test\n- [x] two — test\n- [ ] three — not done\n\n## Wrong Assumptions (Mandatory)\n\n- none\n\n## Sub-Agent Mistakes and Corrections (Mandatory)\n\n- hicks missed a case\n", id}},
		{`INSERT INTO mission_steps (mission_id, step, agent, status, summary) VALUES (?, '1', '@hicks', 'done', 'built it')`, []any{id}},
		{`INSERT INTO mission_steps (mission_id, step, agent, status, summary) VALUES (?, '2', '@apone', 'done', 'review: one fix')`, []any{id}},
		{`INSERT INTO mission_steps (mission_id, step, agent, status, summary) VALUES (?, '2a', '@hicks', 'done', 'fix round 1')`, []any{id}},
		{`INSERT INTO mission_steps (mission_id, step, agent, status, summary) VALUES (?, '3', '@vasquez', 'done', 'escalation')`, []any{id}},
		{`INSERT INTO findings (suggestion, target, mission_id, harness) VALUES ('Quote briefs literally', '@bishop', ?, ?)`, []any{id, harness}},
	}
	for _, s := range statements {
		if _, err := db.Exec(s.query, s.args...); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
}

func startGradeRun(t *testing.T, router *gin.Engine) int64 {
	t.Helper()
	code, body := do(t, router, http.MethodPost, "/v1/triage/runs", map[string]any{"kind": "grade", "model": "test"})
	if code != http.StatusCreated {
		t.Fatalf("start grade run: %d %v", code, body)
	}
	return int64(body["id"].(float64))
}

func claimIDs(t *testing.T, router *gin.Engine, body map[string]any) []string {
	t.Helper()
	code, out := do(t, router, http.MethodPost, "/v1/mission-grades/claim", body)
	if code != http.StatusOK {
		t.Fatalf("claim %v: %d %v", body, code, out)
	}
	ids := []string{}
	for _, m := range out["missions"].([]any) {
		ids = append(ids, m.(map[string]any)["mission"].(map[string]any)["id"].(string))
	}
	return ids
}

func waiting(t *testing.T, router *gin.Engine) int {
	t.Helper()
	_, out := do(t, router, http.MethodGet, "/v1/mission-grades/waiting", nil)
	return int(out["waiting"].(float64))
}

func TestGradingClaimPacketAndWrite(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedGradableMission(t, db, "mission-20261001-01", "kirsch", "complete", "done")
	seedGradableMission(t, db, "mission-20261001-02", "kirsch", "complete", "failed")
	if _, err := db.Exec(`INSERT INTO missions (id, title, status) VALUES ('mission-20261001-03', 'open', 'in-progress')`); err != nil {
		t.Fatal(err)
	}

	if n := waiting(t, router); n != 2 {
		t.Fatalf("waiting = %d, want 2 (an in-progress mission is not gradable)", n)
	}

	run := startGradeRun(t, router)
	code, out := do(t, router, http.MethodPost, "/v1/mission-grades/claim", map[string]any{"run_id": run, "limit": 1})
	if code != http.StatusOK {
		t.Fatalf("claim: %d %v", code, out)
	}
	missions := out["missions"].([]any)
	if len(missions) != 1 {
		t.Fatalf("claim with limit 1 returned %d missions", len(missions))
	}
	packet := missions[0].(map[string]any)
	signals := packet["signals"].(map[string]any)
	want := map[string]float64{"criteria_planned": 3, "criteria_met": 2, "criteria_unmet": 1, "steps": 4,
		"injected_steps": 1, "escalation_steps": 1, "findings": 1}
	for k, v := range want {
		if signals[k] != v {
			t.Errorf("signals[%s] = %v, want %v", k, signals[k], v)
		}
	}
	debrief := packet["debrief"].(map[string]any)
	if _, ok := debrief["Wrong Assumptions"]; !ok {
		t.Errorf("packet debrief lacks Wrong Assumptions: %v", debrief)
	}
	if goal := packet["brief"].(map[string]any)["goal"]; goal != "Ship the thing." {
		t.Errorf("packet brief goal = %q", goal)
	}

	// The same run does not re-claim what it already holds.
	if ids := claimIDs(t, router, map[string]any{"run_id": run}); len(ids) != 1 || ids[0] != "mission-20261001-02" {
		t.Fatalf("second claim in the same run = %v, want only the other mission", ids)
	}

	id := packet["mission"].(map[string]any)["id"].(string)
	code, out = do(t, router, http.MethodPost, "/v1/mission-grades", map[string]any{
		"graded_by": "test", "run_id": run,
		"items": []any{
			map[string]any{"mission_id": id, "grade": "B", "summary": "Two of three criteria met.", "suggestions": "Name the third criterion's owner."},
			map[string]any{"mission_id": "mission-20261001-02", "grade": "F", "insufficient": true, "summary": "both"},
			map[string]any{"mission_id": "mission-20261001-03", "grade": "C", "summary": "never claimed"},
		},
	})
	if code != http.StatusOK || out["written"].(float64) != 1 || len(out["skipped"].([]any)) != 2 {
		t.Fatalf("write: %d %v, want 1 written and 2 skipped", code, out)
	}

	// A verdict is final: rewriting is refused and the mission is never claimed again.
	_, out = do(t, router, http.MethodPost, "/v1/mission-grades", map[string]any{
		"graded_by": "test", "items": []any{map[string]any{"mission_id": id, "grade": "A", "summary": "again"}},
	})
	if out["written"].(float64) != 0 {
		t.Fatalf("a graded mission took a second verdict: %v", out)
	}
	if ids := claimIDs(t, router, map[string]any{"run_id": startGradeRun(t, router)}); len(ids) != 1 || ids[0] == id {
		t.Fatalf("claim after grading = %v; the graded mission must not come back", ids)
	}

	var grade, signalsJSON string
	if err := db.QueryRow(`SELECT grade, signals FROM mission_grades WHERE mission_id = ?`, id).Scan(&grade, &signalsJSON); err != nil || grade != "B" || signalsJSON == "" {
		t.Fatalf("stored grade=%q signals=%q err=%v", grade, signalsJSON, err)
	}

	// The HUD carries it.
	_, hud := do(t, router, http.MethodGet, "/v1/hud/missions?grade=B", nil)
	if rows := hud["missions"].([]any); len(rows) != 1 || rows[0].(map[string]any)["grade"] != "B" {
		t.Fatalf("HUD grade filter = %v", rows)
	}
	_, detail := do(t, router, http.MethodGet, "/v1/hud/missions/"+id, nil)
	if g, ok := detail["grade"].(map[string]any); !ok || g["suggestions"] != "Name the third criterion's owner." {
		t.Fatalf("HUD detail grade = %v", detail["grade"])
	}
}

// TestGradingNeverLoops: a mission claimed and never answered is retried by
// one later run, then skipped for good.
func TestGradingNeverLoops(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedGradableMission(t, db, "mission-20261002-01", "kirsch", "complete", "done")

	for attempt := 1; attempt <= 2; attempt++ {
		if ids := claimIDs(t, router, map[string]any{"run_id": startGradeRun(t, router)}); len(ids) != 1 {
			t.Fatalf("attempt %d claimed %v, want the mission", attempt, ids)
		}
	}
	if ids := claimIDs(t, router, map[string]any{"run_id": startGradeRun(t, router)}); len(ids) != 0 {
		t.Fatalf("third claim = %v; after two unanswered claims the mission must be skipped", ids)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM mission_grades WHERE mission_id = 'mission-20261002-01'`).Scan(&state); err != nil || state != "skipped" {
		t.Fatalf("state = %q (err %v), want skipped", state, err)
	}
	if n := waiting(t, router); n != 0 {
		t.Fatalf("waiting = %d after skip, want 0", n)
	}
}

// TestGradingExplicitClaimAndRegrade: naming a graded mission is refused until
// the operator deletes its verdict.
func TestGradingExplicitClaimAndRegrade(t *testing.T) {
	router, db := newTriageTestRouter(t)
	id := "mission-20261003-01"
	seedGradableMission(t, db, id, "kirsch", "complete", "done")

	claimIDs(t, router, map[string]any{"mission_ids": []string{id}})
	do(t, router, http.MethodPost, "/v1/mission-grades", map[string]any{
		"graded_by": "test", "items": []any{map[string]any{"mission_id": id, "insufficient": true, "summary": "no debrief"}},
	})
	code, out := do(t, router, http.MethodPost, "/v1/mission-grades/claim", map[string]any{"mission_ids": []string{id}})
	if code != http.StatusConflict {
		t.Fatalf("claiming a mission with a verdict: %d %v, want 409", code, out)
	}
	if code, _ := do(t, router, http.MethodDelete, "/v1/mission-grades/"+id, nil); code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
	if ids := claimIDs(t, router, map[string]any{"mission_ids": []string{id}}); len(ids) != 1 {
		t.Fatalf("claim after regrade delete = %v", ids)
	}
	code, out = do(t, router, http.MethodPost, "/v1/mission-grades/claim", map[string]any{"mission_ids": []string{"mission-nope"}})
	if code != http.StatusConflict {
		t.Fatalf("claiming an unknown mission: %d %v, want 409", code, out)
	}
}

func TestGradingPerformance(t *testing.T) {
	router, db := newTriageTestRouter(t)
	grades := map[string]string{"A": "kirsch", "B": "kirsch", "D": "lite"}
	i := 0
	for grade, harness := range grades {
		i++
		id := fmt.Sprintf("mission-20261004-%02d", i)
		seedGradableMission(t, db, id, harness, "complete", "done")
		if _, err := db.Exec(`INSERT INTO mission_grades (mission_id, state, grade, summary) VALUES (?, 'graded', ?, 's')`, id, grade); err != nil {
			t.Fatal(err)
		}
	}
	seedGradableMission(t, db, "mission-20261004-09", "lite", "complete", "done") // waiting

	code, out := do(t, router, http.MethodGet, "/v1/mission-grades/performance", nil)
	if code != http.StatusOK {
		t.Fatalf("performance: %d %v", code, out)
	}
	byName := map[string]map[string]any{}
	for _, h := range out["harnesses"].([]any) {
		row := h.(map[string]any)
		byName[row["harness"].(string)] = row
	}
	if k := byName["kirsch"]; k["avg_points"] != 4.5 || k["avg_grade"] != "A" || k["graded"] != 2.0 {
		t.Errorf("kirsch = %v, want avg 4.5 (A) over 2", k)
	}
	if l := byName["lite"]; l["avg_grade"] != "D" || l["waiting"] != 1.0 {
		t.Errorf("lite = %v, want D with 1 waiting", l)
	}
	if o := out["overall"].(map[string]any); o["graded"] != 3.0 || o["avg_points"] != 3.67 {
		t.Errorf("overall = %v, want 3 graded at 3.67", o)
	}
}

// TestCriteriaOutcome uses lines as real debriefs write them: the result is a
// word after a dash, and the checkbox is often left unticked on a pass.
func TestCriteriaOutcome(t *testing.T) {
	section := "- [ ] `page.php` exists in `test/bishop/` — pass\n" +
		"- All quick-win edits applied as specified — pass. Scope grew from 20 to 60.\n" +
		"- Every rule reads identically — partial. Step 10 graded this \"met with exceptions\".\n" +
		"- No new contradiction introduced — fail\n" +
		"- [x] ticked with no word\n" +
		"- [ ] three — not done\n" +
		"- a line that says nothing either way\n" +
		"not a bullet — pass\n"
	met, partial, unmet, unclear := criteriaOutcome(section)
	if met != 3 || partial != 1 || unmet != 2 || unclear != 1 {
		t.Fatalf("criteriaOutcome = met %d, partial %d, unmet %d, unclear %d; want 3, 1, 2, 1", met, partial, unmet, unclear)
	}
}

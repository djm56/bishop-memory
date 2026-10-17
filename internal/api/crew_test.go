package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/review"
)

// Entry point for every subtest of TestCrew: a GET /v1/crew request served by the
// router from NewRouter (handler crewHandler). Fixtures are seeded with direct SQL
// INSERTs into missions, mission_steps, agent_mistakes, mission_grades, service_records and
// crew; neither the importer nor any write endpoint is exercised.

// crewEnv is one subtest's router and database.
type crewEnv struct {
	t      *testing.T
	router *gin.Engine
	db     *sql.DB
}

// newCrewEnv builds a fresh database and router for one subtest.
func newCrewEnv(t *testing.T) crewEnv {
	t.Helper()
	router, db := newTriageTestRouter(t)
	return crewEnv{t: t, router: router, db: db}
}

// exec runs one SQL statement and fails the test on error.
func (e crewEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Exec(query, args...); err != nil {
		e.t.Fatalf("exec %q: %v", query, err)
	}
}

// missions inserts one mission row per id.
func (e crewEnv) missions(ids ...string) {
	e.t.Helper()
	for _, id := range ids {
		e.exec(`INSERT INTO missions (id, title, status) VALUES (?, ?, 'in-progress')`, id, "Mission "+id)
	}
}

// step inserts a mission_steps row; pass nil for step to store a NULL step.
func (e crewEnv) step(mission string, step any, agent, status, notes string) {
	e.t.Helper()
	e.exec(`INSERT INTO mission_steps (mission_id, step, agent, status, notes) VALUES (?, ?, ?, ?, ?)`,
		mission, step, agent, status, notes)
}

// done inserts a done step with no notes.
func (e crewEnv) done(mission, step, agent string) {
	e.t.Helper()
	e.step(mission, step, agent, "done", "")
}

// get calls GET /v1/crew and returns the crew array.
func (e crewEnv) get() []interface{} {
	e.t.Helper()
	code, body := do(e.t, e.router, http.MethodGet, "/v1/crew", nil)
	if code != http.StatusOK {
		e.t.Fatalf("GET /v1/crew: %d %v", code, body)
	}
	crew, ok := body["crew"].([]interface{})
	if !ok {
		e.t.Fatalf("GET /v1/crew: crew is not an array: %v", body["crew"])
	}
	return crew
}

// verdictLine builds the fixed review line; fixRound < 0 omits fix_round.
func verdictLine(approved bool, step string, fixRound int) string {
	v := "NOT APPROVED"
	if approved {
		v = "APPROVED"
	}
	line := fmt.Sprintf("Verdict: %s; critical=0; warnings=0; reviews_step=%s", v, step)
	if fixRound >= 0 {
		line += fmt.Sprintf("; fix_round=%d", fixRound)
	}
	return line
}

// findRow returns the crew row for agent or fails the test.
func findRow(t *testing.T, crew []interface{}, agent string) map[string]interface{} {
	t.Helper()
	for _, m := range crew {
		member := m.(map[string]interface{})
		if member["agent"].(string) == agent {
			return member
		}
	}
	t.Fatalf("agent %q not found in crew", agent)
	return nil
}

// wantInt fails unless row[key] is the number want.
func wantInt(t *testing.T, row map[string]interface{}, key string, want int) {
	t.Helper()
	got, ok := row[key].(float64)
	if !ok || int(got) != want {
		t.Fatalf("agent %v: %s = %v, want %d", row["agent"], key, row[key], want)
	}
}

// wantNull fails unless row[key] is JSON null.
func wantNull(t *testing.T, row map[string]interface{}, key string) {
	t.Helper()
	if row[key] != nil {
		t.Fatalf("agent %v: %s = %v, want null", row["agent"], key, row[key])
	}
}

// wantString fails unless row[key] is the string want.
func wantString(t *testing.T, row map[string]interface{}, key, want string) {
	t.Helper()
	if got, ok := row[key].(string); !ok || got != want {
		t.Fatalf("agent %v: %s = %v, want %q", row["agent"], key, row[key], want)
	}
}

// agentNames lists the agent of each crew row, in response order.
func agentNames(crew []interface{}) []string {
	names := make([]string, 0, len(crew))
	for _, m := range crew {
		names = append(names, m.(map[string]interface{})["agent"].(string))
	}
	return names
}

// TestCrew covers GET /v1/crew, one invariant per subtest.
func TestCrew(t *testing.T) {
	// Review order: the first review by mission_steps.id decides first_pass_approved.
	// Each agent has its own target step so each direction is asserted separately.
	//   hicks, step 1: reviews labelled "10" (NOT APPROVED, inserted first) and "3"
	//   (APPROVED). Text order of the labels ("10" < "3") equals id order, so only
	//   a reversed id order changes the result.
	//   vasquez, step 2: reviews labelled "9" (APPROVED, inserted first) and "11"
	//   (NOT APPROVED). Text order ("11" < "9") differs from id order, so both a
	//   text order and a reversed id order change the result.
	t.Run("review_order_by_insertion_id", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "@hicks")
		e.done("m1", "2", "@vasquez")
		e.step("m1", "10", "@apone", "done", verdictLine(false, "1", -1))
		e.step("m1", "3", "@apone", "done", verdictLine(true, "1", -1))
		e.step("m1", "9", "@apone", "done", verdictLine(true, "2", -1))
		e.step("m1", "11", "@apone", "done", verdictLine(false, "2", -1))

		crew := e.get()
		hicks := findRow(t, crew, "hicks")
		wantInt(t, hicks, "reviewed_steps", 1)
		// Id order puts NOT APPROVED first: first_pass_approved 0.
		wantInt(t, hicks, "first_pass_approved", 0)
		vasquez := findRow(t, crew, "vasquez")
		wantInt(t, vasquez, "reviewed_steps", 1)
		// Id order puts APPROVED first: first_pass_approved 1.
		wantInt(t, vasquez, "first_pass_approved", 1)
	})

	// first_pass_rate is approved/reviewed, and null when nothing was reviewed.
	t.Run("first_pass_rate", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "@hicks")
		e.done("m1", "2", "@vasquez")
		e.done("m1", "3", "@vasquez")
		e.done("m1", "4", "@newt")
		e.step("m1", "10", "@apone", "done", verdictLine(true, "1", -1))
		e.step("m1", "11", "@apone", "done", verdictLine(true, "2", -1))
		e.step("m1", "12", "@apone", "done", verdictLine(false, "3", -1))

		crew := e.get()
		hicks := findRow(t, crew, "hicks")
		wantInt(t, hicks, "reviewed_steps", 1)
		wantInt(t, hicks, "first_pass_approved", 1)
		if got, ok := hicks["first_pass_rate"].(float64); !ok || got != 1 {
			t.Fatalf("hicks: first_pass_rate = %v, want 1", hicks["first_pass_rate"])
		}
		vasquez := findRow(t, crew, "vasquez")
		wantInt(t, vasquez, "reviewed_steps", 2)
		wantInt(t, vasquez, "first_pass_approved", 1)
		if got, ok := vasquez["first_pass_rate"].(float64); !ok || got != 0.5 {
			t.Fatalf("vasquez: first_pass_rate = %v, want 0.5", vasquez["first_pass_rate"])
		}
		wantNull(t, findRow(t, crew, "newt"), "first_pass_rate")
	})

	// re_reviews, max across the step's reviews: fix_round [3,1] on one step gives 3.
	// "Last review wins" would give 1 and "sum" 4.
	t.Run("fix_round_max_not_last", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "@hicks")
		e.step("m1", "10", "@apone", "done", verdictLine(false, "1", 3))
		e.step("m1", "11", "@apone", "done", verdictLine(true, "1", 1))

		wantInt(t, findRow(t, e.get(), "hicks"), "re_reviews", 3)
	})

	// re_reviews, fix_round on the first review only: [4, none] gives 4, where
	// count-1 would give 1.
	t.Run("fix_round_on_first_review_only", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "@hicks")
		e.step("m1", "10", "@apone", "done", verdictLine(false, "1", 4))
		e.step("m1", "11", "@apone", "done", verdictLine(true, "1", -1))

		wantInt(t, findRow(t, e.get(), "hicks"), "re_reviews", 4)
	})

	// re_reviews, fix_round on a later review only: [none, 2] gives 2, where
	// count-1 would give 1 and a max taken over the first review only would too.
	t.Run("fix_round_on_later_review_only", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "@hicks")
		e.step("m1", "10", "@apone", "done", verdictLine(false, "1", -1))
		e.step("m1", "11", "@apone", "done", verdictLine(true, "1", 2))

		wantInt(t, findRow(t, e.get(), "hicks"), "re_reviews", 2)
	})

	// re_reviews, a single review carrying fix_round=2 gives 2.
	t.Run("fix_round_on_single_review", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "@hicks")
		e.step("m1", "10", "@apone", "done", verdictLine(true, "1", 2))

		wantInt(t, findRow(t, e.get(), "hicks"), "re_reviews", 2)
	})

	// re_reviews with no fix_round anywhere falls back to count-1: three reviews give 2.
	t.Run("re_reviews_count_minus_one_without_fix_round", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "@hicks")
		e.step("m1", "10", "@apone", "done", verdictLine(false, "1", -1))
		e.step("m1", "11", "@apone", "done", verdictLine(false, "1", -1))
		e.step("m1", "12", "@apone", "done", verdictLine(true, "1", -1))

		wantInt(t, findRow(t, e.get(), "hicks"), "re_reviews", 2)
	})

	// steps_run counts status done only; steps_total counts every status.
	t.Run("steps_run_counts_done_only", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.step("m1", "1", "@hicks", "done", "")
		e.step("m1", "2", "@hicks", "in-progress", "")
		e.step("m1", "3", "@hicks", "pending", "")
		e.step("m1", "4", "@hicks", "failed", "")

		hicks := findRow(t, e.get(), "hicks")
		wantInt(t, hicks, "steps_run", 1)
		wantInt(t, hicks, "steps_total", 4)
	})

	// avg_grade on a graded mission with grade F: avg_points is 0 and not null.
	t.Run("avg_grade_all_F", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1", "m2")
		e.done("m1", "1", "hicks")
		e.done("m2", "1", "hicks")
		e.exec(`INSERT INTO mission_grades (mission_id, state, grade) VALUES ('m1', 'graded', 'F')`)

		hicks := findRow(t, e.get(), "hicks")
		wantInt(t, hicks, "graded_missions", 1)
		wantString(t, hicks, "avg_grade", "F")
		if got, ok := hicks["avg_points"].(float64); !ok || got != 0 {
			t.Fatalf("hicks: avg_points = %v, want 0", hicks["avg_points"])
		}
	})

	// avg over graded missions only: m1 A and m2 C average 4 (B); m3 has no grade row.
	t.Run("avg_grade_averages_graded_missions", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1", "m2", "m3")
		e.done("m1", "1", "hicks")
		e.done("m2", "1", "hicks")
		e.done("m3", "1", "hicks")
		e.exec(`INSERT INTO mission_grades (mission_id, state, grade) VALUES ('m1', 'graded', 'A'), ('m2', 'graded', 'C')`)

		hicks := findRow(t, e.get(), "hicks")
		wantInt(t, hicks, "graded_missions", 2)
		if got, ok := hicks["avg_points"].(float64); !ok || got != 4 {
			t.Fatalf("hicks: avg_points = %v, want 4", hicks["avg_points"])
		}
		wantString(t, hicks, "avg_grade", "B")
	})

	// A grade row whose state is pending does not count: graded_missions 0, avg null.
	t.Run("avg_grade_null_when_no_graded_missions", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "hicks")
		e.exec(`INSERT INTO mission_grades (mission_id, state, grade) VALUES ('m1', 'pending', 'A')`)

		hicks := findRow(t, e.get(), "hicks")
		wantInt(t, hicks, "graded_missions", 0)
		wantNull(t, hicks, "avg_grade")
		wantNull(t, hicks, "avg_points")
	})

	// last_active is the max ended_at over the agent's steps. hicks has the later
	// time on the first-inserted step, an earlier one second and a NULL third, plus a
	// service record dated later than both. newt has only a NULL ended_at.
	t.Run("last_active_is_max_step_ended_at", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.exec(`INSERT INTO mission_steps (mission_id, step, agent, status, ended_at) VALUES
			('m1', '1', '@hicks', 'done', '2026-01-02 09:00:00'),
			('m1', '2', '@hicks', 'done', '2026-01-01 10:00:00'),
			('m1', '3', '@hicks', 'done', NULL),
			('m1', '4', '@newt', 'done', NULL)`)
		e.exec(`INSERT INTO service_records (agent, record_date, title, note, source)
			VALUES ('hicks', '2026-12-31', 'late record', 'note', 'bishop-observed')`)

		crew := e.get()
		wantString(t, findRow(t, crew, "hicks"), "last_active", "2026-01-02 09:00:00")
		wantNull(t, findRow(t, crew, "newt"), "last_active")
	})

	// last_active ignores empty-string ended_at, in two subtests over one fixture.
	// The fixture: hicks has only an empty-string ended_at step; ripley has a step
	// with a non-empty ended_at, both served through the same /v1/crew path.
	emptyEndedAtEnv := func(t *testing.T) []interface{} {
		e := newCrewEnv(t)
		e.missions("m1", "m2")
		e.done("m1", "1", "hicks")
		e.done("m2", "1", "ripley")
		e.exec(`INSERT INTO mission_steps (mission_id, step, agent, status, ended_at) VALUES
			('m1', '2', 'hicks', 'done', ''),
			('m2', '2', 'ripley', 'done', '2026-01-01 10:00:00')`)
		return e.get()
	}

	// The guard endedAt != "" must not be removed: if it is, the empty string
	// passes the type check, is marked Valid and serializes as "" instead of null.
	// This subtest asserts hicks only.
	t.Run("last_active_ignores_empty_ended_at", func(t *testing.T) {
		wantNull(t, findRow(t, emptyEndedAtEnv(t), "hicks"), "last_active")
	})

	// Positive control for the subtest above, through the same path: ripley's
	// non-empty ended_at must come back as that value, so a guard that drops every
	// row fails here rather than passing the absence check as "nothing arrived".
	// This subtest asserts ripley only.
	t.Run("last_active_keeps_non_empty_ended_at_control", func(t *testing.T) {
		wantString(t, findRow(t, emptyEndedAtEnv(t), "ripley"), "last_active", "2026-01-01 10:00:00")
	})

	// An agent with a step and no crew row has role null.
	t.Run("role_null_when_no_crew_row", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "@hicks")

		wantNull(t, findRow(t, e.get(), "hicks"), "role")
	})

	// An agent whose crew row carries a role reports that role.
	t.Run("role_present_from_crew_row", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "@hicks")
		e.exec(`INSERT INTO crew (name, role) VALUES ('hicks', 'junior')`)

		wantString(t, findRow(t, e.get(), "hicks"), "role", "junior")
	})

	// Crew-table names join the union through CrewName: "@Bishop" becomes "bishop"
	// with its role, a NULL-role row still yields an agent (role null), and a name
	// that normalises to empty yields none. No other table mentions these agents.
	t.Run("crew_table_names_normalised_and_empty_dropped", func(t *testing.T) {
		e := newCrewEnv(t)
		e.exec(`INSERT INTO crew (name, role) VALUES ('@Bishop', 'commander'), ('newt', NULL), ('  ', 'ghost')`)

		crew := e.get()
		if got := strings.Join(agentNames(crew), ","); got != "bishop,newt" {
			t.Fatalf("crew agents = %q, want %q", got, "bishop,newt")
		}
		wantString(t, findRow(t, crew, "bishop"), "role", "commander")
		wantNull(t, findRow(t, crew, "newt"), "role")
	})

	// Exclusion subtests: each pairs one excluded review with a control review that
	// reaches the same credit path (a review of step 2 in all but the cross-mission
	// subtest, whose control is the m1 review of step 1), so "reviewed_steps 1" means
	// the excluded review was dropped and the control was credited.

	// A review whose text names a step but carries no verdict wording is excluded by
	// the verdict guard alone: review.Parse gives Verdict "" with ReviewsStep "1".
	t.Run("excluded_review_names_step_but_no_verdict", func(t *testing.T) {
		const notes = "Reviewed step 1: nothing to report."
		if v := review.Parse(notes, ""); v.Verdict != "" || v.ReviewsStep != "1" {
			t.Fatalf("fixture precondition: Parse(%q) = verdict %q, reviews_step %q; want \"\" and \"1\"", notes, v.Verdict, v.ReviewsStep)
		}
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "@hicks")
		e.done("m1", "2", "@hicks")
		e.step("m1", "10", "@apone", "done", notes)
		e.step("m1", "11", "@apone", "done", verdictLine(true, "2", -1))

		hicks := findRow(t, e.get(), "hicks")
		wantInt(t, hicks, "reviewed_steps", 1)
		wantInt(t, hicks, "first_pass_approved", 1)
	})

	// A review with a verdict but no named step is excluded. The guard is
	// discriminable only through a legacy mission_steps row with a NULL step: such a
	// row is keyed "" in the handler's step map, so without the guard the review would
	// credit that row's agent (hicks).
	t.Run("excluded_review_has_verdict_but_no_step", func(t *testing.T) {
		const notes = "APPROVED, no critical issues."
		if v := review.Parse(notes, ""); v.Verdict != review.Approved || v.ReviewsStep != "" {
			t.Fatalf("fixture precondition: Parse(%q) = verdict %q, reviews_step %q; want %q and \"\"", notes, v.Verdict, v.ReviewsStep, review.Approved)
		}
		e := newCrewEnv(t)
		e.missions("m1")
		e.step("m1", nil, "@hicks", "done", "")
		e.done("m1", "2", "@hicks")
		e.step("m1", "10", "@apone", "done", notes)
		e.step("m1", "11", "@apone", "done", verdictLine(true, "2", -1))

		wantInt(t, findRow(t, e.get(), "hicks"), "reviewed_steps", 1)
	})

	// A review naming a step that no mission_steps row in its mission has is
	// excluded. The `ok` guard on the step-map lookup is not discriminable: a miss
	// reads a nil map, the agent assertion fails, and nothing is credited either way
	// (recorded as an equivalent mutant). The lookup key is discriminable: keyed by
	// mission alone, step "99" would resolve to a row of m1 and be credited.
	t.Run("excluded_review_names_step_missing_from_mission", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "@hicks")
		e.done("m1", "2", "@hicks")
		e.step("m1", "10", "@apone", "done", verdictLine(true, "99", -1))
		e.step("m1", "11", "@apone", "done", verdictLine(true, "2", -1))

		wantInt(t, findRow(t, e.get(), "hicks"), "reviewed_steps", 1)
	})

	// A review is matched to a step in its own mission only: m2's review names step 1,
	// which exists only in m1. The m1 control review of step 1 is credited once.
	t.Run("excluded_review_names_step_from_other_mission", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1", "m2")
		e.done("m1", "1", "@hicks")
		e.step("m1", "10", "@apone", "done", verdictLine(true, "1", -1))
		e.step("m2", "10", "@apone", "done", verdictLine(true, "1", -1))

		wantInt(t, findRow(t, e.get(), "hicks"), "reviewed_steps", 1)
	})

	// Only apone's steps are reviews: a verdict line written by hicks is ignored, and
	// the control review is written by "x:Apone", which CrewName normalises to apone.
	t.Run("excluded_review_not_written_by_apone", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "@hicks")
		e.done("m1", "2", "@hicks")
		e.step("m1", "10", "@hicks", "done", verdictLine(true, "1", -1))
		e.step("m1", "11", "x:Apone", "done", verdictLine(true, "2", -1))

		wantInt(t, findRow(t, e.get(), "hicks"), "reviewed_steps", 1)
	})

	// Top mistakes: 8 mistakes (2 high, 2 med, 2 low, 2 unrated) for hicks, so the cap
	// of 5 cuts 3 and a cap of 6 would differ. Expected order is impact (high > med >
	// low > unrated) then mission_id descending: m02 m01 m04 m03 m06. The cap hides
	// both unrated rows and m05, so a wrong impact rank or a wrong mission order
	// changes the identities. Mistake text is asserted per position. newt has exactly
	// 6 mistakes (all low) and must also be cut to 5, which separates a cut that
	// starts above 5 from one that starts at 6.
	t.Run("mistakes_top_cap_order_and_text", func(t *testing.T) {
		e := newCrewEnv(t)
		impacts := []any{"high", "high", "med", "med", "low", "low", nil, nil}
		for i, impact := range impacts {
			id := fmt.Sprintf("m%02d", i+1)
			e.missions(id)
			e.exec(`INSERT INTO agent_mistakes (mission_id, ord, agent, agent_raw, mistake, impact)
				VALUES (?, 1, 'hicks', 'hicks', ?, ?)`, id, "mistake in "+id, impact)
		}
		for i := 1; i <= 6; i++ {
			e.exec(`INSERT INTO agent_mistakes (mission_id, ord, agent, agent_raw, mistake, impact)
				VALUES (?, 2, 'newt', 'newt', 'newt mistake', 'low')`, fmt.Sprintf("m%02d", i))
		}

		hicks := findRow(t, e.get(), "hicks")
		mistakes := hicks["mistakes"].(map[string]interface{})
		for _, c := range []struct {
			key  string
			want int
		}{{"high", 2}, {"med", 2}, {"low", 2}, {"unrated", 2}} {
			if got := int(mistakes[c.key].(float64)); got != c.want {
				t.Fatalf("hicks: mistakes.%s = %d, want %d", c.key, got, c.want)
			}
		}
		top := mistakes["top"].([]interface{})
		if len(top) != 5 {
			t.Fatalf("hicks: len(mistakes.top) = %d, want exactly 5", len(top))
		}
		newtMistakes := findRow(t, e.get(), "newt")["mistakes"].(map[string]interface{})
		if n := len(newtMistakes["top"].([]interface{})); n != 5 {
			t.Fatalf("newt: len(mistakes.top) = %d with 6 mistakes, want exactly 5", n)
		}
		want := []struct{ mission, impact string }{
			{"m02", "high"}, {"m01", "high"}, {"m04", "med"}, {"m03", "med"}, {"m06", "low"},
		}
		for i, w := range want {
			item := top[i].(map[string]interface{})
			if item["mission_id"] != w.mission || item["impact"] != w.impact || item["mistake"] != "mistake in "+w.mission {
				t.Fatalf("hicks: mistakes.top[%d] = %v, want mission %s impact %s mistake %q",
					i, item, w.mission, w.impact, "mistake in "+w.mission)
			}
		}
	})

	// Top mistakes rank: one mistake per impact, in missions whose ids agree with
	// neither direction of the impact order. Expected: mb high, md med, ma low, mc
	// unrated (NULL impact). Mission id ascending would give ma mb mc md and descending
	// md mc mb ma, so only the impact rank produces the expected order. The cap does not
	// hide unrated here. newt has no mistakes and a step: its top is an empty array, not
	// null.
	t.Run("mistakes_top_impact_rank_unrated_last", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("ma", "mb", "mc", "md", "m1")
		e.done("m1", "1", "@newt")
		e.exec(`INSERT INTO agent_mistakes (mission_id, ord, agent, agent_raw, mistake, impact) VALUES
			('mc', 1, 'ripley', 'ripley', 'text c', NULL),
			('ma', 1, 'ripley', 'ripley', 'text a', 'low'),
			('md', 1, 'ripley', 'ripley', 'text d', 'med'),
			('mb', 1, 'ripley', 'ripley', 'text b', 'high')`)

		crew := e.get()
		top := findRow(t, crew, "ripley")["mistakes"].(map[string]interface{})["top"].([]interface{})
		if len(top) != 4 {
			t.Fatalf("ripley: len(mistakes.top) = %d, want 4", len(top))
		}
		for i, want := range []string{"mb high", "md med", "ma low", "mc unrated"} {
			item := top[i].(map[string]interface{})
			if got := item["mission_id"].(string) + " " + item["impact"].(string); got != want {
				t.Fatalf("ripley: mistakes.top[%d] = %q, want %q", i, got, want)
			}
		}
		newtTop, ok := findRow(t, crew, "newt")["mistakes"].(map[string]interface{})["top"].([]interface{})
		if !ok || len(newtTop) != 0 {
			t.Fatalf("newt: mistakes.top = %v, want empty array", findRow(t, crew, "newt")["mistakes"])
		}
	})

	// Mistakes with equal impact and equal mission_id order by ord ascending. Seven low
	// mistakes in one mission are inserted with ord descending (7 first), so a
	// comparator that leaves ties alone returns insertion order and the cap of 5 would
	// keep ords 7 to 3. Expected: ords 1 to 5.
	t.Run("mistakes_tie_on_impact_and_mission_ordered_by_ord", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "hicks")
		for ord := 7; ord >= 1; ord-- {
			e.exec(`INSERT INTO agent_mistakes (mission_id, ord, agent, agent_raw, mistake, impact)
				VALUES ('m1', ?, 'hicks', 'hicks', ?, 'low')`, ord, fmt.Sprintf("ord %d", ord))
		}

		top := findRow(t, e.get(), "hicks")["mistakes"].(map[string]interface{})["top"].([]interface{})
		if len(top) != 5 {
			t.Fatalf("hicks: len(mistakes.top) = %d, want exactly 5", len(top))
		}
		for i, item := range top {
			if got, want := item.(map[string]interface{})["mistake"], fmt.Sprintf("ord %d", i+1); got != want {
				t.Fatalf("hicks: mistakes.top[%d].mistake = %v, want %q", i, got, want)
			}
		}
	})

	// A mistake row whose agent is "@Hicks" counts for hicks and makes no second agent.
	// The control is a mistake filed under plain "hicks".
	t.Run("mistakes_agent_name_normalised", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1", "m2")
		e.done("m1", "1", "hicks")
		e.exec(`INSERT INTO agent_mistakes (mission_id, ord, agent, agent_raw, mistake, impact) VALUES
			('m1', 1, '@Hicks', '@Hicks', 'raw-cased agent', 'high'),
			('m2', 1, 'hicks', 'hicks', 'plain agent', 'low')`)

		crew := e.get()
		if got := strings.Join(agentNames(crew), ","); got != "hicks" {
			t.Fatalf("crew agents = %q, want %q", got, "hicks")
		}
		mistakes := findRow(t, crew, "hicks")["mistakes"].(map[string]interface{})
		if high, low := int(mistakes["high"].(float64)), int(mistakes["low"].(float64)); high != 1 || low != 1 {
			t.Fatalf("hicks: mistakes high=%d low=%d, want 1 and 1", high, low)
		}
	})

	// Grades reach only the agents with a step in the graded mission. m1 (A) has an
	// "@hicks" step, m2 (C) has newt, m3 (no grade row) has vasquez. A grade credited to
	// every agent would give hicks 2 graded missions instead of 1.
	t.Run("avg_grade_credited_only_to_agents_in_mission", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1", "m2", "m3")
		e.done("m1", "1", "@hicks")
		e.done("m2", "1", "newt")
		e.done("m3", "1", "vasquez")
		e.exec(`INSERT INTO mission_grades (mission_id, state, grade) VALUES ('m1', 'graded', 'A'), ('m2', 'graded', 'C')`)

		crew := e.get()
		hicks := findRow(t, crew, "hicks")
		wantInt(t, hicks, "graded_missions", 1)
		wantString(t, hicks, "avg_grade", "A")
		newt := findRow(t, crew, "newt")
		wantInt(t, newt, "graded_missions", 1)
		wantString(t, newt, "avg_grade", "C")
		vasquez := findRow(t, crew, "vasquez")
		wantInt(t, vasquez, "graded_missions", 0)
		wantNull(t, vasquez, "avg_grade")
	})

	// Crew rows that normalise to one name: a non-null role beats a null one whichever
	// row comes first, and between two non-null roles the row first in name order wins.
	// Names sort in byte order, so "@..." precedes the bare name.
	//   bishop: "@Bishop" NULL, then "bishop" commander -> commander
	//   ripley: "@Ripley" qa, then "ripley" NULL        -> qa
	//   zed:    "zed" second inserted first, "@Zed" first -> first
	t.Run("crew_role_non_null_beats_null_on_name_collision", func(t *testing.T) {
		e := newCrewEnv(t)
		e.exec(`INSERT INTO crew (name, role) VALUES
			('@Bishop', NULL), ('bishop', 'commander'),
			('@Ripley', 'qa'), ('ripley', NULL),
			('zed', 'second'), ('@Zed', 'first')`)

		crew := e.get()
		if got := strings.Join(agentNames(crew), ","); got != "bishop,ripley,zed" {
			t.Fatalf("crew agents = %q, want %q", got, "bishop,ripley,zed")
		}
		wantString(t, findRow(t, crew, "bishop"), "role", "commander")
		wantString(t, findRow(t, crew, "ripley"), "role", "qa")
		wantString(t, findRow(t, crew, "zed"), "role", "first")
	})

	// Order: steps_run desc, then agent asc. zed leads with 2 done steps; yankee has 1
	// done step of 4 (so ordering by steps_total would move it ahead of zed); alpha,
	// bravo, charlie, delta, echo and foxtrot tie with yankee at steps_run 1 and are
	// inserted out of name order. Six or more tied agents make a missing tiebreak
	// fail with near certainty, since ties would come back in map iteration order.
	t.Run("sort_steps_run_desc_then_name_asc", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "zed")
		e.done("m1", "2", "zed")
		for i, name := range []string{"echo", "bravo", "foxtrot", "alpha", "delta", "charlie"} {
			e.done("m1", fmt.Sprintf("a%d", i), name)
		}
		e.done("m1", "y1", "yankee")
		for i := 2; i <= 4; i++ {
			e.step("m1", fmt.Sprintf("y%d", i), "yankee", "pending", "")
		}

		got := strings.Join(agentNames(e.get()), ",")
		want := "zed,alpha,bravo,charlie,delta,echo,foxtrot,yankee"
		if got != want {
			t.Fatalf("crew order = %q, want %q", got, want)
		}
	})

	// An empty database serialises as exactly {"crew":[]}, not null.
	t.Run("empty_db", func(t *testing.T) {
		e := newCrewEnv(t)
		rec := httptest.NewRecorder()
		e.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/crew", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /v1/crew: %d %s", rec.Code, rec.Body.String())
		}
		if got := strings.TrimSpace(rec.Body.String()); got != `{"crew":[]}` {
			t.Fatalf("raw body = %q, want %q", got, `{"crew":[]}`)
		}
	})

	// "@Hicks", "x:hicks" and " HICKS " in mission_steps merge into one hicks row.
	t.Run("name_normalisation", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "@Hicks")
		e.done("m1", "2", "x:hicks")
		e.done("m1", "3", " HICKS ")

		crew := e.get()
		if got := strings.Join(agentNames(crew), ","); got != "hicks" {
			t.Fatalf("crew agents = %q, want %q", got, "hicks")
		}
		wantInt(t, crew[0].(map[string]interface{}), "steps_run", 3)
	})

	// An agent that normalises to empty is dropped from all three sources (steps,
	// mistakes, service records); only hicks remains.
	t.Run("empty_agent_names_dropped", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "hicks")
		e.step("m1", "2", "", "done", "")
		e.step("m1", "3", "@", "done", "")
		e.exec(`INSERT INTO agent_mistakes (mission_id, ord, agent, agent_raw, mistake, impact)
			VALUES ('m1', 1, '', '', 'orphan', 'low')`)
		e.exec(`INSERT INTO service_records (agent, record_date, title, note, source)
			VALUES ('', '2026-10-01', 't', 'n', 'self-reported')`)

		if got := strings.Join(agentNames(e.get()), ","); got != "hicks" {
			t.Fatalf("crew agents = %q, want %q", got, "hicks")
		}
	})

	// Service records split by source, and the agent is normalised: "@Hicks" counts
	// for hicks and makes no second agent. newt has none, so all three counts are 0.
	t.Run("service_records_split", func(t *testing.T) {
		e := newCrewEnv(t)
		e.missions("m1")
		e.done("m1", "1", "hicks")
		e.done("m1", "2", "newt")
		for i, rec := range []struct{ agent, source string }{
			{"hicks", "self-reported"}, {"@Hicks", "self-reported"}, {"hicks", "bishop-observed"},
		} {
			e.exec(`INSERT INTO service_records (agent, record_date, title, note, source)
				VALUES (?, '2026-10-01', ?, 'note', ?)`, rec.agent, fmt.Sprintf("record %d", i), rec.source)
		}

		crew := e.get()
		if got := strings.Join(agentNames(crew), ","); got != "hicks,newt" {
			t.Fatalf("crew agents = %q, want %q", got, "hicks,newt")
		}
		sr := findRow(t, crew, "hicks")["service_records"].(map[string]interface{})
		for _, c := range []struct {
			key  string
			want int
		}{{"self_reported", 2}, {"bishop_observed", 1}, {"total", 3}} {
			if got := int(sr[c.key].(float64)); got != c.want {
				t.Fatalf("hicks: service_records.%s = %d, want %d", c.key, got, c.want)
			}
		}
		nsr := findRow(t, crew, "newt")["service_records"].(map[string]interface{})
		for _, key := range []string{"total", "self_reported", "bishop_observed"} {
			if got := int(nsr[key].(float64)); got != 0 {
				t.Fatalf("newt: service_records.%s = %d, want 0", key, got)
			}
		}
	})
}

// TestCrewPageRoute checks that GET /crew returns 200 with text/html content type.
func TestCrewPageRoute(t *testing.T) {
	router, _ := newTriageTestRouter(t)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/crew", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /crew: status %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("GET /crew: Content-Type %q, want text/html", ct)
	}
}

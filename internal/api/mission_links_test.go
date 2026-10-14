package api

import (
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// insertMission adds a mission row directly, opened today.
func insertMission(t *testing.T, db *sql.DB, id, harness, status string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO missions (id, title, status, harness) VALUES (?, ?, ?, ?)`,
		id, "Mission "+id, status, harness,
	); err != nil {
		t.Fatalf("insert mission %s: %v", id, err)
	}
}

// stepTimes returns a step's started_at and ended_at.
func stepTimes(t *testing.T, db *sql.DB, missionID, step string) (started, ended sql.NullString) {
	t.Helper()
	if err := db.QueryRow(
		`SELECT started_at, ended_at FROM mission_steps WHERE mission_id = ? AND step = ?`,
		missionID, step,
	).Scan(&started, &ended); err != nil {
		t.Fatalf("lookup step %s/%s: %v", missionID, step, err)
	}
	return started, ended
}

// TestStepTimingIsStamped checks that a live mission's step gets started_at
// when it goes in-progress and ended_at when it finishes, that a caller's own
// value wins, and that a completed mission's replayed steps get neither.
func TestStepTimingIsStamped(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-live", "kirsch", "in-progress")
	insertMission(t, db, "m-done", "kirsch", "complete")

	post := func(mission string, body map[string]any) {
		t.Helper()
		if code, resp := do(t, router, http.MethodPost, "/v1/missions/"+mission+"/steps", body); code >= 300 {
			t.Fatalf("POST step: %d %v", code, resp)
		}
	}

	post("m-live", map[string]any{"step": "1", "agent": "@hicks", "status": "pending"})
	if started, ended := stepTimes(t, db, "m-live", "1"); started.Valid || ended.Valid {
		t.Fatalf("pending step has times (%v, %v), want none", started, ended)
	}

	post("m-live", map[string]any{"step": "1", "status": "in-progress"})
	started, ended := stepTimes(t, db, "m-live", "1")
	if !started.Valid || ended.Valid {
		t.Fatalf("in-progress step times = (%v, %v), want started only", started, ended)
	}

	post("m-live", map[string]any{"step": "1", "status": "done"})
	if s2, e2 := stepTimes(t, db, "m-live", "1"); s2 != started || !e2.Valid {
		t.Fatalf("done step times = (%v, %v), want started kept and ended set", s2, e2)
	}

	post("m-live", map[string]any{"step": "2", "status": "done", "ended_at": "2026-10-01 09:00:00"})
	if _, e := stepTimes(t, db, "m-live", "2"); e.String != "2026-10-01 09:00:00" {
		t.Fatalf("caller ended_at overwritten: %v", e)
	}

	post("m-done", map[string]any{"step": "1", "status": "in-progress"})
	post("m-done", map[string]any{"step": "1", "status": "done"})
	if s, e := stepTimes(t, db, "m-done", "1"); s.Valid || e.Valid {
		t.Fatalf("completed mission's step stamped (%v, %v), want none", s, e)
	}
}

// TestStepSyncSetsSummary checks that a step-sync journal row becomes the
// step's summary, and one for an unknown step changes nothing.
func TestStepSyncSetsSummary(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-1", "kirsch", "in-progress")
	if code, resp := do(t, router, http.MethodPost, "/v1/missions/m-1/steps",
		map[string]any{"step": "1", "agent": "@hicks", "status": "done"}); code >= 300 {
		t.Fatalf("POST step: %d %v", code, resp)
	}

	for _, step := range []string{"1", "9"} {
		if code, resp := do(t, router, http.MethodPost, "/v1/flight-recorder", map[string]any{
			"mission_id": "m-1", "step": step, "agent": "@lambert",
			"event": "step-sync", "note": "Step " + step + " landed",
		}); code >= 300 {
			t.Fatalf("POST flight-recorder: %d %v", code, resp)
		}
	}

	var summary sql.NullString
	if err := db.QueryRow(`SELECT summary FROM mission_steps WHERE mission_id = 'm-1' AND step = '1'`).Scan(&summary); err != nil {
		t.Fatalf("lookup summary: %v", err)
	}
	if summary.String != "Step 1 landed" {
		t.Fatalf("summary = %q, want %q", summary.String, "Step 1 landed")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_steps WHERE mission_id = 'm-1'`).Scan(&n); err != nil {
		t.Fatalf("count steps: %v", err)
	}
	if n != 1 {
		t.Fatalf("%d steps, want 1 (step-sync must not create steps)", n)
	}
}

// TestFindingTakesOpenMission checks that a finding with no mission_id takes
// its harness's open mission, unless it predates that mission or no mission
// of that harness is open.
func TestFindingTakesOpenMission(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-old", "kirsch", "complete")
	insertMission(t, db, "m-open", "kirsch", "in-progress")

	var today string
	if err := db.QueryRow(`SELECT date('now')`).Scan(&today); err != nil {
		t.Fatalf("today: %v", err)
	}

	cases := []struct {
		name, harness, date, mission, want string
	}{
		{"open mission", "kirsch", today, "", "m-open"},
		{"undated", "kirsch", "", "", "m-open"},
		{"explicit mission kept", "kirsch", today, "m-old", "m-old"},
		{"predates mission", "kirsch", "2000-01-01", "", ""},
		{"no open mission", "other", today, "", ""},
	}
	for _, tc := range cases {
		code, resp := do(t, router, http.MethodPost, "/v1/findings", map[string]any{
			"finding_date": tc.date, "target": "t", "suggestion": tc.name,
			"harness": tc.harness, "mission_id": tc.mission,
		})
		if code != http.StatusCreated {
			t.Fatalf("%s: POST finding: %d %v", tc.name, code, resp)
		}
		var got sql.NullString
		if err := db.QueryRow(`SELECT mission_id FROM findings WHERE id = ?`, resp["id"]).Scan(&got); err != nil {
			t.Fatalf("%s: lookup: %v", tc.name, err)
		}
		if got.String != tc.want {
			t.Errorf("%s: mission_id = %q, want %q", tc.name, got.String, tc.want)
		}
	}
}

// TestDocumentSyncFollowsHarnesses checks that an empty sync imports every
// registered harness tagged with its name, and that a mission update imports
// that mission's new files.
func TestDocumentSyncFollowsHarnesses(t *testing.T) {
	router, db := newTriageTestRouter(t)
	root := t.TempDir()
	missionDir := filepath.Join(root, "missions", "m-1")
	if err := os.MkdirAll(missionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(missionDir, "BRIEF.md"), []byte("# Brief\n"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}

	if code, resp := do(t, router, http.MethodPut, "/v1/harnesses/kirsch",
		map[string]any{"memory_root": root}); code != http.StatusOK {
		t.Fatalf("PUT harness: %d %v", code, resp)
	}
	if code, resp := do(t, router, http.MethodPost, "/v1/documents/sync", map[string]any{}); code != http.StatusOK {
		t.Fatalf("sync: %d %v", code, resp)
	}

	var mission, harness sql.NullString
	if err := db.QueryRow(`SELECT mission_id, harness FROM documents WHERE kind = 'brief'`).Scan(&mission, &harness); err != nil {
		t.Fatalf("lookup brief: %v", err)
	}
	if mission.String != "m-1" || harness.String != "kirsch" {
		t.Fatalf("brief tags = (%q, %q), want (m-1, kirsch)", mission.String, harness.String)
	}

	insertMission(t, db, "m-1", "kirsch", "in-progress")
	if err := os.WriteFile(filepath.Join(missionDir, "DEBRIEF.md"), []byte("# Debrief\n"), 0o644); err != nil {
		t.Fatalf("write debrief: %v", err)
	}
	if code, resp := do(t, router, http.MethodPatch, "/v1/missions/m-1",
		map[string]any{"status": "complete", "outcome": "done"}); code != http.StatusOK {
		t.Fatalf("PATCH mission: %d %v", code, resp)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM documents WHERE kind = 'debrief' AND mission_id = 'm-1'`).Scan(&n); err != nil {
		t.Fatalf("count debrief: %v", err)
	}
	if n != 1 {
		t.Fatalf("%d debrief documents after mission update, want 1", n)
	}
}

// TestHUDMission checks the mission board's counts and filters, the mission
// detail's linked records, and the findings mission_id filter the page uses.
func TestHUDMission(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-1", "kirsch", "in-progress")
	insertMission(t, db, "m-2", "other", "complete")
	for _, q := range []string{
		`INSERT INTO mission_steps (mission_id, step, agent, status) VALUES ('m-1', '1', '@hicks', 'done'), ('m-1', '2', 'kirsch:apone', 'pending')`,
		`INSERT INTO findings (id, finding_date, target, suggestion, mission_id, harness) VALUES (7, date('now'), 't', 's', 'm-1', 'kirsch'), (8, date('now'), 't', 's', NULL, 'kirsch')`,
		`INSERT INTO patterns (name, discovered_mission) VALUES ('p', 'm-1')`,
		`INSERT INTO crew (name, role) VALUES ('hicks', 'Developer'), ('vasquez', 'Tester')`,
		`INSERT INTO service_records (agent, record_date, note) VALUES ('hicks', date('now'), 'in range'), ('hicks', '2000-01-01', 'too old'), ('vasquez', date('now'), 'not on the mission')`,
		`INSERT INTO directive_proposals (title, applies_when, rule, rationale, reviewer_check, evidence, state, directive_id) VALUES ('d', 'w', 'r', 'r', 'c', '[7]', 'accepted', 'DIR-001'), ('x', 'w', 'r', 'r', 'c', '[8]', 'accepted', 'DIR-002')`,
		`INSERT INTO documents (source_path, kind, body, sha256, mission_id) VALUES ('/m/m-1/BRIEF.md', 'brief', 'Brief. Goal: Zebra goal', 'x', 'm-1')`,
		`INSERT INTO documents_fts (rowid, title, body) SELECT id, '', body FROM documents`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}

	code, board := do(t, router, http.MethodGet, "/v1/hud/missions", nil)
	if code != http.StatusOK {
		t.Fatalf("board: %d %v", code, board)
	}
	missions := board["missions"].([]any)
	if len(missions) != 2 {
		t.Fatalf("board has %d missions, want 2", len(missions))
	}
	for _, raw := range missions {
		m := raw.(map[string]any)
		if m["id"] == "m-1" && (m["steps"] != float64(2) || m["steps_done"] != float64(1) || m["findings"] != float64(1) || m["has_brief"] != float64(1)) {
			t.Errorf("m-1 counts = %v", m)
		}
	}
	for query, want := range map[string]int{"?harness=kirsch": 1, "?status=complete": 1, "?q=zebra": 1, "?q=m-2": 1, "?q=nothing": 0, "?q=%22unclosed": 0} {
		if _, d := do(t, router, http.MethodGet, "/v1/hud/missions"+query, nil); len(d["missions"].([]any)) != want {
			t.Errorf("board%s has %d missions, want %d", query, len(d["missions"].([]any)), want)
		}
	}

	code, d := do(t, router, http.MethodGet, "/v1/hud/missions/m-1", nil)
	if code != http.StatusOK {
		t.Fatalf("detail: %d %v", code, d)
	}
	for key, want := range map[string]int{"steps": 2, "documents": 1, "patterns": 1, "directives": 1, "crew": 1, "service_records": 1} {
		if got := len(d[key].([]any)); got != want {
			t.Errorf("detail %s has %d rows, want %d", key, got, want)
		}
	}
	if code, _ := do(t, router, http.MethodGet, "/v1/hud/missions/nope", nil); code != http.StatusNotFound {
		t.Errorf("unknown mission: %d, want 404", code)
	}

	_, f := do(t, router, http.MethodGet, "/v1/findings?mission_id=m-1", nil)
	if got := f["findings"].([]any); len(got) != 1 || got[0].(map[string]any)["id"] != float64(7) {
		t.Errorf("findings?mission_id=m-1 = %v, want only #7", got)
	}
}

// TestMissionHarnessIsRecorded checks that POST /v1/missions stores a
// harness, that a PATCH claims a harness-less mission once and can never move
// a mission to another harness, and that GET returns the harness.
func TestMissionHarnessIsRecorded(t *testing.T) {
	router, db := newTriageTestRouter(t)

	if code, resp := do(t, router, http.MethodPost, "/v1/missions",
		map[string]any{"id": "m-1", "title": "With harness", "harness": "kirsch"}); code != http.StatusCreated {
		t.Fatalf("create m-1: %d %v", code, resp)
	}
	if code, resp := do(t, router, http.MethodPost, "/v1/missions",
		map[string]any{"id": "m-2", "title": "Without harness"}); code != http.StatusCreated {
		t.Fatalf("create m-2: %d %v", code, resp)
	}
	// m-2 is claimed; m-1 keeps its harness.
	for _, id := range []string{"m-1", "m-2"} {
		if code, resp := do(t, router, http.MethodPatch, "/v1/missions/"+id,
			map[string]any{"harness": "other"}); code != http.StatusOK {
			t.Fatalf("PATCH %s: %d %v", id, code, resp)
		}
	}
	for id, want := range map[string]string{"m-1": "kirsch", "m-2": "other"} {
		var got sql.NullString
		if err := db.QueryRow(`SELECT harness FROM missions WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("lookup %s: %v", id, err)
		}
		if got.String != want {
			t.Errorf("%s harness = %q, want %q", id, got.String, want)
		}
	}
	if _, m := do(t, router, http.MethodGet, "/v1/missions/m-1", nil); m["harness"] != "kirsch" {
		t.Errorf("GET m-1 harness = %v, want kirsch", m["harness"])
	}
}

// TestMissionUpdateNoteNamesChanges checks that a mission.updated journal
// row says which fields a PATCH changed, from what to what.
func TestMissionUpdateNoteNamesChanges(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-1", "kirsch", "in-progress")

	for _, body := range []map[string]any{
		{"status": "complete", "next_action": "Archive"},
		{"status": "complete"},
	} {
		if code, resp := do(t, router, http.MethodPatch, "/v1/missions/m-1", body); code != http.StatusOK {
			t.Fatalf("PATCH %v: %d %v", body, code, resp)
		}
	}

	rows, err := db.Query(`SELECT note FROM flight_recorder WHERE mission_id = 'm-1' AND event = 'mission.updated' ORDER BY id`)
	if err != nil {
		t.Fatalf("query notes: %v", err)
	}
	defer rows.Close()
	notes := []string{}
	for rows.Next() {
		var note string
		if err := rows.Scan(&note); err != nil {
			t.Fatalf("scan: %v", err)
		}
		notes = append(notes, note)
	}
	want := []string{
		`Mission updated: status: "in-progress" → "complete"; next_action: (none) → "Archive"`,
		"Mission updated (no change)",
	}
	if len(notes) != len(want) {
		t.Fatalf("notes = %q, want %q", notes, want)
	}
	for i := range want {
		if notes[i] != want[i] {
			t.Errorf("note %d = %q, want %q", i, notes[i], want[i])
		}
	}
}

// TestHUDFlagsStaleMissions checks the board's stale flag: an unfinished
// mission with no activity for longer than staleAfter is stale, recent
// journal activity keeps one fresh, and a complete mission is never stale.
func TestHUDFlagsStaleMissions(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "idle", "kirsch", "not-started")
	insertMission(t, db, "busy", "kirsch", "in-progress")
	insertMission(t, db, "done", "kirsch", "complete")
	for _, q := range []string{
		`UPDATE missions SET updated_at = datetime('now', '-5 days')`,
		// A step event today, without touching the mission row.
		`INSERT INTO flight_recorder (mission_id, event, note) VALUES ('busy', 'mission.step', 'Mission step 1 created (status: in-progress)')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}

	_, board := do(t, router, http.MethodGet, "/v1/hud/missions", nil)
	stale := map[string]float64{}
	for _, raw := range board["missions"].([]any) {
		m := raw.(map[string]any)
		stale[m["id"].(string)] = m["stale"].(float64)
	}
	if stale["idle"] != 1 || stale["busy"] != 0 || stale["done"] != 0 {
		t.Errorf("stale = %v, want only idle", stale)
	}
	if _, d := do(t, router, http.MethodGet, "/v1/hud/missions?stale=1", nil); len(d["missions"].([]any)) != 1 {
		t.Errorf("?stale=1 returned %d missions, want 1", len(d["missions"].([]any)))
	}
}

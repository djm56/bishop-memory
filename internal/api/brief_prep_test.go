// Package api — tests for GET /v1/brief-prep.
//
// Each top-level subtest opens its own fresh database (newBriefPrepRouter)
// seeded with documents, missions, mission_lessons, directives,
// directive_proposals, and patterns as needed. The child subtests of
// hostile_goals_no_500 are the exception: they share their parent's database.
//
// Tests mount briefPrepHandler on a fresh gin.New() router, not the production
// router, and drive it through router.ServeHTTP. Every failure message starts
// with its subtest's tag so a kill identifies the property that died.
package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"

	"bishop-memory/internal/store"
)

// openBriefPrepTestDB opens a fresh temp-file SQLite database with
// schema applied.
func openBriefPrepTestDB(t *testing.T) *sql.DB {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite",
		"file:"+dbPath+
			"?_pragma=busy_timeout(5000)"+
			"&_pragma=foreign_keys(1)"+
			"&_pragma=journal_mode(WAL)",
	)
	if err != nil {
		t.Fatalf("open test sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	schemaPath := filepath.Join("..", "..", "db", "schema.sql")
	if err := store.ApplySchema(db, schemaPath); err != nil {
		t.Fatalf("apply schema %s: %v", schemaPath, err)
	}
	if err := store.EnsureColumns(db); err != nil {
		t.Fatalf("ensure columns: %v", err)
	}
	return db
}

// seedDocumentWithMission inserts a document into the database and its FTS index,
// optionally setting mission_id and harness.
func seedDocumentWithMission(t *testing.T, db *sql.DB, path, title, kind, body string, missionID, harness *string) int64 {
	t.Helper()

	sha := "abc123"
	result, err := db.Exec(
		`INSERT INTO documents (source_path, title, kind, body, sha256)
		 VALUES (?, ?, ?, ?, ?)`,
		path, title, kind, body, sha)
	if err != nil {
		t.Fatalf("seed document: %v", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	// Insert into FTS index with the same rowid.
	_, err = db.Exec(
		`INSERT INTO documents_fts(rowid, title, body) VALUES (?, ?, ?)`,
		id, title, body)
	if err != nil {
		t.Fatalf("seed documents_fts: %v", err)
	}

	// Update mission_id and harness in documents if provided.
	if missionID != nil || harness != nil {
		query := `UPDATE documents SET `
		args := []interface{}{}
		parts := []string{}

		if missionID != nil {
			parts = append(parts, `mission_id = ?`)
			args = append(args, *missionID)
		}
		if harness != nil {
			parts = append(parts, `harness = ?`)
			args = append(args, *harness)
		}

		query += strings.Join(parts, ", ") + ` WHERE id = ?`
		args = append(args, id)

		_, err = db.Exec(query, args...)
		if err != nil {
			t.Fatalf("update document mission_id/harness: %v", err)
		}
	}

	return id
}

// seedMission inserts a mission.
func seedMission(t *testing.T, db *sql.DB, id, title, status string, outcome, harness *string) {
	t.Helper()

	_, err := db.Exec(
		`INSERT INTO missions (id, title, status, outcome, harness)
		 VALUES (?, ?, ?, ?, ?)`,
		id, title, status, outcome, harness)
	if err != nil {
		t.Fatalf("seed mission: %v", err)
	}
}

// seedMissionGrade inserts a mission grade.
func seedMissionGrade(t *testing.T, db *sql.DB, missionID, grade string) {
	t.Helper()

	_, err := db.Exec(
		`INSERT INTO mission_grades (mission_id, grade, state)
		 VALUES (?, ?, 'graded')`,
		missionID, grade)
	if err != nil {
		t.Fatalf("seed mission grade: %v", err)
	}
}

// seedMissionLesson inserts a mission lesson.
func seedMissionLesson(t *testing.T, db *sql.DB, missionID, kind string, ord int, text, why, correction *string) {
	t.Helper()

	_, err := db.Exec(
		`INSERT INTO mission_lessons (mission_id, kind, ord, text, why, correction)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		missionID, kind, ord, text, why, correction)
	if err != nil {
		t.Fatalf("seed mission lesson: %v", err)
	}
}

// seedDirective inserts a directive.
func seedDirective(t *testing.T, db *sql.DB, directiveID, title, rule, rationale string) {
	t.Helper()

	_, err := db.Exec(
		`INSERT INTO directives (directive_id, title, rule, rationale)
		 VALUES (?, ?, ?, ?)`,
		directiveID, title, rule, rationale)
	if err != nil {
		t.Fatalf("seed directive: %v", err)
	}
}

// seedDirectiveProposal inserts a directive proposal.
func seedDirectiveProposal(t *testing.T, db *sql.DB, directiveID, state string) {
	t.Helper()

	_, err := db.Exec(
		`INSERT INTO directive_proposals (title, applies_when, rule, rationale, reviewer_check, evidence, state, directive_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"proposal", "when...", "rule...", "rationale...", "review...", "evidence...", state, directiveID)
	if err != nil {
		t.Fatalf("seed directive proposal: %v", err)
	}
}

// seedPattern inserts a pattern.
func seedPattern(t *testing.T, db *sql.DB, name, context, solution string) int64 {
	t.Helper()

	result, err := db.Exec(
		`INSERT INTO patterns (name, context, solution)
		 VALUES (?, ?, ?)`,
		name, context, solution)
	if err != nil {
		t.Fatalf("seed pattern: %v", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	return id
}

// newBriefPrepRouter opens a fresh database and mounts briefPrepHandler on a
// fresh gin.New() router (entry point: router.ServeHTTP, not the production router).
func newBriefPrepRouter(t *testing.T) (*sql.DB, *gin.Engine) {
	t.Helper()
	db := openBriefPrepTestDB(t)
	router := gin.New()
	router.GET("/v1/brief-prep", briefPrepHandler(db))
	return db, router
}

// briefPrepGet issues GET /v1/brief-prep with params and fails, prefixed with
// tag, unless the status is wantCode.
func briefPrepGet(t *testing.T, router *gin.Engine, tag string, params url.Values, wantCode int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/brief-prep?"+params.Encode(), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != wantCode {
		t.Fatalf("%s: status = %d, want %d, body: %s", tag, rec.Code, wantCode, rec.Body.String())
	}
	return rec
}

// briefPrepDecode decodes the response into briefPrepResponse, failing with tag.
func briefPrepDecode(t *testing.T, tag string, rec *httptest.ResponseRecorder) briefPrepResponse {
	t.Helper()
	var decoded briefPrepResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("%s: response not valid JSON: %v", tag, err)
	}
	return decoded
}

// goalParams returns query params carrying only goal.
func goalParams(goal string) url.Values {
	return url.Values{"goal": {goal}}
}

// missionIDsOf returns the mission_ids of resp in order.
func missionIDsOf(resp briefPrepResponse) []string {
	ids := make([]string, 0, len(resp.Missions))
	for _, m := range resp.Missions {
		ids = append(ids, m.MissionID)
	}
	return ids
}

// asJSONMap asserts v is a JSON object, failing with tag and what.
func asJSONMap(t *testing.T, tag, what string, v interface{}) map[string]interface{} {
	t.Helper()
	m, ok := v.(map[string]interface{})
	if !ok {
		t.Fatalf("%s: %s is %T, want JSON object", tag, what, v)
	}
	return m
}

// asJSONSlice asserts v is a JSON array, failing with tag and what.
func asJSONSlice(t *testing.T, tag, what string, v interface{}) []interface{} {
	t.Helper()
	s, ok := v.([]interface{})
	if !ok {
		t.Fatalf("%s: %s is %T, want JSON array (null or missing is a defect)", tag, what, v)
	}
	return s
}

// sortedKeys returns the keys of m sorted and comma-joined.
func sortedKeys(m map[string]interface{}) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// requireEmptyArrays fails with tag unless the raw body carries all three
// top-level arrays as literal [].
func requireEmptyArrays(t *testing.T, tag, body string) {
	t.Helper()
	for _, want := range []string{`"missions":[]`, `"directives":[]`, `"patterns":[]`} {
		if !strings.Contains(body, want) {
			t.Fatalf("%s: raw body missing %s (null or non-empty), got: %s", tag, want, body)
		}
	}
}

// TestBriefPrep runs all subtests.
func TestBriefPrep(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("missing_goal_400", func(t *testing.T) {
		const tag = "missing_goal_400"
		_, router := newBriefPrepRouter(t)

		rec := briefPrepGet(t, router, tag, url.Values{}, http.StatusBadRequest)

		var decoded map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("%s: response body not valid JSON: %v", tag, err)
		}
		if decoded["error"] != "invalid request" {
			t.Fatalf("%s: error = %v, want \"invalid request\"", tag, decoded["error"])
		}
		if details, ok := decoded["details"].(string); !ok || details != "goal is required" {
			t.Fatalf("%s: details = %v, want \"goal is required\"", tag, decoded["details"])
		}
	})

	t.Run("short_unicode_token_dropped", func(t *testing.T) {
		const tag = "short_unicode_token_dropped"
		db, router := newBriefPrepRouter(t)

		// mu1 brief body is exactly "日本" (2 runes, 6 bytes): below the 3-rune minimum.
		mu1, mu2 := "mu1", "mu2"
		seedMission(t, db, mu1, "Mission 1", "complete", nil, nil)
		seedDocumentWithMission(t, db, "/path1", "Brief 1", "brief", "日本", &mu1, nil)
		// mu2 brief body is "crew roster" (matches the surviving token "crew").
		seedMission(t, db, mu2, "Mission 2", "complete", nil, nil)
		seedDocumentWithMission(t, db, "/path2", "Brief 2", "brief", "crew roster", &mu2, nil)

		// Goal "日本 crew": 日本 is dropped by rune count; crew is kept.
		rec := briefPrepGet(t, router, tag, goalParams("日本 crew"), http.StatusOK)
		decoded := briefPrepDecode(t, tag, rec)

		if got := missionIDsOf(decoded); len(got) != 1 || got[0] != mu2 {
			t.Fatalf("%s: 2-rune CJK token must be dropped (rune count, not bytes); missions = %v, want [mu2] only", tag, got)
		}
	})

	t.Run("stopwords_only_200_empty", func(t *testing.T) {
		const tag = "stopwords_only_200_empty"
		_, router := newBriefPrepRouter(t)

		rec := briefPrepGet(t, router, tag, goalParams("the and for"), http.StatusOK)
		decoded := briefPrepDecode(t, tag, rec)

		if len(decoded.Missions) != 0 || len(decoded.Directives) != 0 || len(decoded.Patterns) != 0 {
			t.Fatalf("%s: stopword-only goal must yield no results; got %d missions, %d directives, %d patterns",
				tag, len(decoded.Missions), len(decoded.Directives), len(decoded.Patterns))
		}
		requireEmptyArrays(t, tag, rec.Body.String())
	})

	t.Run("or_match_finds_partial", func(t *testing.T) {
		const tag = "or_match_finds_partial"
		db, router := newBriefPrepRouter(t)

		// m1 brief contains "metrics" only; the goal has two further terms it lacks.
		seedMission(t, db, "m1", "Mission 1", "complete", nil, nil)
		seedDocumentWithMission(t, db, "/path1", "Brief 1", "brief", "metrics", &m1, nil)

		rec := briefPrepGet(t, router, tag, goalParams("crew page metrics"), http.StatusOK)
		decoded := briefPrepDecode(t, tag, rec)

		if got := missionIDsOf(decoded); len(got) != 1 || got[0] != "m1" {
			t.Fatalf("%s: OR semantics must match a brief holding one goal term; missions = %v, want [m1]", tag, got)
		}
	})

	t.Run("ranking_more_terms_first", func(t *testing.T) {
		const tag = "ranking_more_terms_first"
		db, router := newBriefPrepRouter(t)

		seedMission(t, db, "m1", "Mission 1", "complete", nil, nil)
		seedMission(t, db, "m2", "Mission 2", "complete", nil, nil)
		seedDocumentWithMission(t, db, "/path1", "Brief 1", "brief", "crew page metrics analysis data", &m1, nil)
		seedDocumentWithMission(t, db, "/path2", "Brief 2", "brief", "page layout information", &m2, nil)

		rec := briefPrepGet(t, router, tag, goalParams("crew page metrics"), http.StatusOK)
		decoded := briefPrepDecode(t, tag, rec)

		got := missionIDsOf(decoded)
		if len(got) != 2 {
			t.Fatalf("%s: both matching missions must be returned; missions = %v, want [m1 m2]", tag, got)
		}
		if got[0] != "m1" {
			t.Fatalf("%s: mission matching more terms must rank first; missions = %v, want m1 first", tag, got)
		}
	})

	t.Run("brief_kind_only", func(t *testing.T) {
		const tag = "brief_kind_only"
		db, router := newBriefPrepRouter(t)

		seedMission(t, db, "m3", "Mission 3", "complete", nil, nil)
		seedMission(t, db, "m4", "Mission 4", "complete", nil, nil)
		// m3: matching documents of the wrong kind.
		seedDocumentWithMission(t, db, "/path3a", "Progress 3", "progress", "metrics analysis performance", &m3, nil)
		seedDocumentWithMission(t, db, "/path3b", "Debrief 3", "debrief", "metrics analysis findings", &m3, nil)
		// m4: matching brief (control, admitted by the same path).
		seedDocumentWithMission(t, db, "/path4", "Brief 4", "brief", "metrics analysis results", &m4, nil)

		rec := briefPrepGet(t, router, tag, goalParams("metrics analysis"), http.StatusOK)
		decoded := briefPrepDecode(t, tag, rec)

		if got := missionIDsOf(decoded); len(got) != 1 || got[0] != "m4" {
			t.Fatalf("%s: only kind='brief' documents may match; missions = %v, want [m4] (m3 has progress/debrief only)", tag, got)
		}
	})

	t.Run("lessons_split_by_kind", func(t *testing.T) {
		const tag = "lessons_split_by_kind"
		db, router := newBriefPrepRouter(t)

		seedMission(t, db, "m1", "Mission 1", "complete", nil, nil)
		seedDocumentWithMission(t, db, "/path1", "Brief 1", "brief", "metrics data analysis", &m1, nil)

		// wrong-assumption ord 1: why and correction present.
		text1, why, correction := "bad assumption text", "assumed wrong thing", "should have done this"
		seedMissionLesson(t, db, "m1", "wrong-assumption", 1, &text1, &why, &correction)
		// wrong-assumption ord 2: why and correction NULL.
		text2 := "second assumption"
		seedMissionLesson(t, db, "m1", "wrong-assumption", 2, &text2, nil, nil)
		// reuse-step: NULL why/correction; the item must carry only ord and text.
		text3 := "step text here"
		seedMissionLesson(t, db, "m1", "reuse-step", 1, &text3, nil, nil)
		// similar-mission: must not appear anywhere.
		text4 := "similar trigger"
		seedMissionLesson(t, db, "m1", "similar-mission", 1, &text4, nil, nil)

		rec := briefPrepGet(t, router, tag, goalParams("metrics data"), http.StatusOK)

		var raw map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatalf("%s: response not valid JSON: %v", tag, err)
		}
		missions := asJSONSlice(t, tag, "missions", raw["missions"])
		if len(missions) != 1 {
			t.Fatalf("%s: missions count = %d, want 1", tag, len(missions))
		}
		mission := asJSONMap(t, tag, "missions[0]", missions[0])
		wrong := asJSONSlice(t, tag, "wrong_assumptions", mission["wrong_assumptions"])
		reuse := asJSONSlice(t, tag, "reuse_checklist", mission["reuse_checklist"])

		// Wrong assumptions: both rows, ordered by ord, none from other kinds.
		if len(wrong) != 2 {
			t.Fatalf("%s: wrong_assumptions count = %d, want 2 (reuse-step and similar-mission must not appear)", tag, len(wrong))
		}
		w0 := asJSONMap(t, tag, "wrong_assumptions[0]", wrong[0])
		if w0["why"] != why {
			t.Fatalf("%s: wrong_assumptions[0].why = %v, want %q", tag, w0["why"], why)
		}
		if w0["correction"] != correction {
			t.Fatalf("%s: wrong_assumptions[0].correction = %v, want %q", tag, w0["correction"], correction)
		}
		w1 := asJSONMap(t, tag, "wrong_assumptions[1]", wrong[1])
		if w1["text"] != text2 {
			t.Fatalf("%s: wrong_assumptions[1].text = %v, want %q (ord order)", tag, w1["text"], text2)
		}
		if v, ok := w1["why"]; !ok || v != nil {
			t.Fatalf("%s: wrong_assumptions[1].why must be present as null; present=%v value=%v", tag, ok, v)
		}
		if v, ok := w1["correction"]; !ok || v != nil {
			t.Fatalf("%s: wrong_assumptions[1].correction must be present as null (omitted is a contract break); present=%v value=%v", tag, ok, v)
		}

		// Reuse checklist: exactly {ord,text}.
		if len(reuse) != 1 {
			t.Fatalf("%s: reuse_checklist count = %d, want 1 (only the reuse-step)", tag, len(reuse))
		}
		r0 := asJSONMap(t, tag, "reuse_checklist[0]", reuse[0])
		if r0["text"] != text3 {
			t.Fatalf("%s: reuse_checklist[0].text = %v, want %q", tag, r0["text"], text3)
		}
		if got := sortedKeys(r0); got != "ord,text" {
			t.Fatalf("%s: reuse_checklist[0] keys = %q, want \"ord,text\" (reuse items carry no why/correction)", tag, got)
		}
		if strings.Contains(rec.Body.String(), "similar trigger") {
			t.Fatalf("%s: similar-mission text leaked into the response", tag)
		}
	})

	t.Run("directives_active_only", func(t *testing.T) {
		const tag = "directives_active_only"
		db, router := newBriefPrepRouter(t)

		// Active directive (no proposal): the control admitted by the same path.
		seedDirective(t, db, "DIR-001", "Title 1", "metrics rule", "rationale metrics")
		// Under-review directive (pending proposal).
		seedDirective(t, db, "DIR-002", "Title 2", "metrics rule", "rationale metrics")
		seedDirectiveProposal(t, db, "DIR-002", "pending")
		// Retired directive (declined proposal).
		seedDirective(t, db, "DIR-003", "Title 3", "metrics rule", "rationale metrics")
		seedDirectiveProposal(t, db, "DIR-003", "declined")

		rec := briefPrepGet(t, router, tag, goalParams("metrics"), http.StatusOK)
		decoded := briefPrepDecode(t, tag, rec)

		if len(decoded.Directives) != 1 {
			t.Fatalf("%s: only active directives may be returned; count = %d, want 1 (DIR-002 pending and DIR-003 declined excluded)", tag, len(decoded.Directives))
		}
		if decoded.Directives[0].DirectiveID != "DIR-001" {
			t.Fatalf("%s: directive ID = %s, want DIR-001", tag, decoded.Directives[0].DirectiveID)
		}
	})

	t.Run("directive_scoring", func(t *testing.T) {
		const tag = "directive_scoring"
		db, router := newBriefPrepRouter(t)

		// DIR-001 matches 2 goal tokens, DIR-002 matches 1, DIR-003 matches 0.
		seedDirective(t, db, "DIR-001", "Title", "crew page rule", "")
		seedDirective(t, db, "DIR-002", "Title", "page rule", "")
		seedDirective(t, db, "DIR-003", "Title", "unrelated stuff", "rationale")

		rec := briefPrepGet(t, router, tag, goalParams("crew page metrics"), http.StatusOK)
		decoded := briefPrepDecode(t, tag, rec)

		for _, d := range decoded.Directives {
			if d.DirectiveID == "DIR-003" {
				t.Fatalf("%s: zero-score directive DIR-003 must be absent", tag)
			}
		}
		if len(decoded.Directives) != 2 {
			t.Fatalf("%s: directives count = %d, want 2 (DIR-001 and DIR-002 score > 0)", tag, len(decoded.Directives))
		}
		if d := decoded.Directives[0]; d.DirectiveID != "DIR-001" || d.Score != 2 {
			t.Fatalf("%s: first directive = %s score %d, want DIR-001 score 2", tag, d.DirectiveID, d.Score)
		}
		if d := decoded.Directives[1]; d.DirectiveID != "DIR-002" || d.Score != 1 {
			t.Fatalf("%s: second directive = %s score %d, want DIR-002 score 1", tag, d.DirectiveID, d.Score)
		}
	})

	t.Run("pattern_scoring_filter", func(t *testing.T) {
		const tag = "pattern_scoring_filter"
		db, router := newBriefPrepRouter(t)

		// Goal tokens: crew, page, metrics. p1 scores 1, p2 scores 3, p3 scores 0.
		p1 := seedPattern(t, db, "alpha", "metrics notes", "")
		p2 := seedPattern(t, db, "beta", "crew page metrics", "")
		p3 := seedPattern(t, db, "gamma", "unrelated stuff", "")

		rec := briefPrepGet(t, router, tag, goalParams("crew page metrics"), http.StatusOK)
		decoded := briefPrepDecode(t, tag, rec)

		for _, p := range decoded.Patterns {
			if p.ID == int(p3) {
				t.Fatalf("%s: zero-score pattern id=%d must be absent (score %d admitted)", tag, p3, p.Score)
			}
		}
		got := make([]int, 0, len(decoded.Patterns))
		for _, p := range decoded.Patterns {
			got = append(got, p.ID)
		}
		want := []int{int(p2), int(p1)}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s: pattern ids = %v, want %v (score desc: p2 score 3 then p1 score 1)", tag, got, want)
		}
		if decoded.Patterns[0].Score != 3 || decoded.Patterns[1].Score != 1 {
			t.Fatalf("%s: pattern scores = %d,%d, want 3,1", tag, decoded.Patterns[0].Score, decoded.Patterns[1].Score)
		}
	})

	t.Run("pattern_cap_five_tiebreak", func(t *testing.T) {
		const tag = "pattern_cap_five_tiebreak"
		db, router := newBriefPrepRouter(t)

		// Seven patterns, each scoring 1: the cap keeps the five lowest ids.
		var ids []int
		for i := 0; i < 7; i++ {
			ids = append(ids, int(seedPattern(t, db, fmt.Sprintf("pat%d", i), "metrics content", "")))
		}

		rec := briefPrepGet(t, router, tag, goalParams("crew page metrics"), http.StatusOK)
		decoded := briefPrepDecode(t, tag, rec)

		got := make([]int, 0, len(decoded.Patterns))
		for _, p := range decoded.Patterns {
			got = append(got, p.ID)
		}
		if fmt.Sprint(got) != fmt.Sprint(ids[:5]) {
			t.Fatalf("%s: pattern ids = %v, want %v (top 5 of 7 equal scores, id asc)", tag, got, ids[:5])
		}
	})

	t.Run("hostile_goals_no_500", func(t *testing.T) {
		const tag = "hostile_goals_no_500"
		db, router := newBriefPrepRouter(t)

		// One control mission whose brief body is "crew".
		seedMission(t, db, "mcontrol", "Control Mission", "complete", nil, nil)
		seedDocumentWithMission(t, db, "/pathcontrol", "Control Brief", "brief", "crew", idPtr("mcontrol"), nil)

		t.Run("control_goal_crew", func(t *testing.T) {
			ctag := tag + "/control_goal_crew"
			rec := briefPrepGet(t, router, ctag, goalParams("crew"), http.StatusOK)
			decoded := briefPrepDecode(t, ctag, rec)
			if got := missionIDsOf(decoded); len(got) != 1 || got[0] != "mcontrol" {
				t.Fatalf("%s: plain goal must return the control mission; missions = %v, want [mcontrol]", ctag, got)
			}
		})

		// Goals carrying "crew" plus FTS5 syntax. If the raw goal reached MATCH
		// these would be syntax errors (crew NOT, crew OR, unbalanced quote) or a
		// different query; only the tokenizer reducing them to the token "crew"
		// returns mcontrol with status 200.
		withCrew := []string{`crew*`, `"crew"`, `crew NOT`, `crew OR`, `crew" OR "x`}
		for _, goal := range withCrew {
			goal := goal
			t.Run("tokenized_"+strings.ReplaceAll(goal, " ", "_"), func(t *testing.T) {
				gtag := fmt.Sprintf("%s goal %q", tag, goal)
				rec := briefPrepGet(t, router, gtag, goalParams(goal), http.StatusOK)
				decoded := briefPrepDecode(t, gtag, rec)
				if got := missionIDsOf(decoded); len(got) != 1 || got[0] != "mcontrol" {
					t.Fatalf("%s: tokenizer must reduce the goal to \"crew\" and return the control; missions = %v, want [mcontrol]", gtag, got)
				}
			})
		}

		// Goals that yield no match (no tokens, or tokens absent from the corpus):
		// must be 200 with all three arrays literally [].
		noMatch := []string{`step-sync "unbalanced`, `*`, `(`, `NOT`, `col:term`, `a"b`, `NEAR(x y)`}
		for _, goal := range noMatch {
			goal := goal
			t.Run("no_match_"+strings.ReplaceAll(goal, " ", "_"), func(t *testing.T) {
				gtag := fmt.Sprintf("%s goal %q", tag, goal)
				rec := briefPrepGet(t, router, gtag, goalParams(goal), http.StatusOK)
				requireEmptyArrays(t, gtag, rec.Body.String())
			})
		}
	})

	t.Run("response_arrays_present_and_non_null", func(t *testing.T) {
		const tag = "response_arrays_present_and_non_null"
		db, router := newBriefPrepRouter(t)

		// Stopword path: no tokens.
		rec := briefPrepGet(t, router, tag+" stopword path", goalParams("the and for"), http.StatusOK)
		requireEmptyArrays(t, tag+" stopword path", rec.Body.String())

		// Non-empty tokens, nothing matches: top-level arrays built by the
		// full handler path must still be [] rather than null.
		seedMission(t, db, "mother", "Other Mission", "complete", nil, nil)
		seedDocumentWithMission(t, db, "/pathother", "Other Brief", "brief", "something else entirely", idPtr("mother"), nil)
		rec2 := briefPrepGet(t, router, tag+" no-match path", goalParams("zzzunmatched"), http.StatusOK)
		requireEmptyArrays(t, tag+" no-match path", rec2.Body.String())

		// A matched mission with no lessons carries nested empty arrays.
		seedMission(t, db, "mlessons", "Test Mission", "complete", nil, nil)
		seedDocumentWithMission(t, db, "/pathlessons", "Test Brief", "brief", "example data", idPtr("mlessons"), nil)
		rec3 := briefPrepGet(t, router, tag+" nested path", goalParams("example data"), http.StatusOK)
		body3 := rec3.Body.String()
		if !strings.Contains(body3, `"wrong_assumptions":[]`) {
			t.Fatalf("%s: raw body missing '\"wrong_assumptions\":[]', got: %s", tag, body3)
		}
		if !strings.Contains(body3, `"reuse_checklist":[]`) {
			t.Fatalf("%s: raw body missing '\"reuse_checklist\":[]', got: %s", tag, body3)
		}
	})

	t.Run("three_rune_minimum_ascii", func(t *testing.T) {
		const tag = "three_rune_minimum_ascii"
		db, router := newBriefPrepRouter(t)

		// m1 brief "ab" (2 runes, below the minimum); m2 brief "abc" (3 runes, at it).
		m1ascii, m2ascii := "m1", "m2"
		seedMission(t, db, m1ascii, "Mission 1", "complete", nil, nil)
		seedDocumentWithMission(t, db, "/path1", "Brief 1", "brief", "ab", &m1ascii, nil)
		seedMission(t, db, m2ascii, "Mission 2", "complete", nil, nil)
		seedDocumentWithMission(t, db, "/path2", "Brief 2", "brief", "abc", &m2ascii, nil)

		rec := briefPrepGet(t, router, tag, goalParams("ab abc"), http.StatusOK)
		decoded := briefPrepDecode(t, tag, rec)

		if got := missionIDsOf(decoded); len(got) != 1 || got[0] != m2ascii {
			t.Fatalf("%s: 2-rune ASCII token dropped and 3-rune kept; missions = %v, want [m2] only", tag, got)
		}
	})

	t.Run("harness_filter", func(t *testing.T) {
		const tag = "harness_filter"
		db, router := newBriefPrepRouter(t)

		h1, h2 := "kirsch", "bishop"
		seedMission(t, db, "m1", "Mission 1", "complete", nil, &h1)
		seedMission(t, db, "m2", "Mission 2", "complete", nil, &h2)
		seedMission(t, db, "m3", "Mission 3", "complete", nil, nil)
		seedDocumentWithMission(t, db, "/path1", "Brief 1", "brief", "metrics data", &m1, nil)
		seedDocumentWithMission(t, db, "/path2", "Brief 2", "brief", "metrics data", &m2, nil)
		seedDocumentWithMission(t, db, "/path3", "Brief 3", "brief", "metrics data", &m3, nil)

		params := goalParams("metrics data")
		params.Set("harness", "kirsch")
		decoded := briefPrepDecode(t, tag, briefPrepGet(t, router, tag, params, http.StatusOK))
		if got := missionIDsOf(decoded); len(got) != 1 || got[0] != "m1" {
			t.Fatalf("%s: harness=kirsch must return only that harness; missions = %v, want [m1]", tag, got)
		}

		decoded2 := briefPrepDecode(t, tag, briefPrepGet(t, router, tag, goalParams("metrics data"), http.StatusOK))
		if len(decoded2.Missions) != 3 {
			t.Fatalf("%s: without harness all missions match; count = %d, want 3", tag, len(decoded2.Missions))
		}
	})

	t.Run("exclude_param", func(t *testing.T) {
		const tag = "exclude_param"
		db, router := newBriefPrepRouter(t)

		seedMission(t, db, "m1", "Mission 1", "complete", nil, nil)
		seedMission(t, db, "m2", "Mission 2", "complete", nil, nil)
		seedDocumentWithMission(t, db, "/path1", "Brief 1", "brief", "metrics data", &m1, nil)
		seedDocumentWithMission(t, db, "/path2", "Brief 2", "brief", "metrics data", &m2, nil)

		params := goalParams("metrics data")
		params.Set("exclude", "m1")
		decoded := briefPrepDecode(t, tag, briefPrepGet(t, router, tag, params, http.StatusOK))
		if got := missionIDsOf(decoded); len(got) != 1 || got[0] != "m2" {
			t.Fatalf("%s: exclude=m1 must drop m1 and keep m2; missions = %v, want [m2]", tag, got)
		}
	})

	t.Run("limit_five_missions", func(t *testing.T) {
		const tag = "limit_five_missions"
		db, router := newBriefPrepRouter(t)

		for i := 1; i <= 6; i++ {
			id := fmt.Sprintf("m%d", i)
			seedMission(t, db, id, fmt.Sprintf("Mission %d", i), "complete", nil, nil)
			seedDocumentWithMission(t, db, fmt.Sprintf("/path%d", i), fmt.Sprintf("Brief %d", i), "brief", "metrics data", idPtr(id), nil)
		}

		decoded := briefPrepDecode(t, tag, briefPrepGet(t, router, tag, goalParams("metrics data"), http.StatusOK))
		if len(decoded.Missions) != 5 {
			t.Fatalf("%s: missions capped at 5; count = %d, want 5 (6 seeded)", tag, len(decoded.Missions))
		}
	})

	t.Run("grade_null_and_present", func(t *testing.T) {
		const tag = "grade_null_and_present"
		db, router := newBriefPrepRouter(t)

		seedMission(t, db, "m1", "Mission 1", "complete", nil, nil)
		seedMissionGrade(t, db, "m1", "B")
		seedMission(t, db, "m2", "Mission 2", "complete", nil, nil)
		seedDocumentWithMission(t, db, "/path1", "Brief 1", "brief", "metrics data", &m1, nil)
		seedDocumentWithMission(t, db, "/path2", "Brief 2", "brief", "metrics data", &m2, nil)

		decoded := briefPrepDecode(t, tag, briefPrepGet(t, router, tag, goalParams("metrics data"), http.StatusOK))

		var m1Result, m2Result *briefPrepMission
		for i := range decoded.Missions {
			switch decoded.Missions[i].MissionID {
			case "m1":
				m1Result = &decoded.Missions[i]
			case "m2":
				m2Result = &decoded.Missions[i]
			}
		}
		if m1Result == nil || m2Result == nil {
			t.Fatalf("%s: both missions must be returned; missions = %v", tag, missionIDsOf(decoded))
		}
		if m1Result.Grade == nil || *m1Result.Grade != "B" {
			t.Fatalf("%s: graded mission m1 grade = %v, want B", tag, m1Result.Grade)
		}
		if m2Result.Grade != nil {
			t.Fatalf("%s: ungraded mission m2 grade = %v, want null", tag, *m2Result.Grade)
		}
	})
}

// Helper functions.

var (
	m1 = "m1"
	m2 = "m2"
	m3 = "m3"
	m4 = "m4"
)

func idPtr(s string) *string {
	return &s
}

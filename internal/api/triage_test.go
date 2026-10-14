package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/config"
	"bishop-memory/internal/store"
)

// newTriageTestRouter boots a database exactly the way cmd/memoryd does —
// store.Open, ApplySchema from the real db/schema.sql, EnsureColumns — and
// returns the live router, so these tests exercise the shipped schema rather
// than a hand-copied one.
func newTriageTestRouter(t *testing.T) (*gin.Engine, *sql.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := store.Open(filepath.Join(t.TempDir(), "triage.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.ApplySchema(db, filepath.Join("..", "..", "db", "schema.sql")); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if err := store.EnsureColumns(db); err != nil {
		t.Fatalf("ensure columns: %v", err)
	}
	if err := store.EnsureMissionStepsIndex(db); err != nil {
		t.Fatalf("ensure mission steps index: %v", err)
	}
	return NewRouter(config.Config{AppEnv: "test"}, db), db
}

func do(t *testing.T, router *gin.Engine, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: non-JSON body %q", method, path, rec.Body.String())
		}
	}
	return rec.Code, out
}

func seedCategory(t *testing.T, router *gin.Engine, slug string) {
	t.Helper()
	code, _ := do(t, router, http.MethodPut, "/v1/finding-categories/"+slug, map[string]any{
		"name": slug, "description": "test category " + slug, "sort_order": 1,
	})
	if code != http.StatusOK {
		t.Fatalf("seed category %s: status %d", slug, code)
	}
}

func seedFinding(t *testing.T, router *gin.Engine, suggestion string) int64 {
	t.Helper()
	code, out := do(t, router, http.MethodPost, "/v1/findings", map[string]any{
		"finding_date": "2026-10-02", "target": "test", "suggestion": suggestion, "harness": "kirsch",
	})
	if code != http.StatusCreated {
		t.Fatalf("seed finding: status %d body %v", code, out)
	}
	return int64(out["id"].(float64))
}

func TestHealthz_ReportsTriageTables(t *testing.T) {
	router, _ := newTriageTestRouter(t)
	code, out := do(t, router, http.MethodGet, "/healthz", nil)
	if code != http.StatusOK {
		t.Fatalf("healthz: %d %v", code, out)
	}
}

func TestDecision_TransitionsAndRecommendationClosure(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "brief-writing")
	id := seedFinding(t, router, "name the artefact")

	// Classify and recommend approve.
	code, out := do(t, router, http.MethodPut, "/v1/triage/classifications", map[string]any{
		"classified_by": "test-model",
		"items":         []map[string]any{{"finding_id": id, "category": "brief-writing", "confidence": 0.9, "summary": "s"}},
	})
	if code != http.StatusOK {
		t.Fatalf("classify: %d %v", code, out)
	}
	code, out = do(t, router, http.MethodPost, "/v1/finding-recommendations", map[string]any{
		"items": []map[string]any{{"finding_id": id, "recommendation": "approve", "rationale": "implementable"}},
	})
	if code != http.StatusOK || out["written"].(float64) != 1 {
		t.Fatalf("recommend: %d %v", code, out)
	}

	// Reject without a note is refused.
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/findings/%d/decision", id),
		map[string]any{"status": "rejected", "approver": "Donovan"})
	if code != http.StatusBadRequest {
		t.Fatalf("reject without note: want 400, got %d", code)
	}

	// Approve: status moves, recommendation closes as accepted, audit row written.
	code, out = do(t, router, http.MethodPost, fmt.Sprintf("/v1/findings/%d/decision", id),
		map[string]any{"status": "approved", "approver": "Donovan"})
	if code != http.StatusOK || out["changed"] != true {
		t.Fatalf("approve: %d %v", code, out)
	}
	var status, approver, recState string
	if err := db.QueryRow(`SELECT status, approver FROM findings WHERE id = ?`, id).Scan(&status, &approver); err != nil {
		t.Fatal(err)
	}
	if status != "approved" || approver != "Donovan" {
		t.Fatalf("finding after approve: status=%s approver=%s", status, approver)
	}
	if err := db.QueryRow(`SELECT state FROM finding_recommendations WHERE finding_id = ?`, id).Scan(&recState); err != nil {
		t.Fatal(err)
	}
	if recState != "accepted" {
		t.Fatalf("recommendation state: want accepted, got %s", recState)
	}
	var audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM flight_recorder WHERE event = 'finding.decided'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("audit rows: want 1, got %d", audits)
	}

	// Replaying the same decision is a no-op 200.
	code, out = do(t, router, http.MethodPost, fmt.Sprintf("/v1/findings/%d/decision", id),
		map[string]any{"status": "approved", "approver": "Donovan"})
	if code != http.StatusOK || out["changed"] != false {
		t.Fatalf("replay: %d %v", code, out)
	}

	// approved -> approved is fine; rejected -> applied is not a legal move.
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/findings/%d/decision", id),
		map[string]any{"status": "rejected", "approver": "Donovan", "note": "no"})
	if code != http.StatusOK {
		t.Fatalf("approved->rejected: %d", code)
	}
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/findings/%d/decision", id),
		map[string]any{"status": "applied", "approver": "Donovan"})
	if code != http.StatusBadRequest {
		t.Fatalf("rejected->applied: want 400, got %d", code)
	}

	// A recommendation on a decided finding is skipped, not written.
	code, out = do(t, router, http.MethodPost, "/v1/finding-recommendations", map[string]any{
		"items": []map[string]any{{"finding_id": id, "recommendation": "defer", "rationale": "x"}},
	})
	if code != http.StatusOK || out["written"].(float64) != 0 {
		t.Fatalf("recommend decided finding: %d %v", code, out)
	}
}

func TestClassify_RejectsUnknownSlugAndUpserts(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	id := seedFinding(t, router, "pair a zero-result check with a positive control")

	code, out := do(t, router, http.MethodPut, "/v1/triage/classifications", map[string]any{
		"classified_by": "m", "items": []map[string]any{{"finding_id": id, "category": "nope"}},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("unknown slug: want 400, got %d %v", code, out)
	}

	for i := 0; i < 2; i++ {
		code, out = do(t, router, http.MethodPut, "/v1/triage/classifications", map[string]any{
			"classified_by": "m", "items": []map[string]any{{"finding_id": id, "category": "check-design", "directive_candidate": true}},
		})
		if code != http.StatusOK {
			t.Fatalf("classify #%d: %d %v", i, code, out)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM finding_triage WHERE finding_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("triage rows after two upserts: want 1, got %d", n)
	}

	// The list route surfaces the join and the filters.
	code, out = do(t, router, http.MethodGet, "/v1/findings?category=check-design&directive_candidate=1", nil)
	if code != http.StatusOK || len(out["findings"].([]any)) != 1 {
		t.Fatalf("filtered list: %d %v", code, out)
	}
	code, out = do(t, router, http.MethodGet, "/v1/findings?unclassified=1", nil)
	if code != http.StatusOK || len(out["findings"].([]any)) != 0 {
		t.Fatalf("unclassified list: %d %v", code, out)
	}
	code, out = do(t, router, http.MethodGet, "/v1/finding-categories", nil)
	if code != http.StatusOK {
		t.Fatalf("categories: %d", code)
	}
	cats := out["categories"].([]any)
	if len(cats) != 1 || cats[0].(map[string]any)["proposed_count"].(float64) != 1 {
		t.Fatalf("category counts: %v", cats)
	}
}

func TestDirectiveProposal_AcceptAllocatesIDAndAppliesEvidence(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")
	b := seedFinding(t, router, "finding b")

	code, out := do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title":          "Zero-result checks carry a positive control",
		"applies_when":   "A change that adds a verification whose pass condition is an empty result.",
		"rule":           "The check MUST be paired with a control proven to return a non-empty result under the same invocation.",
		"rationale":      "An empty result cannot distinguish a passing check from one that never ran.",
		"reviewer_check": "Does every empty-result check in the change have a paired positive control?",
		"evidence":       []int64{a, b},
	})
	if code != http.StatusCreated {
		t.Fatalf("propose: %d %v", code, out)
	}
	pid := int64(out["id"].(float64))

	code, out = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "accepted", "decided_by": "Donovan", "title": "Zero-result checks need a control"})
	if code != http.StatusOK || out["directive_id"] != "DIR-001" {
		t.Fatalf("accept: %d %v", code, out)
	}
	if !strings.Contains(out["rendered"].(string), "### DIR-001 — Zero-result checks need a control") {
		t.Fatalf("rendered entry did not carry the edited title: %q", out["rendered"])
	}

	var directives int
	if err := db.QueryRow(`SELECT COUNT(*) FROM directives WHERE directive_id = 'DIR-001'`).Scan(&directives); err != nil {
		t.Fatal(err)
	}
	if directives != 1 {
		t.Fatalf("directives rows: want 1, got %d", directives)
	}
	for _, id := range []int64{a, b} {
		var status string
		if err := db.QueryRow(`SELECT status FROM findings WHERE id = ?`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "applied" {
			t.Fatalf("evidence finding %d: want applied, got %s", id, status)
		}
	}

	// A second accepted proposal takes the next id; the rejected one still needs a note.
	code, out = do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title": "t", "applies_when": "a", "rule": "r", "rationale": "x", "reviewer_check": "c", "evidence": []int64{a},
	})
	if code != http.StatusCreated {
		t.Fatalf("propose 2: %d %v", code, out)
	}
	pid2 := int64(out["id"].(float64))
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid2),
		map[string]any{"state": "declined", "decided_by": "Donovan"})
	if code != http.StatusBadRequest {
		t.Fatalf("decline without note: want 400, got %d", code)
	}
	code, out = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid2),
		map[string]any{"state": "accepted", "decided_by": "Donovan"})
	if code != http.StatusOK || out["directive_id"] != "DIR-002" {
		t.Fatalf("accept 2: %d %v", code, out)
	}

	// Over-budget drafts are refused at creation.
	code, _ = do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title": "t", "applies_when": strings.Repeat("a", 300), "rule": strings.Repeat("r", 800),
		"rationale": strings.Repeat("x", 300), "reviewer_check": strings.Repeat("c", 400), "evidence": []int64{a},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("over-budget proposal: want 400, got %d", code)
	}
}

func TestNextCategory_RotatesByLastProcessed(t *testing.T) {
	router, _ := newTriageTestRouter(t)
	seedCategory(t, router, "alpha")
	seedCategory(t, router, "beta")
	a := seedFinding(t, router, "alpha finding")
	b := seedFinding(t, router, "beta finding")
	code, out := do(t, router, http.MethodPut, "/v1/triage/classifications", map[string]any{
		"classified_by": "m", "items": []map[string]any{
			{"finding_id": a, "category": "alpha"}, {"finding_id": b, "category": "beta"},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("classify: %d %v", code, out)
	}

	code, out = do(t, router, http.MethodGet, "/v1/triage/next-category", nil)
	if code != http.StatusOK || out["category"] != "alpha" {
		t.Fatalf("first pick: %d %v", code, out)
	}

	// A finished process run on alpha moves the rotation to beta.
	code, out = do(t, router, http.MethodPost, "/v1/triage/runs", map[string]any{"kind": "process", "category": "alpha"})
	if code != http.StatusCreated {
		t.Fatalf("run start: %d %v", code, out)
	}
	runID := int64(out["id"].(float64))
	code, out = do(t, router, http.MethodPatch, fmt.Sprintf("/v1/triage/runs/%d", runID),
		map[string]any{"status": "done", "considered": 1, "written": 0})
	if code != http.StatusOK {
		t.Fatalf("run finish: %d %v", code, out)
	}
	code, out = do(t, router, http.MethodGet, "/v1/triage/next-category", nil)
	if code != http.StatusOK || out["category"] != "beta" {
		t.Fatalf("second pick: %d %v", code, out)
	}

	// Once every proposed finding has a pending recommendation there is nothing to pick.
	for _, id := range []int64{a, b} {
		code, out = do(t, router, http.MethodPost, "/v1/finding-recommendations", map[string]any{
			"items": []map[string]any{{"finding_id": id, "recommendation": "defer", "rationale": "later"}},
		})
		if code != http.StatusOK {
			t.Fatalf("recommend %d: %d %v", id, code, out)
		}
	}
	code, _ = do(t, router, http.MethodGet, "/v1/triage/next-category", nil)
	if code != http.StatusNotFound {
		t.Fatalf("nothing waiting: want 404, got %d", code)
	}

	// The pending view groups by category.
	code, out = do(t, router, http.MethodGet, "/v1/triage/pending", nil)
	if code != http.StatusOK || out["pending_findings"].(float64) != 2 {
		t.Fatalf("pending: %d %v", code, out)
	}
}

func TestDirectiveUpsert_RequiresDirID(t *testing.T) {
	router, db := newTriageTestRouter(t)
	code, _ := do(t, router, http.MethodPut, "/v1/directives/nope", map[string]any{"title": "t", "rule": "r"})
	if code != http.StatusBadRequest {
		t.Fatalf("bad id: want 400, got %d", code)
	}
	for i := 0; i < 2; i++ {
		code, _ = do(t, router, http.MethodPut, "/v1/directives/DIR-004", map[string]any{"title": "t", "rule": "r", "ratified_at": "2026-10-02"})
		if code != http.StatusOK {
			t.Fatalf("upsert #%d: %d", i, code)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM directives`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("directives after two upserts: want 1, got %d", n)
	}
	code, _ = do(t, router, http.MethodPut, "/v1/harnesses/kirsch", map[string]any{"memory_root": "relative/path"})
	if code != http.StatusBadRequest {
		t.Fatalf("relative memory_root: want 400, got %d", code)
	}
	code, _ = do(t, router, http.MethodPut, "/v1/harnesses/kirsch", map[string]any{"memory_root": "/tmp/memory"})
	if code != http.StatusOK {
		t.Fatalf("harness upsert: %d", code)
	}
}

func TestDirectiveProposal_ReopenAcceptedKeepsDirectiveID(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")

	// Create and accept a proposal to get a directive_id.
	code, out := do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title":          "Test directive",
		"applies_when":   "Test applies",
		"rule":           "Test rule",
		"rationale":      "Test rationale",
		"reviewer_check": "Test check",
		"evidence":       []int64{a},
	})
	if code != http.StatusCreated {
		t.Fatalf("propose: %d %v", code, out)
	}
	pid := int64(out["id"].(float64))

	code, out = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "accepted", "decided_by": "Operator"})
	if code != http.StatusOK || out["directive_id"] != "DIR-001" {
		t.Fatalf("accept: %d %v", code, out)
	}

	// Reopen the proposal.
	code, out = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "Operator", "note": "Need to reconsider"})
	if code != http.StatusOK || out["state"] != "pending" {
		t.Fatalf("reopen: %d %v", code, out)
	}

	// Verify proposal state is pending and directive_id is kept.
	code, out = do(t, router, http.MethodGet, "/v1/directive-proposals?state=pending", nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d", code)
	}
	proposals := out["directive_proposals"].([]any)
	if len(proposals) != 1 {
		t.Fatalf("pending proposals: want 1, got %d", len(proposals))
	}
	p := proposals[0].(map[string]any)
	if p["id"].(float64) != float64(pid) || p["state"] != "pending" {
		t.Fatalf("proposal state: %v", p)
	}
	if p["directive_id"] != "DIR-001" {
		t.Fatalf("directive_id not kept: got %v", p["directive_id"])
	}

	// Verify flight_recorder has directive.reopened event.
	var reopenedEvent bool
	if err := db.QueryRow(
		`SELECT COUNT(*) > 0 FROM flight_recorder WHERE event = 'directive.reopened' AND note LIKE ?`,
		fmt.Sprintf("Proposal #%d reopened from accepted%%", pid)).Scan(&reopenedEvent); err != nil {
		t.Fatal(err)
	}
	if !reopenedEvent {
		t.Fatal("directive.reopened event not found")
	}

	// Verify directives row is untouched.
	var directivesCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM directives WHERE directive_id = 'DIR-001'`).Scan(&directivesCount); err != nil {
		t.Fatal(err)
	}
	if directivesCount != 1 {
		t.Fatalf("directives count: want 1, got %d", directivesCount)
	}

	// Verify evidence finding is still applied.
	var status string
	if err := db.QueryRow(`SELECT status FROM findings WHERE id = ?`, a).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "applied" {
		t.Fatalf("finding status: want applied, got %s", status)
	}
}

func TestDirectiveProposal_ReopenValidation(t *testing.T) {
	router, _ := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")

	// Create a proposal.
	code, out := do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title":          "Test",
		"applies_when":   "Test",
		"rule":           "Test",
		"rationale":      "Test",
		"reviewer_check": "Test",
		"evidence":       []int64{a},
	})
	if code != http.StatusCreated {
		t.Fatalf("propose: %d", code)
	}
	pid := int64(out["id"].(float64))

	// Cannot reopen a pending proposal.
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "Operator", "note": "Test"})
	if code != http.StatusBadRequest {
		t.Fatalf("reopen pending: want 400, got %d", code)
	}

	// Missing note.
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "Operator", "note": ""})
	if code != http.StatusBadRequest {
		t.Fatalf("reopen missing note: want 400, got %d", code)
	}

	// Missing decided_by.
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "", "note": "Test"})
	if code != http.StatusBadRequest {
		t.Fatalf("reopen missing decided_by: want 400, got %d", code)
	}

	// Unknown proposal id.
	code, _ = do(t, router, http.MethodPost, "/v1/directive-proposals/999/reopen",
		map[string]any{"decided_by": "Operator", "note": "Test"})
	if code != http.StatusNotFound {
		t.Fatalf("reopen unknown: want 404, got %d", code)
	}
}

func TestDirectiveProposal_ReopenAndAmendReusesID(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")

	// Create and accept a proposal.
	code, out := do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title":          "Original title",
		"applies_when":   "Test",
		"rule":           "Test",
		"rationale":      "Test",
		"reviewer_check": "Test",
		"evidence":       []int64{a},
	})
	pid := int64(out["id"].(float64))

	code, out = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "accepted", "decided_by": "Operator"})
	if out["directive_id"] != "DIR-001" {
		t.Fatalf("first accept: directive_id = %v, want DIR-001", out["directive_id"])
	}

	// Reopen and amend the proposal.
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "Operator", "note": "Need to refine"})
	if code != http.StatusOK {
		t.Fatalf("reopen: %d", code)
	}

	// Accept with an edited title.
	code, out = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "accepted", "decided_by": "Operator", "title": "Amended title"})
	if code != http.StatusOK || out["directive_id"] != "DIR-001" {
		t.Fatalf("amend accept: %d %v", code, out)
	}

	// Verify directive.amended event was written.
	var amendedEvent bool
	if err := db.QueryRow(
		`SELECT COUNT(*) > 0 FROM flight_recorder WHERE event = 'directive.amended' AND note LIKE ?`,
		"DIR-001%").Scan(&amendedEvent); err != nil {
		t.Fatal(err)
	}
	if !amendedEvent {
		t.Fatal("directive.amended event not found")
	}

	// Verify directives row was updated.
	var title string
	if err := db.QueryRow(`SELECT title FROM directives WHERE directive_id = 'DIR-001'`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Amended title" {
		t.Fatalf("directive title: want 'Amended title', got %q", title)
	}

	// Create a new proposal and accept it — should get DIR-002.
	code, out = do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title":          "New proposal",
		"applies_when":   "Test",
		"rule":           "Test",
		"rationale":      "Test",
		"reviewer_check": "Test",
		"evidence":       []int64{a},
	})
	pid2 := int64(out["id"].(float64))

	code, out = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid2),
		map[string]any{"state": "accepted", "decided_by": "Operator"})
	if code != http.StatusOK || out["directive_id"] != "DIR-002" {
		t.Fatalf("new accept: %d %v", code, out)
	}
}

func TestDirectiveProposal_ReopenAcceptedThenDeclineRetiresDirID(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")

	// Create and accept a proposal.
	code, out := do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title":          "Test",
		"applies_when":   "Test",
		"rule":           "Test",
		"rationale":      "Test",
		"reviewer_check": "Test",
		"evidence":       []int64{a},
	})
	pid := int64(out["id"].(float64))

	code, out = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "accepted", "decided_by": "Operator"})
	dirID := out["directive_id"].(string)

	// Reopen.
	do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "Operator", "note": "Need to decline"})

	// Decline.
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "declined", "decided_by": "Operator", "note": "Not applicable"})
	if code != http.StatusOK {
		t.Fatalf("decline: %d", code)
	}

	// Verify proposal state is declined and directive_id is kept.
	code, out = do(t, router, http.MethodGet, "/v1/directive-proposals?state=declined", nil)
	if code != http.StatusOK {
		t.Fatalf("list declined: %d", code)
	}
	proposals := out["directive_proposals"].([]any)
	if len(proposals) != 1 {
		t.Fatalf("declined proposals: want 1, got %d", len(proposals))
	}
	p := proposals[0].(map[string]any)
	if p["directive_id"] != dirID {
		t.Fatalf("directive_id not kept: want %s, got %v", dirID, p["directive_id"])
	}

	// Verify directive.retired event was written.
	var retiredEvent bool
	if err := db.QueryRow(
		`SELECT COUNT(*) > 0 FROM flight_recorder WHERE event = 'directive.retired' AND note LIKE ?`,
		fmt.Sprintf("%s%%", dirID)).Scan(&retiredEvent); err != nil {
		t.Fatal(err)
	}
	if !retiredEvent {
		t.Fatal("directive.retired event not found")
	}

	// Verify evidence finding is still applied.
	var status string
	if err := db.QueryRow(`SELECT status FROM findings WHERE id = ?`, a).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "applied" {
		t.Fatalf("finding status: want applied, got %s", status)
	}
}

func TestDirectiveProposal_ReopenDeclinedWithoutDirIDThenAcceptAllocatesNew(t *testing.T) {
	router, _ := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")

	// Create and decline a proposal (no directive_id).
	code, out := do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title":          "Test",
		"applies_when":   "Test",
		"rule":           "Test",
		"rationale":      "Test",
		"reviewer_check": "Test",
		"evidence":       []int64{a},
	})
	pid := int64(out["id"].(float64))

	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "declined", "decided_by": "Operator", "note": "Not applicable"})
	if code != http.StatusOK {
		t.Fatalf("decline: %d", code)
	}

	// Reopen.
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "Operator", "note": "Reconsider"})
	if code != http.StatusOK {
		t.Fatalf("reopen: %d", code)
	}

	// Verify proposal is pending.
	code, out = do(t, router, http.MethodGet, "/v1/directive-proposals?state=pending", nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d", code)
	}
	proposals := out["directive_proposals"].([]any)
	if len(proposals) != 1 {
		t.Fatalf("pending proposals: want 1, got %d", len(proposals))
	}

	// Accept — should allocate DIR-001 (no previous directive_ids exist).
	code, out = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "accepted", "decided_by": "Operator"})
	if code != http.StatusOK || out["directive_id"] != "DIR-001" {
		t.Fatalf("accept: %d %v", code, out)
	}
}

// TestDirectiveProposal_ReopenValidationWhitespace tests that whitespace-only
// decided_by and note are rejected with 400 and leave the proposal unchanged.
func TestDirectiveProposal_ReopenValidationWhitespace(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")

	// Create and accept a proposal.
	code, out := do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title":          "Test",
		"applies_when":   "Test",
		"rule":           "Test",
		"rationale":      "Test",
		"reviewer_check": "Test",
		"evidence":       []int64{a},
	})
	if code != http.StatusCreated {
		t.Fatalf("propose: %d", code)
	}
	pid := int64(out["id"].(float64))

	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "accepted", "decided_by": "Operator"})
	if code != http.StatusOK {
		t.Fatalf("accept: %d", code)
	}

	// Try to reopen with whitespace-only decided_by.
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "   ", "note": "Test"})
	if code != http.StatusBadRequest {
		t.Fatalf("whitespace decided_by: want 400, got %d", code)
	}

	// Verify proposal state unchanged.
	var state string
	if err := db.QueryRow(`SELECT state FROM directive_proposals WHERE id = ?`, pid).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "accepted" {
		t.Fatalf("proposal state after rejected reopen: want accepted, got %s", state)
	}

	// Try to reopen with whitespace-only note.
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "Operator", "note": "   "})
	if code != http.StatusBadRequest {
		t.Fatalf("whitespace note: want 400, got %d", code)
	}

	// Verify proposal state still unchanged.
	if err := db.QueryRow(`SELECT state FROM directive_proposals WHERE id = ?`, pid).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "accepted" {
		t.Fatalf("proposal state after second rejected reopen: want accepted, got %s", state)
	}
}

// TestDirectiveProposal_AmendPreservesRatification tests that amending a
// directive preserves the original ratified_at and creates directive.amended event.
func TestDirectiveProposal_AmendPreservesRatification(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")

	// Create and accept a proposal.
	code, out := do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title":          "Original",
		"applies_when":   "Test",
		"rule":           "Test rule",
		"rationale":      "Test",
		"reviewer_check": "Test",
		"evidence":       []int64{a},
	})
	if code != http.StatusCreated {
		t.Fatalf("propose: %d", code)
	}
	pid := int64(out["id"].(float64))

	code, out = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "accepted", "decided_by": "Operator"})
	if code != http.StatusOK {
		t.Fatalf("accept: %d", code)
	}

	// Seed the directives row's ratified_at to a past date.
	if _, err := db.Exec(`UPDATE directives SET ratified_at = '2020-01-01' WHERE directive_id = 'DIR-001'`); err != nil {
		t.Fatal(err)
	}

	// Get original ratified_at.
	var originalRatified string
	if err := db.QueryRow(`SELECT ratified_at FROM directives WHERE directive_id = 'DIR-001'`).Scan(&originalRatified); err != nil {
		t.Fatal(err)
	}
	if originalRatified != "2020-01-01" {
		t.Fatalf("seed failed: expected '2020-01-01', got %q", originalRatified)
	}

	// Reopen and accept with edit (amendment).
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "Operator", "note": "Refine"})
	if code != http.StatusOK {
		t.Fatalf("reopen: %d", code)
	}

	code, out = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "accepted", "decided_by": "Operator", "rule": "Amended rule"})
	if code != http.StatusOK {
		t.Fatalf("amend: %d", code)
	}

	// Verify directives row count unchanged.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM directives WHERE directive_id = 'DIR-001'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("directives count: want 1, got %d", count)
	}

	// Verify ratified_at unchanged.
	var newRatified string
	if err := db.QueryRow(`SELECT ratified_at FROM directives WHERE directive_id = 'DIR-001'`).Scan(&newRatified); err != nil {
		t.Fatal(err)
	}
	if newRatified != originalRatified {
		t.Fatalf("ratified_at changed: was %q, now %q", originalRatified, newRatified)
	}

	// Verify directive.amended event with amended date.
	var amendNote string
	if err := db.QueryRow(
		`SELECT note FROM flight_recorder WHERE event = 'directive.amended' ORDER BY id DESC LIMIT 1`).Scan(&amendNote); err != nil {
		t.Fatal(err)
	}
	today := time.Now().UTC().Format("2006-01-02")
	if !strings.Contains(amendNote, "amended") {
		t.Fatalf("amendment note missing 'amended': %q", amendNote)
	}
	if !strings.Contains(amendNote, "amended "+today) {
		t.Fatalf("amendment note missing dated amendment: %q, expected to contain 'amended %s'", amendNote, today)
	}

	// Verify decided_by/decided_at repopulated on proposal.
	var decidedBy sql.NullString
	var decidedAt sql.NullString
	if err := db.QueryRow(
		`SELECT decided_by, decided_at FROM directive_proposals WHERE id = ?`, pid).Scan(&decidedBy, &decidedAt); err != nil {
		t.Fatal(err)
	}
	if !decidedBy.Valid || !decidedAt.Valid {
		t.Fatalf("decided fields not repopulated: decided_by=%v, decided_at=%v", decidedBy, decidedAt)
	}
}

// TestDirectiveProposal_AmendWithNullRatifiedAt tests that amending a directive
// with NULL ratified_at succeeds and sets ratified_at to today.
func TestDirectiveProposal_AmendWithNullRatifiedAt(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")

	// Create and accept a proposal.
	code, out := do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title":          "Test",
		"applies_when":   "Test",
		"rule":           "Test",
		"rationale":      "Test",
		"reviewer_check": "Test",
		"evidence":       []int64{a},
	})
	if code != http.StatusCreated {
		t.Fatalf("propose: %d", code)
	}
	pid := int64(out["id"].(float64))

	code, out = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "accepted", "decided_by": "Operator"})
	if code != http.StatusOK {
		t.Fatalf("accept: %d", code)
	}

	// Set ratified_at to NULL.
	if _, err := db.Exec(`UPDATE directives SET ratified_at = NULL WHERE directive_id = 'DIR-001'`); err != nil {
		t.Fatal(err)
	}

	// Verify it's NULL.
	var ratifiedBefore sql.NullString
	if err := db.QueryRow(`SELECT ratified_at FROM directives WHERE directive_id = 'DIR-001'`).Scan(&ratifiedBefore); err != nil {
		t.Fatal(err)
	}
	if ratifiedBefore.Valid {
		t.Fatalf("seed failed: expected NULL, got %q", ratifiedBefore.String)
	}

	// Reopen and amend.
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "Operator", "note": "Refine"})
	if code != http.StatusOK {
		t.Fatalf("reopen: %d", code)
	}

	code, out = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "accepted", "decided_by": "Operator", "rule": "Amended rule"})
	if code != http.StatusOK {
		t.Fatalf("amend: %d", code)
	}

	// Verify ratified_at is now today.
	var ratifiedAfter sql.NullString
	if err := db.QueryRow(`SELECT ratified_at FROM directives WHERE directive_id = 'DIR-001'`).Scan(&ratifiedAfter); err != nil {
		t.Fatal(err)
	}
	if !ratifiedAfter.Valid {
		t.Fatalf("ratified_at should be set to today, got NULL")
	}
	today := time.Now().UTC().Format("2006-01-02")
	if ratifiedAfter.String != today {
		t.Fatalf("ratified_at: want %s, got %s", today, ratifiedAfter.String)
	}
}

// TestDirectiveProposal_RetireVisibility tests that retired directives show
// status=retired in GET /v1/directives.
func TestDirectiveProposal_RetireVisibility(t *testing.T) {
	router, _ := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")

	// Create and accept a proposal.
	code, out := do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title":          "Test",
		"applies_when":   "Test",
		"rule":           "Test",
		"rationale":      "Test",
		"reviewer_check": "Test",
		"evidence":       []int64{a},
	})
	pid := int64(out["id"].(float64))

	code, out = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "accepted", "decided_by": "Operator"})
	if code != http.StatusOK || out["directive_id"] != "DIR-001" {
		t.Fatalf("accept: %d %v", code, out)
	}

	// Reopen and decline to retire.
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "Operator", "note": "Retire"})
	if code != http.StatusOK {
		t.Fatalf("reopen: %d", code)
	}

	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "declined", "decided_by": "Operator", "note": "No longer applicable"})
	if code != http.StatusOK {
		t.Fatalf("decline: %d", code)
	}

	// Get directives and check status.
	code, out = do(t, router, http.MethodGet, "/v1/directives", nil)
	if code != http.StatusOK {
		t.Fatalf("list directives: %d", code)
	}
	directives := out["directives"].([]any)
	if len(directives) != 1 {
		t.Fatalf("directives count: want 1, got %d", len(directives))
	}
	d := directives[0].(map[string]any)
	if d["status"] != "retired" {
		t.Fatalf("status: want retired, got %v", d["status"])
	}
}

// TestDirectiveProposal_DeclineWithoutDirIDWritesNoRetired tests that declining
// a proposal without a directive_id does not write a directive.retired event.
func TestDirectiveProposal_DeclineWithoutDirIDWritesNoRetired(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")

	// Create and decline a proposal (no directive_id).
	code, out := do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title":          "Test",
		"applies_when":   "Test",
		"rule":           "Test",
		"rationale":      "Test",
		"reviewer_check": "Test",
		"evidence":       []int64{a},
	})
	pid := int64(out["id"].(float64))

	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "declined", "decided_by": "Operator", "note": "Not applicable"})
	if code != http.StatusOK {
		t.Fatalf("decline: %d", code)
	}

	// Verify no directive.retired row.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM flight_recorder WHERE event = 'directive.retired'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("directive.retired rows: want 0, got %d", count)
	}
}

// TestDirectiveProposal_ReopenTwice tests that reopening twice returns 400 on
// the second attempt and writes no extra flight row.
func TestDirectiveProposal_ReopenTwice(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")

	// Create and accept a proposal.
	code, out := do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title":          "Test",
		"applies_when":   "Test",
		"rule":           "Test",
		"rationale":      "Test",
		"reviewer_check": "Test",
		"evidence":       []int64{a},
	})
	pid := int64(out["id"].(float64))

	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "accepted", "decided_by": "Operator"})
	if code != http.StatusOK {
		t.Fatalf("accept: %d", code)
	}

	// First reopen.
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "Operator", "note": "First reopen"})
	if code != http.StatusOK {
		t.Fatalf("first reopen: %d", code)
	}

	// Count flight rows after first reopen.
	var countAfterFirst int
	if err := db.QueryRow(`SELECT COUNT(*) FROM flight_recorder WHERE event = 'directive.reopened'`).Scan(&countAfterFirst); err != nil {
		t.Fatal(err)
	}

	// Second reopen (should fail).
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "Operator", "note": "Second reopen"})
	if code != http.StatusBadRequest {
		t.Fatalf("second reopen: want 400, got %d", code)
	}

	// Verify no extra flight row.
	var countAfterSecond int
	if err := db.QueryRow(`SELECT COUNT(*) FROM flight_recorder WHERE event = 'directive.reopened'`).Scan(&countAfterSecond); err != nil {
		t.Fatal(err)
	}
	if countAfterSecond != countAfterFirst {
		t.Fatalf("flight rows: want %d, got %d", countAfterFirst, countAfterSecond)
	}
}

// TestDirectiveProposal_ReopenRetiredThenAccept tests reopening a declined
// (retired) proposal with a directive_id, then accepting it to restore active status.
func TestDirectiveProposal_ReopenRetiredThenAccept(t *testing.T) {
	router, _ := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")

	// Create, accept, then decline to retire.
	code, out := do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title":          "Test",
		"applies_when":   "Test",
		"rule":           "Test",
		"rationale":      "Test",
		"reviewer_check": "Test",
		"evidence":       []int64{a},
	})
	pid := int64(out["id"].(float64))

	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "accepted", "decided_by": "Operator"})
	if code != http.StatusOK {
		t.Fatalf("accept: %d", code)
	}

	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "Operator", "note": "Retire"})
	if code != http.StatusOK {
		t.Fatalf("reopen: %d", code)
	}

	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "declined", "decided_by": "Operator", "note": "Retire"})
	if code != http.StatusOK {
		t.Fatalf("decline: %d", code)
	}

	// Reopen the retired proposal.
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "Operator", "note": "Reconsider"})
	if code != http.StatusOK {
		t.Fatalf("reopen retired: %d", code)
	}

	// Accept to restore.
	code, out = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "accepted", "decided_by": "Operator"})
	if code != http.StatusOK {
		t.Fatalf("accept retired: %d", code)
	}

	// Verify same DIR-001 is used.
	if out["directive_id"] != "DIR-001" {
		t.Fatalf("directive_id: want DIR-001, got %v", out["directive_id"])
	}

	// Verify status is back to active.
	code, out = do(t, router, http.MethodGet, "/v1/directives", nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d", code)
	}
	directives := out["directives"].([]any)
	if len(directives) != 1 {
		t.Fatalf("directives count: want 1, got %d", len(directives))
	}
	d := directives[0].(map[string]any)
	if d["status"] != "active" {
		t.Fatalf("status: want active, got %v", d["status"])
	}
}

// TestDirectiveProposal_ReopenFromExpired tests reopening a proposal in
// expired state (seeded directly since non-test code never writes it).
func TestDirectiveProposal_ReopenFromExpired(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")

	// Create a proposal.
	code, out := do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title":          "Test",
		"applies_when":   "Test",
		"rule":           "Test",
		"rationale":      "Test",
		"reviewer_check": "Test",
		"evidence":       []int64{a},
	})
	pid := int64(out["id"].(float64))

	// Directly seed it to expired state.
	if _, err := db.Exec(`UPDATE directive_proposals SET state = 'expired', decided_by = 'Triage', decided_at = CURRENT_TIMESTAMP, decision_note = 'Expired recommendation' WHERE id = ?`, pid); err != nil {
		t.Fatal(err)
	}

	// Reopen from expired.
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "Operator", "note": "Reconsider"})
	if code != http.StatusOK {
		t.Fatalf("reopen expired: %d", code)
	}

	// Verify state is pending.
	var state string
	if err := db.QueryRow(`SELECT state FROM directive_proposals WHERE id = ?`, pid).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "pending" {
		t.Fatalf("state: want pending, got %s", state)
	}

	// Verify flight_recorder note contains prior decision info.
	var note string
	if err := db.QueryRow(
		`SELECT note FROM flight_recorder WHERE event = 'directive.reopened' AND note LIKE ?`,
		fmt.Sprintf("Proposal #%d%%", pid)).Scan(&note); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(note, "previously decided by") {
		t.Fatalf("note missing 'previously decided by': %q", note)
	}
	if !strings.Contains(note, "Triage") {
		t.Fatalf("note missing decider 'Triage': %q", note)
	}
	if !strings.Contains(note, "Expired recommendation") {
		t.Fatalf("note missing prior decision note: %q", note)
	}
}

// TestDirectiveProposal_ReopenDeclinedWithoutDirIDThenAccept tests that reopening
// a declined proposal without a directive_id captures the prior decision info.
func TestDirectiveProposal_ReopenDeclinedWithoutDirIDThenAccept(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")

	// Create a proposal.
	code, out := do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title":          "Test",
		"applies_when":   "Test",
		"rule":           "Test",
		"rationale":      "Test",
		"reviewer_check": "Test",
		"evidence":       []int64{a},
	})
	if code != http.StatusCreated {
		t.Fatalf("propose: %d", code)
	}
	pid := int64(out["id"].(float64))

	// Decline it directly (no directive_id).
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid),
		map[string]any{"state": "declined", "decided_by": "Reviewer", "note": "Not ready"})
	if code != http.StatusOK {
		t.Fatalf("decline: %d", code)
	}

	// Reopen.
	code, _ = do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": "Operator", "note": "Reconsider"})
	if code != http.StatusOK {
		t.Fatalf("reopen: %d", code)
	}

	// Verify flight_recorder note contains prior decision info (no directive_id in this case).
	var note string
	if err := db.QueryRow(
		`SELECT note FROM flight_recorder WHERE event = 'directive.reopened' AND note LIKE ?`,
		fmt.Sprintf("Proposal #%d%%", pid)).Scan(&note); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(note, "previously decided by") {
		t.Fatalf("note missing 'previously decided by': %q", note)
	}
	if !strings.Contains(note, "Reviewer") {
		t.Fatalf("note missing decider 'Reviewer': %q", note)
	}
	if !strings.Contains(note, "Not ready") {
		t.Fatalf("note missing prior decision note: %q", note)
	}
	// Should NOT contain directive_id since it was declined without one.
	if strings.Contains(note, "directive_id=") {
		t.Fatalf("note should not contain directive_id: %q", note)
	}
}

// proposeDirective files a directive proposal citing the given findings and
// returns its ID.
func proposeDirective(t *testing.T, router *gin.Engine, title string, evidence ...int64) int64 {
	t.Helper()
	code, out := do(t, router, http.MethodPost, "/v1/directive-proposals", map[string]any{
		"title":          title,
		"applies_when":   "Test",
		"rule":           "Test",
		"rationale":      "Test",
		"reviewer_check": "Test",
		"evidence":       evidence,
	})
	if code != http.StatusCreated {
		t.Fatalf("propose: %d", code)
	}
	return int64(out["id"].(float64))
}

// decideProposal records a decision (accepted or declined) on a proposal and
// fails the test unless it succeeds. An empty note is omitted.
func decideProposal(t *testing.T, router *gin.Engine, pid int64, state, by, note string) {
	t.Helper()
	body := map[string]any{"state": state, "decided_by": by}
	if note != "" {
		body["note"] = note
	}
	code, _ := do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/decision", pid), body)
	if code != http.StatusOK {
		t.Fatalf("decide %s on #%d: %d", state, pid, code)
	}
}

// reopenProposal reopens a decided proposal and fails the test unless it
// succeeds.
func reopenProposal(t *testing.T, router *gin.Engine, pid int64, by, note string) {
	t.Helper()
	code, _ := do(t, router, http.MethodPost, fmt.Sprintf("/v1/directive-proposals/%d/reopen", pid),
		map[string]any{"decided_by": by, "note": note})
	if code != http.StatusOK {
		t.Fatalf("reopen #%d: %d", pid, code)
	}
}

// firstDirectiveStatus returns the status of the first directive in
// GET /v1/directives.
func firstDirectiveStatus(t *testing.T, router *gin.Engine) any {
	t.Helper()
	code, out := do(t, router, http.MethodGet, "/v1/directives", nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d", code)
	}
	return out["directives"].([]any)[0].(map[string]any)["status"]
}

// hudDirectivesFor returns the directives array of a mission's HUD detail.
func hudDirectivesFor(t *testing.T, router *gin.Engine, missionID string) []any {
	t.Helper()
	code, detail := do(t, router, http.MethodGet, "/v1/hud/missions/"+missionID, nil)
	if code != http.StatusOK {
		t.Fatalf("HUD detail: %d", code)
	}
	return detail["directives"].([]any)
}

// TestDirectiveProposal_PriorDecisionInFlightNote tests that the W1 note
// content includes prior decider, timestamp, and decision note.
func TestDirectiveProposal_PriorDecisionInFlightNote(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")

	pid := proposeDirective(t, router, "Test", a)
	decideProposal(t, router, pid, "accepted", "Alice", "Important decision note")

	// Read the prior decided_at before the reopen overwrites the row.
	var priorAt string
	if err := db.QueryRow(`SELECT decided_at FROM directive_proposals WHERE id = ?`, pid).Scan(&priorAt); err != nil {
		t.Fatal(err)
	}
	if priorAt == "" {
		t.Fatal("accepted proposal has empty decided_at")
	}

	reopenProposal(t, router, pid, "Bob", "Need to review")

	// Verify flight_recorder note contains prior decision info.
	var note string
	if err := db.QueryRow(
		`SELECT note FROM flight_recorder WHERE event = 'directive.reopened' AND note LIKE ?`,
		fmt.Sprintf("Proposal #%d%%", pid)).Scan(&note); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(note, "previously decided by") {
		t.Fatalf("note missing 'previously decided by': %q", note)
	}
	if !strings.Contains(note, "Alice") {
		t.Fatalf("note missing decider 'Alice': %q", note)
	}
	if !strings.Contains(note, "Important decision note") {
		t.Fatalf("note missing prior decision note: %q", note)
	}

	// The note must carry the actual prior decided_at value, not just " at ".
	if want := "Alice at " + priorAt; !strings.Contains(note, want) {
		t.Fatalf("note missing prior decided_at %q: %q", want, note)
	}
}

// TestDirectiveProposal_StatusDerivedCorrectly tests the three-valued status
// derivation for directives. The subtests run in order over one proposal's
// life; each reports on its own.
func TestDirectiveProposal_StatusDerivedCorrectly(t *testing.T) {
	router, _ := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")
	pid := proposeDirective(t, router, "Test1", a)

	steps := []struct {
		name string
		act  func(t *testing.T)
		want string
	}{
		{"accepted is active", func(t *testing.T) { decideProposal(t, router, pid, "accepted", "Operator", "") }, "active"},
		{"reopened accepted is under-review", func(t *testing.T) { reopenProposal(t, router, pid, "Operator", "Reopen") }, "under-review"},
		{"re-accepted is active", func(t *testing.T) { decideProposal(t, router, pid, "accepted", "Operator", "") }, "active"},
		{"declined with id is retired", func(t *testing.T) {
			reopenProposal(t, router, pid, "Operator", "Reopen again")
			decideProposal(t, router, pid, "declined", "Operator", "No longer needed")
		}, "retired"},
		{"reopened retired is under-review", func(t *testing.T) { reopenProposal(t, router, pid, "Operator", "Reconsider") }, "under-review"},
		{"reopened retired then accepted is active", func(t *testing.T) { decideProposal(t, router, pid, "accepted", "Operator", "") }, "active"},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			s.act(t)
			if got := firstDirectiveStatus(t, router); got != s.want {
				t.Fatalf("status: want %s, got %v", s.want, got)
			}
		})
	}
}

// TestDirectiveProposal_HUDShowsDirectiveWithStatus tests that the HUD includes
// directives with their computed status, and that reopened directives remain visible.
func TestDirectiveProposal_HUDShowsDirectiveWithStatus(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "check-design")
	a := seedFinding(t, router, "finding a")

	// Seed a mission with the finding.
	const mission = "hud-test-mission"
	if _, err := db.Exec(
		`INSERT INTO missions (id, title, status, harness) VALUES (?, ?, ?, ?)`,
		mission, "Test Mission", "in-progress", "test-harness"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE findings SET mission_id = ? WHERE id = ?`, mission, a); err != nil {
		t.Fatal(err)
	}
	pid := proposeDirective(t, router, "TestHUD", a)

	// wantOne asserts the HUD lists exactly one directive with the status.
	wantOne := func(t *testing.T, status string) {
		t.Helper()
		got := hudDirectivesFor(t, router, mission)
		if len(got) != 1 {
			t.Fatalf("HUD directives count: want 1, got %d", len(got))
		}
		if s := got[0].(map[string]any)["status"]; s != status {
			t.Fatalf("HUD status: want %s, got %v", status, s)
		}
	}
	// wantNone asserts a retired directive is absent from the HUD.
	wantNone := func(t *testing.T) {
		t.Helper()
		if got := hudDirectivesFor(t, router, mission); len(got) != 0 {
			t.Fatalf("HUD directives count: want 0 (retired is not shown), got %d", len(got))
		}
	}

	t.Run("active directive shows", func(t *testing.T) {
		decideProposal(t, router, pid, "accepted", "Operator", "")
		wantOne(t, "active")
	})
	t.Run("reopened directive shows under-review", func(t *testing.T) {
		reopenProposal(t, router, pid, "Operator", "Reopen")
		wantOne(t, "under-review")
	})
	t.Run("re-accepted directive is active", func(t *testing.T) {
		decideProposal(t, router, pid, "accepted", "Operator", "")
		wantOne(t, "active")
	})
	t.Run("declined directive is gone", func(t *testing.T) {
		reopenProposal(t, router, pid, "Operator", "Reopen again")
		decideProposal(t, router, pid, "declined", "Operator", "No longer needed")
		wantNone(t)
	})
	t.Run("reopened retired directive shows under-review", func(t *testing.T) {
		reopenProposal(t, router, pid, "Operator", "Reconsider")
		wantOne(t, "under-review")
	})
	t.Run("re-declined directive is gone again", func(t *testing.T) {
		decideProposal(t, router, pid, "declined", "Operator", "Still no")
		wantNone(t)
	})
}

// TestOperatorCloseTriageRun_StaledRun tests closing a stale triage run.
func TestOperatorCloseTriageRun_StaledRun(t *testing.T) {
	router, db := newTriageTestRouter(t)

	// Seed a triage run that started 2 hours ago (well past the 30-minute threshold).
	now := time.Now().UTC()
	twoHoursAgo := now.Add(-2 * time.Hour).Format("2006-01-02 15:04:05")
	seedCategory(t, router, "brief-writing")
	_, err := db.Exec(`
		INSERT INTO triage_runs (id, kind, category, status, started_at)
		VALUES (1, 'process', 'brief-writing', 'running', ?)
	`, twoHoursAgo)
	if err != nil {
		t.Fatal(err)
	}

	// Close it
	code, out := do(t, router, http.MethodPost, "/v1/triage/runs/1/operator-close", map[string]any{
		"status":     "done",
		"decided_by": "Operator",
		"note":       "Stuck process run, marked done.",
	})
	if code != http.StatusOK || out["closed"] != true {
		t.Fatalf("operator-close: want 200, got %d body %v", code, out)
	}

	// Verify status and finished_at were set
	var status string
	var finishedAt sql.NullString
	if err := db.QueryRow(`SELECT status, finished_at FROM triage_runs WHERE id = 1`).Scan(&status, &finishedAt); err != nil {
		t.Fatal(err)
	}
	if status != "done" || !finishedAt.Valid {
		t.Fatalf("triage_run after close: status=%s finished_at=%v", status, finishedAt)
	}

	// Verify notes were appended
	var notes sql.NullString
	if err := db.QueryRow(`SELECT notes FROM triage_runs WHERE id = 1`).Scan(&notes); err != nil {
		t.Fatal(err)
	}
	if !notes.Valid || !strings.Contains(notes.String, "Stuck process run") {
		t.Fatalf("triage_run notes: %v", notes)
	}

	// Verify flight_recorder event was written
	var eventCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM flight_recorder WHERE event = 'triage_run.operator_closed' AND agent = 'operator:Operator'`).
		Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("flight_recorder rows: want 1, got %d", eventCount)
	}

	// Verify last_processed_at is NOT stamped (process run should be retried).
	// The category is seeded above, so a stamping handler fails here.
	var lastProcessedAt sql.NullString
	if err := db.QueryRow(`SELECT last_processed_at FROM finding_categories WHERE slug = 'brief-writing'`).Scan(&lastProcessedAt); err != nil {
		t.Fatal(err)
	}
	if lastProcessedAt.Valid {
		t.Fatalf("last_processed_at must stay NULL after an operator close, got %q", lastProcessedAt.String)
	}
}

// TestOperatorCloseTriageRun_RecentActivity tests that a run with recent child table activity is not stale.
func TestOperatorCloseTriageRun_RecentActivity(t *testing.T) {
	router, db := newTriageTestRouter(t)

	// Seed a triage run that started 2 hours ago but had a recommendation written 5 minutes ago.
	now := time.Now().UTC()
	twoHoursAgo := now.Add(-2 * time.Hour).Format("2006-01-02 15:04:05")
	fiveMinutesAgo := now.Add(-5 * time.Minute).Format("2006-01-02 15:04:05")

	_, err := db.Exec(`
		INSERT INTO triage_runs (id, kind, category, status, started_at)
		VALUES (1, 'classify', NULL, 'running', ?)
	`, twoHoursAgo)
	if err != nil {
		t.Fatal(err)
	}

	// Seed a finding and a recent recommendation
	id := seedFinding(t, router, "test finding")
	seedCategory(t, router, "test-cat")
	_, err = db.Exec(`
		INSERT INTO finding_triage (finding_id, category, classified_by, run_id)
		VALUES (?, 'test-cat', 'test', 1)
	`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		INSERT INTO finding_recommendations (finding_id, recommendation, rationale, run_id, created_at)
		VALUES (?, 'approve', 'test', 1, ?)
	`, id, fiveMinutesAgo)
	if err != nil {
		t.Fatal(err)
	}

	// Attempt to close it should fail: not stale
	code, out := do(t, router, http.MethodPost, "/v1/triage/runs/1/operator-close", map[string]any{
		"status":     "done",
		"decided_by": "Operator",
		"note":       "Test",
	})
	if code != http.StatusConflict {
		t.Fatalf("operator-close recent activity: want 409, got %d body %v", code, out)
	}
	if !strings.Contains(fmt.Sprintf("%v", out), "not stale") {
		t.Fatalf("error should mention staleness: %v", out)
	}
}

// TestOperatorCloseTriageRun_AlreadyFinished tests that a finished run cannot be closed.
func TestOperatorCloseTriageRun_AlreadyFinished(t *testing.T) {
	router, db := newTriageTestRouter(t)

	// Seed a finished triage run
	now := time.Now().UTC()
	startedAt := now.Add(-1 * time.Hour).Format("2006-01-02 15:04:05")
	finishedAt := now.Add(-50 * time.Minute).Format("2006-01-02 15:04:05")
	_, err := db.Exec(`
		INSERT INTO triage_runs (id, kind, status, started_at, finished_at)
		VALUES (1, 'process', 'done', ?, ?)
	`, startedAt, finishedAt)
	if err != nil {
		t.Fatal(err)
	}

	// Attempt to close it should fail
	code, out := do(t, router, http.MethodPost, "/v1/triage/runs/1/operator-close", map[string]any{
		"status":     "done",
		"decided_by": "Operator",
		"note":       "Test",
	})
	if code != http.StatusConflict {
		t.Fatalf("operator-close finished run: want 409, got %d body %v", code, out)
	}
	if !strings.Contains(fmt.Sprintf("%v", out), "already done") {
		t.Fatalf("error should mention current status: %v", out)
	}
}

// TestOperatorCloseTriageRun_Unknown tests that an unknown run returns 404.
func TestOperatorCloseTriageRun_Unknown(t *testing.T) {
	router, _ := newTriageTestRouter(t)

	code, out := do(t, router, http.MethodPost, "/v1/triage/runs/999/operator-close", map[string]any{
		"status":     "done",
		"decided_by": "Operator",
		"note":       "Test",
	})
	if code != http.StatusNotFound {
		t.Fatalf("operator-close unknown run: want 404, got %d body %v", code, out)
	}
}

// TestOperatorCloseTriageRun_BlankFields tests validation of required fields.
func TestOperatorCloseTriageRun_BlankFields(t *testing.T) {
	router, db := newTriageTestRouter(t)

	// Seed a stale run
	now := time.Now().UTC()
	twoHoursAgo := now.Add(-2 * time.Hour).Format("2006-01-02 15:04:05")
	_, err := db.Exec(`
		INSERT INTO triage_runs (id, kind, status, started_at)
		VALUES (1, 'process', 'running', ?)
	`, twoHoursAgo)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("blank decided_by", func(t *testing.T) {
		code, _ := do(t, router, http.MethodPost, "/v1/triage/runs/1/operator-close", map[string]any{
			"status":     "done",
			"decided_by": "   ",
			"note":       "Test",
		})
		if code != http.StatusBadRequest {
			t.Fatalf("operator-close blank decided_by: want 400, got %d", code)
		}
	})

	t.Run("blank note", func(t *testing.T) {
		code, _ := do(t, router, http.MethodPost, "/v1/triage/runs/1/operator-close", map[string]any{
			"status":     "done",
			"decided_by": "Operator",
			"note":       "   ",
		})
		if code != http.StatusBadRequest {
			t.Fatalf("operator-close blank note: want 400, got %d", code)
		}
	})

	t.Run("bad status", func(t *testing.T) {
		code, _ := do(t, router, http.MethodPost, "/v1/triage/runs/1/operator-close", map[string]any{
			"status":     "stuck",
			"decided_by": "Operator",
			"note":       "Test",
		})
		if code != http.StatusBadRequest {
			t.Fatalf("operator-close bad status: want 400, got %d", code)
		}
	})
}

// TestListTriageRuns_IncludesLastActivityAndStale tests that GET /v1/triage/runs includes
// last_activity_at and stale fields, and that stale is computed correctly.
func TestListTriageRuns_IncludesLastActivityAndStale(t *testing.T) {
	router, db := newTriageTestRouter(t)

	// Seed a stale run (started 2 hours ago)
	now := time.Now().UTC()
	twoHoursAgo := now.Add(-2 * time.Hour).Format("2006-01-02 15:04:05")
	_, err := db.Exec(`
		INSERT INTO triage_runs (id, kind, status, started_at)
		VALUES (1, 'process', 'running', ?)
	`, twoHoursAgo)
	if err != nil {
		t.Fatal(err)
	}

	// Seed a recent run (started 5 minutes ago)
	fiveMinutesAgo := now.Add(-5 * time.Minute).Format("2006-01-02 15:04:05")
	_, err = db.Exec(`
		INSERT INTO triage_runs (id, kind, status, started_at)
		VALUES (2, 'classify', 'running', ?)
	`, fiveMinutesAgo)
	if err != nil {
		t.Fatal(err)
	}

	// Seed a finished run
	oneHourAgo := now.Add(-1 * time.Hour).Format("2006-01-02 15:04:05")
	fiftyMinutesAgo := now.Add(-50 * time.Minute).Format("2006-01-02 15:04:05")
	_, err = db.Exec(`
		INSERT INTO triage_runs (id, kind, status, started_at, finished_at)
		VALUES (3, 'grade', 'done', ?, ?)
	`, oneHourAgo, fiftyMinutesAgo)
	if err != nil {
		t.Fatal(err)
	}

	// List runs
	code, out := do(t, router, http.MethodGet, "/v1/triage/runs", nil)
	if code != http.StatusOK {
		t.Fatalf("list runs: want 200, got %d", code)
	}

	runs := out["runs"].([]any)
	if len(runs) != 3 {
		t.Fatalf("list runs count: want 3, got %d", len(runs))
	}

	// Check the stale run (id=1)
	staleRun := runs[2].(map[string]any) // Runs are returned desc by id, so id=1 is last
	if staleRun["id"].(float64) != 1 {
		t.Fatalf("unexpected run order")
	}
	if _, ok := staleRun["last_activity_at"]; !ok {
		t.Fatalf("last_activity_at field missing from stale run")
	}
	if stale, ok := staleRun["stale"].(bool); !ok || !stale {
		t.Fatalf("stale run should have stale=true, got %v", staleRun["stale"])
	}

	// Check the recent run (id=2)
	recentRun := runs[1].(map[string]any)
	if recentRun["id"].(float64) != 2 {
		t.Fatalf("unexpected run order")
	}
	if stale, ok := recentRun["stale"].(bool); !ok || stale {
		t.Fatalf("recent run should have stale=false, got %v", recentRun["stale"])
	}

	// Check the finished run (id=3)
	finishedRun := runs[0].(map[string]any)
	if finishedRun["id"].(float64) != 3 {
		t.Fatalf("unexpected run order")
	}
	if stale, ok := finishedRun["stale"].(bool); !ok || stale {
		t.Fatalf("finished run should have stale=false (only running runs are stale), got %v", finishedRun["stale"])
	}
}

// TestListTriageRuns_LastActivityFromChildTables tests that last_activity_at includes
// the most recent timestamp from child tables.
func TestListTriageRuns_LastActivityFromChildTables(t *testing.T) {
	router, db := newTriageTestRouter(t)

	// Seed a run started 2 hours ago
	now := time.Now().UTC()
	twoHoursAgo := now.Add(-2 * time.Hour).Format("2006-01-02 15:04:05")
	_, err := db.Exec(`
		INSERT INTO triage_runs (id, kind, status, started_at)
		VALUES (1, 'classify', 'running', ?)
	`, twoHoursAgo)
	if err != nil {
		t.Fatal(err)
	}

	// Add a finding_recommendations row with a recent timestamp (5 minutes ago)
	fiveMinutesAgo := now.Add(-5 * time.Minute).Format("2006-01-02 15:04:05")
	seedFinding(t, router, "test")
	_, err = db.Exec(`
		INSERT INTO finding_recommendations (finding_id, recommendation, rationale, run_id, created_at)
		VALUES (1, 'approve', 'test', 1, ?)
	`, fiveMinutesAgo)
	if err != nil {
		t.Fatal(err)
	}

	// List runs
	code, out := do(t, router, http.MethodGet, "/v1/triage/runs", nil)
	if code != http.StatusOK {
		t.Fatalf("list runs: want 200, got %d", code)
	}

	runs := out["runs"].([]any)
	run := runs[0].(map[string]any)
	if run["id"].(float64) != 1 {
		t.Fatalf("unexpected run id")
	}

	// last_activity_at is exactly the recommendation's timestamp, not started_at.
	if got := run["last_activity_at"].(string); got != fiveMinutesAgo {
		t.Fatalf("last_activity_at = %q, want %q", got, fiveMinutesAgo)
	}

	// The run should NOT be stale since the child table has recent activity
	if stale := run["stale"].(bool); stale {
		t.Fatalf("run with recent child table activity should not be stale")
	}
}

// sqliteTS is the format SQLite's CURRENT_TIMESTAMP writes; tests seed every
// run-activity timestamp in it.
const sqliteTS = "2006-01-02 15:04:05"

// tsAgo returns the SQLite-format UTC timestamp d before now.
func tsAgo(d time.Duration) string {
	return time.Now().UTC().Add(-d).Format(sqliteTS)
}

// seedRunningRun inserts a 'running' run with the given id, kind, category
// ("" for NULL), notes ("" for NULL) and age of started_at.
func seedRunningRun(t *testing.T, db *sql.DB, id int64, kind, category, notes string, startedAgo time.Duration) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO triage_runs (id, kind, category, status, notes, started_at)
		VALUES (?, ?, ?, 'running', ?, ?)`, id, kind, nullIfEmpty(category), nullIfEmpty(notes), tsAgo(startedAgo)); err != nil {
		t.Fatalf("seed run %d: %v", id, err)
	}
}

// childActivitySources lists every table timestamp that counts as run activity.
// Each seeder writes one row linked to run 1 whose only fresh timestamp is ts;
// every other timestamp on the row is old (or NULL), so the row can only make
// the run look active through the column under test.
var childActivitySources = []struct {
	name string
	seed func(t *testing.T, router *gin.Engine, db *sql.DB, ts string)
}{
	{"finding_triage.classified_at", func(t *testing.T, router *gin.Engine, db *sql.DB, ts string) {
		id := seedFinding(t, router, "f")
		seedCategory(t, router, "cat")
		mustExec(t, db, `INSERT INTO finding_triage (finding_id, category, classified_by, run_id, classified_at)
			VALUES (?, 'cat', 'test', 1, ?)`, id, ts)
	}},
	{"finding_groups.created_at", func(t *testing.T, router *gin.Engine, db *sql.DB, ts string) {
		seedCategory(t, router, "cat")
		mustExec(t, db, `INSERT INTO finding_groups (category, title, summary, run_id, created_at)
			VALUES ('cat', 'g', 's', 1, ?)`, ts)
	}},
	{"finding_recommendations.created_at", func(t *testing.T, router *gin.Engine, db *sql.DB, ts string) {
		id := seedFinding(t, router, "f")
		mustExec(t, db, `INSERT INTO finding_recommendations (finding_id, recommendation, rationale, run_id, created_at)
			VALUES (?, 'approve', 'r', 1, ?)`, id, ts)
	}},
	{"directive_proposals.created_at", func(t *testing.T, router *gin.Engine, db *sql.DB, ts string) {
		mustExec(t, db, `INSERT INTO directive_proposals (title, applies_when, rule, rationale, reviewer_check, evidence, run_id, created_at)
			VALUES ('t', 'a', 'r', 'r', 'c', '[]', 1, ?)`, ts)
	}},
	{"mission_grades.claimed_at", func(t *testing.T, router *gin.Engine, db *sql.DB, ts string) {
		mustExec(t, db, `INSERT INTO missions (id, title) VALUES ('m-1', 'm')`)
		mustExec(t, db, `INSERT INTO mission_grades (mission_id, run_id, claimed_at, created_at)
			VALUES ('m-1', 1, ?, ?)`, ts, tsAgo(3*time.Hour))
	}},
	{"mission_grades.graded_at", func(t *testing.T, router *gin.Engine, db *sql.DB, ts string) {
		mustExec(t, db, `INSERT INTO missions (id, title) VALUES ('m-1', 'm')`)
		mustExec(t, db, `INSERT INTO mission_grades (mission_id, run_id, graded_at, created_at)
			VALUES ('m-1', 1, ?, ?)`, ts, tsAgo(3*time.Hour))
	}},
	{"mission_grades.created_at", func(t *testing.T, router *gin.Engine, db *sql.DB, ts string) {
		mustExec(t, db, `INSERT INTO missions (id, title) VALUES ('m-1', 'm')`)
		mustExec(t, db, `INSERT INTO mission_grades (mission_id, run_id, created_at)
			VALUES ('m-1', 1, ?)`, ts)
	}},
}

// mustExec runs a seeding statement or fails the test.
func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("seed: %v\n%s", err, q)
	}
}

// listedRun returns run id from GET /v1/triage/runs.
func listedRun(t *testing.T, router *gin.Engine, id float64) map[string]any {
	t.Helper()
	code, out := do(t, router, http.MethodGet, "/v1/triage/runs", nil)
	if code != http.StatusOK {
		t.Fatalf("list runs: want 200, got %d", code)
	}
	for _, r := range out["runs"].([]any) {
		if run := r.(map[string]any); run["id"].(float64) == id {
			return run
		}
	}
	t.Fatalf("run %v not listed", id)
	return nil
}

func operatorClose(t *testing.T, router *gin.Engine, id int, status, note string) (int, map[string]any) {
	t.Helper()
	return do(t, router, http.MethodPost, fmt.Sprintf("/v1/triage/runs/%d/operator-close", id), map[string]any{
		"status": status, "decided_by": "Operator", "note": note,
	})
}

// TestTriageRunActivity_EveryChildTimestampSource proves each run-activity
// column is part of last activity, in both routes. A run started 2h ago whose
// newest timestamp is 5 minutes old is not stale and cannot be closed; with a
// 45-minute-old newest timestamp it is stale and closes. last_activity_at is
// the seeded value exactly.
func TestTriageRunActivity_EveryChildTimestampSource(t *testing.T) {
	for _, src := range childActivitySources {
		for _, tc := range []struct {
			name      string
			age       time.Duration
			wantStale bool
		}{
			{"newest is 5m old", 5 * time.Minute, false},
			{"newest is 45m old", 45 * time.Minute, true},
		} {
			t.Run(src.name+"/"+tc.name, func(t *testing.T) {
				router, db := newTriageTestRouter(t)
				seedRunningRun(t, db, 1, "classify", "", "", 2*time.Hour)
				ts := tsAgo(tc.age)
				src.seed(t, router, db, ts)

				run := listedRun(t, router, 1)
				if got := run["last_activity_at"].(string); got != ts {
					t.Fatalf("last_activity_at = %q, want %q", got, ts)
				}
				if got := run["stale"].(bool); got != tc.wantStale {
					t.Fatalf("list stale = %v, want %v", got, tc.wantStale)
				}

				code, out := operatorClose(t, router, 1, "done", "x")
				want := http.StatusConflict
				if tc.wantStale {
					want = http.StatusOK
				}
				if code != want {
					t.Fatalf("operator-close: want %d, got %d body %v", want, code, out)
				}
			})
		}
	}
}

// TestTriageRunActivity_ThirtyMinuteBoundary: 29 minutes idle is not stale and
// cannot be closed; 31 minutes idle is stale and can. Both routes agree.
func TestTriageRunActivity_ThirtyMinuteBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		idle      time.Duration
		wantStale bool
		wantCode  int
	}{
		{"29m", 29 * time.Minute, false, http.StatusConflict},
		{"31m", 31 * time.Minute, true, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, db := newTriageTestRouter(t)
			seedRunningRun(t, db, 1, "classify", "", "", tc.idle)
			if got := listedRun(t, router, 1)["stale"].(bool); got != tc.wantStale {
				t.Fatalf("list stale = %v, want %v", got, tc.wantStale)
			}
			if code, out := operatorClose(t, router, 1, "done", "x"); code != tc.wantCode {
				t.Fatalf("operator-close: want %d, got %d body %v", tc.wantCode, code, out)
			}
		})
	}
}

// TestOperatorCloseTriageRun_FailedProcessRun: closing as failed records the
// status, leaves the rotation untouched, and omits an absent category from the
// flight note.
func TestOperatorCloseTriageRun_FailedProcessRun(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "brief-writing")
	seedRunningRun(t, db, 1, "process", "brief-writing", "", 2*time.Hour)
	seedRunningRun(t, db, 2, "classify", "", "", 2*time.Hour)

	if code, out := operatorClose(t, router, 1, "failed", "gave up"); code != http.StatusOK || out["status"] != "failed" {
		t.Fatalf("close failed: %d %v", code, out)
	}
	var status string
	var finished, processed sql.NullString
	if err := db.QueryRow(`SELECT status, finished_at FROM triage_runs WHERE id = 1`).Scan(&status, &finished); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || !finished.Valid {
		t.Fatalf("run after failed close: status=%s finished_at=%v", status, finished)
	}
	if err := db.QueryRow(`SELECT last_processed_at FROM finding_categories WHERE slug = 'brief-writing'`).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if processed.Valid {
		t.Fatalf("last_processed_at must stay NULL, got %q", processed.String)
	}

	if code, _ := operatorClose(t, router, 2, "done", "no category"); code != http.StatusOK {
		t.Fatalf("close classify run: %d", code)
	}
	var note string
	if err := db.QueryRow(`SELECT note FROM flight_recorder WHERE event = 'triage_run.operator_closed' ORDER BY id DESC LIMIT 1`).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "(classify)") || strings.Contains(note, ", )") {
		t.Fatalf("flight note should omit the empty category, got %q", note)
	}
}

// TestOperatorCloseTriageRun_NotesAppended: existing notes are kept and the
// operator line follows them; a NULL notes column simply becomes the operator
// line.
func TestOperatorCloseTriageRun_NotesAppended(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedRunningRun(t, db, 1, "classify", "", "agent progress note", 2*time.Hour)
	seedRunningRun(t, db, 2, "classify", "", "", 2*time.Hour)

	for _, id := range []int{1, 2} {
		if code, out := operatorClose(t, router, id, "done", "closing it"); code != http.StatusOK {
			t.Fatalf("close %d: %d %v", id, code, out)
		}
	}
	var withNotes, withoutNotes string
	if err := db.QueryRow(`SELECT notes FROM triage_runs WHERE id = 1`).Scan(&withNotes); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT notes FROM triage_runs WHERE id = 2`).Scan(&withoutNotes); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(withNotes, "agent progress note\n[operator-closed done by Operator, idle since ") ||
		!strings.HasSuffix(withNotes, "] closing it") {
		t.Fatalf("notes with prior text = %q", withNotes)
	}
	if !strings.HasPrefix(withoutNotes, "[operator-closed done by Operator, idle since ") {
		t.Fatalf("notes without prior text = %q", withoutNotes)
	}
}

// TestListTriageRuns_CorruptTimestampDegradesOneRow: a running run with an
// unparseable started_at (so unparseable last activity) must not fail the list.
// Both runs are served, the valid old one is stale, the corrupt one reports
// stale=false with its raw last_activity_at, and operator-close on the corrupt
// run still refuses with 500.
func TestListTriageRuns_CorruptTimestampDegradesOneRow(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedRunningRun(t, db, 1, "classify", "", "", 2*time.Hour)
	mustExec(t, db, `INSERT INTO triage_runs (id, kind, status, started_at) VALUES (2, 'classify', 'running', 'not-a-time')`)

	good := listedRun(t, router, 1)
	if good["stale"] != true {
		t.Fatalf("valid old run: stale = %v, want true", good["stale"])
	}
	bad := listedRun(t, router, 2)
	if bad["stale"] != false {
		t.Fatalf("corrupt run: stale = %v, want false", bad["stale"])
	}
	if got := bad["last_activity_at"]; got != "not-a-time" {
		t.Fatalf("corrupt run last_activity_at = %v, want raw %q", got, "not-a-time")
	}

	if code, out := operatorClose(t, router, 2, "done", "x"); code != http.StatusInternalServerError {
		t.Fatalf("operator-close on corrupt run: want 500, got %d %v", code, out)
	}
}

// TestFinishTriageRun_RefusedAfterOperatorClose: a late agent PATCH on a run the
// operator already closed gets 409 and changes nothing: not status, notes,
// finished_at, last_processed_at, nor the flight recorder.
func TestFinishTriageRun_RefusedAfterOperatorClose(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "brief-writing")
	seedRunningRun(t, db, 1, "process", "brief-writing", "", 2*time.Hour)
	if code, out := operatorClose(t, router, 1, "failed", "stuck"); code != http.StatusOK {
		t.Fatalf("close: %d %v", code, out)
	}
	// Pin finished_at so an overwrite is detectable within the same second.
	mustExec(t, db, `UPDATE triage_runs SET finished_at = '2020-01-01 00:00:00' WHERE id = 1`)

	var wantNotes string
	if err := db.QueryRow(`SELECT notes FROM triage_runs WHERE id = 1`).Scan(&wantNotes); err != nil {
		t.Fatal(err)
	}

	code, out := do(t, router, http.MethodPatch, "/v1/triage/runs/1", map[string]any{
		"status": "done", "notes": "late agent report", "considered": 9, "written": 9,
	})
	if code != http.StatusConflict || out["error"] != "run is already failed" {
		t.Fatalf("late PATCH: want 409 'run is already failed', got %d %v", code, out)
	}

	var status, notes, finished string
	var considered, written int
	if err := db.QueryRow(`SELECT status, notes, finished_at, considered, written FROM triage_runs WHERE id = 1`).
		Scan(&status, &notes, &finished, &considered, &written); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || notes != wantNotes || finished != "2020-01-01 00:00:00" || considered != 0 || written != 0 {
		t.Fatalf("run changed by refused PATCH: status=%s notes=%q finished_at=%s considered=%d written=%d",
			status, notes, finished, considered, written)
	}
	var processed sql.NullString
	if err := db.QueryRow(`SELECT last_processed_at FROM finding_categories WHERE slug = 'brief-writing'`).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if processed.Valid {
		t.Fatalf("refused PATCH stamped last_processed_at: %q", processed.String)
	}
	var flights int
	if err := db.QueryRow(`SELECT COUNT(*) FROM flight_recorder WHERE event = 'triage.process'`).Scan(&flights); err != nil {
		t.Fatal(err)
	}
	if flights != 0 {
		t.Fatalf("refused PATCH wrote %d triage.process flight rows", flights)
	}
}

// TestFinishTriageRun_RunningRunUnchanged: the normal agent finish still works
// (200, status set, rotation stamped for process/done, one flight row), and a
// repeat finish is then refused.
func TestFinishTriageRun_RunningRunUnchanged(t *testing.T) {
	router, db := newTriageTestRouter(t)
	seedCategory(t, router, "brief-writing")
	code, out := do(t, router, http.MethodPost, "/v1/triage/runs", map[string]any{"kind": "process", "category": "brief-writing"})
	if code != http.StatusCreated {
		t.Fatalf("start run: %d %v", code, out)
	}
	id := int64(out["id"].(float64))
	path := fmt.Sprintf("/v1/triage/runs/%d", id)

	code, out = do(t, router, http.MethodPatch, path, map[string]any{"status": "done", "considered": 3, "written": 2, "notes": "ok"})
	if code != http.StatusOK || out["updated"] != true || out["status"] != "done" {
		t.Fatalf("PATCH running run: %d %v", code, out)
	}
	var status, notes string
	var considered, written int
	var processed sql.NullString
	if err := db.QueryRow(`SELECT status, notes, considered, written FROM triage_runs WHERE id = ?`, id).Scan(&status, &notes, &considered, &written); err != nil {
		t.Fatal(err)
	}
	if status != "done" || notes != "ok" || considered != 3 || written != 2 {
		t.Fatalf("run after PATCH: %s %q %d %d", status, notes, considered, written)
	}
	if err := db.QueryRow(`SELECT last_processed_at FROM finding_categories WHERE slug = 'brief-writing'`).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if !processed.Valid {
		t.Fatalf("process/done PATCH must stamp last_processed_at")
	}
	var flights int
	if err := db.QueryRow(`SELECT COUNT(*) FROM flight_recorder WHERE event = 'triage.process'`).Scan(&flights); err != nil || flights != 1 {
		t.Fatalf("flight rows = %d (err %v), want 1", flights, err)
	}

	if code, out = do(t, router, http.MethodPatch, path, map[string]any{"status": "failed"}); code != http.StatusConflict {
		t.Fatalf("second PATCH: want 409, got %d %v", code, out)
	}
	if code, _ = do(t, router, http.MethodPatch, "/v1/triage/runs/9999", map[string]any{"status": "done"}); code != http.StatusNotFound {
		t.Fatalf("PATCH unknown run: want 404, got %d", code)
	}
}

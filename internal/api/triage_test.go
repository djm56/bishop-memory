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

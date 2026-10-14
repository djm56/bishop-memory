package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// writeMissionFile writes missions/<mission>/<file> under root.
func writeMissionFile(t *testing.T, root, mission, file, content string) {
	t.Helper()
	dir := filepath.Join(root, "missions", mission)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
}

// requireCount asserts that resp[key] is the JSON number want.
func requireCount(t *testing.T, label string, resp map[string]any, key string, want float64) {
	t.Helper()
	got, ok := resp[key].(float64)
	if !ok {
		t.Errorf("%s: %q missing or not a number in %v", label, key, resp)
		return
	}
	if got != want {
		t.Errorf("%s: %s got %v, want %v", label, key, got, want)
	}
}

// TestDocumentSyncReportsCriteriaCounts drives POST /v1/documents/sync in both
// response shapes. Two harnesses each hold one prose-only brief and one holds
// a brief with bullets: the all-harness shape sums to 2 skipped, the
// single-harness shape reports that harness's 1.
func TestDocumentSyncReportsCriteriaCounts(t *testing.T) {
	router, db := newTriageTestRouter(t)
	rootA, rootB := t.TempDir(), t.TempDir()
	writeMissionFile(t, rootA, "m-a", "BRIEF.md", "## Acceptance Criteria\nprose only\n")
	writeMissionFile(t, rootA, "m-good", "BRIEF.md", "## Acceptance Criteria\n- one\n- two\n")
	writeMissionFile(t, rootB, "m-b", "BRIEF.md", "## Acceptance Criteria\nprose only\n")
	for _, h := range []struct{ name, root string }{{"alpha", rootA}, {"beta", rootB}} {
		if code, resp := do(t, router, http.MethodPut, "/v1/harnesses/"+h.name, map[string]any{"memory_root": h.root}); code != http.StatusOK {
			t.Fatalf("PUT harness %s: %d %v", h.name, code, resp)
		}
	}
	insertMission(t, db, "m-a", "alpha", "in-progress")
	insertMission(t, db, "m-good", "alpha", "in-progress")
	insertMission(t, db, "m-b", "beta", "in-progress")

	// All registered harnesses: the "roots" shape carries the summed counts.
	code, all := do(t, router, http.MethodPost, "/v1/documents/sync", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("sync all: %d %v", code, all)
	}
	if _, ok := all["roots"]; !ok {
		t.Fatalf("sync all: expected the roots shape, got %v", all)
	}
	requireCount(t, "sync all", all, "criteria_skipped", 2)
	requireCount(t, "sync all", all, "criteria_errors", 0)
	if all["synced"] != true {
		t.Errorf("sync all: synced got %v, want true", all["synced"])
	}

	// One harness: the single-root shape carries only that harness's counts.
	code, one := do(t, router, http.MethodPost, "/v1/documents/sync", map[string]any{"harness": "alpha"})
	if code != http.StatusOK {
		t.Fatalf("sync alpha: %d %v", code, one)
	}
	if _, ok := one["root"]; !ok {
		t.Fatalf("sync alpha: expected the root shape, got %v", one)
	}
	requireCount(t, "sync alpha", one, "criteria_skipped", 1)
	requireCount(t, "sync alpha", one, "criteria_errors", 0)

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_criteria WHERE mission_id = 'm-good'`).Scan(&rows); err != nil || rows != 2 {
		t.Errorf("m-good criteria rows = %d, err %v; want 2", rows, err)
	}
}

// TestDocumentSyncReportsCriteriaErrors drops mission_criteria so every
// refresh fails: the sync still answers 200 and criteria_errors is nonzero.
func TestDocumentSyncReportsCriteriaErrors(t *testing.T) {
	router, db := newTriageTestRouter(t)
	root := t.TempDir()
	writeMissionFile(t, root, "m-1", "BRIEF.md", "## Acceptance Criteria\n- one\n")
	if code, resp := do(t, router, http.MethodPut, "/v1/harnesses/alpha", map[string]any{"memory_root": root}); code != http.StatusOK {
		t.Fatalf("PUT harness: %d %v", code, resp)
	}
	insertMission(t, db, "m-1", "alpha", "in-progress")
	if _, err := db.Exec(`DROP TABLE mission_criteria`); err != nil {
		t.Fatalf("drop mission_criteria: %v", err)
	}

	code, resp := do(t, router, http.MethodPost, "/v1/documents/sync", map[string]any{"harness": "alpha"})
	if code != http.StatusOK {
		t.Fatalf("sync with failing criteria storage: %d %v", code, resp)
	}
	requireCount(t, "sync", resp, "criteria_errors", 1)
	requireCount(t, "sync", resp, "criteria_skipped", 0)
}

// TestDocumentPushReportsCriteriaCounts pushes a prose-only brief and a brief
// with bullets in one request: the response counts one skipped, no errors, and
// the brief with bullets is stored. A second push after dropping
// mission_criteria reports the failure as criteria_errors with status 200.
func TestDocumentPushReportsCriteriaCounts(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-prose", "", "in-progress")
	insertMission(t, db, "m-good", "", "in-progress")

	push := func(files ...map[string]string) (int, map[string]any) {
		return do(t, router, http.MethodPost, "/v1/documents/push", map[string]any{"harness": "kirsch", "files": files})
	}
	code, resp := push(
		map[string]string{"path": "/h/missions/m-prose/BRIEF.md", "rel_path": "missions/m-prose/BRIEF.md", "content": "## Acceptance Criteria\nprose only\n"},
		map[string]string{"path": "/h/missions/m-good/BRIEF.md", "rel_path": "missions/m-good/BRIEF.md", "content": "## Acceptance Criteria\n- one\n- two\n"},
	)
	if code != http.StatusOK {
		t.Fatalf("push: %d %v", code, resp)
	}
	requireCount(t, "push", resp, "files", 2)
	requireCount(t, "push", resp, "criteria_skipped", 1)
	requireCount(t, "push", resp, "criteria_errors", 0)
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_criteria WHERE mission_id = 'm-good'`).Scan(&rows); err != nil || rows != 2 {
		t.Errorf("m-good criteria rows = %d, err %v; want 2", rows, err)
	}

	if _, err := db.Exec(`DROP TABLE mission_criteria`); err != nil {
		t.Fatalf("drop mission_criteria: %v", err)
	}
	code, resp = push(
		map[string]string{"path": "/h/missions/m-good/BRIEF.md", "rel_path": "missions/m-good/BRIEF.md", "content": "## Acceptance Criteria\n- one\n- three\n"},
	)
	if code != http.StatusOK {
		t.Fatalf("push with failing criteria storage: %d %v", code, resp)
	}
	requireCount(t, "push failing", resp, "criteria_errors", 1)
	requireCount(t, "push failing", resp, "criteria_skipped", 0)
}

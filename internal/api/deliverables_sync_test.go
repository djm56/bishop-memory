package api

import (
	"net/http"
	"testing"
)

// TestDocumentSyncReportsDeliverablesCounts drives POST /v1/documents/sync in both
// response shapes. Two harnesses each hold one debrief whose Deliverables Changed section has no bullets,
// and one holds a debrief with good bullets: the all-harness shape sums to 2 skipped,
// the single-harness shape reports 1.
func TestDocumentSyncReportsDeliverablesCounts(t *testing.T) {
	router, db := newTriageTestRouter(t)
	rootA, rootB := t.TempDir(), t.TempDir()
	writeMissionFile(t, rootA, "m-a", "DEBRIEF.md", "## Deliverables Changed\nno bullets\n")
	writeMissionFile(t, rootA, "m-good", "DEBRIEF.md", `## Deliverables Changed
- `+"`file.go`"+` — created
`)
	writeMissionFile(t, rootB, "m-b", "DEBRIEF.md", "## Deliverables Changed\nno bullets\n")
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
	requireCount(t, "sync all", all, "deliverables_skipped", 2)
	requireCount(t, "sync all", all, "deliverables_errors", 0)
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
	requireCount(t, "sync alpha", one, "deliverables_skipped", 1)
	requireCount(t, "sync alpha", one, "deliverables_errors", 0)

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_deliverables WHERE mission_id = 'm-good'`).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("m-good mission_deliverables rows = %d, err %v; want 1", rows, err)
	}
}

// TestDocumentSyncReportsDeliverablesErrors drops mission_deliverables so every
// refresh fails: the sync still answers 200 and deliverables_errors is nonzero.
func TestDocumentSyncReportsDeliverablesErrors(t *testing.T) {
	router, db := newTriageTestRouter(t)
	root := t.TempDir()
	writeMissionFile(t, root, "m-1", "DEBRIEF.md", `## Deliverables Changed
- `+"`file.go`"+` — test file
`)
	if code, resp := do(t, router, http.MethodPut, "/v1/harnesses/alpha", map[string]any{"memory_root": root}); code != http.StatusOK {
		t.Fatalf("PUT harness: %d %v", code, resp)
	}
	insertMission(t, db, "m-1", "alpha", "in-progress")
	if _, err := db.Exec(`DROP TABLE mission_deliverables`); err != nil {
		t.Fatalf("drop mission_deliverables: %v", err)
	}

	code, resp := do(t, router, http.MethodPost, "/v1/documents/sync", map[string]any{"harness": "alpha"})
	if code != http.StatusOK {
		t.Fatalf("sync with failing deliverables storage: %d %v", code, resp)
	}
	requireCount(t, "sync", resp, "deliverables_errors", 1)
	requireCount(t, "sync", resp, "deliverables_skipped", 0)
}

// TestDocumentPushReportsDeliverablesCounts pushes an unreadable debrief and one
// with good deliverables in one request: the response counts one skipped,
// no errors, and the good debrief is stored. A second push after dropping
// mission_deliverables reports the failure as deliverables_errors with status 200.
func TestDocumentPushReportsDeliverablesCounts(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-prose", "", "in-progress")
	insertMission(t, db, "m-good", "", "in-progress")

	push := func(files ...map[string]string) (int, map[string]any) {
		return do(t, router, http.MethodPost, "/v1/documents/push", map[string]any{"harness": "kirsch", "files": files})
	}
	code, resp := push(
		map[string]string{"path": "/h/missions/m-prose/DEBRIEF.md", "rel_path": "missions/m-prose/DEBRIEF.md", "content": "## Deliverables Changed\nprose only\n"},
		map[string]string{"path": "/h/missions/m-good/DEBRIEF.md", "rel_path": "missions/m-good/DEBRIEF.md", "content": `## Deliverables Changed
- ` + "`file1.go`" + ` — first file
- ` + "`file2.go`" + ` — second file
`},
	)
	if code != http.StatusOK {
		t.Fatalf("push: %d %v", code, resp)
	}
	requireCount(t, "push", resp, "files", 2)
	requireCount(t, "push", resp, "deliverables_skipped", 1)
	requireCount(t, "push", resp, "deliverables_errors", 0)
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_deliverables WHERE mission_id = 'm-good'`).Scan(&rows); err != nil || rows != 2 {
		t.Errorf("m-good mission_deliverables rows = %d, err %v; want 2", rows, err)
	}

	if _, err := db.Exec(`DROP TABLE mission_deliverables`); err != nil {
		t.Fatalf("drop mission_deliverables: %v", err)
	}
	code, resp = push(
		map[string]string{"path": "/h/missions/m-good/DEBRIEF.md", "rel_path": "missions/m-good/DEBRIEF.md", "content": `## Deliverables Changed
- ` + "`updated.go`" + ` — updated file
`},
	)
	if code != http.StatusOK {
		t.Fatalf("push with failing deliverables storage: %d %v", code, resp)
	}
	requireCount(t, "push failing", resp, "deliverables_errors", 1)
	requireCount(t, "push failing", resp, "deliverables_skipped", 0)
}

package api

import (
	"net/http"
	"testing"
)

// TestDocumentSyncReportsLessonsCounts drives POST /v1/documents/sync in both
// response shapes. Each harness holds one debrief whose Wrong Assumptions
// section has no table, and alpha also holds a readable one: the all-harness
// shape sums to 2 skipped, the alpha-only shape reports 1.
func TestDocumentSyncReportsLessonsCounts(t *testing.T) {
	router, db := newTriageTestRouter(t)
	rootA, rootB := t.TempDir(), t.TempDir()
	writeMissionFile(t, rootA, "m-a", "DEBRIEF.md", "## Wrong Assumptions\nno table\n")
	writeMissionFile(t, rootA, "m-good", "DEBRIEF.md", "## Wrong Assumptions\n| Assumption | Why | Fix |\n|---|---|---|\n| a | b | c |\n")
	writeMissionFile(t, rootB, "m-b", "DEBRIEF.md", "## Wrong Assumptions\nno table\n")
	for _, h := range []struct{ name, root string }{{"alpha", rootA}, {"beta", rootB}} {
		if code, resp := do(t, router, http.MethodPut, "/v1/harnesses/"+h.name, map[string]any{"memory_root": h.root}); code != http.StatusOK {
			t.Fatalf("PUT harness %s: %d %v", h.name, code, resp)
		}
	}
	insertMission(t, db, "m-a", "alpha", "in-progress")
	insertMission(t, db, "m-good", "alpha", "in-progress")
	insertMission(t, db, "m-b", "beta", "in-progress")

	code, all := do(t, router, http.MethodPost, "/v1/documents/sync", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("sync all: %d %v", code, all)
	}
	if _, ok := all["roots"]; !ok {
		t.Fatalf("sync all: expected the roots shape, got %v", all)
	}
	requireCount(t, "sync all", all, "lessons_skipped", 2)
	requireCount(t, "sync all", all, "lessons_errors", 0)

	code, one := do(t, router, http.MethodPost, "/v1/documents/sync", map[string]any{"harness": "alpha"})
	if code != http.StatusOK {
		t.Fatalf("sync alpha: %d %v", code, one)
	}
	if _, ok := one["root"]; !ok {
		t.Fatalf("sync alpha: expected the root shape, got %v", one)
	}
	requireCount(t, "sync alpha", one, "lessons_skipped", 1)
	requireCount(t, "sync alpha", one, "lessons_errors", 0)
}

// TestDocumentSyncReportsLessonsErrors drops mission_lessons so every refresh
// fails: the sync still answers 200, and lessons_errors is 1 in both response
// shapes (one harness, and all harnesses), checked in one assertion that
// reports both values.
func TestDocumentSyncReportsLessonsErrors(t *testing.T) {
	router, db := newTriageTestRouter(t)
	root := t.TempDir()
	writeMissionFile(t, root, "m-1", "DEBRIEF.md", "## Wrong Assumptions\n| Assumption | Why | Fix |\n|---|---|---|\n| a | b | c |\n")
	if code, resp := do(t, router, http.MethodPut, "/v1/harnesses/alpha", map[string]any{"memory_root": root}); code != http.StatusOK {
		t.Fatalf("PUT harness: %d %v", code, resp)
	}
	insertMission(t, db, "m-1", "alpha", "in-progress")
	if _, err := db.Exec(`DROP TABLE mission_lessons`); err != nil {
		t.Fatalf("drop mission_lessons: %v", err)
	}
	code, one := do(t, router, http.MethodPost, "/v1/documents/sync", map[string]any{"harness": "alpha"})
	if code != http.StatusOK {
		t.Fatalf("sync one harness with failing lessons storage: %d %v", code, one)
	}
	code, all := do(t, router, http.MethodPost, "/v1/documents/sync", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("sync all harnesses with failing lessons storage: %d %v", code, all)
	}
	if _, ok := all["roots"]; !ok {
		t.Fatalf("sync all harnesses: expected the roots shape, got %v", all)
	}
	if one["lessons_errors"] != float64(1) || all["lessons_errors"] != float64(1) {
		t.Errorf("sync errors: lessons_errors roots=%v root=%v, want 1 and 1", all["lessons_errors"], one["lessons_errors"])
	}
	requireCount(t, "sync errors", one, "lessons_skipped", 0)
}

// TestDocumentPushReportsLessonsCounts pushes an unreadable debrief and a
// readable one: one skipped, no errors, the readable one stored. A second push
// after dropping mission_lessons reports lessons_errors 1 with status 200.
func TestDocumentPushReportsLessonsCounts(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-prose", "", "in-progress")
	insertMission(t, db, "m-good", "", "in-progress")
	push := func(files ...map[string]string) (int, map[string]any) {
		return do(t, router, http.MethodPost, "/v1/documents/push", map[string]any{"harness": "kirsch", "files": files})
	}
	code, resp := push(
		map[string]string{"path": "/h/missions/m-prose/DEBRIEF.md", "rel_path": "missions/m-prose/DEBRIEF.md", "content": "## Wrong Assumptions\nprose only\n"},
		map[string]string{"path": "/h/missions/m-good/DEBRIEF.md", "rel_path": "missions/m-good/DEBRIEF.md", "content": "## Wrong Assumptions\n| Assumption | Why | Fix |\n|---|---|---|\n| a | b | c |\n| d | e | f |\n"},
	)
	if code != http.StatusOK {
		t.Fatalf("push: %d %v", code, resp)
	}
	requireCount(t, "push", resp, "files", 2)
	requireCount(t, "push", resp, "lessons_skipped", 1)
	requireCount(t, "push", resp, "lessons_errors", 0)
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_lessons WHERE mission_id = 'm-good'`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("m-good lessons after push: rows=%d err=%v, want 2", rows, err)
	}
	if _, err := db.Exec(`DROP TABLE mission_lessons`); err != nil {
		t.Fatalf("drop mission_lessons: %v", err)
	}
	code, resp = push(map[string]string{"path": "/h/missions/m-good/DEBRIEF.md", "rel_path": "missions/m-good/DEBRIEF.md", "content": "## Wrong Assumptions\n| Assumption | Why | Fix |\n|---|---|---|\n| x | y | z |\n"})
	if code != http.StatusOK {
		t.Fatalf("push failing: %d %v", code, resp)
	}
	requireCount(t, "push failing", resp, "lessons_errors", 1)
	requireCount(t, "push failing", resp, "lessons_skipped", 0)
}

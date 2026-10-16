package api

import (
	"fmt"
	"testing"
)

// TestHUDMissionDetailLessons drives GET /v1/hud/missions/:id and checks that
// "lessons" lists wrong assumptions, then the reuse trigger, then the steps in
// ord order, with why and correction as JSON null where NULL.
func TestHUDMissionDetailLessons(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-lessons", "test", "in-progress")
	if _, err := db.Exec(`INSERT INTO mission_lessons (mission_id, kind, ord, text, why, correction) VALUES
		('m-lessons', 'reuse-step', 2, 'step two', NULL, NULL),
		('m-lessons', 'reuse-step', 1, 'step one', NULL, NULL),
		('m-lessons', 'similar-mission', 1, 'any merge', NULL, NULL),
		('m-lessons', 'wrong-assumption', 1, 'assumed', 'it was not', 'fixed')`); err != nil {
		t.Fatalf("insert lessons: %v", err)
	}
	lessons := hudDetailArray(t, hudGet(t, router, "m-lessons"), "lessons")
	want := []string{"wrong-assumption 1 assumed", "similar-mission 1 any merge", "reuse-step 1 step one", "reuse-step 2 step two"}
	if len(lessons) != len(want) {
		t.Fatalf("lessons has %d rows, want %d", len(lessons), len(want))
	}
	for i, w := range want {
		row := hudRow(t, "lessons", lessons, i)
		if got := fmt.Sprintf("%v %v %v", row["kind"], row["ord"], row["text"]); got != w {
			t.Errorf("lessons[%d]: got %q, want %q", i, got, w)
		}
	}
	if wa := hudRow(t, "lessons", lessons, 0); wa["why"] != "it was not" || wa["correction"] != "fixed" {
		t.Errorf("wrong-assumption why/correction: got %v / %v, want it was not / fixed", wa["why"], wa["correction"])
	}
	if step := hudRow(t, "lessons", lessons, 2); step["why"] != nil || step["correction"] != nil {
		t.Errorf("reuse-step why/correction: got %v / %v, want null / null", step["why"], step["correction"])
	}
}

// TestHUDMissionDetailLessonsEmpty checks "lessons" is an empty array when the
// mission has none.
func TestHUDMissionDetailLessonsEmpty(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-no-lessons", "test", "in-progress")
	if lessons := hudDetailArray(t, hudGet(t, router, "m-no-lessons"), "lessons"); len(lessons) != 0 {
		t.Errorf("lessons with none stored: got %d rows, want 0", len(lessons))
	}
}

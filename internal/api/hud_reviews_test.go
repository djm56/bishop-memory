package api

import (
	"fmt"
	"testing"
)

// TestHUDMissionDetailReviews drives GET /v1/hud/missions/:id and checks that
// "reviews" lists the @apone steps only, under any harness prefix, in numeric
// step order (step 10, inserted first, comes last), with the outcome parsed
// from the notes (the summary when the notes carry no verdict) and JSON null
// for a value the text does not give. The @hicks step carries verdict wording
// too, as a control that the agent filter, not the notes, keeps it out.
func TestHUDMissionDetailReviews(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-reviews", "test", "in-progress")
	if _, err := db.Exec(`INSERT INTO mission_steps (mission_id, step, agent, status, notes, summary) VALUES
		('m-reviews', '10', '@apone', 'done', 'APPROVED', NULL),
		('m-reviews', '1', '@hicks', 'done', 'APPROVED build', NULL),
		('m-reviews', '2', '@apone', 'done', 'Review of 1: NOT APPROVED — 2 CRITICAL', 'Step 2 review APPROVED'),
		('m-reviews', '3', 'harness:apone', 'done', 'Verdict: APPROVED; critical=0; warnings=1; reviews_step=1a; fix_round=1', NULL),
		('m-reviews', '4', '@apone', 'done', NULL, 'APPROVED, no CRITICAL'),
		('m-reviews', '5', '@apone', 'in-progress', NULL, NULL)`); err != nil {
		t.Fatalf("insert steps: %v", err)
	}
	reviews := hudDetailArray(t, hudGet(t, router, "m-reviews"), "reviews")
	want := []string{
		"2 @apone not-approved 2 <nil> 1 <nil> text",
		"3 harness:apone approved 0 1 1a 1 line",
		"4 @apone approved 0 <nil> <nil> <nil> text",
		"5 @apone <nil> <nil> <nil> <nil> <nil> none",
		"10 @apone approved <nil> <nil> <nil> <nil> text",
	}
	if len(reviews) != len(want) {
		t.Fatalf("reviews has %d rows, want %d", len(reviews), len(want))
	}
	for i, w := range want {
		row := hudRow(t, "reviews", reviews, i)
		got := fmt.Sprintf("%v %v %v %v %v %v %v %v", row["step"], row["agent"], row["verdict"], row["critical"],
			row["warnings"], row["reviews_step"], row["fix_round"], row["source"])
		if got != w {
			t.Errorf("reviews[%d]: got %q, want %q", i, got, w)
		}
	}
}

// TestHUDMissionDetailReviewsEmpty checks "reviews" is an empty array when the
// mission has steps but none by @apone; the step list is checked first, so an
// empty "reviews" cannot come from a mission with no steps at all.
func TestHUDMissionDetailReviewsEmpty(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-no-reviews", "test", "in-progress")
	if _, err := db.Exec(`INSERT INTO mission_steps (mission_id, step, agent, status, notes) VALUES
		('m-no-reviews', '1', '@hicks', 'done', 'APPROVED build')`); err != nil {
		t.Fatalf("insert step: %v", err)
	}
	detail := hudGet(t, router, "m-no-reviews")
	if steps := hudDetailArray(t, detail, "steps"); len(steps) != 1 {
		t.Fatalf("steps: got %d rows, want 1", len(steps))
	}
	if reviews := hudDetailArray(t, detail, "reviews"); len(reviews) != 0 {
		t.Errorf("reviews with no @apone step: got %d rows, want 0", len(reviews))
	}
}

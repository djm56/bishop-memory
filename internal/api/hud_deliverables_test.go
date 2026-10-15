package api

import (
	"testing"
)

// TestHUDMissionDetailDeliverables drives GET /v1/hud/missions/:id and
// verifies the "deliverables" field returns rows ordered by ord, with path
// as JSON null when NULL, and an empty array when no rows exist.
func TestHUDMissionDetailDeliverables(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-deliverables", "test", "in-progress")

	// Insert some deliverables: one with path, one without path (NULL), ordered
	// as ord 2, 1, 3 to verify ORDER BY ord in the query.
	if _, err := db.Exec(
		`INSERT INTO mission_deliverables (mission_id, ord, path, note)
		 VALUES (?, ?, ?, ?), (?, ?, ?, ?), (?, ?, ?, ?)`,
		"m-deliverables", 2, "internal/file2.go", "second deliverable",
		"m-deliverables", 1, "internal/file1.go", "first deliverable",
		"m-deliverables", 3, nil, "no path here",
	); err != nil {
		t.Fatalf("insert deliverables: %v", err)
	}

	detail := hudGet(t, router, "m-deliverables")
	deliverables := hudDetailArray(t, detail, "deliverables")

	if len(deliverables) != 3 {
		t.Fatalf("deliverables has %d rows, want 3", len(deliverables))
	}

	// Row 1: path and note
	row1 := hudRow(t, "deliverables", deliverables, 0)
	ord1, ok := row1["ord"].(float64)
	if !ok || ord1 != 1 {
		t.Errorf("row 0 ord: got %v, want 1", row1["ord"])
	}
	if row1["path"] != "internal/file1.go" {
		t.Errorf("row 0 path: got %v, want %q", row1["path"], "internal/file1.go")
	}
	if row1["note"] != "first deliverable" {
		t.Errorf("row 0 note: got %v, want %q", row1["note"], "first deliverable")
	}

	// Row 2: different path
	row2 := hudRow(t, "deliverables", deliverables, 1)
	ord2, ok := row2["ord"].(float64)
	if !ok || ord2 != 2 {
		t.Errorf("row 1 ord: got %v, want 2", row2["ord"])
	}
	if row2["path"] != "internal/file2.go" {
		t.Errorf("row 1 path: got %v, want %q", row2["path"], "internal/file2.go")
	}

	// Row 3: path is JSON null
	row3 := hudRow(t, "deliverables", deliverables, 2)
	ord3, ok := row3["ord"].(float64)
	if !ok || ord3 != 3 {
		t.Errorf("row 2 ord: got %v, want 3", row3["ord"])
	}
	if row3["path"] != nil {
		t.Errorf("row 2 path: got %v, want nil (JSON null)", row3["path"])
	}
	if row3["note"] != "no path here" {
		t.Errorf("row 2 note: got %v, want %q", row3["note"], "no path here")
	}
}

// TestHUDMissionDetailDeliverablesEmpty verifies that when no deliverables
// exist, the "deliverables" field is an empty array.
func TestHUDMissionDetailDeliverablesEmpty(t *testing.T) {
	router, db := newTriageTestRouter(t)
	insertMission(t, db, "m-no-deliverables", "test", "in-progress")

	detail := hudGet(t, router, "m-no-deliverables")
	deliverables := hudDetailArray(t, detail, "deliverables")

	if len(deliverables) != 0 {
		t.Errorf("deliverables: got %d items, want 0 (empty array)", len(deliverables))
	}
}

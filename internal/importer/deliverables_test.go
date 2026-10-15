package importer

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// deliverablesCount returns the number of mission_deliverables rows for a mission.
func deliverablesCount(t *testing.T, db *sql.DB, missionID string) int {
	t.Helper()
	return countRows(t, db, `SELECT COUNT(*) FROM mission_deliverables WHERE mission_id = ?`, missionID)
}

// deliverableAt returns the path and note at ord. path is a sql.NullString
// to explicitly represent NULL values.
func deliverableAt(t *testing.T, db *sql.DB, missionID string, ord int) (path sql.NullString, note string) {
	t.Helper()
	if err := db.QueryRow(
		`SELECT path, note FROM mission_deliverables WHERE mission_id = ? AND ord = ?`, missionID, ord,
	).Scan(&path, &note); err != nil {
		t.Fatalf("deliverable at ord %d of %s: %v", ord, missionID, err)
	}
	return
}

// deliverableAtContext is deliverableAt with a context: it returns the error
// instead of failing, so the caller decides how to report it. It never calls
// t.Fatalf, which makes it safe to use where a deadline must be reported by the
// test goroutine.
func deliverableAtContext(ctx context.Context, db *sql.DB, missionID string, ord int) (path sql.NullString, note string, err error) {
	err = db.QueryRowContext(ctx,
		`SELECT path, note FROM mission_deliverables WHERE mission_id = ? AND ord = ?`, missionID, ord,
	).Scan(&path, &note)
	return
}

// TestStoreMissionDeliverablesNoMissionsRow tests that when the mission does not exist,
// DeliverablesSkipped is incremented and rows are not touched.
func TestStoreMissionDeliverablesNoMissionsRow(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-does-not-exist"

	harness := "test-harness"
	body := `
# Deliverables Changed

- ` + "`a/b.go`" + ` — created new file
- ` + "`internal/x.go`" + ` — updated logic
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body, "sha", missionID, harness); err != nil {
		t.Fatalf("upsert document: %v", err)
	}

	stats := storeMissionDeliverables(db, missionID, harness)
	if stats.DeliverablesSkipped != 1 {
		t.Errorf("DeliverablesSkipped: got %d, want 1", stats.DeliverablesSkipped)
	}
	if stats.DeliverablesErrors != 0 {
		t.Errorf("DeliverablesErrors: got %d, want 0", stats.DeliverablesErrors)
	}
	if n := deliverablesCount(t, db, missionID); n != 0 {
		t.Errorf("rows after skip: got %d, want 0", n)
	}
}

// TestStoreMissionDeliverablesEmptyMissionID tests that when missionID is empty,
// the function returns an empty SyncStats without touching the database.
func TestStoreMissionDeliverablesEmptyMissionID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	harness := "test-harness"
	stats := storeMissionDeliverables(db, "", harness)
	if stats != (SyncStats{}) {
		t.Errorf("empty missionID: got %+v, want {}", stats)
	}
}

// TestStoreMissionDeliverablesWithBullets tests that a well-formed section
// is parsed and stored with exact ord, path (including NULL) and note.
func TestStoreMissionDeliverablesWithBullets(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-good"
	seedMission(t, db, missionID)

	harness := "test-harness"
	body := `
# Deliverables Changed

- ` + "`internal/importer/deliverables.go`" + ` — new module
- ` + "`internal/api/hud.go`" + ` — added deliverables query
- No path here, just a note
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body, "sha", missionID, harness); err != nil {
		t.Fatalf("upsert document: %v", err)
	}

	stats := storeMissionDeliverables(db, missionID, harness)
	if stats.DeliverablesSkipped != 0 {
		t.Errorf("DeliverablesSkipped: got %d, want 0", stats.DeliverablesSkipped)
	}
	if stats.DeliverablesErrors != 0 {
		t.Errorf("DeliverablesErrors: got %d, want 0", stats.DeliverablesErrors)
	}

	n := deliverablesCount(t, db, missionID)
	if n != 3 {
		t.Errorf("row count: got %d, want 3", n)
	}

	// Check first row: path with exact value, ord=1
	path, note := deliverableAt(t, db, missionID, 1)
	if !path.Valid || path.String != "internal/importer/deliverables.go" {
		t.Errorf("row 1 path: got Valid=%v String=%q, want Valid=true String=%q", path.Valid, path.String, "internal/importer/deliverables.go")
	}
	if note != "new module" {
		t.Errorf("row 1 note: got %q, want %q", note, "new module")
	}

	// Check second row: different path
	path, note = deliverableAt(t, db, missionID, 2)
	if !path.Valid || path.String != "internal/api/hud.go" {
		t.Errorf("row 2 path: got Valid=%v String=%q, want Valid=true String=%q", path.Valid, path.String, "internal/api/hud.go")
	}
	if note != "added deliverables query" {
		t.Errorf("row 2 note: got %q, want %q", note, "added deliverables query")
	}

	// Check third row: path is NULL
	path, note = deliverableAt(t, db, missionID, 3)
	if path.Valid {
		t.Errorf("row 3 path: got Valid=%v String=%q, want Valid=false", path.Valid, path.String)
	}
	if note != "No path here, just a note" {
		t.Errorf("row 3 note: got %q, want %q", note, "No path here, just a note")
	}
}

// TestStoreMissionDeliverablesReplaceOnReimport tests that a re-import replaces
// existing rows when the section is readable.
func TestStoreMissionDeliverablesReplaceOnReimport(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-replace"
	seedMission(t, db, missionID)

	harness := "test-harness"
	body1 := `
# Deliverables Changed

- ` + "`file1.go`" + ` — first file
- ` + "`file2.go`" + ` — second file
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body1, "sha1", missionID, harness); err != nil {
		t.Fatalf("upsert document 1: %v", err)
	}
	stats := storeMissionDeliverables(db, missionID, harness)
	if stats.DeliverablesSkipped != 0 || stats.DeliverablesErrors != 0 {
		t.Errorf("first import: DeliverablesSkipped=%d DeliverablesErrors=%d, want 0 and 0", stats.DeliverablesSkipped, stats.DeliverablesErrors)
	}
	if n := deliverablesCount(t, db, missionID); n != 2 {
		t.Errorf("after first import: got %d rows, want 2", n)
	}

	// Re-import with different content
	body2 := `
# Deliverables Changed

- ` + "`new_file.go`" + ` — only file now
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body2, "sha2", missionID, harness); err != nil {
		t.Fatalf("upsert document 2: %v", err)
	}
	stats = storeMissionDeliverables(db, missionID, harness)
	if stats.DeliverablesSkipped != 0 {
		t.Errorf("DeliverablesSkipped on re-import: got %d, want 0", stats.DeliverablesSkipped)
	}
	if stats.DeliverablesErrors != 0 {
		t.Errorf("DeliverablesErrors on re-import: got %d, want 0", stats.DeliverablesErrors)
	}

	n := deliverablesCount(t, db, missionID)
	if n != 1 {
		t.Errorf("after re-import: got %d rows, want 1", n)
	}

	path, _ := deliverableAt(t, db, missionID, 1)
	if !path.Valid || path.String != "new_file.go" {
		t.Errorf("row 1 path after re-import: got Valid=%v String=%q, want Valid=true String=%q", path.Valid, path.String, "new_file.go")
	}
}

// TestStoreMissionDeliverablesNoBulletSection tests that when the heading
// exists but contains no bullets, existing rows are kept and DeliverablesSkipped
// is incremented exactly once.
func TestStoreMissionDeliverablesNoBulletSection(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-no-bullets"
	seedMission(t, db, missionID)

	harness := "test-harness"
	// First, insert a row
	body1 := `
# Deliverables Changed

- ` + "`file.go`" + ` — a file
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body1, "sha1", missionID, harness); err != nil {
		t.Fatalf("upsert document 1: %v", err)
	}
	storeMissionDeliverables(db, missionID, harness)

	// Re-import with heading but only prose
	body2 := `
# Deliverables Changed

This section has prose but no bullets.
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body2, "sha2", missionID, harness); err != nil {
		t.Fatalf("upsert document 2: %v", err)
	}
	stats := storeMissionDeliverables(db, missionID, harness)
	if stats.DeliverablesSkipped != 1 {
		t.Errorf("DeliverablesSkipped: got %d, want 1 (unreadable section)", stats.DeliverablesSkipped)
	}
	if stats.DeliverablesErrors != 0 {
		t.Errorf("DeliverablesErrors: got %d, want 0", stats.DeliverablesErrors)
	}

	// Rows should be kept
	n := deliverablesCount(t, db, missionID)
	if n != 1 {
		t.Errorf("row count after unreadable: got %d, want 1 (rows kept)", n)
	}
}

// TestStoreMissionDeliverablesNoneOnlySection tests that a section with only
// "none" bullets deletes all rows and counts skipped as 0.
func TestStoreMissionDeliverablesNoneOnlySection(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-none-only"
	seedMission(t, db, missionID)

	harness := "test-harness"
	// First, insert a row
	body1 := `
# Deliverables Changed

- ` + "`file.go`" + ` — a file
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body1, "sha1", missionID, harness); err != nil {
		t.Fatalf("upsert document 1: %v", err)
	}
	storeMissionDeliverables(db, missionID, harness)

	// Re-import with only "none" bullets
	body2 := `
# Deliverables Changed

- None.
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body2, "sha2", missionID, harness); err != nil {
		t.Fatalf("upsert document 2: %v", err)
	}
	stats := storeMissionDeliverables(db, missionID, harness)
	if stats.DeliverablesSkipped != 0 {
		t.Errorf("DeliverablesSkipped: got %d, want 0 (none-only section)", stats.DeliverablesSkipped)
	}
	if stats.DeliverablesErrors != 0 {
		t.Errorf("DeliverablesErrors: got %d, want 0", stats.DeliverablesErrors)
	}

	n := deliverablesCount(t, db, missionID)
	if n != 0 {
		t.Errorf("after importing none-only section: got %d rows, want 0 (deleted)", n)
	}
}

// TestStoreMissionDeliverablesNoHeading tests that when the heading is absent,
// existing rows are kept and nothing is counted as skipped.
func TestStoreMissionDeliverablesNoHeading(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-no-heading"
	seedMission(t, db, missionID)

	harness := "test-harness"
	// Insert a row first
	body1 := `
# Deliverables Changed

- ` + "`file.go`" + ` — a file
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body1, "sha1", missionID, harness); err != nil {
		t.Fatalf("upsert document 1: %v", err)
	}
	storeMissionDeliverables(db, missionID, harness)

	// Re-import with no deliverables heading
	body2 := `
# Some Other Section

This debrief has no deliverables section.
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body2, "sha2", missionID, harness); err != nil {
		t.Fatalf("upsert document 2: %v", err)
	}
	stats := storeMissionDeliverables(db, missionID, harness)
	if stats.DeliverablesSkipped != 0 {
		t.Errorf("DeliverablesSkipped: got %d, want 0 (heading absent, rows kept)", stats.DeliverablesSkipped)
	}
	if stats.DeliverablesErrors != 0 {
		t.Errorf("DeliverablesErrors: got %d, want 0", stats.DeliverablesErrors)
	}

	// Rows should be kept
	n := deliverablesCount(t, db, missionID)
	if n != 1 {
		t.Errorf("row count: got %d, want 1 (rows kept)", n)
	}
}

// TestStoreMissionDeliverablesNoDebrief tests that when there is no debrief,
// existing rows are kept and nothing is counted.
func TestStoreMissionDeliverablesNoDebrief(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-no-debrief"
	seedMission(t, db, missionID)

	harness := "test-harness"
	// Directly insert a row (no debrief document exists yet)
	if _, err := db.Exec(
		`INSERT INTO mission_deliverables (mission_id, ord, path, note)
		 VALUES (?, ?, ?, ?)`,
		missionID, 1, "existing/file.go", "existing row",
	); err != nil {
		t.Fatalf("insert initial row: %v", err)
	}

	// Call storeMissionDeliverables when no debrief exists
	stats := storeMissionDeliverables(db, missionID, harness)
	if stats.DeliverablesSkipped != 0 {
		t.Errorf("DeliverablesSkipped: got %d, want 0 (no debrief, rows kept)", stats.DeliverablesSkipped)
	}
	if stats.DeliverablesErrors != 0 {
		t.Errorf("DeliverablesErrors: got %d, want 0", stats.DeliverablesErrors)
	}

	// Rows should be kept
	n := deliverablesCount(t, db, missionID)
	if n != 1 {
		t.Errorf("row count: got %d, want 1 (kept)", n)
	}
}

// TestStoreMissionDeliverablesReadFailure tests the read-failure path:
// a database error reading the debrief adds DeliverablesErrors and keeps rows.
func TestStoreMissionDeliverablesReadFailure(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-read-fail"
	seedMission(t, db, missionID)

	harness := "test-harness"
	// Seed initial rows
	body1 := `
# Deliverables Changed

- ` + "`seeded.go`" + ` — seeded file
`
	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body1, "sha1", missionID, harness); err != nil {
		t.Fatalf("upsert document: %v", err)
	}
	storeMissionDeliverables(db, missionID, harness)
	if n := deliverablesCount(t, db, missionID); n != 1 {
		t.Errorf("initial state: got %d rows, want 1", n)
	}

	// Break documentBody to force a read error
	breakDocumentKind(t, db, "debrief")

	// Call storeMissionDeliverables — should get DeliverablesErrors 1 and keep rows
	stats := storeMissionDeliverables(db, missionID, harness)
	if stats.DeliverablesErrors != 1 {
		t.Errorf("read-error path: DeliverablesErrors got %d, want 1", stats.DeliverablesErrors)
	}
	if n := deliverablesCount(t, db, missionID); n != 1 {
		t.Errorf("rows after read error: got %d, want 1 (kept)", n)
	}
}

// TestStoreMissionDeliverablesReplaceFailure tests the replace-failure path:
// when the transaction fails, DeliverablesErrors is incremented and rows are unchanged.
func TestStoreMissionDeliverablesReplaceFailure(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-replace-fail"
	seedMission(t, db, missionID)

	harness := "test-harness"
	// Seed two existing mission_deliverables rows
	if _, err := db.Exec(
		`INSERT INTO mission_deliverables (mission_id, ord, path, note)
		 VALUES (?, ?, ?, ?), (?, ?, ?, ?)`,
		missionID, 1, "original/file1.go", "original row 1",
		missionID, 2, "original/file2.go", "original row 2",
	); err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	// Create a trigger that fails on the second INSERT
	if _, err := db.Exec(`
		CREATE TRIGGER fail_second BEFORE INSERT ON mission_deliverables
		WHEN NEW.ord = 2
		BEGIN SELECT RAISE(ABORT, 'forced'); END
	`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	// Import a debrief with at least two bullets
	body := `
# Deliverables Changed

- ` + "`new_file1.go`" + ` — new row 1
- ` + "`new_file2.go`" + ` — new row 2
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body, "sha", missionID, harness); err != nil {
		t.Fatalf("upsert document: %v", err)
	}

	// Call storeMissionDeliverables — should fail on the trigger
	stats := storeMissionDeliverables(db, missionID, harness)
	if stats.DeliverablesErrors != 1 {
		t.Errorf("replace-failure path: DeliverablesErrors got %d, want 1", stats.DeliverablesErrors)
	}
	if stats.DeliverablesSkipped != 0 {
		t.Errorf("replace-failure path: DeliverablesSkipped got %d, want 0", stats.DeliverablesSkipped)
	}

	// Verify the two ORIGINAL rows are still present (transaction rolled back).
	// The reads run on the test goroutine under a 5s deadline: a transaction
	// left open holds the only connection and the read then times out.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, want := range []struct {
		ord        int
		path, note string
	}{
		{1, "original/file1.go", "original row 1"},
		{2, "original/file2.go", "original row 2"},
	} {
		path, note, err := deliverableAtContext(ctx, db, missionID, want.ord)
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("rollback missing: connection still held")
		}
		if err != nil {
			t.Fatalf("row %d after rollback: %v", want.ord, err)
		}
		if !path.Valid || path.String != want.path || note != want.note {
			t.Errorf("row %d after rollback: got (Valid=%v String=%q, %q), want (Valid=true String=%q, %q)", want.ord, path.Valid, path.String, note, want.path, want.note)
		}
	}
}

// TestImportMarkdownStatsDebriefStoresDeliverables tests that ImportMarkdownStats
// on a mission's DEBRIEF.md (mission row present) stores deliverables rows and
// returns correct statistics.
func TestImportMarkdownStatsDebriefStoresDeliverables(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-import-debrief"
	seedMission(t, db, missionID)

	harness := "test-harness"
	debriefContent := []byte(`
# Deliverables Changed

- ` + "`internal/importer/deliverables.go`" + ` — new file
- ` + "`internal/api/hud.go`" + ` — updated hud
- Just a note
`)

	stats, err := ImportMarkdownStats(db, "/path/to/DEBRIEF.md", "missions/"+missionID+"/DEBRIEF.md", harness, debriefContent)
	if err != nil {
		t.Fatalf("ImportMarkdownStats: %v", err)
	}
	if stats.DeliverablesSkipped != 0 {
		t.Errorf("DeliverablesSkipped: got %d, want 0", stats.DeliverablesSkipped)
	}
	if stats.DeliverablesErrors != 0 {
		t.Errorf("DeliverablesErrors: got %d, want 0", stats.DeliverablesErrors)
	}

	n := deliverablesCount(t, db, missionID)
	if n != 3 {
		t.Errorf("rows stored: got %d, want 3", n)
	}
}

// TestImportMarkdownStatsDebriefNoBulletsCountsSkipped drives the import entry
// point (ImportMarkdownStats, which merges the stats through add()) with a
// debrief whose "Deliverables Changed" section has no bullets and asserts the
// skipped count reaches the caller as exactly 1.
func TestImportMarkdownStatsDebriefNoBulletsCountsSkipped(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-import-debrief-nobullets"
	seedMission(t, db, missionID)

	debriefContent := []byte(`
# Deliverables Changed

Prose only, no bullets here.
`)
	stats, err := ImportMarkdownStats(db, "/path/to/DEBRIEF.md", "missions/"+missionID+"/DEBRIEF.md", "test-harness", debriefContent)
	if err != nil {
		t.Fatalf("ImportMarkdownStats: %v", err)
	}
	if stats.DeliverablesSkipped != 1 {
		t.Errorf("DeliverablesSkipped via import path: got %d, want 1 (section without bullets)", stats.DeliverablesSkipped)
	}
	if stats.DeliverablesErrors != 0 {
		t.Errorf("DeliverablesErrors via import path: got %d, want 0", stats.DeliverablesErrors)
	}
}

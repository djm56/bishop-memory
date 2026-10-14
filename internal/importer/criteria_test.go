package importer

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedMission inserts a missions row so criteria can be stored for id.
func seedMission(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO missions (id, title, status) VALUES (?, ?, ?)`,
		id, "Test Mission", "in-progress",
	); err != nil {
		t.Fatalf("create mission %s: %v", id, err)
	}
}

// countRows runs a single-integer query and fails the test if the query errors,
// so a broken query is never read as a legitimate zero.
func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

// criteriaCount returns the number of mission_criteria rows for a mission.
func criteriaCount(t *testing.T, db *sql.DB, missionID string) int {
	t.Helper()
	return countRows(t, db, `SELECT COUNT(*) FROM mission_criteria WHERE mission_id = ?`, missionID)
}

// resultAt returns the result of the criterion at ord, "NULL" when it is NULL.
func resultAt(t *testing.T, db *sql.DB, missionID string, ord int) string {
	t.Helper()
	var r sql.NullString
	if err := db.QueryRow(
		`SELECT result FROM mission_criteria WHERE mission_id = ? AND ord = ?`, missionID, ord,
	).Scan(&r); err != nil {
		t.Fatalf("result at ord %d of %s: %v", ord, missionID, err)
	}
	if !r.Valid {
		return "NULL"
	}
	return r.String
}

// criterionAt returns the stored criterion text at ord.
func criterionAt(t *testing.T, db *sql.DB, missionID string, ord int) string {
	t.Helper()
	var c string
	if err := db.QueryRow(
		`SELECT criterion FROM mission_criteria WHERE mission_id = ? AND ord = ?`, missionID, ord,
	).Scan(&c); err != nil {
		t.Fatalf("criterion at ord %d of %s: %v", ord, missionID, err)
	}
	return c
}

// importDoc imports content as the brief or debrief of a mission through
// ImportMarkdownStats and fails the test on an import error.
func importDoc(t *testing.T, db *sql.DB, source, kindFile, missionID, harness, content string) SyncStats {
	t.Helper()
	stats, err := ImportMarkdownStats(db, source, "missions/"+missionID+"/"+kindFile, harness, []byte(content))
	if err != nil {
		t.Fatalf("import %s: %v", source, err)
	}
	return stats
}

func TestExtractSection(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		heading   string
		want      string
		wantFound bool
	}{
		{
			name:      "simple section",
			body:      "## Acceptance Criteria\n- Criterion 1\n- Criterion 2\n",
			heading:   "Acceptance Criteria",
			want:      "- Criterion 1\n- Criterion 2",
			wantFound: true,
		},
		{
			name:      "section followed by another",
			body:      "## Acceptance Criteria\n- Criterion 1\n- Criterion 2\n\n## Another Section\nSome content here",
			heading:   "Acceptance Criteria",
			want:      "- Criterion 1\n- Criterion 2",
			wantFound: true,
		},
		{
			name:      "subsection after is included",
			body:      "## Acceptance Criteria\n- Criterion 1\n\n### Details\nMore info",
			heading:   "Acceptance Criteria",
			want:      "- Criterion 1\n\n### Details\nMore info",
			wantFound: true,
		},
		{
			name:      "same level heading stops extraction",
			body:      "## Acceptance Criteria\n- Criterion 1\n\n## Key Files\n- /path/to/file",
			heading:   "Acceptance Criteria",
			want:      "- Criterion 1",
			wantFound: true,
		},
		{
			name:      "higher level heading stops extraction",
			body:      "## Acceptance Criteria\n- Criterion 1\n\n# Top\n- other",
			heading:   "Acceptance Criteria",
			want:      "- Criterion 1",
			wantFound: true,
		},
		{
			name:      "missing section",
			body:      "## Other Section\nContent here\n",
			heading:   "Acceptance Criteria",
			want:      "",
			wantFound: false,
		},
		{
			name:      "heading with empty body is found",
			body:      "## Acceptance Criteria\n\n## Next\n- x",
			heading:   "Acceptance Criteria",
			want:      "",
			wantFound: true,
		},
		{
			name:      "case insensitive",
			body:      "## acceptance criteria\n- Criterion 1\n",
			heading:   "Acceptance Criteria",
			want:      "- Criterion 1",
			wantFound: true,
		},
		{
			name:      "prefix heading matches",
			body:      "## Acceptance Criteria Outcome (QA)\n- First criterion\n- Second criterion\n\n## Another Section",
			heading:   "Acceptance Criteria",
			want:      "- First criterion\n- Second criterion",
			wantFound: true,
		},
		{
			name:      "heading sharing no prefix does not match",
			body:      "## Acceptance Notes for later\n- not a criterion\n\n## Another Section",
			heading:   "Acceptance Criteria",
			want:      "",
			wantFound: false,
		},
		{
			name:      "heading shorter than the wanted text does not match",
			body:      "## Acceptance\n- not a criterion",
			heading:   "Acceptance Criteria",
			want:      "",
			wantFound: false,
		},
		{
			name:      "hash run without a space is not a heading",
			body:      "##Acceptance Criteria\n- not a criterion",
			heading:   "Acceptance Criteria",
			want:      "",
			wantFound: false,
		},
		{
			name:      "hash inside a code fence does not end the section",
			body:      "## Acceptance Criteria\n- Criterion with code\n```\n# This is not a heading\n```\n- Another criterion\n\n## Next Section",
			heading:   "Acceptance Criteria",
			want:      "- Criterion with code\n```\n# This is not a heading\n```\n- Another criterion",
			wantFound: true,
		},
		{
			name:      "same level heading inside a code fence does not end the section",
			body:      "## Acceptance Criteria\n- one\n```\n## Not a heading\n```\n- two",
			heading:   "Acceptance Criteria",
			want:      "- one\n```\n## Not a heading\n```\n- two",
			wantFound: true,
		},
		{
			name:      "wanted heading inside a code fence does not start the section",
			body:      "```\n## Acceptance Criteria\n- fake\n```\n## Acceptance Criteria\n- real\n",
			heading:   "Acceptance Criteria",
			want:      "- real",
			wantFound: true,
		},
		{
			name:      "wanted heading only inside a code fence is not found",
			body:      "```\n## Acceptance Criteria\n- fake\n```\n",
			heading:   "Acceptance Criteria",
			want:      "",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := extractSection(tt.body, tt.heading)
			if found != tt.wantFound {
				t.Errorf("found: got %v, want %v", found, tt.wantFound)
			}
			if got != tt.want {
				t.Errorf("section: got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStoreMissionCriteria(t *testing.T) {
	tests := []struct {
		name            string
		briefBody       string
		debriefBody     string
		wantRows        int
		wantFirstResult string // "NULL" when the first row's result is NULL
	}{
		{
			name:            "brief only",
			briefBody:       "## Acceptance Criteria\n- First criterion\n- Second criterion",
			wantRows:        2,
			wantFirstResult: "NULL",
		},
		{
			name:            "debrief only",
			debriefBody:     "## Acceptance Criteria Outcome\n- [x] First — pass\n- [ ] Second — unmet",
			wantRows:        2,
			wantFirstResult: "met",
		},
		{
			name:            "debrief overrides brief",
			briefBody:       "## Acceptance Criteria\n- First criterion\n- Second criterion",
			debriefBody:     "## Acceptance Criteria Outcome\n- [x] First — pass",
			wantRows:        1,
			wantFirstResult: "met",
		},
		{
			name:      "no criteria heading at all",
			briefBody: "## Some Section\nNo criteria here",
			wantRows:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			missionID := "mission-test-table"
			seedMission(t, db, missionID)

			if tt.briefBody != "" {
				importDoc(t, db, "/test/brief.md", "BRIEF.md", missionID, "test-harness", tt.briefBody)
			}
			if tt.debriefBody != "" {
				importDoc(t, db, "/test/debrief.md", "DEBRIEF.md", missionID, "test-harness", tt.debriefBody)
			}

			if got := criteriaCount(t, db, missionID); got != tt.wantRows {
				t.Errorf("rows: got %d, want %d", got, tt.wantRows)
			}
			if tt.wantRows > 0 {
				if got := resultAt(t, db, missionID, 1); got != tt.wantFirstResult {
					t.Errorf("first result: got %q, want %q", got, tt.wantFirstResult)
				}
			}
		})
	}
}

// TestStoreMissionCriteriaEmptyMissionID calls storeMissionCriteria directly:
// an empty id returns zero statistics, while an unknown non-empty id through
// the same call is counted as skipped, so the empty-id result is not just the
// absence of any counting.
func TestStoreMissionCriteriaEmptyMissionID(t *testing.T) {
	db := openTestDB(t)

	if got := storeMissionCriteria(db, "", "h", "brief"); got != (SyncStats{}) {
		t.Errorf("empty mission id: got %+v, want zero stats", got)
	}
	if got := storeMissionCriteria(db, "mission-unknown", "h", "brief"); got.CriteriaSkipped != 1 {
		t.Errorf("unknown mission id (control): CriteriaSkipped got %d, want 1", got.CriteriaSkipped)
	}
}

// TestStoreMissionCriteriaBriefAfterDebrief imports the debrief and then the
// brief through ImportMarkdownStats and checks the debrief's graded rows stay.
func TestStoreMissionCriteriaBriefAfterDebrief(t *testing.T) {
	db := openTestDB(t)
	missionID := "mission-test-brief-after-debrief"
	seedMission(t, db, missionID)

	importDoc(t, db, "/test/debrief.md", "DEBRIEF.md", missionID, "test-harness",
		"## Acceptance Criteria Outcome\n- [x] First — pass. Evidence here.\n- [ ] Second — unmet")
	if got := criteriaCount(t, db, missionID); got != 2 {
		t.Fatalf("after debrief: got %d rows, want 2", got)
	}

	importDoc(t, db, "/test/brief.md", "BRIEF.md", missionID, "test-harness",
		"## Acceptance Criteria\n- First criterion\n- Second criterion\n- Third criterion")

	if got := criteriaCount(t, db, missionID); got != 2 {
		t.Errorf("after brief: got %d rows, want 2 (debrief rows persist)", got)
	}
	if got := resultAt(t, db, missionID, 1); got != "met" {
		t.Errorf("after brief: result at ord 1 got %q, want %q", got, "met")
	}
}

// TestStoreMissionCriteriaUnchangedHashBackfill re-imports a file whose content
// hash is unchanged and checks mission_criteria is still filled.
func TestStoreMissionCriteriaUnchangedHashBackfill(t *testing.T) {
	db := openTestDB(t)
	missionID := "mission-test-unchanged-hash"
	seedMission(t, db, missionID)

	brief := "## Acceptance Criteria\n- Criterion 1\n- Criterion 2"
	importDoc(t, db, "/test/brief.md", "BRIEF.md", missionID, "test-harness", brief)

	if _, err := db.Exec(`DELETE FROM mission_criteria WHERE mission_id = ?`, missionID); err != nil {
		t.Fatalf("delete criteria: %v", err)
	}
	if got := criteriaCount(t, db, missionID); got != 0 {
		t.Fatalf("after delete: got %d rows, want 0", got)
	}

	importDoc(t, db, "/test/brief.md", "BRIEF.md", missionID, "test-harness", brief)
	if got := criteriaCount(t, db, missionID); got != 2 {
		t.Errorf("after unchanged re-import: got %d rows, want 2", got)
	}
}

// TestStoreMissionCriteriaUnreadableDebriefKeepsGradedRows seeds graded rows
// from a good debrief, then re-imports the same debrief with only prose under
// its heading while the brief still has criteria: the graded rows must remain.
func TestStoreMissionCriteriaUnreadableDebriefKeepsGradedRows(t *testing.T) {
	db := openTestDB(t)
	missionID := "mission-test-unreadable-graded"
	seedMission(t, db, missionID)

	importDoc(t, db, "/test/brief.md", "BRIEF.md", missionID, "test-harness",
		"## Acceptance Criteria\n- First criterion\n- Second criterion\n- Third criterion")
	importDoc(t, db, "/test/debrief.md", "DEBRIEF.md", missionID, "test-harness",
		"## Acceptance Criteria Outcome\n- First — pass. ok\n- Second — unmet")

	// Control: the graded rows are in place before the unreadable import.
	if got := criteriaCount(t, db, missionID); got != 2 {
		t.Fatalf("seed: got %d rows, want 2", got)
	}
	if got := resultAt(t, db, missionID, 1); got != "met" {
		t.Fatalf("seed: result at ord 1 got %q, want %q", got, "met")
	}

	stats := importDoc(t, db, "/test/debrief.md", "DEBRIEF.md", missionID, "test-harness",
		"## Acceptance Criteria Outcome\nJust prose, no bullet criteria here.\n\n## Next Section")

	if stats.CriteriaSkipped != 1 {
		t.Errorf("unreadable debrief: CriteriaSkipped got %d, want 1", stats.CriteriaSkipped)
	}
	if got := criteriaCount(t, db, missionID); got != 2 {
		t.Errorf("after unreadable debrief: got %d rows, want 2 (graded rows kept, not the brief's 3)", got)
	}
	if got := resultAt(t, db, missionID, 1); got != "met" {
		t.Errorf("after unreadable debrief: result at ord 1 got %q, want %q (graded row replaced)", got, "met")
	}
	if got := resultAt(t, db, missionID, 2); got != "unmet" {
		t.Errorf("after unreadable debrief: result at ord 2 got %q, want %q", got, "unmet")
	}

	// A brief import while the debrief is unreadable must not replace them either.
	importDoc(t, db, "/test/brief.md", "BRIEF.md", missionID, "test-harness",
		"## Acceptance Criteria\n- A\n- B\n- C\n- D")
	if got := criteriaCount(t, db, missionID); got != 2 {
		t.Errorf("brief import after unreadable debrief: got %d rows, want 2", got)
	}
	if got := resultAt(t, db, missionID, 1); got != "met" {
		t.Errorf("brief import after unreadable debrief: result at ord 1 got %q, want %q", got, "met")
	}
}

// TestStoreMissionCriteriaUnreadableDebriefWithoutGradedRowsFollowsBrief is the
// mirror of the keep-graded-rows test: with an unreadable debrief and no graded
// rows, a changed brief still replaces the ungraded rows.
func TestStoreMissionCriteriaUnreadableDebriefWithoutGradedRowsFollowsBrief(t *testing.T) {
	db := openTestDB(t)
	missionID := "mission-test-unreadable-ungraded"
	seedMission(t, db, missionID)

	importDoc(t, db, "/test/brief.md", "BRIEF.md", missionID, "test-harness",
		"## Acceptance Criteria\n- First\n- Second")
	importDoc(t, db, "/test/debrief.md", "DEBRIEF.md", missionID, "test-harness",
		"## Acceptance Criteria Outcome\nprose only")
	if got := criteriaCount(t, db, missionID); got != 2 {
		t.Fatalf("before brief change: got %d rows, want 2", got)
	}

	importDoc(t, db, "/test/brief.md", "BRIEF.md", missionID, "test-harness",
		"## Acceptance Criteria\n- First\n- Second\n- Third")
	if got := criteriaCount(t, db, missionID); got != 3 {
		t.Errorf("after brief change: got %d rows, want 3", got)
	}
	if got := resultAt(t, db, missionID, 3); got != "NULL" {
		t.Errorf("after brief change: result at ord 3 got %q, want NULL", got)
	}
}

// TestStoreMissionCriteriaProseOnlySectionSkipped checks that a brief or
// debrief heading with prose or nothing under it is counted as skipped (a
// brief with bullets through the same path is the control, and a brief with no
// heading is not counted). It stores no rows for a mission that had none; that
// rows already stored survive is TestStoreMissionCriteriaProseOnlyBriefKeepsRows.
func TestStoreMissionCriteriaProseOnlySectionSkipped(t *testing.T) {
	db := openTestDB(t)
	proseID, goodID := "mission-test-prose", "mission-test-good"
	seedMission(t, db, proseID)
	seedMission(t, db, goodID)

	good := importDoc(t, db, "/test/good.md", "BRIEF.md", goodID, "test-harness",
		"## Acceptance Criteria\n- Criterion 1")
	if good.CriteriaSkipped != 0 {
		t.Errorf("brief with bullets (control): CriteriaSkipped got %d, want 0", good.CriteriaSkipped)
	}
	if got := criteriaCount(t, db, goodID); got != 1 {
		t.Errorf("brief with bullets (control): got %d rows, want 1", got)
	}

	prose := importDoc(t, db, "/test/prose.md", "BRIEF.md", proseID, "test-harness",
		"## Acceptance Criteria\nJust prose, no bullets.")
	if prose.CriteriaSkipped != 1 {
		t.Errorf("prose-only brief section: CriteriaSkipped got %d, want 1", prose.CriteriaSkipped)
	}
	if got := criteriaCount(t, db, proseID); got != 0 {
		t.Errorf("prose-only brief section: got %d rows, want 0", got)
	}

	empty := importDoc(t, db, "/test/empty.md", "DEBRIEF.md", proseID, "test-harness",
		"## Acceptance Criteria Outcome\n\n## Next")
	if empty.CriteriaSkipped != 1 {
		t.Errorf("empty debrief section: CriteriaSkipped got %d, want 1", empty.CriteriaSkipped)
	}

	// A missing heading is not unreadable: nothing is counted.
	absent := importDoc(t, db, "/test/absent.md", "BRIEF.md", goodID, "other-harness", "# Brief\n\nNo criteria heading.")
	if absent.CriteriaSkipped != 0 {
		t.Errorf("brief without the heading: CriteriaSkipped got %d, want 0", absent.CriteriaSkipped)
	}
}

// TestStoreMissionCriteriaMissingMissionSkippedThenFilled imports a brief
// before its missions row exists: it is counted as skipped and stores nothing;
// re-importing the same file once the row exists fills the rows.
func TestStoreMissionCriteriaMissingMissionSkippedThenFilled(t *testing.T) {
	db := openTestDB(t)
	missionID := "mission-test-missing-then-fill"
	brief := "## Acceptance Criteria\n- Criterion 1\n- Criterion 2"

	first := importDoc(t, db, "/test/brief.md", "BRIEF.md", missionID, "test-harness", brief)
	if first.CriteriaSkipped != 1 {
		t.Errorf("no mission row: CriteriaSkipped got %d, want 1", first.CriteriaSkipped)
	}
	if got := criteriaCount(t, db, missionID); got != 0 {
		t.Fatalf("no mission row: got %d rows, want 0", got)
	}

	seedMission(t, db, missionID)
	second := importDoc(t, db, "/test/brief.md", "BRIEF.md", missionID, "test-harness", brief)
	if second.CriteriaSkipped != 0 {
		t.Errorf("mission row present: CriteriaSkipped got %d, want 0", second.CriteriaSkipped)
	}
	if got := criteriaCount(t, db, missionID); got != 2 {
		t.Errorf("mission row present: got %d rows, want 2", got)
	}
}

// TestStoreMissionCriteriaDBFailureCountedNotFatal drops mission_criteria so
// every refresh fails, then imports a brief: the import returns no error, the
// document is stored and CriteriaErrors is counted. A fresh database through
// the same import is the control: no errors and the rows are written.
func TestStoreMissionCriteriaDBFailureCountedNotFatal(t *testing.T) {
	brief := "## Acceptance Criteria\n- Criterion 1\n- Criterion 2"
	missionID := "mission-test-db-failure"

	control := openTestDB(t)
	seedMission(t, control, missionID)
	ok := importDoc(t, control, "/test/brief.md", "BRIEF.md", missionID, "test-harness", brief)
	if ok.CriteriaErrors != 0 || criteriaCount(t, control, missionID) != 2 {
		t.Fatalf("control: errors %d, rows %d; want 0 errors and 2 rows", ok.CriteriaErrors, criteriaCount(t, control, missionID))
	}

	db := openTestDB(t)
	seedMission(t, db, missionID)
	if _, err := db.Exec(`DROP TABLE mission_criteria`); err != nil {
		t.Fatalf("drop mission_criteria: %v", err)
	}
	stats, err := ImportMarkdownStats(db, "/test/brief.md", "missions/"+missionID+"/BRIEF.md", "test-harness", []byte(brief))
	if err != nil {
		t.Fatalf("import must not fail when criteria storage fails: %v", err)
	}
	if stats.CriteriaErrors != 1 {
		t.Errorf("CriteriaErrors got %d, want 1", stats.CriteriaErrors)
	}
	if stats.CriteriaSkipped != 0 {
		t.Errorf("CriteriaSkipped got %d, want 0", stats.CriteriaSkipped)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM documents WHERE mission_id = ? AND kind = 'brief'`, missionID); got != 1 {
		t.Errorf("brief document rows got %d, want 1 (import aborted)", got)
	}
}

// TestSyncHarnessStatsCountsSkipped walks a memory tree through
// SyncHarnessStats: a prose-only brief, an empty debrief section and a brief
// whose mission row is missing are each counted once, while a brief with
// bullets is not and has its rows stored.
func TestSyncHarnessStatsCountsSkipped(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	write := func(mission, file, content string) {
		t.Helper()
		dir := filepath.Join(root, "missions", mission)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write("m-prose", "BRIEF.md", "## Acceptance Criteria\nprose only\n")
	write("m-empty", "DEBRIEF.md", "## Acceptance Criteria Outcome\n\n## Next\n")
	write("m-good", "BRIEF.md", "## Acceptance Criteria\n- one\n- two\n")
	write("m-nomission", "BRIEF.md", "## Acceptance Criteria\n- one\n")
	for _, id := range []string{"m-prose", "m-empty", "m-good"} {
		seedMission(t, db, id)
	}

	for run := 1; run <= 2; run++ {
		stats, err := SyncHarnessStats(db, root, "kirsch")
		if err != nil {
			t.Fatalf("run %d: SyncHarnessStats: %v", run, err)
		}
		if stats.CriteriaSkipped != 3 {
			t.Errorf("run %d: CriteriaSkipped got %d, want 3 (prose, empty, no mission row)", run, stats.CriteriaSkipped)
		}
		if stats.CriteriaErrors != 0 {
			t.Errorf("run %d: CriteriaErrors got %d, want 0", run, stats.CriteriaErrors)
		}
	}
	if got := criteriaCount(t, db, "m-good"); got != 2 {
		t.Errorf("m-good rows got %d, want 2", got)
	}

	// The error-free wrappers return only the error and still import.
	if err := SyncHarness(db, root, "kirsch"); err != nil {
		t.Errorf("SyncHarness: %v", err)
	}
}

// TestStoreMissionCriteriaPrefersImportingHarness gives one mission a brief
// from each of two harnesses, the importing one sorting later by source_path.
// The importing harness's brief must be the one stored, whichever harness it is.
func TestStoreMissionCriteriaPrefersImportingHarness(t *testing.T) {
	db := openTestDB(t)
	missionID := "mission-test-two-harness"
	seedMission(t, db, missionID)

	alpha := "## Acceptance Criteria\n- alpha one"
	zeta := "## Acceptance Criteria\n- zeta one\n- zeta two"

	importDoc(t, db, "/a/BRIEF.md", "BRIEF.md", missionID, "alpha", alpha)
	if got := criterionAt(t, db, missionID, 1); got != "alpha one" {
		t.Fatalf("alpha only: criterion 1 got %q, want %q", got, "alpha one")
	}

	// zeta's source_path sorts after alpha's; plain source_path order would keep alpha.
	importDoc(t, db, "/z/BRIEF.md", "BRIEF.md", missionID, "zeta", zeta)
	if got := criteriaCount(t, db, missionID); got != 2 {
		t.Errorf("zeta importing: got %d rows, want 2 (zeta's brief preferred)", got)
	}
	if got := criterionAt(t, db, missionID, 1); got != "zeta one" {
		t.Errorf("zeta importing: criterion 1 got %q, want %q", got, "zeta one")
	}

	// Mirror: alpha importing again must switch back to its own brief.
	importDoc(t, db, "/a/BRIEF.md", "BRIEF.md", missionID, "alpha", alpha)
	if got := criteriaCount(t, db, missionID); got != 1 {
		t.Errorf("alpha importing: got %d rows, want 1 (alpha's brief preferred)", got)
	}
	if got := criterionAt(t, db, missionID, 1); got != "alpha one" {
		t.Errorf("alpha importing: criterion 1 got %q, want %q", got, "alpha one")
	}
}

// TestStoreMissionCriteriaFallsBackToSourcePath checks that when the importing
// harness holds no brief for the mission, the lowest source_path is chosen.
func TestStoreMissionCriteriaFallsBackToSourcePath(t *testing.T) {
	db := openTestDB(t)
	missionID := "mission-test-fallback"
	seedMission(t, db, missionID)

	importDoc(t, db, "/a/BRIEF.md", "BRIEF.md", missionID, "alpha", "## Acceptance Criteria\n- alpha one")
	importDoc(t, db, "/z/BRIEF.md", "BRIEF.md", missionID, "zeta", "## Acceptance Criteria\n- zeta one")
	if got := criterionAt(t, db, missionID, 1); got != "zeta one" {
		t.Fatalf("before fallback: criterion 1 got %q, want %q", got, "zeta one")
	}
	// A debrief import by a third harness, which holds no brief, re-reads the
	// brief with the lowest source_path.
	importDoc(t, db, "/m/DEBRIEF.md", "DEBRIEF.md", missionID, "mid", "## Acceptance Criteria Outcome\nprose")
	if got := criterionAt(t, db, missionID, 1); got != "alpha one" {
		t.Errorf("fallback: criterion 1 got %q, want %q", got, "alpha one")
	}
}

// TestCleanCriterionTextBriefPreservation checks that cleanCriterionText strips
// only the bullet marker and checkbox and keeps the rest of the line.
func TestCleanCriterionTextBriefPreservation(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "multi-pass criterion", in: "- multi-pass verification", want: "multi-pass verification"},
		{name: "dash with when clause", in: "- read-only? - done when approved", want: "read-only? - done when approved"},
		{name: "checkbox with dash text", in: "- [x] criterion - pass the test", want: "criterion - pass the test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanCriterionText(tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// seedCriteriaRow inserts one mission_criteria row directly, bypassing the
// importer; result "" stores NULL.
func seedCriteriaRow(t *testing.T, db *sql.DB, missionID string, ord int, criterion, result string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO mission_criteria (mission_id, ord, criterion, result) VALUES (?, ?, ?, ?)`,
		missionID, ord, criterion, nullIfEmpty(result),
	); err != nil {
		t.Fatalf("seed criterion %d of %s: %v", ord, missionID, err)
	}
}

// TestStoreMissionCriteriaProseOnlyBriefKeepsRows imports a brief with two
// criteria and then the same file path with a prose-only criteria section
// (ungraded rows, no debrief): the two rows survive and the re-import is
// counted as skipped. A brief that has no criteria heading at all, for another
// mission through the same path, deletes its rows: the control showing the
// keep is not just a refresh that never writes.
func TestStoreMissionCriteriaProseOnlyBriefKeepsRows(t *testing.T) {
	db := openTestDB(t)
	keepID, dropID := "mission-test-keep-prose", "mission-test-drop-absent"
	seedMission(t, db, keepID)
	seedMission(t, db, dropID)

	good := "## Acceptance Criteria\n- First\n- Second"
	importDoc(t, db, "/test/keep.md", "BRIEF.md", keepID, "h", good)
	importDoc(t, db, "/test/drop.md", "BRIEF.md", dropID, "h", good)

	prose := importDoc(t, db, "/test/keep.md", "BRIEF.md", keepID, "h", "## Acceptance Criteria\nProse only, no bullets.")
	if prose.CriteriaSkipped != 1 {
		t.Errorf("prose-only re-import: CriteriaSkipped got %d, want 1", prose.CriteriaSkipped)
	}
	if got := criteriaCount(t, db, keepID); got != 2 {
		t.Errorf("prose-only re-import: got %d rows, want 2 kept", got)
	}
	if got := criterionAt(t, db, keepID, 2); got != "Second" {
		t.Errorf("prose-only re-import: criterion 2 got %q, want %q", got, "Second")
	}

	importDoc(t, db, "/test/drop.md", "BRIEF.md", dropID, "h", "# Brief\n\nNo criteria heading.")
	if got := criteriaCount(t, db, dropID); got != 0 {
		t.Errorf("brief without the heading (control): got %d rows, want 0 deleted", got)
	}
}

// breakDocumentKind replaces the documents table with a view over the original
// that raises an error when the body of a document of failKind is read, and
// behaves normally for every other kind.
func breakDocumentKind(t *testing.T, db *sql.DB, failKind string) {
	t.Helper()
	if _, err := db.Exec(`ALTER TABLE documents RENAME TO documents_moved`); err != nil {
		t.Fatalf("rename documents: %v", err)
	}
	if _, err := db.Exec(`CREATE VIEW documents AS SELECT mission_id, kind, harness, source_path,
		CASE WHEN kind = '` + failKind + `' THEN abs(-9223372036854775808) ELSE body END AS body
		FROM documents_moved`); err != nil {
		t.Fatalf("create documents view: %v", err)
	}
}

// TestStoreMissionCriteriaReadFailureKeepsRows calls storeMissionCriteria
// directly, once per document whose read fails (the brief, then the debrief;
// the other is readable). Per mission, rows are seeded and the stored documents
// have no criteria heading, so a read error taken as an absent document would
// delete the rows. The control mission is refreshed before the documents table
// is broken: its absent headings delete its rows with no error. After the
// break, the rows survive and CriteriaErrors is 1.
func TestStoreMissionCriteriaReadFailureKeepsRows(t *testing.T) {
	for _, failKind := range []string{"brief", "debrief"} {
		t.Run(failKind+" read fails", func(t *testing.T) {
			db := openTestDB(t) // a fresh temp file per subtest: the schema break below is discarded with it
			controlID, failID := "mission-test-read-control", "mission-test-read-fail"
			for _, id := range []string{controlID, failID} {
				seedMission(t, db, id)
				importDoc(t, db, "/test/"+id+"-b.md", "BRIEF.md", id, "h", "# Brief\n\nNo criteria heading.")
				importDoc(t, db, "/test/"+id+"-d.md", "DEBRIEF.md", id, "h", "# Debrief\n\nNo criteria heading.")
				seedCriteriaRow(t, db, id, 1, "First", "")
				seedCriteriaRow(t, db, id, 2, "Second", "met")
			}

			if st := storeMissionCriteria(db, controlID, "h", "brief"); st.CriteriaErrors != 0 {
				t.Fatalf("control: CriteriaErrors got %d, want 0", st.CriteriaErrors)
			}
			if got := criteriaCount(t, db, controlID); got != 0 {
				t.Fatalf("control: got %d rows, want 0 (absent headings delete)", got)
			}

			breakDocumentKind(t, db, failKind)
			st := storeMissionCriteria(db, failID, "h", "brief")
			if st.CriteriaErrors != 1 {
				t.Errorf("%s read failure: CriteriaErrors got %d, want 1", failKind, st.CriteriaErrors)
			}
			if got := criteriaCount(t, db, failID); got != 2 {
				t.Errorf("%s read failure: got %d rows, want 2 kept", failKind, got)
			}

			// Positive control: only failKind is broken. The other kind's body
			// is still readable through the view, so the error above came from
			// the guard for failKind alone.
			other := map[string]string{"brief": "debrief", "debrief": "brief"}[failKind]
			if body, ok, err := documentBody(db, failID, other, "h"); err != nil || !ok || body == "" {
				t.Errorf("control: %s through the broken view: body %q, ok %v, err %v; want readable", other, body, ok, err)
			}
			if _, _, err := documentBody(db, failID, failKind, "h"); err == nil {
				t.Errorf("control: %s through the broken view: want a read error", failKind)
			}
		})
	}
}

// TestStoreMissionCriteriaExistsCheckFailureKeepsRows renames `missions` after
// seeding rows and a brief document, then changes the stored brief so that a
// refresh that went on would replace the two rows with one. The refresh must
// stop: rows unchanged, CriteriaErrors 1. A control refresh on the same
// document before the rename replaces the rows with one, showing the change
// would otherwise take effect.
func TestStoreMissionCriteriaExistsCheckFailureKeepsRows(t *testing.T) {
	db := openTestDB(t)
	id := "mission-test-exists-fail"
	seedMission(t, db, id)
	importDoc(t, db, "/test/brief.md", "BRIEF.md", id, "h", "## Acceptance Criteria\n- First\n- Second")
	if _, err := db.Exec(
		`UPDATE documents SET body = ? WHERE mission_id = ?`, "## Acceptance Criteria\n- Only", id,
	); err != nil {
		t.Fatalf("change stored brief: %v", err)
	}

	if _, err := db.Exec(`ALTER TABLE missions RENAME TO missions_moved`); err != nil {
		t.Fatalf("rename missions: %v", err)
	}
	st := storeMissionCriteria(db, id, "h", "brief")
	if st.CriteriaErrors != 1 {
		t.Errorf("exists-check failure: CriteriaErrors got %d, want 1", st.CriteriaErrors)
	}
	if got := criteriaCount(t, db, id); got != 2 {
		t.Errorf("exists-check failure: got %d rows, want 2 kept", got)
	}

	if _, err := db.Exec(`ALTER TABLE missions_moved RENAME TO missions`); err != nil {
		t.Fatalf("restore missions: %v", err)
	}
	if st := storeMissionCriteria(db, id, "h", "brief"); st.CriteriaErrors != 0 {
		t.Fatalf("control: CriteriaErrors got %d, want 0", st.CriteriaErrors)
	}
	if got := criteriaCount(t, db, id); got != 1 {
		t.Errorf("control: got %d rows, want 1 after the refresh ran", got)
	}
}

// TestStoreMissionCriteriaGradedCountFailureKeepsRows renames the `result`
// column so the graded-row count fails while COUNT(*) still works. The debrief
// is unreadable and the brief has no criteria section, so a count error read
// as "no graded rows" would return silently with CriteriaErrors 0; the call
// must count the error and leave the rows. A control run before the rename
// returns no error.
func TestStoreMissionCriteriaGradedCountFailureKeepsRows(t *testing.T) {
	db := openTestDB(t)
	id := "mission-test-graded-fail"
	seedMission(t, db, id)
	importDoc(t, db, "/test/debrief.md", "DEBRIEF.md", id, "h", "## Acceptance Criteria Outcome\nprose only")
	seedCriteriaRow(t, db, id, 1, "First", "met")
	seedCriteriaRow(t, db, id, 2, "Second", "unmet")

	if st := storeMissionCriteria(db, id, "h", "debrief"); st.CriteriaErrors != 0 || st.CriteriaSkipped != 1 {
		t.Fatalf("control: got %+v, want 0 errors and 1 skipped", st)
	}

	if _, err := db.Exec(`ALTER TABLE mission_criteria RENAME COLUMN result TO result_moved`); err != nil {
		t.Fatalf("rename result: %v", err)
	}
	st := storeMissionCriteria(db, id, "h", "debrief")
	if st.CriteriaErrors != 1 {
		t.Errorf("graded-count failure: CriteriaErrors got %d, want 1", st.CriteriaErrors)
	}
	if got := criteriaCount(t, db, id); got != 2 {
		t.Errorf("graded-count failure: got %d rows, want 2 kept", got)
	}
}

// TestStoreMissionCriteriaTrimsAndSkipsEmpty imports a CRLF debrief whose first
// bullet is a bare checkbox: the bare bullet is not stored, and neither the
// stored criterion nor the evidence of a bullet that is not the last line ends
// in a carriage return (the last line's is removed earlier, by the section trim). A brief whose
// only bullet is a bare checkbox counts as unreadable and keeps existing rows.
func TestStoreMissionCriteriaTrimsAndSkipsEmpty(t *testing.T) {
	db := openTestDB(t)
	id, keepID := "mission-test-crlf", "mission-test-empty-only"
	seedMission(t, db, id)
	seedMission(t, db, keepID)

	importDoc(t, db, "/test/debrief.md", "DEBRIEF.md", id, "h",
		"## Acceptance Criteria Outcome\r\n- [x]\r\n- [x] Real - pass. Evidence text\r\n- [ ] Last - unmet\r\n")
	if got := criteriaCount(t, db, id); got != 2 {
		t.Fatalf("got %d rows, want 2 (bare bullet skipped)", got)
	}
	var crit string
	var ev sql.NullString
	if err := db.QueryRow(`SELECT criterion, evidence FROM mission_criteria WHERE mission_id = ? AND ord = 1`, id).Scan(&crit, &ev); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if strings.HasSuffix(crit, "\r") || strings.HasSuffix(ev.String, "\r") {
		t.Errorf("carriage return stored: criterion %q, evidence %q", crit, ev.String)
	}
	if ev.String != "Evidence text" {
		t.Errorf("evidence got %q, want %q", ev.String, "Evidence text")
	}

	importDoc(t, db, "/test/keep.md", "BRIEF.md", keepID, "h", "## Acceptance Criteria\n- First")
	st := importDoc(t, db, "/test/keep.md", "BRIEF.md", keepID, "h", "## Acceptance Criteria\n- [x]")
	if st.CriteriaSkipped != 1 || criteriaCount(t, db, keepID) != 1 {
		t.Errorf("empty-only brief: skipped %d rows %d, want 1 and 1", st.CriteriaSkipped, criteriaCount(t, db, keepID))
	}
}

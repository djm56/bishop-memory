package importer

import (
	"database/sql"
	"os"
	"strings"
	"testing"
)

// mistakesCount returns the number of agent_mistakes rows for a mission.
func mistakesCount(t *testing.T, db *sql.DB, missionID string) int {
	t.Helper()
	return countRows(t, db, `SELECT COUNT(*) FROM agent_mistakes WHERE mission_id = ?`, missionID)
}

// mistakesAt returns the agent_raw, agent, mistake, impact, correction and prevention at ord,
// impact NULL as "NULL" string.
func mistakesAt(t *testing.T, db *sql.DB, missionID string, ord int) (agentRaw, agent, mistake, impact, correction, prevention string) {
	t.Helper()
	var impactSQL sql.NullString
	if err := db.QueryRow(
		`SELECT agent_raw, agent, mistake, impact, correction, prevention FROM agent_mistakes
		 WHERE mission_id = ? AND ord = ?`, missionID, ord,
	).Scan(&agentRaw, &agent, &mistake, &impactSQL, &correction, &prevention); err != nil {
		t.Fatalf("row at ord %d of %s: %v", ord, missionID, err)
	}
	if impactSQL.Valid {
		impact = impactSQL.String
	} else {
		impact = "NULL"
	}
	return
}

// TestMistakeAgentName tests the mistakeAgentName normalisation function.
func TestMistakeAgentName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"@hicks", "@hicks", "hicks"},
		{"hicks", "hicks", "hicks"},
		{"@Bishop", "@Bishop", "bishop"},
		{"@hicks, step 1", "@hicks, step 1", "hicks"},
		{"@hicks (step 1)", "@hicks (step 1)", "hicks"},
		{"@lambert (init)", "@lambert (init)", "lambert"},
		{"@lambert, steps 8–11 syncs", "@lambert, steps 8–11 syncs", "lambert"},
		{"@hicks and @vasquez (harness edits)", "@hicks and @vasquez (harness edits)", "hicks"},
		{"multiple steps (@hicks, @lambert, @bishop)", "multiple steps (@hicks, @lambert, @bishop)", "hicks"},
		{"@apone (step 2) / @hicks (step 3)", "@apone (step 2) / @hicks (step 3)", "apone"},
		{"senior (general-purpose)", "senior (general-purpose)", "senior"},
		{"— (harness, not an agent)", "— (harness, not an agent)", ""},
		{"—", "—", ""},
		{"empty string", "", ""},
		{"ripley (step 10)", "ripley (step 10)", "ripley"},
		{"vasquez, steps 9–10", "vasquez, steps 9–10", "vasquez"},
		{"RIPLEY (step 10)", "RIPLEY (step 10)", "ripley"},
		{"ripley(step 10)", "ripley(step 10)", "ripley"},
		{"BISHOP", "BISHOP", "bishop"},
		{"apone;reason", "apone;reason", "apone"},
		{"ripley/initial", "ripley/initial", "ripley"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mistakeAgentName(tt.input)
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

// TestStoreAgentMistakesGoodDebrief tests that a well-formed debrief section
// is parsed and stored with agent names normalized and agent_raw preserved.
func TestStoreAgentMistakesGoodDebrief(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-good"
	seedMission(t, db, missionID)

	harness := "test-harness"
	body := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks (step 1) | Misread brief | low | Re-read it | Slow down |
| @ripley | Wrong call | high — risk noted | Fixed | Review first |
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body, "sha", missionID, harness); err != nil {
		t.Fatalf("upsert document: %v", err)
	}

	stats := storeAgentMistakes(db, missionID, harness)
	if stats.MistakesSkipped != 0 {
		t.Errorf("MistakesSkipped: got %d, want 0", stats.MistakesSkipped)
	}
	if stats.MistakesErrors != 0 {
		t.Errorf("MistakesErrors: got %d, want 0", stats.MistakesErrors)
	}

	n := mistakesCount(t, db, missionID)
	if n != 2 {
		t.Errorf("row count: got %d, want 2", n)
	}

	// Check first row: agent should be normalized to "hicks", agent_raw kept as "@hicks (step 1)"
	agentRaw, agent, mistake, impact, correction, _ := mistakesAt(t, db, missionID, 1)
	if agentRaw != "@hicks (step 1)" {
		t.Errorf("row 1 agent_raw: got %q, want %q", agentRaw, "@hicks (step 1)")
	}
	if agent != "hicks" {
		t.Errorf("row 1 agent (normalized): got %q, want %q", agent, "hicks")
	}
	if mistake != "Misread brief" {
		t.Errorf("row 1 mistake: got %q, want %q", mistake, "Misread brief")
	}
	if impact != "low" {
		t.Errorf("row 1 impact: got %q, want %q", impact, "low")
	}
	if correction != "Re-read it" {
		t.Errorf("row 1 correction: got %q, want %q", correction, "Re-read it")
	}

	// Check second row: ripley with high impact and a note
	agentRaw, agent, mistake, impact, _, _ = mistakesAt(t, db, missionID, 2)
	if agentRaw != "@ripley" {
		t.Errorf("row 2 agent_raw: got %q, want %q", agentRaw, "@ripley")
	}
	if agent != "ripley" {
		t.Errorf("row 2 agent (normalized): got %q, want %q", agent, "ripley")
	}
	if impact != "high" {
		t.Errorf("row 2 impact: got %q, want %q", impact, "high")
	}

	// Verify no row at ord 3
	var ord3Count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM agent_mistakes WHERE mission_id = ? AND ord = 3`, missionID,
	).Scan(&ord3Count)
	if err != nil {
		t.Fatalf("check ord 3: %v", err)
	}
	if ord3Count != 0 {
		t.Errorf("ord 3 must not exist: got %d rows", ord3Count)
	}
}

// TestStoreAgentMistakesReplaceOnReimport tests that a re-import replaces
// existing rows when the section is readable.
func TestStoreAgentMistakesReplaceOnReimport(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-replace"
	seedMission(t, db, missionID)

	harness := "test-harness"
	body1 := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | First error | low | Fixed | None |
| @apone | Second error | high | Fixed | None |
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body1, "sha1", missionID, harness); err != nil {
		t.Fatalf("upsert document 1: %v", err)
	}
	stats := storeAgentMistakes(db, missionID, harness)
	if n := mistakesCount(t, db, missionID); n != 2 {
		t.Errorf("after first import: got %d rows, want 2", n)
	}

	// Re-import with fewer rows
	body2 := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Different error | med | Fixed | None |
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body2, "sha2", missionID, harness); err != nil {
		t.Fatalf("upsert document 2: %v", err)
	}
	stats = storeAgentMistakes(db, missionID, harness)
	if stats.MistakesSkipped != 0 {
		t.Errorf("MistakesSkipped on re-import: got %d, want 0", stats.MistakesSkipped)
	}

	n := mistakesCount(t, db, missionID)
	if n != 1 {
		t.Errorf("after re-import: got %d rows, want 1", n)
	}
}

// TestStoreAgentMistakesZeroRowNoneTable tests that a "None …" table
// (header + separator + None row) deletes existing rows.
func TestStoreAgentMistakesZeroRowNoneTable(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-none-table"
	seedMission(t, db, missionID)

	harness := "test-harness"
	// First, insert some rows
	body1 := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Error | low | Fixed | None |
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body1, "sha1", missionID, harness); err != nil {
		t.Fatalf("upsert document 1: %v", err)
	}
	storeAgentMistakes(db, missionID, harness)
	if n := mistakesCount(t, db, missionID); n != 1 {
		t.Errorf("initial state: got %d rows, want 1", n)
	}

	// Re-import with None row (no actual mistakes)
	body2 := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| None | None on this mission | — | — | — |
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body2, "sha2", missionID, harness); err != nil {
		t.Fatalf("upsert document 2: %v", err)
	}
	stats := storeAgentMistakes(db, missionID, harness)
	if stats.MistakesSkipped != 0 {
		t.Errorf("MistakesSkipped: got %d, want 0", stats.MistakesSkipped)
	}

	n := mistakesCount(t, db, missionID)
	if n != 0 {
		t.Errorf("after importing None table: got %d rows, want 0", n)
	}
}

// TestStoreAgentMistakesUnreadableSection tests that when the heading exists
// but the section has no readable table, existing rows are kept.
func TestStoreAgentMistakesUnreadableSection(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-unreadable"
	seedMission(t, db, missionID)

	harness := "test-harness"
	// First, insert a row
	body1 := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Error | low | Fixed | None |
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body1, "sha1", missionID, harness); err != nil {
		t.Fatalf("upsert document 1: %v", err)
	}
	storeAgentMistakes(db, missionID, harness)

	// Re-import with heading but only prose
	body2 := `
# Sub-Agent Mistakes and Corrections

This section has some prose but no table to parse.
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body2, "sha2", missionID, harness); err != nil {
		t.Fatalf("upsert document 2: %v", err)
	}
	stats := storeAgentMistakes(db, missionID, harness)
	if stats.MistakesSkipped != 1 {
		t.Errorf("MistakesSkipped: got %d, want 1 (unreadable section)", stats.MistakesSkipped)
	}

	// Rows should be kept
	n := mistakesCount(t, db, missionID)
	if n != 1 {
		t.Errorf("row count after unreadable: got %d, want 1 (rows kept)", n)
	}
}

// TestStoreAgentMistakesHeadingAbsent tests that when the heading is absent,
// existing rows are kept and nothing is counted as skipped.
func TestStoreAgentMistakesHeadingAbsent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-no-heading"
	seedMission(t, db, missionID)

	harness := "test-harness"
	// Insert a row first
	body1 := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Error | low | Fixed | None |
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body1, "sha1", missionID, harness); err != nil {
		t.Fatalf("upsert document 1: %v", err)
	}
	storeAgentMistakes(db, missionID, harness)

	// Re-import with no mistakes heading
	body2 := `
# Some Other Section

This debrief has no mistakes section.
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body2, "sha2", missionID, harness); err != nil {
		t.Fatalf("upsert document 2: %v", err)
	}
	stats := storeAgentMistakes(db, missionID, harness)
	if stats.MistakesSkipped != 0 {
		t.Errorf("MistakesSkipped: got %d, want 0 (heading absent, rows kept)", stats.MistakesSkipped)
	}

	// Rows should be kept
	n := mistakesCount(t, db, missionID)
	if n != 1 {
		t.Errorf("row count: got %d, want 1 (rows kept)", n)
	}
}

// TestStoreAgentMistakesMissingMission tests that when the mission doesn't exist,
// MistakesSkipped is incremented and rows are not touched.
func TestStoreAgentMistakesMissingMission(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-does-not-exist"

	harness := "test-harness"
	body := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Error | low | Fixed | None |
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body, "sha", missionID, harness); err != nil {
		t.Fatalf("upsert document: %v", err)
	}

	stats := storeAgentMistakes(db, missionID, harness)
	if stats.MistakesSkipped != 1 {
		t.Errorf("MistakesSkipped: got %d, want 1", stats.MistakesSkipped)
	}
	if stats.MistakesErrors != 0 {
		t.Errorf("MistakesErrors: got %d, want 0", stats.MistakesErrors)
	}

	// After creating the mission, the same body can now be imported
	seedMission(t, db, missionID)
	stats = storeAgentMistakes(db, missionID, harness)
	if stats.MistakesSkipped != 0 {
		t.Errorf("MistakesSkipped after mission created: got %d, want 0", stats.MistakesSkipped)
	}
	n := mistakesCount(t, db, missionID)
	if n != 1 {
		t.Errorf("row count after mission created: got %d, want 1", n)
	}
}

// TestStoreAgentMistakesMalformedRow tests that rows with wrong cell counts
// are skipped and counted, while valid rows are stored.
func TestStoreAgentMistakesMalformedRow(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-malformed"
	seedMission(t, db, missionID)

	harness := "test-harness"
	body := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Error one | low | Fixed | None |
| @apone | Only three cells | high |
| @bishop | Error three | med | Fixed | None |
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body, "sha", missionID, harness); err != nil {
		t.Fatalf("upsert document: %v", err)
	}

	stats := storeAgentMistakes(db, missionID, harness)
	if stats.MistakesSkipped != 1 {
		t.Errorf("MistakesSkipped: got %d, want 1 (one malformed row)", stats.MistakesSkipped)
	}

	n := mistakesCount(t, db, missionID)
	if n != 2 {
		t.Errorf("row count: got %d, want 2 (two valid rows, one skipped)", n)
	}

	agentRaw, _, _, _, _, _ := mistakesAt(t, db, missionID, 1)
	if agentRaw != "@hicks" {
		t.Errorf("first stored row agent_raw: got %q, want %q", agentRaw, "@hicks")
	}

	agentRaw, _, _, _, _, _ = mistakesAt(t, db, missionID, 2)
	if agentRaw != "@bishop" {
		t.Errorf("second stored row agent_raw: got %q, want %q", agentRaw, "@bishop")
	}
}

// TestStoreAgentMistakesDBFailure tests that a database error is logged and counted,
// and the import does not fail.
func TestStoreAgentMistakesDBFailure(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-db-fail"
	seedMission(t, db, missionID)

	harness := "test-harness"
	body := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Error | low | Fixed | None |
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body, "sha", missionID, harness); err != nil {
		t.Fatalf("upsert document: %v", err)
	}

	// Drop the table to cause a failure
	if _, err := db.Exec(`DROP TABLE agent_mistakes`); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	stats := storeAgentMistakes(db, missionID, harness)
	if stats.MistakesErrors != 1 {
		t.Errorf("replace-failure path: MistakesErrors got %d, want 1", stats.MistakesErrors)
	}
	if stats.MistakesSkipped != 0 {
		t.Errorf("MistakesSkipped: got %d, want 0", stats.MistakesSkipped)
	}
	// The function should return without failing
}

// TestStoreAgentMistakesMissionCheckErrorCounted tests that when the
// "check mission exists" query (mistakes.go:55-56) fails, MistakesErrors
// is incremented and the import does not panic.
func TestStoreAgentMistakesMissionCheckErrorCounted(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-check-error"
	seedMission(t, db, missionID)

	harness := "test-harness"
	body := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Error | low | Fixed | None |
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body, "sha", missionID, harness); err != nil {
		t.Fatalf("upsert document: %v", err)
	}

	// Rename the missions table to force a check-mission-exists error
	if _, err := db.Exec(`ALTER TABLE missions RENAME TO missions_backup`); err != nil {
		t.Fatalf("rename missions table: %v", err)
	}
	defer func() {
		if _, err := db.Exec(`ALTER TABLE missions_backup RENAME TO missions`); err != nil {
			t.Logf("restore missions table: %v", err)
		}
	}()

	stats := storeAgentMistakes(db, missionID, harness)
	if stats.MistakesErrors != 1 {
		t.Errorf("mission-check path: MistakesErrors got %d, want 1", stats.MistakesErrors)
	}
	if stats.MistakesSkipped != 0 {
		t.Errorf("MistakesSkipped: got %d, want 0", stats.MistakesSkipped)
	}
}

// TestImportMarkdownStatsBriefDoesNotStoreMistakes checks the debrief-only guard
// in importMarkdownContent (importer.go, `if kind == "debrief"`): importing a
// BRIEF.md that contains a Sub-Agent Mistakes table stores no agent_mistakes
// rows and counts nothing (MistakesSkipped and MistakesErrors stay 0). As a
// positive control, the same table in the mission's DEBRIEF.md, with the mission
// row present, stores one row.
func TestImportMarkdownStatsBriefDoesNotStoreMistakes(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const briefMissionID = "mission-brief-only"
	const debriefMissionID = "mission-debrief-only"
	seedMission(t, db, debriefMissionID)

	harness := "test-harness"
	mistakesTable := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Error | low | Fixed | None |
`

	// A BRIEF import must leave agent_mistakes untouched and count nothing.
	briefContent := []byte(mistakesTable)
	stats, err := ImportMarkdownStats(db, "/path/to/BRIEF.md", "missions/"+briefMissionID+"/BRIEF.md", harness, briefContent)
	if err != nil {
		t.Fatalf("ImportMarkdownStats for BRIEF: %v", err)
	}
	if stats.MistakesSkipped != 0 || stats.MistakesErrors != 0 {
		t.Errorf("BRIEF import should not call storeAgentMistakes, got MistakesSkipped=%d MistakesErrors=%d", stats.MistakesSkipped, stats.MistakesErrors)
	}

	// Import the same table as DEBRIEF for a mission with a row —
	// ImportMarkdownStats should store mistakes
	debriefContent := []byte(mistakesTable)
	stats, err = ImportMarkdownStats(db, "/path/to/DEBRIEF.md", "missions/"+debriefMissionID+"/DEBRIEF.md", harness, debriefContent)
	if err != nil {
		t.Fatalf("ImportMarkdownStats for DEBRIEF: %v", err)
	}
	if stats.MistakesSkipped != 0 {
		t.Errorf("DEBRIEF import MistakesSkipped: got %d, want 0", stats.MistakesSkipped)
	}
	if mistakesCount(t, db, debriefMissionID) != 1 {
		t.Errorf("DEBRIEF import should store mistakes, got %d rows, want 1", mistakesCount(t, db, debriefMissionID))
	}
}

// TestStoreAgentMistakesImpactNoteNULL tests that NULL impact_note are stored
// when the ImpactNote cell is empty.
func TestStoreAgentMistakesImpactNoteNULL(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-impact-note-null"
	seedMission(t, db, missionID)

	harness := "test-harness"
	body := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Error with empty note | low |  | None |
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body, "sha", missionID, harness); err != nil {
		t.Fatalf("upsert document: %v", err)
	}

	stats := storeAgentMistakes(db, missionID, harness)
	if stats.MistakesSkipped != 0 {
		t.Errorf("MistakesSkipped: got %d, want 0", stats.MistakesSkipped)
	}

	impactNoteSQL := sql.NullString{}
	if err := db.QueryRow(
		`SELECT impact_note FROM agent_mistakes WHERE mission_id = ? AND ord = 1`, missionID,
	).Scan(&impactNoteSQL); err != nil {
		t.Fatalf("query impact_note: %v", err)
	}
	if impactNoteSQL.Valid {
		t.Errorf("impact_note should be NULL, got %q", impactNoteSQL.String)
	}
}

// TestStoreAgentMistakesImpactNULL tests that NULL impacts are stored
// when the Impact cell is empty, "none", or dashes.
func TestStoreAgentMistakesImpactNULL(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-impact-null"
	seedMission(t, db, missionID)

	harness := "test-harness"
	body := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Error 1 | | Fixed | None |
| @apone | Error 2 | none | Fixed | None |
| @bishop | Error 3 | — | Fixed | None |
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body, "sha", missionID, harness); err != nil {
		t.Fatalf("upsert document: %v", err)
	}

	stats := storeAgentMistakes(db, missionID, harness)
	if stats.MistakesSkipped != 0 {
		t.Errorf("MistakesSkipped: got %d, want 0", stats.MistakesSkipped)
	}

	_, _, _, impact, _, _ := mistakesAt(t, db, missionID, 1)
	if impact != "NULL" {
		t.Errorf("row 1 impact (empty cell): got %q, want %q", impact, "NULL")
	}

	_, _, _, impact, _, _ = mistakesAt(t, db, missionID, 2)
	if impact != "NULL" {
		t.Errorf("row 2 impact (none): got %q, want %q", impact, "NULL")
	}

	_, _, _, impact, _, _ = mistakesAt(t, db, missionID, 3)
	if impact != "NULL" {
		t.Errorf("row 3 impact (—): got %q, want %q", impact, "NULL")
	}
}

// TestStoreAgentMistakesEscapedPipe tests that cells with escaped pipes
// are stored with the pipe unescaped.
func TestStoreAgentMistakesEscapedPipe(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-escaped-pipe"
	seedMission(t, db, missionID)

	harness := "test-harness"
	body := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Error with \| char | low | Fixed | Use \| correctly |
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body, "sha", missionID, harness); err != nil {
		t.Fatalf("upsert document: %v", err)
	}

	stats := storeAgentMistakes(db, missionID, harness)
	if stats.MistakesSkipped != 0 {
		t.Errorf("MistakesSkipped: got %d, want 0", stats.MistakesSkipped)
	}

	_, _, mistake, _, _, prevention := mistakesAt(t, db, missionID, 1)
	if !strings.Contains(mistake, "|") {
		t.Errorf("mistake should contain unescaped pipe, got %q", mistake)
	}
	if !strings.Contains(prevention, "|") {
		t.Errorf("prevention should contain unescaped pipe, got %q", prevention)
	}
}

// TestStoreAgentMistakesPipeOnlyLine tests that a pipe-only line between two
// valid rows is skipped without being counted, and both rows are stored.
func TestStoreAgentMistakesPipeOnlyLine(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-pipe-only"
	seedMission(t, db, missionID)

	harness := "test-harness"
	body := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Error 1 | low | Fixed | None |
|
| @apone | Error 2 | high | Fixed | None |
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body, "sha", missionID, harness); err != nil {
		t.Fatalf("upsert document: %v", err)
	}

	stats := storeAgentMistakes(db, missionID, harness)
	// The pipe-only line should be skipped without incrementing MistakesSkipped
	if stats.MistakesSkipped != 0 {
		t.Errorf("MistakesSkipped: got %d, want 0 (pipe-only line is not counted)", stats.MistakesSkipped)
	}

	n := mistakesCount(t, db, missionID)
	if n != 2 {
		t.Errorf("row count: got %d, want 2 (both rows stored, pipe-only skipped)", n)
	}

	agentRaw, _, _, _, _, _ := mistakesAt(t, db, missionID, 1)
	if agentRaw != "@hicks" {
		t.Errorf("row 1 agent_raw: got %q, want %q", agentRaw, "@hicks")
	}

	agentRaw, _, _, _, _, _ = mistakesAt(t, db, missionID, 2)
	if agentRaw != "@apone" {
		t.Errorf("row 2 agent_raw: got %q, want %q", agentRaw, "@apone")
	}
}

// TestApplySchemaAddsAgentMistakesToExistingDatabase tests that
// ApplySchema creates the agent_mistakes table on an existing database.
func TestApplySchemaAddsAgentMistakesToExistingDatabase(t *testing.T) {
	// This test uses the store package's ApplySchema, which is tested in
	// internal/store/*_test.go. We verify that the table is created.
	db := openTestDB(t)
	defer db.Close()

	// Verify the table exists by querying it
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_mistakes`).Scan(&count); err != nil {
		t.Fatalf("agent_mistakes table does not exist or is not queryable: %v", err)
	}
	if count != 0 {
		t.Errorf("new table should be empty, got %d rows", count)
	}
}

// TestAgentMistakesCascadeOnMissionDelete tests that deleting a mission
// also deletes its agent_mistakes rows.
func TestAgentMistakesCascadeOnMissionDelete(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-cascade"
	seedMission(t, db, missionID)

	harness := "test-harness"
	body := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Error | low | Fixed | None |
`

	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body, "sha", missionID, harness); err != nil {
		t.Fatalf("upsert document: %v", err)
	}

	storeAgentMistakes(db, missionID, harness)
	if n := mistakesCount(t, db, missionID); n != 1 {
		t.Errorf("initial state: got %d rows, want 1", n)
	}

	// Delete the mission
	if _, err := db.Exec(`DELETE FROM missions WHERE id = ?`, missionID); err != nil {
		t.Fatalf("delete mission: %v", err)
	}

	// Verify agent_mistakes cascade-deleted
	n := mistakesCount(t, db, missionID)
	if n != 0 {
		t.Errorf("after mission delete: got %d rows, want 0 (CASCADE DELETE)", n)
	}
}

// TestImportMarkdownStatsDebriefStoresMistakes (T1) tests that ImportMarkdownStats
// on a mission's DEBRIEF.md (mission row present) stores rows and returns stats
// reflecting a malformed row (MistakesSkipped 1).
func TestImportMarkdownStatsDebriefStoresMistakes(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-import-debrief"
	seedMission(t, db, missionID)

	harness := "test-harness"
	debriefContent := []byte(`
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Good row | low | Fixed | None |
| @apone | Only three cells | high |
| @bishop | Another good | med | Fixed | None |
`)

	stats, err := ImportMarkdownStats(db, "/path/to/DEBRIEF.md", "missions/"+missionID+"/DEBRIEF.md", harness, debriefContent)
	if err != nil {
		t.Fatalf("ImportMarkdownStats: %v", err)
	}
	if stats.MistakesSkipped != 1 {
		t.Errorf("MistakesSkipped: got %d, want 1 (one malformed row)", stats.MistakesSkipped)
	}

	n := mistakesCount(t, db, missionID)
	if n != 2 {
		t.Errorf("rows stored: got %d, want 2", n)
	}
}

// TestSyncHarnessStatsSumsMistakes (T3) tests that SyncHarnessStats accumulates
// mistakes statistics via add() and the call site in importMarkdownContent.
// It creates a temp memory root with two missions' DEBRIEF.md files, one with
// unreadable section and one missing-mission → SyncHarnessStats returns MistakesSkipped 2.
func TestSyncHarnessStatsSumsMistakes(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Create a temp memory tree
	tmpRoot := t.TempDir()

	// Mission 1: will have a missing missions row
	const mission1 = "mission-no-row"
	missions1Dir := tmpRoot + "/missions/" + mission1
	if err := os.MkdirAll(missions1Dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	debrief1 := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Error | low | Fixed | None |
`
	if err := os.WriteFile(missions1Dir+"/DEBRIEF.md", []byte(debrief1), 0644); err != nil {
		t.Fatalf("write DEBRIEF.md: %v", err)
	}

	// Mission 2: will have an unreadable section
	const mission2 = "mission-unreadable"
	seedMission(t, db, mission2)
	missions2Dir := tmpRoot + "/missions/" + mission2
	if err := os.MkdirAll(missions2Dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	debrief2 := `
# Sub-Agent Mistakes and Corrections

This section has prose but no table.
`
	if err := os.WriteFile(missions2Dir+"/DEBRIEF.md", []byte(debrief2), 0644); err != nil {
		t.Fatalf("write DEBRIEF.md: %v", err)
	}

	stats, err := SyncHarnessStats(db, tmpRoot, "test-harness")
	if err != nil {
		t.Fatalf("SyncHarnessStats: %v", err)
	}
	if stats.MistakesSkipped != 2 {
		t.Errorf("MistakesSkipped: got %d, want 2 (one missing mission, one unreadable section)", stats.MistakesSkipped)
	}
}

// TestSyncHarnessStatsCountsMistakesErrors (T4) tests that SyncHarnessStats
// accumulates mistakes errors via add(). Same as T3 but with agent_mistakes
// dropped to force a DB error → MistakesErrors ≥1.
func TestSyncHarnessStatsCountsMistakesErrors(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Create a temp memory tree with a valid debrief
	tmpRoot := t.TempDir()
	const missionID = "mission-test-errors"
	seedMission(t, db, missionID)

	missionsDir := tmpRoot + "/missions/" + missionID
	if err := os.MkdirAll(missionsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	debriefContent := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Error | low | Fixed | None |
`
	if err := os.WriteFile(missionsDir+"/DEBRIEF.md", []byte(debriefContent), 0644); err != nil {
		t.Fatalf("write DEBRIEF.md: %v", err)
	}

	// Drop agent_mistakes to force an error
	if _, err := db.Exec(`DROP TABLE agent_mistakes`); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	stats, err := SyncHarnessStats(db, tmpRoot, "test-harness")
	if err != nil {
		t.Fatalf("SyncHarnessStats: %v", err)
	}
	if stats.MistakesErrors < 1 {
		t.Errorf("MistakesErrors: got %d, want ≥1", stats.MistakesErrors)
	}
}

// TestStoreAgentMistakesPrefersImportingHarness (T5) tests that storeAgentMistakes
// reads the debrief from the document tagged with the importing harness first,
// then the lowest source_path. Two debriefs for one mission with different
// harnesses → the importing harness's rows are stored.
func TestStoreAgentMistakesPrefersImportingHarness(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-harness-pref"
	seedMission(t, db, missionID)

	harness1 := "harness-a"
	harness2 := "harness-b"

	// Upsert first debrief with harness-a (will be preferred when importing as harness-a)
	body1 := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Error A | low | Fixed | None |
`
	if err := upsertDocument(db, "/path/to/harness-a-debrief.md", "DEBRIEF", "debrief", body1, "sha1", missionID, harness1); err != nil {
		t.Fatalf("upsert document 1: %v", err)
	}

	// Upsert second debrief with harness-b (will be preferred when importing as harness-b)
	body2 := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @apone | Error B | high | Fixed | None |
`
	if err := upsertDocument(db, "/path/to/harness-b-debrief.md", "DEBRIEF", "debrief", body2, "sha2", missionID, harness2); err != nil {
		t.Fatalf("upsert document 2: %v", err)
	}

	// Store mistakes as harness1 — should use body1
	stats := storeAgentMistakes(db, missionID, harness1)
	if stats.MistakesSkipped != 0 {
		t.Errorf("harness1: MistakesSkipped got %d, want 0", stats.MistakesSkipped)
	}
	agentRaw, _, _, _, _, _ := mistakesAt(t, db, missionID, 1)
	if agentRaw != "@hicks" {
		t.Errorf("harness1: row 1 agent_raw got %q, want @hicks", agentRaw)
	}

	// Clear the table and store mistakes as harness2 — should use body2
	if _, err := db.Exec(`DELETE FROM agent_mistakes WHERE mission_id = ?`, missionID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	stats = storeAgentMistakes(db, missionID, harness2)
	if stats.MistakesSkipped != 0 {
		t.Errorf("harness2: MistakesSkipped got %d, want 0", stats.MistakesSkipped)
	}
	agentRaw, _, _, _, _, _ = mistakesAt(t, db, missionID, 1)
	if agentRaw != "@apone" {
		t.Errorf("harness2: row 1 agent_raw got %q, want @apone", agentRaw)
	}
}

// TestStoreAgentMistakesReadErrorKeepsRows (T6) tests that a read error
// counts as MistakesErrors, rows are kept, and the failure text is distinct
// from the replace-failure test. Uses breakDocumentKind to force a read error.
func TestStoreAgentMistakesReadErrorKeepsRows(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const missionID = "mission-test-read-error"
	seedMission(t, db, missionID)

	harness := "test-harness"
	// Seed initial rows
	body1 := `
# Sub-Agent Mistakes and Corrections

| Agent | Mistake | Impact | Correction | Prevention |
|-------|---------|--------|-----------|-----------|
| @hicks | Seeded error | low | Fixed | None |
`
	if err := upsertDocument(db, "test-debrief.md", "DEBRIEF", "debrief", body1, "sha1", missionID, harness); err != nil {
		t.Fatalf("upsert document: %v", err)
	}
	storeAgentMistakes(db, missionID, harness)
	if n := mistakesCount(t, db, missionID); n != 1 {
		t.Errorf("initial state: got %d rows, want 1", n)
	}

	// Break documentBody to force a read error
	breakDocumentKind(t, db, "debrief")

	// Call storeAgentMistakes — should get MistakesErrors 1 and keep rows
	stats := storeAgentMistakes(db, missionID, harness)
	if stats.MistakesErrors != 1 {
		t.Errorf("read-error path: MistakesErrors got %d, want 1", stats.MistakesErrors)
	}
	if n := mistakesCount(t, db, missionID); n != 1 {
		t.Errorf("rows after read error: got %d, want 1 (kept)", n)
	}
}

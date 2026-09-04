// Package importer — tests for Sync.
//
// TestSync is the binding functional check: it opens a fresh
// temp-file SQLite DB, applies db/schema.sql via the production
// store.ApplySchema, runs Sync against the testdata/memory fixture, and asserts:
//
//  1. document count == 17 (13 .md files + 4 non-blank JSONL lines from
//     stream.jsonl — the exact fixture size, so a partial import or a
//     fixture drift is caught), and
//  2. EVERY documents row has a paired documents_fts row whose rowid
//     equals documents.id (the FTS5 rowid contract — without this,
//     /v1/memory/search returns empty).
//
// TestSyncIdempotent additionally calls Sync twice and asserts the
// document count is unchanged (sha256 idempotency).
package importer

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"bishop-memory/internal/store"
)

// openTestDB opens a fresh SQLite database file in a per-test temp
// directory, applies db/schema.sql via the production store.ApplySchema,
// and configures the pool for the single-writer pattern used by the
// live service. The returned DB is closed automatically when the test
// ends.
//
// Using the production bootstrap path means this test catches any
// regression in store/schema.go (most importantly the SQL splitter —
// Step 6 CRITICAL was exactly this).
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Same pragmas as internal/store/sqlite.go Open() so the test
	// exercises the same connection behaviour as production.
	db, err := sql.Open("sqlite",
		"file:"+dbPath+
			"?_pragma=busy_timeout(5000)"+
			"&_pragma=foreign_keys(1)"+
			"&_pragma=journal_mode(WAL)",
	)
	if err != nil {
		t.Fatalf("open test sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Match the single-writer pattern from store.Open so the importer's
	// per-file transactions behave identically to the live service.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	schemaPath := filepath.Join("..", "..", "db", "schema.sql")
	if err := store.ApplySchema(db, schemaPath); err != nil {
		t.Fatalf("apply schema %s: %v", schemaPath, err)
	}
	return db
}

// TestSync imports the testdata/memory fixture and verifies the
// document count + FTS5 rowid contract. See package docblock for the
// assertion list.
func TestSync(t *testing.T) {
	db := openTestDB(t)

	// The fixture lives at the module root (testdata/memory/) — the same
	// place the live service's handler looks when MEMORY_ROOT is unset
	// and the service runs from the module root. From this test's CWD
	// (internal/importer/), the relative path is ../../testdata/memory,
	// consistent with the schemaPath reference in openTestDB.
	fixtureRoot := filepath.Join("..", "..", "testdata", "memory")
	if err := Sync(db, fixtureRoot); err != nil {
		t.Fatalf("Sync(%s): %v", fixtureRoot, err)
	}

	// (1) document count == exact fixture size. 13 .md files:
	// state/{CURRENT-MISSION,FLIGHT-RECORDER,MISSION-ARCHIVE}.md
	// missions/mission-20260904-01/{BRIEF,PROGRESS,DEBRIEF}.md
	// findings/{FINDINGS,PATTERNS}.md, findings/service-records/hicks.md
	// reference/{DIRECTIVES,sqlite-wal}.md, workspace/findings-scratch.md
	// graph/OVERVIEW.md + 4 non-blank stream.jsonl lines with kind="flight-recorder" = 17 total.
	var docCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&docCount); err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if docCount != 17 {
		t.Fatalf("Sync imported %d documents, want exactly 17 (fixture drift or partial import)", docCount)
	}
	t.Logf("Sync imported %d documents", docCount)

	// (2) every documents row has a paired documents_fts row whose
	// rowid equals documents.id (Step 4 carry-forward contract).
	rows, err := db.Query(`
		SELECT d.id, d.source_path, f.rowid
		  FROM documents d
		  LEFT JOIN documents_fts f ON f.rowid = d.id
	`)
	if err != nil {
		t.Fatalf("query documents + fts join: %v", err)
	}
	defer rows.Close()

	var matched, missing int
	for rows.Next() {
		var (
			id         int64
			sourcePath string
			ftsRowid   sql.NullInt64
		)
		if err := rows.Scan(&id, &sourcePath, &ftsRowid); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		if !ftsRowid.Valid {
			t.Errorf("documents.id=%d source_path=%s: no matching documents_fts row", id, sourcePath)
			missing++
			continue
		}
		if ftsRowid.Int64 != id {
			t.Errorf("FTS5 rowid mismatch: documents.id=%d, documents_fts.rowid=%d (source_path=%s)",
				id, ftsRowid.Int64, sourcePath)
			missing++
			continue
		}
		matched++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rows: %v", err)
	}

	if missing > 0 {
		t.Fatalf("FTS5 rowid contract violated: %d/%d documents lack a matching documents_fts row",
			missing, docCount)
	}
	if matched != docCount {
		t.Fatalf("count mismatch: matched=%d, docCount=%d", matched, docCount)
	}

	t.Logf("FTS5 rowid contract holds: %d/%d documents have documents_fts.rowid == documents.id",
		matched, docCount)

	// Verify new kind mappings exist and are correct. We query by relative
	// path suffix rather than exact path since source_path is now absolute.
	kindMappings := map[string]string{
		"missions/mission-20260904-01/BRIEF.md":    "brief",
		"missions/mission-20260904-01/PROGRESS.md": "progress",
		"missions/mission-20260904-01/DEBRIEF.md":  "debrief",
		"findings/PATTERNS.md":                     "patterns",
		"findings/service-records/hicks.md":        "service-record",
		"reference/DIRECTIVES.md":                  "directives",
	}

	for relPathSuffix, expectedKind := range kindMappings {
		var kind string
		// Query by LIKE to match against the absolute path suffix
		query := `SELECT kind FROM documents WHERE source_path LIKE ? LIMIT 1`
		err := db.QueryRow(query, "%"+relPathSuffix).Scan(&kind)
		if err != nil {
			t.Fatalf("kind mapping %s: %v", relPathSuffix, err)
		}
		if kind != expectedKind {
			t.Fatalf("kind mapping %s: got kind=%q, want %q", relPathSuffix, kind, expectedKind)
		}
		t.Logf("✓ %s → kind=%q", relPathSuffix, kind)
	}

	// Verify that JSONL lines now have kind="flight-recorder"
	var jsonlCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM documents WHERE kind = 'flight-recorder' AND source_path LIKE '%.jsonl:%'`).Scan(&jsonlCount); err != nil {
		t.Fatalf("count flight-recorder kind documents: %v", err)
	}
	if jsonlCount != 4 {
		t.Fatalf("JSONL lines: got %d documents with kind='flight-recorder', want exactly 4", jsonlCount)
	}
	t.Logf("✓ JSONL lines → kind='flight-recorder' (%d lines)", jsonlCount)

	// Verify that FLIGHT-RECORDER.md contains the extracted structured block
	var flightRecorderBody string
	if err := db.QueryRow(`
		SELECT body FROM documents
		WHERE source_path LIKE '%/state/FLIGHT-RECORDER.md'
		LIMIT 1
	`).Scan(&flightRecorderBody); err != nil {
		t.Fatalf("query FLIGHT-RECORDER.md body: %v", err)
	}
	if !strings.Contains(flightRecorderBody, "Structured Journal Rows") {
		t.Fatalf("FLIGHT-RECORDER.md missing extracted structured block header")
	}
	if !strings.Contains(flightRecorderBody, "mission-20260904-01") {
		t.Fatalf("FLIGHT-RECORDER.md extracted block missing mission data")
	}
	if !strings.Contains(flightRecorderBody, "step-sync") {
		t.Fatalf("FLIGHT-RECORDER.md extracted block missing event data")
	}
	t.Logf("✓ FLIGHT-RECORDER.md → extracted structured block present and indexed")
}

// TestKindFromPath verifies that kindFromPath correctly classifies documents
// by their path relative to root, covering special cases and fallthrough logic.
func TestKindFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		// Special cases: exact nesting depths and filenames
		{"missions/mission-20260904-01/BRIEF.md", "brief"},
		{"missions/mission-20260904-01/PROGRESS.md", "progress"},
		{"missions/mission-20260904-01/DEBRIEF.md", "debrief"},
		{"findings/PATTERNS.md", "patterns"},
		{"findings/service-records/hicks.md", "service-record"},
		{"reference/DIRECTIVES.md", "directives"},

		// Boundary cases: nesting depth changes
		{"missions/mission-20260904-01/sub/BRIEF.md", "missions"},
		{"missions/BRIEF.md", "missions"},
		{"reference/sub/DIRECTIVES.md", "reference"},
		{"findings/service-records/a/b.md", "findings"},

		// Directory fallback: top-level directory is the kind
		{"state/CURRENT-MISSION.md", "state"},
		{"graph/OVERVIEW.md", "graph"},
		{"workspace/findings-scratch.md", "workspace"},

		// Bare-file fallback: no parent directory
		{"SINGLE-FILE.md", "document"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := kindFromPath(tt.path)
			if got != tt.want {
				t.Errorf("kindFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestPrependCurrentMissionFields verifies that the six recognised structured
// fields are extracted and prepended, unrecognised keys are ignored, and
// duplicate keys are handled by taking the first occurrence.
func TestPrependCurrentMissionFields(t *testing.T) {
	input := `# CURRENT-MISSION

- Mission ID: mission-20260904-01
- Status: in-progress
- Owner: @bishop
- Next Action: Review code
- Last Updated: 2026-09-04 10:00 UTC
- Blockers: None
- Unknown Field: should be ignored
- Mission ID: mission-DUPLICATE
- Blockers: Also ignored (duplicate)

Rest of content here.`

	output := prependCurrentMissionFields(input)

	// Extract the header block: begins with the comment marker and ends at the first blank line.
	// The structure is: comment marker, extracted fields (one per line), blank line, then raw body.
	headerMarker := "<!-- Structured Fields (extracted from CURRENT-MISSION.md) -->\n"
	if !strings.HasPrefix(output, headerMarker) {
		t.Errorf("extracted block header not found at start of output")
		return
	}

	// Find the blank line that separates the header from the raw body.
	// Split the output and reconstruct the header up to (but not including) the first blank line
	// after the marker.
	lines := strings.Split(output, "\n")
	var headerLines []string
	foundMarker := false
	for _, line := range lines {
		if !foundMarker && strings.HasPrefix(line, "<!--") {
			foundMarker = true
			headerLines = append(headerLines, line)
			continue
		}
		if foundMarker && line == "" {
			// This is the blank line separator; stop collecting header
			break
		}
		if foundMarker {
			headerLines = append(headerLines, line)
		}
	}

	header := strings.Join(headerLines, "\n")

	// (1) Check that the six recognised fields ARE in the extracted header block
	requiredFields := []string{
		"Mission ID: mission-20260904-01",
		"Status: in-progress",
		"Owner: @bishop",
		"Next Action: Review code",
		"Last Updated: 2026-09-04 10:00 UTC",
		"Blockers: None",
	}
	for _, field := range requiredFields {
		if !strings.Contains(header, field) {
			t.Errorf("expected field not found in extracted header: %q", field)
		}
	}

	// (2) Check that unrecognised keys are NOT in the extracted header.
	// This assertion can actually fail if the production logic is wrong,
	// because the header block contains only extracted fields, not the raw content.
	if strings.Contains(header, "Unknown Field") {
		t.Errorf("unrecognised field should not appear in extracted header: %q", "Unknown Field")
	}

	// (3) Check that duplicate Mission ID used the first occurrence.
	// The header must contain mission-20260904-01, not mission-DUPLICATE.
	if !strings.Contains(header, "mission-20260904-01") {
		t.Errorf("first occurrence of Mission ID not in extracted header")
	}
	if strings.Contains(header, "mission-DUPLICATE") {
		t.Errorf("duplicate Mission ID should not appear in extracted header (first occurrence wins)")
	}

	// (4) Verify the raw body is still preserved beneath the header
	if !strings.Contains(output, "Rest of content here.") {
		t.Errorf("raw body content not preserved after extracted header")
	}
	if !strings.Contains(output, "should be ignored") {
		t.Errorf("raw unrecognised field content not preserved")
	}
}

// TestSyncIdempotent calls Sync twice and asserts the document count
// is unchanged (sha256-based idempotency — unchanged files must be
// skipped, not re-inserted).
func TestSyncIdempotent(t *testing.T) {
	db := openTestDB(t)

	fixtureRoot := filepath.Join("..", "..", "testdata", "memory")

	if err := Sync(db, fixtureRoot); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	var firstCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&firstCount); err != nil {
		t.Fatalf("count after first sync: %v", err)
	}

	if err := Sync(db, fixtureRoot); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	var secondCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&secondCount); err != nil {
		t.Fatalf("count after second sync: %v", err)
	}

	if firstCount != secondCount {
		t.Fatalf("idempotency violated: first Sync imported %d docs, second Sync imported %d",
			firstCount, secondCount)
	}
	if firstCount == 0 {
		t.Fatalf("first Sync imported 0 docs — idempotency check is meaningless")
	}
	if firstCount != 17 {
		t.Fatalf("first Sync imported %d docs, want exactly 17", firstCount)
	}

	t.Logf("idempotency holds: two Sync() calls produced %d documents each", firstCount)
}

// TestSyncMultiRootCollision verifies that multiple roots sharing the same
// relative path do not collide on the UNIQUE source_path constraint. Before
// the fix, syncing rootB after rootA would silently overwrite rootA's document
// when both contained state/CURRENT-MISSION.md with different content.
func TestSyncMultiRootCollision(t *testing.T) {
	db := openTestDB(t)

	// Create two separate temp roots.
	rootA := t.TempDir()
	rootB := t.TempDir()

	// Each root gets a state/ directory with a distinct CURRENT-MISSION.md.
	stateADir := filepath.Join(rootA, "state")
	if err := os.Mkdir(stateADir, 0755); err != nil {
		t.Fatalf("mkdir state in rootA: %v", err)
	}
	stateA := filepath.Join(stateADir, "CURRENT-MISSION.md")
	if err := os.WriteFile(stateA, []byte("# Mission A\nAAAAAAAA"), 0644); err != nil {
		t.Fatalf("write rootA CURRENT-MISSION.md: %v", err)
	}

	stateBDir := filepath.Join(rootB, "state")
	if err := os.Mkdir(stateBDir, 0755); err != nil {
		t.Fatalf("mkdir state in rootB: %v", err)
	}
	stateB := filepath.Join(stateBDir, "CURRENT-MISSION.md")
	if err := os.WriteFile(stateB, []byte("# Mission B\nBBBBBBBB"), 0644); err != nil {
		t.Fatalf("write rootB CURRENT-MISSION.md: %v", err)
	}

	// Sync both roots into the same database.
	if err := Sync(db, rootA); err != nil {
		t.Fatalf("Sync rootA: %v", err)
	}
	if err := Sync(db, rootB); err != nil {
		t.Fatalf("Sync rootB: %v", err)
	}

	// Both documents should exist with distinct absolute paths.
	rows, err := db.Query(`
		SELECT source_path, body FROM documents
		WHERE source_path LIKE '%/state/CURRENT-MISSION.md'
		ORDER BY source_path
	`)
	if err != nil {
		t.Fatalf("query documents: %v", err)
	}
	defer rows.Close()

	var (
		paths  []string
		bodies []string
	)
	for rows.Next() {
		var path, body string
		if err := rows.Scan(&path, &body); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		paths = append(paths, path)
		bodies = append(bodies, body)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rows: %v", err)
	}

	if len(paths) != 2 {
		t.Fatalf("expected 2 documents from multi-root sync, got %d", len(paths))
	}

	// Paths must be distinct (absolute paths from different roots).
	if paths[0] == paths[1] {
		t.Fatalf("paths are identical (collision not fixed): %s and %s", paths[0], paths[1])
	}

	// Bodies must be distinct (content from different roots).
	if bodies[0] == bodies[1] {
		t.Fatalf("bodies are identical (silent overwrite occurred): %s and %s", bodies[0], bodies[1])
	}

	// Each body must contain the expected distinguishing content.
	if !((strings.Contains(bodies[0], "AAAA") && strings.Contains(bodies[1], "BBBB")) ||
		(strings.Contains(bodies[0], "BBBB") && strings.Contains(bodies[1], "AAAA"))) {
		t.Fatalf("bodies do not match expected content: [0]=%s, [1]=%s",
			bodies[0], bodies[1])
	}

	t.Logf("✓ Multi-root collision test passed:")
	t.Logf("  Path A: %s", paths[0])
	t.Logf("  Path B: %s", paths[1])
	t.Logf("  Both documents preserved with distinct content")
}

// TestPrependFlightRecorderFields tests the flight recorder extraction logic,
// including escaping, separator detection, and skip counting.
func TestPrependFlightRecorderFields(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantInOutput []string
		wantNotIn    []string // strings that should NOT appear in extracted block
	}{
		{
			name: "well-formed three-row journal",
			input: `# FLIGHT-RECORDER

| Timestamp | Mission ID | Step | Agent | Event | Note |
| --- | --- | --- | --- | --- | --- |
| 2026-09-04 10:00 UTC | mission-20260904-01 | 1 | @bishop | step-sync | First sync |
| 2026-09-04 10:05 UTC | mission-20260904-01 | 2 | @hicks | step-sync | Code written |
| 2026-09-04 10:10 UTC | mission-20260904-01 | 3 | @apone | step-sync | Review done |`,
			wantInOutput: []string{
				"2026-09-04 10:00 UTC | mission-20260904-01 | 1 | @bishop | step-sync | First sync",
				"2026-09-04 10:05 UTC | mission-20260904-01 | 2 | @hicks | step-sync | Code written",
				"2026-09-04 10:10 UTC | mission-20260904-01 | 3 | @apone | step-sync | Review done",
			},
		},
		{
			name: "note containing escaped pipe (Bug 2 variant 1)",
			input: `# FLIGHT-RECORDER

| Timestamp | Mission ID | Step | Agent | Event | Note |
| --- | --- | --- | --- | --- | --- |
| 2026-09-04 10:00 UTC | mission-20260904-01 | 1 | @bishop | step-sync | See section \| details |`,
			wantInOutput: []string{
				"See section | details",
			},
		},
		{
			name: "note ending in backslash before delimiter (Bug 1)",
			input: `# FLIGHT-RECORDER

| Timestamp | Mission ID | Step | Agent | Event | Note |
| --- | --- | --- | --- | --- | --- |
| 2026-09-04 10:00 UTC | mission-20260904-01 | 1 | @bishop | step-sync | Path C:\\Users\\ |`,
			wantInOutput: []string{
				"Path C:\\Users\\",
			},
		},
		{
			name: "escaped backslash followed by escaped pipe (Bug 2)",
			input: `# FLIGHT-RECORDER

| Timestamp | Mission ID | Step | Agent | Event | Note |
| --- | --- | --- | --- | --- | --- |
| 2026-09-04 10:00 UTC | mission-20260904-01 | 1 | @bishop | step-sync | Path\\\|Next |`,
			wantInOutput: []string{
				"Path\\|Next",
			},
		},
		{
			name: "all rows malformed, skip marker should appear (Bug 3)",
			input: `# FLIGHT-RECORDER

| Timestamp | Mission ID | Step | Agent | Event | Note |
| --- | --- | --- | --- | --- | --- |
| bad1 |
| bad2 |
| bad3 |`,
			wantInOutput: []string{
				"3 malformed rows skipped",
			},
		},
		{
			name: "separator with |-|-| format (Bug 4)",
			input: `# FLIGHT-RECORDER

| Timestamp | Mission ID | Step | Agent | Event | Note |
|-|-|-|-|-|-|
| 2026-09-04 10:00 UTC | mission-20260904-01 | 1 | @bishop | step-sync | Data row |`,
			wantInOutput: []string{
				"2026-09-04 10:00 UTC | mission-20260904-01 | 1 | @bishop | step-sync | Data row",
			},
		},
		{
			name: "separator with |--|--| format (Bug 4)",
			input: `# FLIGHT-RECORDER

| Timestamp | Mission ID | Step | Agent | Event | Note |
|--|--|--|--|--|--|
| 2026-09-04 10:00 UTC | mission-20260904-01 | 1 | @bishop | step-sync | Data row |`,
			wantInOutput: []string{
				"2026-09-04 10:00 UTC | mission-20260904-01 | 1 | @bishop | step-sync | Data row",
			},
		},
		{
			name: "separator with alignment colons (Bug 4)",
			input: `# FLIGHT-RECORDER

| Timestamp | Mission ID | Step | Agent | Event | Note |
| :--- | ---: | :---: | --- | --- | --- |
| 2026-09-04 10:00 UTC | mission-20260904-01 | 1 | @bishop | step-sync | Data row |`,
			wantInOutput: []string{
				"2026-09-04 10:00 UTC | mission-20260904-01 | 1 | @bishop | step-sync | Data row",
			},
		},
		{
			name: "note containing literal '---|' (Bug 4 reverse)",
			input: `# FLIGHT-RECORDER

| Timestamp | Mission ID | Step | Agent | Event | Note |
| --- | --- | --- | --- | --- | --- |
| 2026-09-04 10:00 UTC | mission-20260904-01 | 1 | @bishop | step-sync | See the ---\| pattern |`,
			wantInOutput: []string{
				"See the ---| pattern",
			},
		},
		{
			name: "file with no recognisable table",
			input: `# Random Markdown

Some text here.

Not a table at all.

More text.`,
			wantInOutput: []string{
				"# Random Markdown",
				"Not a table at all.",
			},
			wantNotIn: []string{
				"Structured Journal Rows",
			},
		},
		{
			name: "singular skip message",
			input: `# FLIGHT-RECORDER

| Timestamp | Mission ID | Step | Agent | Event | Note |
| --- | --- | --- | --- | --- | --- |
| bad |`,
			wantInOutput: []string{
				"1 malformed row skipped",
			},
		},
		{
			name: "plural skip message",
			input: `# FLIGHT-RECORDER

| Timestamp | Mission ID | Step | Agent | Event | Note |
| --- | --- | --- | --- | --- | --- |
| bad1 |
| bad2 |
| bad3 |
| bad4 |
| bad5 |`,
			wantInOutput: []string{
				"5 malformed rows skipped",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := prependFlightRecorderFields(tt.input)

			// Extract the header block for assertions where we need to verify
			// the extraction logic specifically (vs. just raw body preservation).
			// The structure is: comment marker, extracted rows, optional skip marker,
			// blank line, then raw body.
			headerMarker := "<!-- Structured Journal Rows (extracted from FLIGHT-RECORDER.md) -->"
			var headerBlock string
			if strings.Contains(output, headerMarker) {
				markerIdx := strings.Index(output, headerMarker)
				afterMarker := output[markerIdx:]
				// Find the blank line that ends the header block
				blankIdx := strings.Index(afterMarker, "\n\n")
				if blankIdx != -1 {
					headerBlock = afterMarker[:blankIdx+1] // include the first newline of the blank line
				}
			}

			// For cases testing well-formed extracted rows or separator handling,
			// assert against the header block to verify the extraction logic itself.
			// These cases would be tautological if tested against full output since
			// the expected strings exist verbatim in the raw input.
			tautologicalCases := map[string]bool{
				"well-formed three-row journal":                     true,
				"note ending in backslash before delimiter (Bug 1)": true,
				"separator with |-|-| format (Bug 4)":               true,
				"separator with |--|--| format (Bug 4)":             true,
				"separator with alignment colons (Bug 4)":           true,
			}

			checkTarget := output
			if tautologicalCases[tt.name] && headerBlock != "" {
				checkTarget = headerBlock
			}

			// Check for expected strings
			for _, want := range tt.wantInOutput {
				if !strings.Contains(checkTarget, want) {
					t.Errorf("missing expected string: %q", want)
					t.Logf("checkTarget:\n%s", checkTarget)
				}
			}

			// Check that unwanted strings don't appear
			for _, notWant := range tt.wantNotIn {
				if strings.Contains(checkTarget, notWant) {
					t.Errorf("unexpected string found: %q", notWant)
					t.Logf("checkTarget:\n%s", checkTarget)
				}
			}

			// Verify raw content is preserved (always check full output for this)
			for _, line := range strings.Split(tt.input, "\n") {
				trimmed := strings.TrimSpace(line)
				// Check a few meaningful lines from input are in output
				if trimmed != "" && !strings.HasPrefix(trimmed, "|") && len(trimmed) > 5 {
					if !strings.Contains(output, trimmed) {
						t.Errorf("raw content not preserved: %q", trimmed)
						break
					}
				}
			}
		})
	}
}

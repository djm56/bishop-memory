// Package importer walks the agent's memory tree and ingests its content
// into the documents + documents_fts (FTS5) tables.
//
// Sync is the entry point invoked by the /v1/documents/sync handler. It
// is the only writer to those two tables (the importer controls the
// documents lifecycle; there are no AFTER INSERT/UPDATE/DELETE triggers
// on documents_fts — see db/schema.sql for the rationale). Importing a
// BRIEF.md or DEBRIEF.md also refreshes mission_criteria (see criteria.go),
// importing a DEBRIEF.md also refreshes agent_mistakes (see mistakes.go), and
// importing a DEBRIEF.md also refreshes mission_deliverables (see deliverables.go).
//
// What gets imported:
//
//   - Markdown files (*.md) become one document per file. The document's
//     kind is derived from its path relative to root:
//
//     state/CURRENT-MISSION.md              -> "state"
//     state/FLIGHT-RECORDER.md              -> "state"
//     state/MISSION-ARCHIVE.md              -> "state"
//     missions/<id>/BRIEF.md                -> "brief"    (EXACTLY one
//     missions/<id>/PROGRESS.md             -> "progress"  dir under missions/
//     missions/<id>/DEBRIEF.md              -> "debrief"
//     findings/FINDINGS.md                  -> "findings"
//     findings/PATTERNS.md                  -> "patterns"
//     findings/service-records/<name>.md    -> "service-record"
//     reference/DIRECTIVES.md               -> "directives"
//     graph/<anything>.md                   -> "graph"
//     reference/<anything>.md               -> "reference"
//     workspace/<anything>.md               -> "workspace"
//     <top-level>/<file>.md                 -> "<top-level>"
//     <root>/<file>.md                      -> "document" (fallback)
//
//     The missions/<id>/BRIEF/PROGRESS/DEBRIEF mapping is intentionally
//     tight: it applies only to exactly those filenames at one directory
//     level under missions/. A missions/<id>/<sub>/BRIEF.md (deeper nested)
//     or a bare missions/BRIEF.md (no <id>) falls through to the
//     top-level-directory-name kind (or "document" fallback) — those files
//     are NOT mission metadata.
//
//     Note: graph/ directory has no counterpart in the harness memory tree
//     and is retained deliberately as a fixture exercising the plain
//     directory-name fallback; nothing should remove it as leftover.
//
//     The CURRENT-MISSION.md parser additionally extracts the structured
//     field list (Mission ID / Status / Owner / Next Action /
//     Last Updated / Blockers — per
//     .claude/templates/state/STATE-FILE-TEMPLATE.md) and prepends a
//     duplicated block at the top of the body so the values are
//     searchable both in their original position and as a single
//     structured block. The raw file content is preserved verbatim
//     below the prefix.
//
//   - JSONL files (*.jsonl) are split line by line; each non-blank line
//     becomes one document with kind="flight-recorder". Title is the parsed
//     event (or the filename if parsing fails); body is a
//     structured composition of mission_id, step, agent, event, note, and
//     occurred_at fields (or the raw line as a fallback); source_path is
//     "<abspath>:<line-number>" so each line is unique per filesystem location.
//
// What does NOT get imported:
//
//   - The flight_recorder, missions, mission_steps, findings, patterns,
//     service_records, and directives tables are owned by the live HTTP API
//     (POST /v1/flight-recorder, /v1/missions, etc.); the importer never
//     writes to them.
//
//   - Besides documents and documents_fts, the importer package does write four
//     tables: mission_criteria, refreshed when a BRIEF.md or DEBRIEF.md is
//     imported (criteria.go); agent_mistakes, refreshed when a DEBRIEF.md is
//     imported (mistakes.go); mission_deliverables, refreshed when a DEBRIEF.md
//     is imported (deliverables.go); and crew, upserted by SyncAgents and
//     ImportAgent (agents.go).
//
// Idempotency:
//
//	Each document is upserted by source_path (UNIQUE in
//	db/schema.sql). The sha256 column is the hex-encoded SHA-256 of
//	the document body bytes; unchanged files (same source_path, same
//	sha256) are skipped. A sha change triggers an UPDATE + FTS5
//	DELETE/INSERT pair, all in one transaction.
//
// FTS5 rowid contract (Step 4 carry-forward — binding):
//
//	documents.id is INTEGER PRIMARY KEY AUTOINCREMENT, so it IS the
//	SQLite rowid. The importer inserts each documents_fts row with
//	rowid = documents.id, so /v1/memory/search can join them with:
//
//	    JOIN documents d ON d.id = documents_fts.rowid
//
//	The pair (documents INSERT/UPDATE, documents_fts INSERT or
//	DELETE+INSERT) lives in the same transaction, so the two tables
//	cannot drift out of sync.
//
// Path safety:
//
//	The walker canonicalises root via filepath.EvalSymlinks (so symlink
//	comparisons are stable) and refuses to:
//	  (a) visit any path whose cleaned absolute form is not a lexical
//	      child of root, and
//	  (b) follow a symlink that resolves outside root.
//	Both checks return an error so the failure is surfaced, not
//	silently swallowed. Only .md and .jsonl files are processed;
//	dotfiles and other extensions are skipped silently.
package importer

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MaxJSONLLineBytes is the largest single line a JSONL file may have
// before the line scanner gives up. 16 MiB matches bufio.Scanner's
// default maximum plus headroom for large notes.
const MaxJSONLLineBytes = 16 * 1024 * 1024

// SyncStats counts what the criteria, mistakes, and deliverables imports did during one sync or import.
type SyncStats struct {
	// CriteriaSkipped counts brief or debrief imports whose criteria were not
	// read: the mission has no missions row yet, or the criteria heading is
	// present with no criterion bullets. See storeMissionCriteria.
	CriteriaSkipped int
	// CriteriaErrors counts database failures while refreshing mission_criteria.
	// They are logged and counted; they never fail the import.
	CriteriaErrors int
	// MistakesSkipped counts debrief imports whose agent mistakes were not
	// stored: the mission has no missions row yet, the mistakes heading is
	// present with no readable table, or the section contains unparseable rows.
	// See storeAgentMistakes.
	MistakesSkipped int
	// MistakesErrors counts database failures while refreshing agent_mistakes.
	// They are logged and counted; they never fail the import.
	MistakesErrors int
	// DeliverablesSkipped counts debrief imports whose deliverables were not
	// stored: the mission has no missions row yet, or the deliverables heading is
	// present with no readable bullets. See storeMissionDeliverables.
	DeliverablesSkipped int
	// DeliverablesErrors counts database failures while refreshing mission_deliverables.
	// They are logged and counted; they never fail the import.
	DeliverablesErrors int
}

// add accumulates o into s.
func (s *SyncStats) add(o SyncStats) {
	s.CriteriaSkipped += o.CriteriaSkipped
	s.CriteriaErrors += o.CriteriaErrors
	s.MistakesSkipped += o.MistakesSkipped
	s.MistakesErrors += o.MistakesErrors
	s.DeliverablesSkipped += o.DeliverablesSkipped
	s.DeliverablesErrors += o.DeliverablesErrors
}

// Sync walks the memory tree at root and imports its contents into the
// documents + documents_fts tables via db. See the package docblock for
// the full design (kind mapping, idempotency, FTS5 rowid contract,
// path safety, .jsonl handling).
//
// On success every visited file (and every non-blank .jsonl line) is
// reflected in the documents table with a paired documents_fts row at
// the same rowid. Any error from the walk / read / parse / upsert chain
// is returned (the walker stops at the first error); callers
// (syncDocumentsHandler) surface it as a 502 to the HTTP client.
//
// The root is resolved via filepath.Abs and verified to exist as a
// directory. Relative paths are accepted (the handler defaults to
// "testdata/memory" when MEMORY_ROOT is unset).
func Sync(db *sql.DB, root string) error {
	_, err := SyncHarnessStats(db, root, "")
	return err
}

// SyncHarness is Sync with every imported document tagged with the harness
// whose memory root this is. An empty harness leaves documents.harness as it
// was (NULL for a new row).
func SyncHarness(db *sql.DB, root, harness string) error {
	_, err := SyncHarnessStats(db, root, harness)
	return err
}

// SyncHarnessStats is SyncHarness that also accumulates and returns
// criteria, mistakes, and deliverables statistics (skipped sections and database errors).
func SyncHarnessStats(db *sql.DB, root, harness string) (SyncStats, error) {
	resolvedRoot, cleanRoot, err := resolveRoot(root)
	if err != nil {
		return SyncStats{}, err
	}

	stats := SyncStats{}

	// Walk the RESOLVED root (not absRoot). Walking the unresolved path
	// when root is a symlink would make filepath.Rel(absRoot, cleanPath)
	// produce paths that start with the symlink's leaf name, which then
	// fail the cleanRoot prefix check below (and which would also break
	// the kind mapping, since the relpath would not match the documented
	// memory tree structure: state/, missions/<id>/, findings/, reference/, etc.).
	walkErr := filepath.WalkDir(resolvedRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return visit(db, resolvedRoot, cleanRoot, harness, path, d, &stats)
	})

	return stats, walkErr
}

// missionFiles are the per-mission documents SyncMission re-imports.
var missionFiles = []string{"BRIEF.md", "PROGRESS.md", "DEBRIEF.md"}

// SyncMission imports only missions/<missionID>/BRIEF.md, PROGRESS.md and
// DEBRIEF.md under root — the cheap, targeted re-import run after a mission
// update. Files that do not exist yet are skipped. missionID must be a single
// path segment. It discards the criteria, mistakes, and deliverables statistics; see SyncMissionStats.
func SyncMission(db *sql.DB, root, harness, missionID string) error {
	_, err := SyncMissionStats(db, root, harness, missionID)
	return err
}

// SyncMissionStats is SyncMission that also returns the criteria, mistakes, and deliverables statistics
// accumulated over the files it imported.
func SyncMissionStats(db *sql.DB, root, harness, missionID string) (SyncStats, error) {
	var stats SyncStats
	if missionID == "" || missionID != filepath.Base(missionID) || strings.HasPrefix(missionID, ".") {
		return stats, fmt.Errorf("invalid mission id %q", missionID)
	}
	resolvedRoot, cleanRoot, err := resolveRoot(root)
	if err != nil {
		return stats, err
	}
	for _, name := range missionFiles {
		path := filepath.Join(resolvedRoot, "missions", missionID, name)
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return stats, fmt.Errorf("stat %s: %w", path, err)
		}
		if err := visit(db, resolvedRoot, cleanRoot, harness, path, fs.FileInfoToDirEntry(info), &stats); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

// resolveRoot makes root absolute, checks it is a directory and resolves its
// symlinks. cleanRoot is the resolved root with a trailing separator, the
// prefix every visited path must carry.
func resolveRoot(root string) (resolvedRoot, cleanRoot string, err error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve memory root %q: %w", root, err)
	}

	rootInfo, err := os.Stat(absRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", fmt.Errorf("memory root %q does not exist", absRoot)
		}
		return "", "", fmt.Errorf("stat memory root %q: %w", absRoot, err)
	}
	if !rootInfo.IsDir() {
		return "", "", fmt.Errorf("memory root %q is not a directory", absRoot)
	}

	// Resolve root via EvalSymlinks so the prefix check below is stable
	// even when root itself is a symlink. (WalkDir does NOT follow
	// symlinks during traversal; we still evaluate them per-entry.)
	resolvedRoot, err = filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve memory root symlinks %q: %w", absRoot, err)
	}
	cleanRoot = filepath.Clean(resolvedRoot) + string(filepath.Separator)
	return resolvedRoot, cleanRoot, nil
}

// visit applies the path-safety checks to one walked entry and imports it if
// it is a Markdown or JSONL file, accumulating criteria, mistakes and deliverables statistics in stats.
func visit(db *sql.DB, resolvedRoot, cleanRoot, harness, path string, d fs.DirEntry, stats *SyncStats) error {
	cleanPath := filepath.Clean(path)

	// (a) Path safety: every visited path must be a lexical child
	// of resolved root. The trailing-separator trick prevents the
	// "/foo/memory" vs "/foo/memory-other" false-positive.
	if !strings.HasPrefix(cleanPath+string(filepath.Separator), cleanRoot) {
		return fmt.Errorf("path escapes memory root: %s", path)
	}

	// (b) Symlink escape check. WalkDir reports symlinks as
	// ModeSymlink DirEntry types; we resolve the target and verify
	// it lands inside root. Error rather than skip so a misconfigured
	// tree fails loudly instead of silently dropping content.
	if d.Type()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("resolve symlink %s: %w", path, err)
		}
		absResolved, err := filepath.Abs(resolved)
		if err != nil {
			return fmt.Errorf("abs symlink %s: %w", path, err)
		}
		if !strings.HasPrefix(filepath.Clean(absResolved)+string(filepath.Separator), cleanRoot) {
			return fmt.Errorf("symlink %s escapes memory root (resolves to %s)", path, absResolved)
		}
	}

	// Skip directories — WalkDir recurses automatically — except hidden
	// ones (.git, .claude, .opencode). A memory tree has none, so meeting one
	// means the root is a repository rather than its memory directory, and
	// importing it would file the whole repository under kinds like
	// ".claude".
	if d.IsDir() {
		if path != resolvedRoot && strings.HasPrefix(d.Name(), ".") {
			return fs.SkipDir
		}
		return nil
	}

	// Skip hidden files (.gitkeep, .DS_Store, ...).
	base := filepath.Base(cleanPath)
	if strings.HasPrefix(base, ".") {
		return nil
	}

	// relPath is computed from the RESOLVED root (same as WalkDir's
	// starting directory above) so a symlinked root produces a
	// well-formed relpath matching the memory tree structure (e.g.,
	// "state/...", "missions/<id>/PROGRESS.md") instead of one prefixed
	// with the symlink's leaf name.
	relPath, err := filepath.Rel(resolvedRoot, cleanPath)
	if err != nil {
		return fmt.Errorf("relative path %s: %w", path, err)
	}
	// Normalise to forward-slash so the kind mapping (and JSONL
	// source_path) is consistent across platforms.
	relPath = filepath.ToSlash(relPath)

	switch strings.ToLower(filepath.Ext(cleanPath)) {
	case ".md":
		return importMarkdown(db, cleanPath, relPath, harness, stats)
	case ".jsonl":
		return importJSONL(db, cleanPath, relPath, harness)
	default:
		return nil // unknown extension: skip silently
	}
}

// importMarkdown reads one Markdown file, classifies it by its path,
// and upserts it as a single document. CURRENT-MISSION.md gets a
// structured-field prefix prepended to its body (see package docblock).
func importMarkdown(db *sql.DB, absPath, relPath, harness string, stats *SyncStats) error {
	content, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", absPath, err)
	}
	return importMarkdownContent(db, absPath, relPath, harness, content, stats)
}

// ImportMarkdown imports one Markdown file's content as if the walker had
// found it at relPath under a memory root: the same kind, title, mission id,
// structured-field prefixes and upsert. sourcePath is stored as the document's
// unique key — the file's absolute path on the machine it lives on. It
// discards the criteria, mistakes, and deliverables statistics. No production code calls it: the document
// push route (POST /v1/documents/push) calls ImportMarkdownStats. It is kept as
// the error-only form of that function.
func ImportMarkdown(db *sql.DB, sourcePath, relPath, harness string, content []byte) error {
	_, err := ImportMarkdownStats(db, sourcePath, relPath, harness, content)
	return err
}

// ImportMarkdownStats is ImportMarkdown that also returns the criteria,
// mistakes, and deliverables statistics for this one file. For a BRIEF.md or DEBRIEF.md the upsert is
// followed by a refresh of mission_criteria (see storeMissionCriteria) and for a DEBRIEF.md
// a refresh of agent_mistakes (see mistakes.go) and mission_deliverables (see deliverables.go),
// whose failures are counted in the statistics and never returned as an error.
func ImportMarkdownStats(db *sql.DB, sourcePath, relPath, harness string, content []byte) (SyncStats, error) {
	var stats SyncStats
	err := importMarkdownContent(db, sourcePath, relPath, harness, content, &stats)
	return stats, err
}

// importMarkdownContent is the single implementation behind ImportMarkdown,
// ImportMarkdownStats and the walker: it upserts the document and, for a brief
// or debrief, accumulates the criteria refresh result into stats, and for a
// debrief, accumulates the mistakes and deliverables refresh results into stats.
func importMarkdownContent(db *sql.DB, sourcePath, relPath, harness string, content []byte, stats *SyncStats) error {
	absPath := sourcePath
	sha := sha256Hex(content)
	kind := kindFromPath(relPath)
	title := markdownTitle(string(content), filepath.Base(relPath))
	body := string(content)

	// CURRENT-MISSION.md and FLIGHT-RECORDER.md get the structured-field prefix;
	// the raw content is preserved below the prefix so a full-text search still
	// finds every original token.
	if filepath.Base(relPath) == "CURRENT-MISSION.md" {
		body = prependCurrentMissionFields(body)
	}
	if filepath.Base(relPath) == "FLIGHT-RECORDER.md" {
		body = prependFlightRecorderFields(body)
	}

	// Use absPath for source_path so multiple roots with the same relative
	// path do not collide on the UNIQUE constraint and silently overwrite.
	// The kind mapping and title extraction continue to use relPath.
	missionID := missionIDFromPath(relPath)
	if err := upsertDocument(db, absPath, title, kind, body, sha, missionID, harness); err != nil {
		return err
	}
	if kind == "brief" || kind == "debrief" {
		stats.add(storeMissionCriteria(db, missionID, harness, kind))
	}
	if kind == "debrief" {
		stats.add(storeAgentMistakes(db, missionID, harness))
		stats.add(storeMissionDeliverables(db, missionID, harness))
	}
	return nil
}

// missionIDFromPath returns <id> for missions/<id>/BRIEF.md, PROGRESS.md or
// DEBRIEF.md — the same paths kindFromPath files as mission metadata — and ""
// for anything else.
func missionIDFromPath(relPath string) string {
	switch kindFromPath(relPath) {
	case "brief", "progress", "debrief":
		return strings.Split(filepath.ToSlash(relPath), "/")[1]
	}
	return ""
}

// importJSONL reads one JSONL file line by line and upserts each
// non-blank line as a separate document with kind="flight-recorder". The
// source_path is "<abspath>:<line-number>" — using the absolute path ensures
// that multiple distinct roots with the same relative JSONL file do not
// collide on the UNIQUE constraint, and each line can be re-imported
// idempotently.
//
// JSON parsing is best-effort: a line that fails json.Unmarshal is
// still imported (raw line as body, filename as title) so unexpected
// event shapes do not silently drop content.
func importJSONL(db *sql.DB, absPath, relPath, harness string) error {
	f, err := os.Open(absPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", absPath, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Larger buffer than the 64 KiB default — notes can be long.
	scanner.Buffer(make([]byte, 1024*1024), MaxJSONLLineBytes)

	fallbackTitle := strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath))

	lineNo := 0
	for scanner.Scan() {
		lineNo++

		// Trim trailing CR (Windows line endings) + surrounding whitespace.
		line := strings.TrimSpace(strings.TrimRight(scanner.Text(), "\r"))
		if line == "" {
			continue
		}

		sha := sha256Hex([]byte(line))
		title := fallbackTitle
		body := line

		// Best-effort JSON parse — on success, compose body from the
		// flight_recorder table fields and use event as title.
		var ev struct {
			Event      string `json:"event"`
			MissionID  string `json:"mission_id"`
			Step       string `json:"step"`
			Agent      string `json:"agent"`
			Note       string `json:"note"`
			OccurredAt string `json:"occurred_at"`
		}
		if jerr := json.Unmarshal([]byte(line), &ev); jerr == nil {
			if ev.Event != "" {
				title = ev.Event
			}
			parts := make([]string, 0, 5)
			if ev.MissionID != "" {
				parts = append(parts, "mission_id: "+ev.MissionID)
			}
			if ev.Step != "" {
				parts = append(parts, "step: "+ev.Step)
			}
			if ev.Agent != "" {
				parts = append(parts, "agent: "+ev.Agent)
			}
			if ev.Event != "" {
				parts = append(parts, "event: "+ev.Event)
			}
			if ev.Note != "" {
				parts = append(parts, "note: "+ev.Note)
			}
			if ev.OccurredAt != "" {
				parts = append(parts, "occurred_at: "+ev.OccurredAt)
			}
			if len(parts) > 0 {
				body = strings.Join(parts, "\n")
			}
		}

		sourcePath := fmt.Sprintf("%s:%d", absPath, lineNo)
		if uerr := upsertDocument(db, sourcePath, title, "flight-recorder", body, sha, "", harness); uerr != nil {
			return fmt.Errorf("upsert %s line %d: %w", relPath, lineNo, uerr)
		}
	}

	if serr := scanner.Err(); serr != nil {
		return fmt.Errorf("scan %s: %w", absPath, serr)
	}
	return nil
}

// upsertDocument inserts or updates one documents row + its paired
// documents_fts row inside a single transaction. The FTS5 rowid equals
// the documents.id (Step 4 carry-forward contract — see package
// docblock) so /v1/memory/search can join them.
//
// Behaviour by existing-row state:
//
//   - Row missing      → INSERT documents, then INSERT documents_fts
//     with rowid = LastInsertId().
//   - Row present,     → no-op (commit empty tx to release).
//     sha256 matches
//   - Row present,     → UPDATE documents, DELETE documents_fts(rowid),
//     sha256 differs    INSERT documents_fts(rowid) again.
//
// Any SQL error rolls the whole transaction back so the two tables
// stay consistent.
//
// missionID and harness are written on every path, including the unchanged
// one, so a re-sync fills them on rows imported before the columns existed.
// An empty harness keeps the stored value.
func upsertDocument(db *sql.DB, sourcePath, title, kind, body, sha, missionID, harness string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx for %s: %w", sourcePath, err)
	}
	// Rollback is a no-op after a successful Commit.
	defer tx.Rollback()

	var (
		existingID  int64
		existingSHA string
	)
	err = tx.QueryRow(
		`SELECT id, sha256 FROM documents WHERE source_path = ?`,
		sourcePath,
	).Scan(&existingID, &existingSHA)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		// INSERT path: new source_path.
		result, ierr := tx.Exec(
			`INSERT INTO documents
			        (source_path, title, kind, body, sha256, mission_id, harness,
			         imported_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?,
			         CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			sourcePath, title, kind, body, sha, nullIfEmpty(missionID), nullIfEmpty(harness),
		)
		if ierr != nil {
			return fmt.Errorf("insert documents %s: %w", sourcePath, ierr)
		}
		id, lerr := result.LastInsertId()
		if lerr != nil {
			return fmt.Errorf("last insert id %s: %w", sourcePath, lerr)
		}
		if _, ferr := tx.Exec(
			`INSERT INTO documents_fts (rowid, title, body) VALUES (?, ?, ?)`,
			id, title, body,
		); ferr != nil {
			return fmt.Errorf("insert documents_fts %s: %w", sourcePath, ferr)
		}

	case err != nil:
		return fmt.Errorf("lookup documents %s: %w", sourcePath, err)

	default:
		// Existing row: skip if unchanged, otherwise update both tables.
		if existingSHA == sha {
			// Content unchanged: leave the FTS row alone, but keep the
			// kind and the mission and harness tags current. The kind can
			// be stale when the file was first imported from a root at the
			// wrong level.
			if _, uerr := tx.Exec(
				`UPDATE documents
				    SET kind = ?, mission_id = ?, harness = COALESCE(?, harness)
				  WHERE id = ?
				    AND (kind IS NOT ? OR mission_id IS NOT ?
				         OR (? IS NOT NULL AND harness IS NOT ?))`,
				kind, nullIfEmpty(missionID), nullIfEmpty(harness), existingID,
				kind, nullIfEmpty(missionID), nullIfEmpty(harness), nullIfEmpty(harness),
			); uerr != nil {
				return fmt.Errorf("tag documents %s: %w", sourcePath, uerr)
			}
			if cerr := tx.Commit(); cerr != nil {
				return fmt.Errorf("commit no-op %s: %w", sourcePath, cerr)
			}
			return nil
		}

		if _, uerr := tx.Exec(
			`UPDATE documents
			    SET title = ?, kind = ?, body = ?, sha256 = ?,
			        mission_id = ?, harness = COALESCE(?, harness),
			        updated_at = CURRENT_TIMESTAMP
			  WHERE id = ?`,
			title, kind, body, sha, nullIfEmpty(missionID), nullIfEmpty(harness), existingID,
		); uerr != nil {
			return fmt.Errorf("update documents %s: %w", sourcePath, uerr)
		}

		// FTS5 has no UPSERT; delete the old row then insert the new one.
		// Both share rowid = documents.id so the search join key holds.
		if _, derr := tx.Exec(
			`DELETE FROM documents_fts WHERE rowid = ?`,
			existingID,
		); derr != nil {
			return fmt.Errorf("delete documents_fts %s: %w", sourcePath, derr)
		}
		if _, ierr := tx.Exec(
			`INSERT INTO documents_fts (rowid, title, body) VALUES (?, ?, ?)`,
			existingID, title, body,
		); ierr != nil {
			return fmt.Errorf("reinsert documents_fts %s: %w", sourcePath, ierr)
		}
	}

	if cerr := tx.Commit(); cerr != nil {
		return fmt.Errorf("commit %s: %w", sourcePath, cerr)
	}
	return nil
}

// nullIfEmpty maps "" to SQL NULL.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// sha256Hex returns the lowercase hex-encoded SHA-256 of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// markdownTitle returns the first H1 heading in content (lines starting
// with "# "), or the filename without extension if no heading exists.
func markdownTitle(content, fallback string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	if ext := filepath.Ext(fallback); ext != "" {
		fallback = strings.TrimSuffix(fallback, ext)
	}
	return fallback
}

// kindFromPath classifies a Markdown file by its path relative to
// root. See the package docblock for the full mapping table.
//
// The relPath is assumed to be forward-slash normalised (Sync calls
// filepath.ToSlash before passing it down). Splitting on "/" keeps the
// kind mapping stable across platforms.
func kindFromPath(relPath string) string {
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	base := parts[len(parts)-1]

	// missions/<id>/BRIEF.md     -> "brief"
	// missions/<id>/PROGRESS.md  -> "progress"
	// missions/<id>/DEBRIEF.md   -> "debrief"
	// findings/PATTERNS.md       -> "patterns"
	// findings/service-records/<name>.md -> "service-record"
	// reference/DIRECTIVES.md    -> "directives"
	//
	// These special cases apply only at exactly the specified nesting
	// level. A missions/<id>/<sub>/BRIEF.md (4+ parts) falls through to
	// "missions". A bare missions/BRIEF.md (2 parts) falls through to
	// "missions". These files are NOT mission metadata.
	if len(parts) == 3 && parts[0] == "missions" {
		switch base {
		case "BRIEF.md":
			return "brief"
		case "PROGRESS.md":
			return "progress"
		case "DEBRIEF.md":
			return "debrief"
		}
	}

	if len(parts) == 2 && parts[0] == "findings" {
		switch base {
		case "PATTERNS.md":
			return "patterns"
		}
	}

	if len(parts) == 3 && parts[0] == "findings" && parts[1] == "service-records" {
		return "service-record"
	}

	if len(parts) == 2 && parts[0] == "reference" {
		switch base {
		case "DIRECTIVES.md":
			return "directives"
		}
	}

	// Everything else: the top-level directory is the kind. A bare
	// filename with no parent directory falls back to "document".
	if len(parts) >= 2 {
		return parts[0]
	}
	return "document"
}

// currentMissionKnownFields is the set of structured field keys the
// CURRENT-MISSION.md parser recognises (per
// .claude/templates/state/STATE-FILE-TEMPLATE.md). Exactly six fields
// are recognized; Project and Notes from the old ACTIVE-TASK.md are dropped.
var currentMissionKnownFields = map[string]bool{
	"Mission ID":   true,
	"Status":       true,
	"Owner":        true,
	"Next Action":  true,
	"Last Updated": true,
	"Blockers":     true,
}

// prependCurrentMissionFields extracts the recognised field list from
// CURRENT-MISSION.md content and prepends a duplicated block at the top
// of the body. The raw file content is preserved verbatim below the
// prefix.
//
// Rationale: the bullet fields are already searchable in their
// original position, but prepending them as "Key: Value" lines (no
// leading "- ", no template preamble) makes them findable for agents
// that search for the bare key/value pairs (e.g. "Mission ID: mission-20260904-01").
//
// The first occurrence of each key wins (later duplicates in the
// file are ignored) and only recognised keys are extracted — unknown
// bullet items are left in the raw content.
func prependCurrentMissionFields(body string) string {
	var fields []string
	seen := map[string]bool{}

	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		rest := strings.TrimPrefix(line, "- ")
		colon := strings.Index(rest, ":")
		if colon <= 0 || colon == len(rest)-1 {
			continue
		}
		key := strings.TrimSpace(rest[:colon])
		val := strings.TrimSpace(rest[colon+1:])
		if !currentMissionKnownFields[key] || seen[key] {
			continue
		}
		seen[key] = true
		fields = append(fields, fmt.Sprintf("%s: %s", key, val))
	}

	if len(fields) == 0 {
		return body
	}

	var b strings.Builder
	b.WriteString("<!-- Structured Fields (extracted from CURRENT-MISSION.md) -->\n")
	for _, f := range fields {
		b.WriteString(f)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(body)
	return b.String()
}

// prependFlightRecorderFields extracts the structured rows from
// FLIGHT-RECORDER.md's table and prepends them as indexed lines at the top
// of the body. The raw file content is preserved verbatim below the prefix.
//
// Escaping rule (applied uniformly in one pass):
//   - \\ → literal backslash; the following character is NOT treated as escaped
//   - \| → literal pipe (not a delimiter)
//   - \ followed by anything else → literal backslash and that character
//   - bare, unescaped | → cell delimiter
//
// Design: rows are not split into one document each. FTS5 already indexes the
// whole file body so rows are already findable; the gain would be snippet
// granularity only. Meanwhile MISSION-ARCHIVE.md and PROGRESS.md are also
// tables, and splitting one table but not the others would invent an
// inconsistency. The genuinely valuable version of this idea is ingesting
// journal rows into the flight_recorder table so they can be queried through
// /v1/flight-recorder — that is a real capability, it is not what the document
// importer is for, and it is out of scope for this mission.
func prependFlightRecorderFields(body string) string {
	var (
		rows    []string
		skipped int
	)

	for _, rawLine := range strings.Split(body, "\n") {
		line := strings.TrimSpace(rawLine)

		// Skip empty lines
		if line == "" {
			continue
		}

		// Skip non-table lines (must start with |)
		if !strings.HasPrefix(line, "|") {
			continue
		}

		// Check if this is a separator row: split the line into cells,
		// then verify each cell (trimmed) consists only of - and : characters.
		// This handles GitHub-flavoured Markdown separators like:
		//   |-|-|, |--|--|, | :--- | ---: |, etc.
		cells := splitFlightRecorderCells(line)
		if isSeparatorRow(cells) {
			continue
		}

		// We expect exactly 8 cells: empty, cell1, cell2, cell3, cell4, cell5, cell6, empty
		if len(cells) != 8 {
			skipped++
			continue
		}

		// Extract and trim the six cells
		var (
			timestamp = strings.TrimSpace(cells[1])
			missionID = strings.TrimSpace(cells[2])
			step      = strings.TrimSpace(cells[3])
			agent     = strings.TrimSpace(cells[4])
			event     = strings.TrimSpace(cells[5])
			note      = strings.TrimSpace(cells[6])
		)

		// Skip the header row: first cell is "Timestamp"
		if timestamp == "Timestamp" {
			continue
		}

		// Skip rows that don't have meaningful content
		if timestamp == "" || missionID == "" {
			skipped++
			continue
		}

		// Compose a readable line from the parsed fields
		parts := []string{
			timestamp,
			missionID,
			step,
			agent,
			event,
			note,
		}
		rows = append(rows, strings.Join(parts, " | "))
	}

	// Always emit the skipped marker if rows were skipped, even if no rows parsed.
	// Only skip emitting the marker if nothing was skipped AND no rows parsed.
	if len(rows) == 0 && skipped == 0 {
		return body
	}

	var b strings.Builder
	b.WriteString("<!-- Structured Journal Rows (extracted from FLIGHT-RECORDER.md) -->\n")
	for _, row := range rows {
		b.WriteString(row)
		b.WriteString("\n")
	}
	if skipped > 0 {
		noun := "row"
		if skipped > 1 {
			noun = "rows"
		}
		b.WriteString(fmt.Sprintf("<!-- %d malformed %s skipped -->\n", skipped, noun))
	}
	b.WriteString("\n")
	b.WriteString(body)
	return b.String()
}

// splitFlightRecorderCells splits a flight recorder table row on pipes,
// applying standard backslash escaping in a single pass. This handles:
//   - \\ → literal backslash (next character is not treated as escaped)
//   - \| → literal pipe (not a delimiter)
//   - \ followed by anything else → both backslash and character, literally
//   - bare | → cell delimiter
func splitFlightRecorderCells(line string) []string {
	var cells []string
	var current strings.Builder
	runes := []rune(line)

	for i := 0; i < len(runes); i++ {
		ch := runes[i]

		// Handle backslash escaping
		if ch == '\\' && i+1 < len(runes) {
			next := runes[i+1]
			if next == '\\' {
				// \\ → literal backslash; skip the escape sequence and
				// do not treat what follows as escaped
				current.WriteRune('\\')
				i++ // skip the second backslash
			} else if next == '|' {
				// \| → literal pipe (not a delimiter)
				current.WriteRune('|')
				i++ // skip the pipe
			} else {
				// \ followed by anything else → both characters literally
				current.WriteRune('\\')
				current.WriteRune(next)
				i++ // skip the next character
			}
		} else if ch == '|' {
			// Unescaped pipe: this is a cell delimiter
			cells = append(cells, current.String())
			current.Reset()
		} else {
			current.WriteRune(ch)
		}
	}

	// Add the final cell (after the last pipe)
	cells = append(cells, current.String())
	return cells
}

// isSeparatorRow checks whether the given cells constitute a Markdown table
// separator row. A separator row has at least 1 cell (after trimming leading
// and trailing empty cells from the | delimiters), and each non-empty cell
// (trimmed) consists only of dashes (-) and colons (:), with at least one dash.
//
// NOTE: This function performs structural validation regardless of position in
// the file. A pipe-delimited line matching the separator pattern anywhere in the
// content would be treated as a separator row. However, this is unreachable
// against real FLIGHT-RECORDER.md journal content because the template uses
// em-dash (—) for the Step cell on complete and blocked rows, not ASCII hyphens.
// The limitation is documented rather than adding positional anchoring, which
// would add complexity for an unreachable case.
func isSeparatorRow(cells []string) bool {
	// A valid table has at least 3 cells (leading empty, content, trailing empty).
	if len(cells) < 3 {
		return false
	}

	// Trim leading and trailing empty cells
	start := 0
	for start < len(cells) && strings.TrimSpace(cells[start]) == "" {
		start++
	}
	end := len(cells) - 1
	for end >= 0 && strings.TrimSpace(cells[end]) == "" {
		end--
	}

	// After trimming empties, we need at least one content cell
	if start > end {
		return false
	}

	// Every remaining cell must be a valid separator: only - and :, with at least one -
	for i := start; i <= end; i++ {
		trimmed := strings.TrimSpace(cells[i])
		if trimmed == "" {
			return false // empty cell in the middle
		}
		hasDash := false
		for _, r := range trimmed {
			if r == '-' {
				hasDash = true
			} else if r != ':' {
				return false // invalid character
			}
		}
		if !hasDash {
			return false // no dash at all
		}
	}

	return true
}

// Package importer walks the agent's memory tree and ingests its content
// into the documents + documents_fts (FTS5) tables.
//
// Sync is the entry point invoked by the /v1/documents/sync handler. It
// is the only writer to those two tables (the importer controls the
// documents lifecycle; there are no AFTER INSERT/UPDATE/DELETE triggers
// on documents_fts — see db/schema.sql for the rationale).
//
// What gets imported:
//
//   - Markdown files (*.md) become one document per file. The document's
//     kind is derived from its path relative to root:
//
//     state/ACTIVE-TASK.md                  -> "state"
//     state/EVENT-LOG.md                    -> "state"
//     tasks/<id>/CONTEXT.md                 -> "context"   (EXACTLY one
//     tasks/<id>/PROGRESS.md                -> "progress"   dir under tasks/
//     improvements/IMPROVEMENTS.md          -> "improvements"
//     graph/<anything>.md                   -> "graph"
//     reference/<anything>.md               -> "reference"
//     agent-documents/<anything>.md         -> "agent-documents"
//     <top-level>/<file>.md                 -> "<top-level>"
//     <root>/<file>.md                      -> "document" (fallback)
//
//     The "context"/"progress" mapping is intentionally tight: it
//     applies only to exactly tasks/<id>/CONTEXT.md and
//     tasks/<id>/PROGRESS.md. A tasks/CONTEXT.md (no <id>) or a
//     tasks/<id>/<sub>/CONTEXT.md (deeper nested) falls through to
//     the top-level-directory-name kind (or "document" fallback) —
//     those files are NOT project context/progress.
//
//     The ACTIVE-TASK.md parser additionally extracts the structured
//     field list (Task ID / Project / Status / Owner / Next Action /
//     Last Updated / Blockers / Notes — per
//     .opencode/templates/state/STATE-FILE-TEMPLATE.md) and prepends a
//     duplicated block at the top of the body so the values are
//     searchable both in their original position and as a single
//     structured block. The raw file content is preserved verbatim
//     below the prefix.
//
//   - JSONL files (*.jsonl) are split line by line; each non-blank line
//     becomes one document with kind="event". Title is the parsed
//     event_type (or the filename if parsing fails); body is a
//     summary + note + task_id composition (or the raw line as a
//     fallback); source_path is "<relpath>:<line-number>" so each line
//     is unique.
//
// What does NOT get imported:
//
//   - The events, tasks, task_runs, agents, and improvements tables are
//     owned by the live HTTP API (POST /v1/events, /v1/tasks, etc.).
//     The importer only populates the search index — it never writes
//     to those tables.
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
// default maximum plus headroom for large event summaries.
const MaxJSONLLineBytes = 16 * 1024 * 1024

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
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve memory root %q: %w", root, err)
	}

	rootInfo, err := os.Stat(absRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("memory root %q does not exist", absRoot)
		}
		return fmt.Errorf("stat memory root %q: %w", absRoot, err)
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("memory root %q is not a directory", absRoot)
	}

	// Resolve root via EvalSymlinks so the prefix check below is stable
	// even when root itself is a symlink. (WalkDir does NOT follow
	// symlinks during traversal; we still evaluate them per-entry.)
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return fmt.Errorf("resolve memory root symlinks %q: %w", absRoot, err)
	}
	cleanRoot := filepath.Clean(resolvedRoot) + string(filepath.Separator)

	// Walk the RESOLVED root (not absRoot). Walking the unresolved path
	// when root is a symlink would make filepath.Rel(absRoot, cleanPath)
	// produce paths that start with the symlink's leaf name, which then
	// fail the cleanRoot prefix check below (and which would also break
	// the kind mapping, since the relpath would not match the documented
	// "state/", "tasks/<id>/", etc. layout).
	walkErr := filepath.WalkDir(resolvedRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

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

		// Skip directories — WalkDir recurses automatically.
		if d.IsDir() {
			return nil
		}

		// Skip hidden files (.gitkeep, .DS_Store, ...).
		base := filepath.Base(cleanPath)
		if strings.HasPrefix(base, ".") {
			return nil
		}

		// relPath is computed from the RESOLVED root (same as WalkDir's
		// starting directory above) so a symlinked root produces a
		// well-formed "state/...", "tasks/<id>/CONTEXT.md" relpath
		// instead of one prefixed with the symlink's leaf name.
		relPath, err := filepath.Rel(resolvedRoot, cleanPath)
		if err != nil {
			return fmt.Errorf("relative path %s: %w", path, err)
		}
		// Normalise to forward-slash so the kind mapping (and JSONL
		// source_path) is consistent across platforms.
		relPath = filepath.ToSlash(relPath)

		switch strings.ToLower(filepath.Ext(cleanPath)) {
		case ".md":
			return importMarkdown(db, cleanPath, relPath)
		case ".jsonl":
			return importJSONL(db, cleanPath, relPath)
		default:
			return nil // unknown extension: skip silently
		}
	})

	return walkErr
}

// importMarkdown reads one Markdown file, classifies it by its path,
// and upserts it as a single document. ACTIVE-TASK.md gets a
// structured-field prefix prepended to its body (see package docblock).
func importMarkdown(db *sql.DB, absPath, relPath string) error {
	content, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", absPath, err)
	}

	sha := sha256Hex(content)
	kind := kindFromPath(relPath)
	title := markdownTitle(string(content), filepath.Base(relPath))
	body := string(content)

	// ACTIVE-TASK.md gets the structured-field prefix; the raw content
	// is preserved below the prefix so a full-text search still finds
	// every original token.
	if filepath.Base(relPath) == "ACTIVE-TASK.md" {
		body = prependActiveTaskFields(body)
	}

	return upsertDocument(db, absPath, title, kind, body, sha)
}

// importJSONL reads one JSONL file line by line and upserts each
// non-blank line as a separate document with kind="event". The
// source_path is "<relpath>:<line-number>" so each line is unique and
// can be re-imported idempotently.
//
// JSON parsing is best-effort: a line that fails json.Unmarshal is
// still imported (raw line as body, filename as title) so unexpected
// event shapes do not silently drop content.
func importJSONL(db *sql.DB, absPath, relPath string) error {
	f, err := os.Open(absPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", absPath, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Larger buffer than the 64 KiB default — event summaries can be long.
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

		// Best-effort JSON parse — on success, normalise body to
		// "summary\nnote\ntask_id: <id>" and use event_type as title.
		var ev struct {
			EventType string `json:"event_type"`
			Summary   string `json:"summary"`
			Note      string `json:"note"`
			TaskID    string `json:"task_id"`
		}
		if jerr := json.Unmarshal([]byte(line), &ev); jerr == nil {
			if ev.EventType != "" {
				title = ev.EventType
			}
			parts := make([]string, 0, 3)
			if ev.Summary != "" {
				parts = append(parts, ev.Summary)
			}
			if ev.Note != "" {
				parts = append(parts, ev.Note)
			}
			if ev.TaskID != "" {
				parts = append(parts, "task_id: "+ev.TaskID)
			}
			if len(parts) > 0 {
				body = strings.Join(parts, "\n")
			}
		}

		sourcePath := fmt.Sprintf("%s:%d", relPath, lineNo)
		if uerr := upsertDocument(db, sourcePath, title, "event", body, sha); uerr != nil {
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
func upsertDocument(db *sql.DB, sourcePath, title, kind, body, sha string) error {
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
			        (source_path, title, kind, body, sha256,
			         imported_at, updated_at)
			 VALUES (?, ?, ?, ?, ?,
			         CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			sourcePath, title, kind, body, sha,
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
			// No-op — commit (no-op) so the deferred Rollback releases cleanly.
			if cerr := tx.Commit(); cerr != nil {
				return fmt.Errorf("commit no-op %s: %w", sourcePath, cerr)
			}
			return nil
		}

		if _, uerr := tx.Exec(
			`UPDATE documents
			    SET title = ?, kind = ?, body = ?, sha256 = ?,
			        updated_at = CURRENT_TIMESTAMP
			  WHERE id = ?`,
			title, kind, body, sha, existingID,
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

	// tasks/<id>/CONTEXT.md  -> "context"
	// tasks/<id>/PROGRESS.md -> "progress"
	//
	// The kind follows the filename, but ONLY at exactly one directory
	// level under tasks/ (so relPath has 3 forward-slash-separated
	// parts: "tasks", "<id>", "CONTEXT.md|PROGRESS.md"). A
	// tasks/<id>/<sub>/CONTEXT.md (4 parts) or a bare tasks/CONTEXT.md
	// (2 parts) falls through to the top-level-directory kind rule
	// below — those files are NOT project context/progress.
	if len(parts) == 3 && parts[0] == "tasks" {
		switch base {
		case "CONTEXT.md":
			return "context"
		case "PROGRESS.md":
			return "progress"
		}
	}

	// Everything else: the top-level directory is the kind. A bare
	// filename with no parent directory falls back to "document".
	if len(parts) >= 2 {
		return parts[0]
	}
	return "document"
}

// activeTaskKnownFields is the set of structured field keys the
// ACTIVE-TASK.md parser recognises (per
// .opencode/templates/state/STATE-FILE-TEMPLATE.md).
var activeTaskKnownFields = map[string]bool{
	"Task ID":      true,
	"Project":      true,
	"Status":       true,
	"Owner":        true,
	"Next Action":  true,
	"Last Updated": true,
	"Blockers":     true,
	"Notes":        true,
}

// prependActiveTaskFields extracts the recognised field list from
// ACTIVE-TASK.md content and prepends a duplicated block at the top
// of the body. The raw file content is preserved verbatim below the
// prefix.
//
// Rationale: the bullet fields are already searchable in their
// original position, but prepending them as "Key: Value" lines (no
// leading "- ", no template preamble) makes them findable for agents
// that search for the bare key/value pairs (e.g. "Task ID: task-20260820-02").
//
// The first occurrence of each key wins (later duplicates in the
// file are ignored) and only recognised keys are extracted — unknown
// bullet items are left in the raw content.
func prependActiveTaskFields(body string) string {
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
		if !activeTaskKnownFields[key] || seen[key] {
			continue
		}
		seen[key] = true
		fields = append(fields, fmt.Sprintf("%s: %s", key, val))
	}

	if len(fields) == 0 {
		return body
	}

	var b strings.Builder
	b.WriteString("<!-- Structured Fields (extracted from ACTIVE-TASK.md) -->\n")
	for _, f := range fields {
		b.WriteString(f)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(body)
	return b.String()
}

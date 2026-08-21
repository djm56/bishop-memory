// Package importer — tests for Sync.
//
// TestSync is the binding functional check for Step 5 (and the
// end-to-end Step 6 CRITICAL regression check): it opens a fresh
// temp-file SQLite DB, applies db/schema.sql via the production
// store.ApplySchema (so any regression in the SQL splitter is caught
// here), runs Sync against the testdata/memory fixture, and asserts:
//
//  1. document count == 11 (the exact fixture size — tighter than
//     Step 5's "count > 0" so a partial import or a fixture drift is
//     caught), and
//  2. EVERY documents row has a paired documents_fts row whose rowid
//     equals documents.id (the Step 4 carry-forward FTS5 rowid
//     contract — without this, /v1/memory/search returns empty).
//
// TestSyncIdempotent additionally calls Sync twice and asserts the
// document count is unchanged (sha256 idempotency).
package importer

import (
	"database/sql"
	"path/filepath"
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

	// (1) document count == exact fixture size. 7 .md files
	// (state/{ACTIVE-TASK,EVENT-LOG}.md, graph/OVERVIEW.md,
	// improvements/IMPROVEMENTS.md, reference/sqlite-wal.md,
	// tasks/task-20260820-02/{CONTEXT,PROGRESS}.md) + 4 non-blank
	// EVENT-STREAM.jsonl lines = 11.
	var docCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&docCount); err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if docCount != 11 {
		t.Fatalf("Sync imported %d documents, want exactly 11 (fixture drift or partial import)", docCount)
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
	if firstCount != 11 {
		t.Fatalf("first Sync imported %d docs, want exactly 11", firstCount)
	}

	t.Logf("idempotency holds: two Sync() calls produced %d documents each", firstCount)
}

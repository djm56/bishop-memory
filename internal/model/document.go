// Package model — Document is one imported Markdown (or JSONL) file
// from the agent's memory tree. Documents are indexed into an FTS5
// virtual table (documents_fts, see db/schema.sql) so /v1/memory/search
// can return ranked hits.
//
// SearchResult augments Document with the FTS5 bm25 rank + snippet.
// SyncRequest is the JSON body for POST /v1/documents/sync.
package model

// Document is one imported file. Fields map directly onto the documents
// table columns; JSON tags mirror the API wire format.
//
// SourcePath is the absolute (post-Clean) path used for idempotent
// upserts. SHA256 is the content hash used by the importer to skip
// unchanged files on re-sync.
type Document struct {
	// ID is the autoincrement primary key from the documents table.
	ID int64 `json:"id"`

	// SourcePath is the absolute path of the imported file, unique per
	// row (UNIQUE constraint in db/schema.sql).
	SourcePath string `json:"source_path"`

	// Title is the document heading (first H1 for Markdown) when present.
	Title string `json:"title,omitempty"`

	// Kind classifies the source. For Markdown, it is the top-level
	// directory name under the memory root ("graph", "reference",
	// "improvements", "agent-documents", "state", ...), or the
	// special-cased "context" / "progress" for exactly
	// tasks/<id>/{CONTEXT,PROGRESS}.md, or the fallback "document" for a
	// bare top-level file. Every JSONL line gets the fixed kind "event".
	// See internal/importer/importer.go kindFromPath for the exact
	// mapping — this list is illustrative, not exhaustive, and "kind"
	// values are never "active-task" or "event-stream" (those do not
	// occur; state/ACTIVE-TASK.md's kind is "state", and JSONL lines
	// are "event").
	Kind string `json:"kind,omitempty"`

	// Body is the full indexed content (Markdown body or JSONL line).
	Body string `json:"body"`

	// SHA256 is the hex-encoded SHA-256 of the file bytes, used for
	// incremental sync (re-importing unchanged docs is a no-op).
	SHA256 string `json:"sha256"`

	// ImportedAt is the SQLite CURRENT_TIMESTAMP of the first import.
	ImportedAt string `json:"imported_at"`

	// UpdatedAt is the SQLite CURRENT_TIMESTAMP of the last upsert.
	UpdatedAt string `json:"updated_at"`
}

// SearchResult is a Document returned by /v1/memory/search, augmented
// with the FTS5 bm25 rank and a snippet built from the matched terms.
//
// Document is embedded (not a pointer) so JSON clients receive a flat
// shape: { id, source_path, title, ..., rank, snippet }.
type SearchResult struct {
	// Document embeds the matched document so clients can access all
	// fields without a follow-up fetch.
	Document

	// Rank is the bm25 score returned by FTS5 (lower = more relevant).
	Rank float64 `json:"rank,omitempty"`

	// Snippet is the FTS5 snippet() output with the matched terms
	// wrapped in <mark> tags (rendered client-side).
	Snippet string `json:"snippet,omitempty"`
}

// SyncRequest is the JSON body for POST /v1/documents/sync. The
// importer uses Root as the MEMORY_ROOT override; when empty it falls
// back to the configured default (env MEMORY_ROOT or testdata fixture).
type SyncRequest struct {
	// Root is an optional override of the configured memory root.
	// Bounded only by the underlying filesystem; no length check here
	// because the importer validates path safety at walk time.
	Root string `json:"root,omitempty" binding:"omitempty"`
}

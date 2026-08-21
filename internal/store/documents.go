// Package store — data access for the documents + FTS5 search index.
//
// Documents are imported from the agent's .claude/memory tree
// (graph/, reference/, improvements/, agent-documents/) and indexed via
// an FTS5 virtual table so /v1/memory/search can return ranked hits.
//
// TODO: implement in Phase 2 (see project_memory.md).
package store

// TODO: implement in Phase 2 — typed helpers for documents + FTS5:
//   UpsertDocument(ctx, doc model.Document) error
//   SearchDocuments(ctx, query string, limit int) ([]model.SearchHit, error)
//   DeleteAllDocuments(ctx) error

# Task Context: task-20260820-02

This is the sample CONTEXT file shipped with the bishop-memory importer
test fixture. It exercises the `tasks/<id>/CONTEXT.md` → kind="context"
mapping.

## Background

bishop-memory is a Go + Gin + SQLite + FTS5 service for agent memory.
Phase 2 finishes the Phase 1 scaffold and adds an importer that walks
the memory tree, populates a search index, and powers /v1/memory/search.

## Key decisions

- Single-writer SQLite (SetMaxOpenConns(1)) to avoid "database is locked".
- Standalone FTS5 table (no external-content + triggers); the importer
  is the single writer and pairs documents_fts.rowid = documents.id in
  the same transaction.
- Markdown body is the whole file; ACTIVE-TASK.md gets a duplicated
  structured-field block prepended for searchability.

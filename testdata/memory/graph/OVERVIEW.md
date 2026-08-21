# Sample Graph Reference

This file exercises the top-level directory kind mapping. The
importer derives the document kind from the top-level directory
relative to MEMORY_ROOT, so `graph/<file>.md` → kind="graph".

## Purpose

Bishop-memory is the canonical memory service for the Bishop agent
crew. The importer walks the memory tree and indexes every Markdown
file plus every JSONL line into the FTS5 search index so agents can
query past context with /v1/memory/search.

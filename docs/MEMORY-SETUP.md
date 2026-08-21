# Memory System Setup and Usage

This guide covers how bishop-memory stores and retrieves agent memory, and how to use the service day-to-day.

## What Gets Stored

The service maintains **seven tables** in SQLite:

| Table | Purpose | Rows | Notes |
|-------|---------|------|-------|
| `tasks` | Durable task records (the API's primary object) | ~1 per agent task | Title, status (open/active/blocked/complete/cancelled), priority, next_action, blockers, timestamps |
| `task_runs` | One row per agent execution attempt against a task | ~1 per step in a task | Agent identity, status (e.g., started, completed, failed), optional timestamps |
| `events` | Append-only audit log (task.*, agent.*, improvement.* events) | ~N per day | Event type (dotted discriminator), summary, optional agent identity, timestamps |
| `agents` | Registered agent identities | ~10 | Name, role, registration timestamp. Phase 3 stub. |
| `improvements` | Durable improvement records (mirrors `.claude/memory/improvements`) | ~20-50 | Title, status, body, timestamps. Phase 3 stub. |
| `documents` | Imported Markdown/JSONL files and their lines | ~100-1000+ | Source path, title, kind (document type), body (full text), SHA-256 hash, timestamps |
| `documents_fts` | FTS5 search index over documents | ~100-1000+ | Virtual table (no separate storage) — allows full-text search via `memory_search` |

The `documents` and `documents_fts` tables are populated by the **importer** (seeded via `/v1/documents/sync`). The rest are populated by API calls and handlers.

### Document Kinds

When the importer walks your memory tree, it assigns a **kind** to each document based on its path:

| Path | Kind | Example |
|------|------|---------|
| `state/ACTIVE-TASK.md` | `state` | Current task metadata |
| `state/EVENT-LOG.md` | `state` | Event log snapshot |
| `tasks/<id>/CONTEXT.md` | `context` | Task context (only exact match) |
| `tasks/<id>/PROGRESS.md` | `progress` | Task progress tracker |
| `improvements/IMPROVEMENTS.md` | `improvements` | Improvement records |
| `graph/<file>.md` | `graph` | Project dependency graphs and symbols |
| `reference/<file>.md` | `reference` | Conventions, patterns, style guides |
| `agent-documents/<file>.md` | `agent-documents` | Temporary task-working documents |
| `<top-level>/<file>.md` | `<top-level>` | Directory-based fallback (e.g., `README.md` → `README`) |
| `<root>/<file>.md` | `document` | Fallback for top-level Markdown files |

The `context` and `progress` kinds apply **only** to exactly `tasks/<id>/CONTEXT.md` and `tasks/<id>/PROGRESS.md`. Nested variants (e.g., `tasks/<id>/sub/CONTEXT.md`) fall through to the directory-name kind.

## Seeding the Index from Existing Memory

The first time you start bishop-memory, the database is empty. To populate it with your existing `.claude/memory/` tree:

### Step 1: Start the Service

```bash
make run
# or, if installed as a service:
# systemctl start bishop-memory.service
```

### Step 2: Trigger a Full Import

```bash
# Default: imports from testdata/memory
curl -X POST http://127.0.0.1:8787/v1/documents/sync -H 'Content-Type: application/json' -d '{}'

# Or specify a custom root (e.g., your actual memory tree):
curl -X POST http://127.0.0.1:8787/v1/documents/sync \
  -H 'Content-Type: application/json' \
  -d '{"root": "/path/to/.claude/memory"}'
```

### Step 3: Verify

```bash
# Check how many documents were imported
sqlite3 data/memory.db 'SELECT COUNT(*) FROM documents;'

# Try a search
curl 'http://127.0.0.1:8787/v1/memory/search?q=task'
```

## How Incremental Re-Sync Works

The importer uses **SHA-256 content hashing** to avoid re-processing unchanged files:

1. **On import:** For each Markdown/JSONL file found, the importer computes `sha256(file_contents)` and stores it in the `documents.sha256` column.

2. **On re-sync:** When you run `/v1/documents/sync` again:
   - The importer walks the memory tree again.
   - For each file, it recomputes the hash.
   - If the hash matches what's in the database (same `source_path`, same `sha256`), the file is skipped.
   - If the hash differs, the document is UPDATEd + the FTS5 index is rebuilt for that entry (DELETE old, INSERT new).

3. **Transactional consistency:** The documents table and FTS5 index updates happen in the same transaction, so they never drift out of sync.

### Triggering Re-Sync

After you manually edit your memory files (e.g., updating `ACTIVE-TASK.md` or appending to `IMPROVEMENTS.md`), re-sync to pick up the changes:

```bash
curl -X POST http://127.0.0.1:8787/v1/documents/sync \
  -H 'Content-Type: application/json' \
  -d '{"root": "/path/to/.claude/memory"}'
```

Re-sync is **idempotent and incremental** — unchanged files are skipped, so it's safe to run frequently without performance penalty.

## Search: FTS5 and Stemming

`memory_search` uses **FTS5 (Full-Text Search 5)** with **porter English stemming** (`porter unicode61` tokenizer).

### Query Syntax

| Query | Matches | Example |
|-------|---------|---------|
| Single word | Word and stemmed variants | `task` matches `task`, `tasks`, `tasking` |
| Multiple words | Implicit AND — all words must appear | `memory search` matches documents containing both `memory` AND `search` |
| Phrase | Exact word sequence | `"memory search"` matches only that phrase |
| Prefix wildcard | Words starting with prefix | `task*` matches `task`, `tasks`, `taskbar` |
| Boolean NOT | Exclude documents | `-deprecated` or `NOT deprecated` |

### Examples

```bash
# Search for agent-related tasks
curl 'http://127.0.0.1:8787/v1/memory/search?q=agent'

# Search for a phrase
curl 'http://127.0.0.1:8787/v1/memory/search?q="task%20complete"'

# Prefix search
curl 'http://127.0.0.1:8787/v1/memory/search?q=improv*'

# Multiple words (AND)
curl 'http://127.0.0.1:8787/v1/memory/search?q=code%20review%20feedback'
```

### Stemming

The `porter unicode61` tokenizer stems English words to their root form:

- `task`, `tasks`, `tasking` → all stem to `task`
- `search`, `searching`, `searches` → all stem to `search`
- `improve`, `improved`, `improvement`, `improving` → all stem to `improve`

Unicode handling means accented characters, quotes, and dashes are tokenized correctly (important for international memory or Markdown with fancy quotes).

### Search Results

`memory_search` returns up to **20 results** per query, ranked by relevance. Each result includes:

```json
{
  "id": 42,
  "source_path": "improvements/IMPROVEMENTS.md",
  "kind": "improvements",
  "title": "Add request validation",
  "snippet": "... every handler validates its input. Request-supplied paths are ...",
  "score": 4.5
}
```

The `snippet` is a short excerpt highlighting the matched text. The `score` reflects relevance (higher = more relevant).

## Day-to-Day Agent Usage

### Scenario 1: Agent Starts a New Task

An agent calls the MCP tool `task_create`:

```json
{
  "id": "task-20260821-01",
  "title": "Implement search filtering",
  "status": "open",
  "priority": "high",
  "next_action": "Draft API contract"
}
```

This inserts a row into the `tasks` table. The agent can then:

- Update status: `task_update` with new status
- Record execution: `task_run_record` to log "started", "in progress", "completed"
- Append events: `event_append` to record "task.created", "task.updated", etc.

### Scenario 2: Agent Searches for Context

Before starting work, an agent searches the memory index:

```bash
tool: memory_search
arg q: "search filtering"
```

Returns documents matching the query, ranked by relevance. The agent reads snippets and sources to understand prior work.

### Scenario 3: Agent Records Execution

At each step, an agent calls `task_run_record`:

```json
{
  "id": "task-20260821-01",
  "agent": "junior-developer",
  "status": "in-progress",
  "summary": "Implemented search endpoint with date filtering",
  "started_at": "2026-08-21T14:30:00Z",
  "ended_at": "2026-08-21T15:45:00Z"
}
```

This logs when the agent worked, what it did, and how long it took. Multiple runs against the same task build an audit trail.

### Scenario 4: Agent Appends an Event

An agent calls `event_append` for noteworthy events:

```json
{
  "event_type": "task.completed",
  "summary": "Search filtering task complete — all tests passing",
  "task_id": "task-20260821-01",
  "agent": "junior-developer"
}
```

This creates a durable audit-trail entry with the agent's identity. The event appears in `task_list` (recent events) and in `/v1/events?task_id=...` (task-scoped events).

### Scenario 5: Sync Updated Memory Files

If the agent modifies `.claude/memory/` files directly (e.g., appends to `IMPROVEMENTS.md`), it triggers a re-sync:

```bash
curl -X POST http://127.0.0.1:8787/v1/documents/sync -H 'Content-Type: application/json' -d '{}'
```

The importer walks the tree, computes hashes, updates changed documents, and rebuilds the search index. Unchanged files are skipped.

## Phase 3: Generated Views (Not Yet Implemented)

**Currently, `ACTIVE-TASK.md` and `EVENT-STREAM.jsonl` remain hand-written files** in your `.claude/memory/` tree. Agents read and write them directly.

**Phase 3 will invert this:** These files become **generated views** produced by `internal/renderer` from the database. Agents will only call the HTTP API (`/v1/tasks`, `/v1/events`), and the files will be automatically regenerated from the database state.

This change does **not happen in this task** — Phase 3 is future work. For now:

- Continue writing `ACTIVE-TASK.md` and `EVENT-STREAM.jsonl` as you do today.
- The importer ingests them into the database for searchability.
- The service does not yet generate them back.

## References

- **`../README.md`** — Installation, configuration, service management.
- **`../CHANGELOG.md`** — Notable changes and fixes.
- **`api-contract.md`** — Full HTTP API request/response details.
- **`architecture.md`** — Service architecture and layering.

# Memory System Setup and Usage

This guide covers how bishop-memory stores and retrieves agent memory, and how to use the service day-to-day.

## What Gets Stored

The service maintains **ten tables** in SQLite:

| Table | Purpose | Rows | Notes |
|-------|---------|------|-------|
| `missions` | Durable mission records (replaces legacy tasks) | ~1 per harness mission | Title, status (not-started/in-progress/blocked/complete), outcome (done/failed), priority, next_action, blockers, timestamps |
| `mission_steps` | One row per step in PROGRESS.md (replaces task_runs) | ~1 per step in a mission | Step label, phase, agent identity, status (pending/in-progress/done/failed), notes, summary, optional timestamps |
| `flight_recorder` | Append-only audit log for the harness | ~N per day | Event type (dotted discriminator), mission scope, step label, agent identity, note, timestamps (occurred_at and created_at separate) |
| `crew` | Registered agent identities (replaces agents table) | ~10 | Name, role, registration timestamp |
| `findings` | Durable findings ledger (mirrors `.claude/memory/findings/FINDINGS.md`) | ~20-100 | Target, suggestion, rationale, status (proposed/approved/applied/rejected/retired/superseded), approver, timestamps |
| `patterns` | Advisory reusable patterns (mirrors `.claude/memory/findings/PATTERNS.md`) | ~10-50 | Name, context, solution, example, discovery timestamp/mission |
| `service_records` | Per-agent calibration observations | ~20-100 | Agent (subject), title, note, adjustment, source (self-reported/bishop-observed), timestamps |
| `directives` | Binding, human-ratified rules (mirrors `.claude/memory/reference/DIRECTIVES.md`) | ~5-20 | Title, rule, rationale, directive ID (natural key), timestamps. Read-only via API. |
| `documents` | Imported Markdown/JSONL files and their lines | ~100-1000+ | Source path, title, kind (document type), body (full text), SHA-256 hash, timestamps |
| `documents_fts` | FTS5 search index over documents | ~100-1000+ | Virtual table (no separate storage) — allows full-text search via `memory_search` |

The `documents` and `documents_fts` tables are populated by the **importer** (seeded via `/v1/documents/sync`). The rest are populated by API calls and handlers.

### Document Kinds

When the importer walks your memory tree, it assigns a **kind** to each document based on its path. The mapping is implemented in `internal/importer/importer.go` (`kindFromPath` function):

| Path | Kind | Notes |
|------|------|-------|
| `state/CURRENT-MISSION.md` | `state` | Mission state file; structured field prefix prepended during import for searchability |
| `state/FLIGHT-RECORDER.md` | `state` | Audit journal; structured field prefix prepended during import |
| `state/MISSION-ARCHIVE.md` | `state` | Mission archive index |
| `missions/<id>/BRIEF.md` | `brief` | Mission brief (exactly one per mission folder only) |
| `missions/<id>/PROGRESS.md` | `progress` | Mission progress tracker — PROGRESS.md rows (exactly one per mission folder only) |
| `missions/<id>/DEBRIEF.md` | `debrief` | Mission completion debrief (exactly one per mission folder only) |
| `findings/FINDINGS.md` | `findings` | Findings ledger |
| `findings/PATTERNS.md` | `patterns` | Advisory patterns |
| `findings/service-records/<name>.md` | `service-record` | Per-agent service history (exactly at 3-level nesting under findings/service-records/) |
| `reference/DIRECTIVES.md` | `directives` | Binding rules (exactly at 2-level nesting) |
| `graph/<file>.md` | `graph` | Project graphs and symbols (directory-based fallback) |
| `reference/<file>.md` | `reference` | Conventions, patterns, guides (directory-based fallback, except DIRECTIVES.md which is special-cased) |
| `workspace/<file>.md` | `workspace` | Working artifacts and scratch files (directory-based fallback) |
| `<top-level>/<file>.md` | `<top-level>` | Directory-based fallback (e.g., `README.md` → kind `README`) |
| `<root>/<file>.md` | `document` | Bare files at the root with no parent directory |

**Special cases:**
- `missions/<id>/BRIEF.md`, `missions/<id>/PROGRESS.md`, `missions/<id>/DEBRIEF.md` are recognized **only** when at exactly 3-level nesting (segments = 3, first = "missions"). A `missions/<id>/sub/BRIEF.md` (deeper nested) or bare `missions/BRIEF.md` (missing <id>) falls through to the directory-based kind ("missions").
- `findings/service-records/<name>.md` is recognized **only** at exactly 3-level nesting (segments = 3, first = "findings", second = "service-records").
- `reference/DIRECTIVES.md` is recognized **only** at exactly 2-level nesting (segments = 2, first = "reference").
- All other files use the top-level directory name as the kind, or "document" as a fallback for bare files at the root.

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

After you manually edit your memory files (e.g., updating `CURRENT-MISSION.md` or appending to `FINDINGS.md`), re-sync to pick up the changes:

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

`memory_search` returns up to **20 results** per query, ranked by relevance using FTS5 BM25 scoring. Each result includes:

```json
{
  "id": 42,
  "source_path": "/absolute/path/to/findings/FINDINGS.md",
  "kind": "findings",
  "title": "First heading from the file",
  "snippet": "... every handler validates its input. Request-supplied paths are ...",
  "rank": 4.5
}
```

The `snippet` is a short excerpt highlighting the matched text with `<mark>` tags. The `rank` field is the FTS5 BM25 score (lower = more relevant).

## Day-to-Day Agent Usage

### Scenario 1: Bishop Creates a New Mission

Bishop calls `mission_create` to start a mission:

```json
{
  "id": "mission-20260821-01",
  "title": "Refactor search API",
  "status": "not-started",
  "priority": "high",
  "owner": "claude-code:bishop",
  "next_action": "Delegate to junior developer"
}
```

This inserts a row into the `missions` table. Bishop can then update the mission as work progresses with `mission_update` (status, next_action, blockers, outcome).

### Scenario 2: Agent Searches for Context Before Starting

Before starting work, an agent calls `memory_search` to find prior work in the same domain:

```bash
tool: memory_search
args:
  q: "search filtering"
  limit: 20
```

Returns documents matching the query, ranked by FTS5 BM25 score (lower = more relevant). The agent reads snippets and source paths to understand prior patterns and decisions.

### Scenario 3: Agent Records a Step Execution

At each step, an agent calls `mission_step_record` to log progress:

```json
{
  "id": "mission-20260821-01",
  "step": "1",
  "phase": "Implementation",
  "agent": "hicks",
  "status": "in-progress",
  "summary": "Drafted search endpoint and wrote 15 unit tests",
  "notes": "Deferred date-range filtering to step 2 per Bishop request",
  "started_at": "2026-08-21T14:30:00Z",
  "ended_at": "2026-08-21T15:45:00Z"
}
```

This logs the step's execution history. Multiple `mission_step_record` calls build a detailed execution timeline for the mission.

### Scenario 4: Agent Appends an Event to the Flight Recorder

An agent calls `flight_recorder_append` for noteworthy harness-level events:

```json
{
  "mission_id": "mission-20260821-01",
  "step": "1",
  "event": "step.complete",
  "note": "Code review found 2 issues; marked as fix-round 1 of 2",
  "agent": "hicks",
  "occurred_at": "2026-08-21T15:45 UTC"
}
```

This creates a durable audit-trail entry scoped to the mission and step. The event includes agent identity and occurred_at timestamp (separate from created_at for audit traceability).

### Scenario 5: Agent Proposes a Finding

An agent calls `finding_append` to propose an improvement:

```json
{
  "suggestion": "Schema validation should reject out-of-range priority values at the SQL layer, not the application layer",
  "rationale": "Prevents silent data corruption if the API layer somehow bypasses validation",
  "target": "schema.sql",
  "mission_id": "mission-20260821-01",
  "finding_date": "2026-08-21"
}
```

This creates a finding with status='proposed' (always). Only the human operator can advance status. The agent can never write or change approver/date_approved fields — those belong to the human.

### Scenario 6: Sync Updated Memory Files

If the harness modifies `.claude/memory/` files directly (e.g., appends to `FINDINGS.md` or updates `PROGRESS.md`), the agent calls `documents_sync` to import the changes:

```bash
tool: documents_sync
args:
  root: "/path/to/.claude/memory"
```

The importer walks the tree, computes hashes, updates changed documents, and rebuilds the FTS5 search index. Unchanged files are skipped. Sync is idempotent and safe to run frequently.

## Current Architecture: Read-Only Importer

The harness owns the `.claude/memory/` tree — it reads and writes files directly (PROGRESS.md, FLIGHT-RECORDER.md, FINDINGS.md, etc.). The bishop-memory service **imports but does not write back**. This deliberate separation of concerns provides:

- **Harness authority:** The harness is the authoritative source; files are the ground truth.
- **Service visibility:** The service provides search, query, and HTTP API access for agents.
- **Auditability:** Every event passes through the HTTP API with agent identity captured.

The `internal/renderer` is a deliberate stub. A Phase 3 plan to generate Markdown/JSONL views from the database was considered but deferred — keeping the service read-only simplifies the architecture and avoids bidirectional sync complexity. If future work requires bidirectional sync, that design decision will be revisited explicitly.

## References

- **`INSTALL.md`** — Complete installation and setup guide (daemon, MCP registration, index seeding, troubleshooting).
- **`../README.md`** — Installation, configuration, service management.
- **`../CHANGELOG.md`** — Notable changes and fixes.
- **`api-contract.md`** — Full HTTP API request/response details.
- **`architecture.md`** — Service architecture and layering.

# bishop-memory Migration Plan

## Goal

Move from direct Markdown/JSONL file manipulation by agents to an API-first memory service, then expose that service through a constrained MCP adapter. The existing `.claude/memory/` tree is preserved as a compatibility surface during the transition.

## Phase 2: Seed the database

Before agents can stop writing files, the service must ingest the existing memory corpus.

- Walk `.claude/memory/graph/`, `reference/`, `improvements/`, and `agent-documents/` for Markdown files.
- Parse `ACTIVE-TASK.md` into the `tasks` table.
- Import `EVENT-STREAM.jsonl` into the `events` table.
- Build an FTS5 index over documents and sections.

After Phase 2, the database is the authoritative copy of all imported memory, but agents may still write files directly.

## Phase 3: API-first operation

Agents switch to the HTTP API as the primary interface:

- Task changes go through `/v1/tasks`.
- All activity becomes an event through `/v1/events`.
- Direct writes to `ACTIVE-TASK.md` and `EVENT-STREAM.jsonl` stop.
- Those files become **generated compatibility views** produced by `internal/renderer` from the database.

This phase makes the service the single source of truth while keeping the file layout readable for humans and legacy tooling.

## Phase 4: MCP adapter

Add an MCP adapter exposing only these **8 tools**:

| Tool | Purpose |
|------|---------|
| `memory_search` | FTS5 recall across imported memory |
| `task_list` | List tasks with optional filters |
| `task_get` | Fetch a single task |
| `task_create` | Create a new task |
| `task_update` | Update task fields or status |
| `event_append` | Append an agent/system event |
| `task_run_record` | Record an execution attempt for a task |
| `documents_sync` | Trigger an import of Markdown/JSONL files into the search index |

The adapter deliberately does **not** expose raw SQL or arbitrary file-write tools. The service remains the policy boundary and audit point. Agent identity is composed as `<BISHOP_HARNESS>:<agent>` on write tools that record the actor (event_append, task_run_record).

## Migration sequencing

```text
Phase 1  →  Phase 2  →  Phase 3  →  Phase 4
Core       Import      API-first   MCP
scaffold   existing    agents      adapter
           memory      use API
```

1. **Phase 1** delivers the runnable core.
2. **Phase 2** imports existing memory so the database is populated.
3. **Phase 3** retires direct agent file writes; files become views.
4. **Phase 4** adds the MCP adapter for clean agent integration.

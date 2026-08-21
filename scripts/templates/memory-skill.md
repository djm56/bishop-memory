---
name: memory
description: bishop-memory — shared central memory server (localhost:8787) for tasks, events, and document search. Use the 8 bishop-memory MCP tools instead of writing Markdown/JSONL files locally.
license: MIT
compatibility: opencode
metadata:
  service: bishop-memory
  harness: opencode
  transport: stdio (mcpd) -> http://127.0.0.1:8787
---

# bishop-memory — shared central memory

bishop-memory is the project's shared central memory service. It runs as a
local HTTP daemon (`memoryd`) on `http://127.0.0.1:8787` and stores tasks,
events, task runs, agents, improvements, and imported documents in a single
SQLite database with an FTS5 search index.

This skill documents how to talk to bishop-memory from an opencode sub-agent
via the MCP adapter (`mcpd`), how the 8 tools compose, and the agent-identity
convention that ties audit events back to the calling sub-agent.

## What bishop-memory is

- **Local HTTP service**, not a remote API. Bound to `127.0.0.1:8787`.
- **SQLite + FTS5** store; `modernc.org/sqlite` driver (CGo-free).
- **Append-only audit log** of every state mutation (task created/updated,
  event appended, run recorded, document imported).
- **FTS5 document index** over imported Markdown/JSONL so the crew can search
  prior knowledge without re-reading the filesystem.
- **Replaces file-based memory** (`ACTIVE-TASK.md`, `EVENT-LOG.jsonl`,
  `EVENT-STREAM.jsonl`) — the API is the source of truth, files are a Phase 3
  read-only mirror.

## The MCP adapter — `mcpd`

`mcpd` is the MCP stdio server that proxies `bishop-memory`'s HTTP API.
When opencode boots, the install script (`scripts/install-opencode.sh`)
registers `mcpd` under the `mcp.bishop-memory` key in `opencode.json` with:

- `type: "local"`
- `command: ["<bishop-memory>/bin/mcpd"]`
- `environment: { BISHOP_MEMORY_URL: "http://127.0.0.1:8787", BISHOP_HARNESS: "opencode" }`

mcpd reads `BISHOP_HARNESS` to compose agent identity (see below). `BISHOP_MEMORY_URL`
defaults to `http://127.0.0.1:8787` if unset.

## The 8 MCP tools — when to call

| Tool | When to call |
|------|--------------|
| `memory_search` | Before coding, search imported documents (Markdown/JSONL) for relevant context — same way you consult graphify. |
| `task_list` | When you need an up-to-date view of all tasks and their status/priority. |
| `task_get` | When you need one specific task by ID (the orchestrator hands you the ID in the brief). |
| `task_create` | When the orchestrator (or your brief) tells you to create a new durable task record. |
| `task_update` | When status / priority / next_action / blockers change on an existing task. |
| `event_append` | When something durable happened that should be in the audit log (task.*, agent.*, improvement.*). |
| `task_run_record` | When you (or another sub-agent) completed an attempt against a task. |
| `documents_sync` | When the operator (or the orchestrator) explicitly asks to re-import the memory tree. |

All 8 tools return MCP text. Read tools (`memory_search`, `task_list`,
`task_get`) return JSON result bodies. Write tools (`task_create`,
`task_update`, `event_append`, `task_run_record`, `documents_sync`) return
a small `{"id":...,"created":true}` / `{"appended":true,"id":...}` /
`{"recorded":true,"task_id":...}` / `{"root":...,"synced":true}` envelope.

## Agent identity convention

Two of the write tools (`event_append`, `task_run_record`) capture actor
identity. The other write tools (`task_create`, `task_update`,
`documents_sync`) do **not** capture an agent this round — they are
plan-frozen from Phase 2 and adding an agent field is a documented
deferral.

When you call a tool that captures identity, you must pass an `agent`
argument. That argument is your **sub-agent name** (the part AFTER the
harness prefix): `"orchestrator"`, `"junior-developer"`,
`"senior-developer"`, `"code-reviewer"`, `"documentation-writer"`,
`"research-specialist"`.

`mcpd` then composes the stored identity as `"<BISHOP_HARNESS>:<agent>"`:

- opencode + orchestrator → `opencode:orchestrator`
- opencode + junior-developer → `opencode:junior-developer`

Two tools need it:

- `event_append` — pass `agent: "<your-sub-agent-name>"`
- `task_run_record` — pass `agent: "<your-sub-agent-name>"`

`task_create` and `task_update` have **NO agent param** this round. If you
need actor attribution on a task mutation, capture it indirectly by
making a surrounding `event_append` call (the orchestrator pattern).

`documents_sync` is a system trigger, not an agent action — no identity
composition.

## When to consult memory (before coding)

Just like the graphify rule, before you start a coding task you should
search bishop-memory for prior knowledge:

1. **First check graphify** for code-level structure (the existing rule).
2. **Then call `memory_search`** for higher-level context: prior tasks,
   prior improvements, imported Markdown/JSONL notes, design rationale,
   operator preferences.
3. If `memory_search` returns hits, **read the top result before coding**
   — it often short-circuits a "should I do X?" question with a precedent
   set by an earlier task.

The orchestrator already does this. Sub-agents should too when the brief
is ambiguous, touches a system you've never touched, or asks for a
non-trivial decision.

## How to start the server

Pick one of:

- **Foreground** (good for dev): `cd <bishop-memory> && make run` (runs
  `memoryd` directly via `go run`).
- **launchd daemon** (good for always-on): run `scripts/install-daemon.sh`
  to install `~/Library/LaunchAgents/com.bishop-memory.memoryd.plist`,
  then `launchctl load -w ~/Library/LaunchAgents/com.bishop-memory.memoryd.plist`.

Health check:

```bash
curl http://127.0.0.1:8787/healthz
```

## Quick examples

```text
# Search imported documents
memory_search(q: "importer path escape")

# Record a run completion
task_run_record(id: "task-20260820-03", agent: "junior-developer", status: "completed", summary: "Step 5 deliverable shipped")

# Append an audit event from a sub-agent
event_append(task_id: "task-20260820-03", event_type: "agent.heartbeat", summary: "Step 5 complete — scripts + templates delivered", agent: "junior-developer")
```

## Migration / Existing DBs

> **Existing Phase 2 databases** (created before task-20260820-03) lack the
> `events.agent` column. Before agent-identity capture works on such a DB,
> run:
>
> ```bash
> sqlite3 data/memory.db 'ALTER TABLE events ADD COLUMN agent TEXT;'
> ```
>
> Clean-env installs (task-20260820-03 onward) are unaffected — the column
> is in `db/schema.sql`.

This is a **carry-forward from Step 4** of task-20260820-03 (Phase 3): the
agent-identity composition only lands once every store writes the column.
The schema migration is a one-line `ALTER TABLE` and is safe to run on a
live DB (the column is added as nullable, existing rows default to `NULL`,
which the audit reader treats as "system / unknown actor").

---
name: memory
description: bishop-memory — shared central memory server (localhost:8787) for missions, findings, service records, and document search. Use the 16 bishop-memory MCP tools instead of writing Markdown files locally.
license: MIT
compatibility: opencode
metadata:
  service: bishop-memory
  harness: opencode
  transport: stdio (mcpd) -> http://127.0.0.1:8787
---

# bishop-memory — shared central memory

bishop-memory is the project's shared central memory service. It runs as a
local HTTP daemon (`memoryd`) on `http://127.0.0.1:8787` and stores missions,
flight-recorder events, findings, patterns, service records, and imported
documents in a single SQLite database with an FTS5 search index.

This skill documents how to talk to bishop-memory from an opencode sub-agent
via the MCP adapter (`mcpd`), what the 16 tools do, and the agent-identity
convention that ties write events back to the calling sub-agent.

## What bishop-memory is

- **Local HTTP service**, not a remote API. Bound to `127.0.0.1:8787`.
- **SQLite + FTS5** store; `modernc.org/sqlite` driver (CGo-free).
- **Append-only audit journal** (flight-recorder) of every state mutation.
- **FTS5 document index** over imported Markdown so the crew can search prior
  knowledge without re-reading the filesystem.
- **Mission tracker** with status (`not-started`/`in-progress`/`blocked`/`complete`)
  and outcome (`done`/`failed`).
- **Findings ledger** for observations and improvements (status set by operator only).
- **Service records** for observations about agent performance and behaviour.

## The MCP adapter — `mcpd`

`mcpd` is the MCP stdio server that proxies `bishop-memory`'s HTTP API.
When opencode boots, the install script (`scripts/install-opencode.sh`)
registers `mcpd` under the `mcp.bishop-memory` key in `opencode.json` with:

- `type: "local"`
- `command: ["<bishop-memory>/bin/mcpd"]`
- `environment: { BISHOP_MEMORY_URL: "http://127.0.0.1:8787", BISHOP_HARNESS: "opencode" }`

mcpd reads `BISHOP_HARNESS` to compose agent identity (see below). `BISHOP_MEMORY_URL`
defaults to `http://127.0.0.1:8787` if unset.

## The 16 MCP tools

### Read tools (search and retrieval — no identity required)

| Tool | Purpose |
|------|---------|
| `memory_search` | Search imported documents (FTS5) by query string. Returns ranked hits with snippets. Optional cap on results (the server caps at 20). Currently advisory only — the server always returns its internal LIMIT 20. |
| `mission_list` | List all missions, newest-updated first. |
| `mission_get` | Get a single mission by ID. |
| `mission_steps_list` | List mission steps (PROGRESS.md rows) for a mission. |
| `finding_list` | List findings, newest-first. Optional status filter (`proposed`, `approved`, `applied`, `rejected`, `retired`, `superseded`). |
| `pattern_list` | List advisory patterns, newest-first. Patterns are non-binding; directives win on any conflict. |
| `service_record_list` | List service records (observations about agent performance), newest-first. Optional agent filter by subject name. |

### Write tools (state mutations — agent identity on exactly 2)

| Tool | Purpose | Identity |
|------|---------|----------|
| `mission_create` | Create a new mission. | None (plan-frozen deferral) |
| `mission_update` | Update mission status/outcome/owner/priority/notes. | None (plan-frozen deferral) |
| `flight_recorder_append` | Append an audit event. Actor is composed `<BISHOP_HARNESS>:<agent>`. | **YES** (actor) |
| `mission_step_record` | Record a mission step attempt. Actor is composed `<BISHOP_HARNESS>:<agent>`. | **YES** (actor) |
| `documents_sync` | Trigger document import from the memory root into the FTS5 index. | None (system action) |
| `finding_append` | Append a finding to the findings ledger. Always created `proposed`. | None (status is operator-only) |
| `pattern_append` | Append an advisory pattern. | None |
| `service_record_append` | Record an observation about an agent's performance. | None (subject name only) |

## Agent identity convention — exactly 2 tools need it

**Only `flight_recorder_append` and `mission_step_record` capture the actor.**
They do this by composing `"<BISHOP_HARNESS>:<agent>"` and storing the composed
identity on the audit trail.

When you call one of these tools, pass an `agent` argument with your sub-agent
name (the part AFTER the harness prefix). Crew members are: `bishop`, `hicks`,
`vasquez`, `apone`, `lambert`.

Examples:
- opencode + bishop → `opencode:bishop`
- opencode + hicks → `opencode:hicks`

**Identity composition failure mode:** Passing the composed identity
(e.g., `"opencode:hicks"`) instead of just the agent name (e.g., `"hicks"`)
causes records to be stored under the wrong identity — for example, all records
under `service_record_append` tagged with `"opencode:hicks"` instead of
`"hicks"` will not match filters searching for agent `hicks`, and the record
files stop mirroring what the tools return. Compose identity on exactly these
two tools by passing the sub-agent name alone.

**Tools that capture agent identity:**

- `flight_recorder_append` — pass `agent: "<sub-agent-name>"` (e.g. `"bishop"`, `"hicks"`)
- `mission_step_record` — pass `agent: "<sub-agent-name>"` (e.g. `"hicks"`)

**Tools that do NOT capture identity (or pass agent as subject, not actor):**

- `mission_create` and `mission_update` — no agent param (plan-frozen).
  If you need actor attribution on a mission mutation, record it indirectly
  via a surrounding `flight_recorder_append` call.
- `documents_sync` — system-triggered, no actor.
- `finding_append` — no actor; findings are created `proposed` and status
  changes belong to the human operator alone (no API write path exists
  for status/approver/date_approved).
- `pattern_append` — no actor.
- `service_record_append` — `agent` argument names the **subject** (which crew
  member the record is about), not the caller. Passed through unchanged to match
  the filesystem service-records layout.
- `service_record_list` — `agent` filter parameter names the **subject**, not
  the caller. Passed through unchanged.

## Finding status and approvals

Findings are created with status `proposed`. Agents **cannot set or change**
status, approver, or date_approved — these fields belong to the human operator.

The `finding_list` tool lets you read findings at a specific status (`proposed`
to see what's awaiting approval, `approved` to see binding findings, etc.).
Advancing a finding's status from `proposed` to `approved` is done by the
operator, not through the API.

## Directives are read-only

Directives (binding, human-ratified rules) are exposed via `memory_search` so
agents can read the rules that bind them. There is **no write endpoint** and
**no MCP write tool** for directives — they are too important to modify
programmatically.

## Mission vocabulary

A mission has two independent fields:

- **status**: one of `not-started`, `in-progress`, `blocked`, `complete`
- **outcome**: one of `done`, `failed`, or null

A mission can be `complete` with a null `outcome` while Bishop is still
setting the outcome field (part of the closing sequence), so be prepared for
missions at `complete` status to later gain an outcome.

**Governance distinction**: `mission.outcome` is set by the harness during its
closing sequence via `mission_update` — agents (including the primary agent
Bishop) may call this to advance it. `finding.status`, by contrast, is never
settable through the API by anyone; the operator advances it outside the
service. Do not confuse the two fields' governance models.

## Memory tree structure

bishop-memory imports markdown and JSONL from your harness memory root:

- `state/` — mission and flight-recorder state files
- `missions/<mission-id>/` — mission briefs, progress, and debriefs
- `findings/` — findings ledger
- `findings/service-records/` — service records keyed by agent name
- `reference/` — directives and reference material
- `workspace/` — scratch and working documents during a mission

All are indexed by FTS5; `memory_search` returns hits across the full tree.

## When to consult memory (before working)

Before you start a mission or coding task, search bishop-memory for prior
knowledge:

1. Check graphify for code-level structure (if available).
2. Call `memory_search` for higher-level context: prior missions, findings,
   design notes, operator preferences.
3. If results come back, read the top one — it often answers "should I do X?"
   with a precedent set by an earlier mission.

Bishop already does this. Sub-agents should too when the brief
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

# Create a new mission
mission_create(id: "mission-20260905-01", title: "Refactor harness vocabulary", status: "not-started")

# Record a mission step attempt
mission_step_record(id: "mission-20260905-01", step: "29", agent: "hicks", status: "done", summary: "Templates rewritten")

# Append an audit event
flight_recorder_append(mission_id: "mission-20260905-01", step: "29", event: "step-sync", note: "Step 29 complete", agent: "bishop")

# Append a finding (always proposed — operator approves it)
finding_append(suggestion: "Agent identity should be composed for exactly 2 tools", target: "mcpd", mission_id: "mission-20260905-01")

# Record an observation about agent performance
service_record_append(agent: "hicks", title: "Performance note", note: "Completed step 29 within time budget", source: "bishop-observed")
```

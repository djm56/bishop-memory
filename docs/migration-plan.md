# bishop-memory Migration — Completion Record

This document records the successful completion of the four-phase migration from file-based agent memory to an API-first service architecture. All phases have been completed. See `docs/architecture.md` for the current system design and `CHANGELOG.md` for detailed changes per release.

## Overview

The original goal was to move from direct Markdown/JSONL file manipulation by agents to an API-first memory service, then expose that service through a constrained MCP adapter. The existing `.claude/memory/` tree is preserved as a compatibility surface and continues to be read and written by the harness.

## Phase 1: Gin + SQLite core

**Status: ✅ Complete (release 0.1.0)**

The service core was scaffolded as a single Go binary (`cmd/memoryd`) with:

- Gin HTTP router, `/v1` resource versioning, `/healthz` liveness endpoint
- `modernc.org/sqlite` driver, WAL mode, foreign key enforcement
- `db/schema.sql` applying the 10-table harness vocabulary on startup
- Request ID middleware, structured access logging, panic recovery
- Configuration via environment variables (`HTTP_HOST`, `MEMORY_ROOT`, `LOG_LEVEL`)
- Database persistence layer in `internal/store` with inline SQL in handlers (Phase 2 design)

## Phase 2: Seed the database

**Status: ✅ Complete (release 0.2.0)**

The importer was implemented to walk the `.claude/memory/` tree and populate the database:

- Markdown file import from `state/`, `missions/`, `findings/`, `reference/`, `workspace/`, and `graph/` directories
- JSONL line-by-line import for structured records
- SHA-256 content hashing for incremental sync (unchanged files are skipped on re-import)
- FTS5 full-text search index built over documents and sections during import
- `/v1/documents/sync` endpoint to trigger import runs on demand

After Phase 2, the database became the authoritative copy of imported memory, but agents continued to write files directly.

## Phase 3: API-first operation

**Status: ✅ Mostly complete (release 0.3.0)**

Agents switched to the HTTP API as their primary interface:

- Mission management: `/v1/missions`, `/v1/missions/:id/steps` with new vocabulary (replacing legacy "tasks")
- Flight recorder: `/v1/flight-recorder` for durable audit-trail events
- Knowledge surface: `/v1/findings`, `/v1/patterns`, `/v1/service-records`, `/v1/directives`
- Memory search: `/v1/memory/search` with FTS5 ranking and snippets
- Validation enforcement: out-of-vocabulary enum values return 400 (not 500 from database constraints)
- Agent identity composition: `<harness>:<agent>` recorded on audit rows for actor attribution

**What did NOT happen:** The original Phase 3 plan included `internal/renderer`, which would generate `.claude/memory/ACTIVE-TASK.md` and `EVENT-STREAM.jsonl` as compatibility views from the database. This was deliberately deferred — the service remains read-only for its own writes. The harness owns the memory tree; the service imports and queries it, but does not write back. The renderer is a stub; this limitation is documented in `docs/architecture.md`.

## Phase 4: MCP adapter

**Status: ✅ Complete (release 0.4.0)**

The `cmd/mcpd` stdio Model Context Protocol server was implemented to expose 15 tools to agents:

**Read tools (no agent identity):**
- `memory_search` — FTS5 search over documents
- `mission_list`, `mission_get` — Mission read access
- `mission_steps_list` — Step history read access
- `finding_list`, `pattern_list`, `service_record_list` — Knowledge ledger read access
- (Directives are read-only; no write tool exposed)

**Write tools (agent identity captured):**
- `mission_create`, `mission_update` — Mission state management
- `flight_recorder_append` — Audit trail events
- `mission_step_record` — Step execution recording
- `finding_append`, `pattern_append`, `service_record_append` — Knowledge ledger writes
- `documents_sync` — Trigger memory tree import

The adapter remains the policy boundary: agents have no raw SQL or arbitrary file-write tools. Agent identity is composed as `<BISHOP_HARNESS>:<agent>` on write tools that record the actor, allowing audit attribution. The MCP schema for each tool mirrors the underlying HTTP wire shapes.

## What changed about the schema vocabulary

| Old name | New name | Rationale |
|----------|----------|-----------|
| `tasks` table | `missions` | The harness vocabulary uses "mission" consistently; tasks were ambiguous. |
| `task_runs` | `mission_steps` | Steps mirror PROGRESS.md row structure (Step, Phase, Agent, Status, Notes). |
| `events` table | `flight_recorder` | More clearly names the audit journal purpose. |
| `agents` | `crew` | More precise: the table lists crew members, not agent instances. |
| `improvements` | `findings` + `patterns` | Findings are specific observations; patterns are reusable solutions. |
| (none) | `service_records` | New table for per-agent calibration notes. |
| (none) | `directives` | New table for binding, human-ratified rules. |

The `documents` and `documents_fts` tables remained largely unchanged in purpose (full-text search index).

## Migration sequencing

```
Phase 1 (Core)  →  Phase 2 (Import)  →  Phase 3 (API-first)  →  Phase 4 (MCP)
```

1. Phase 1 delivered the runnable core service.
2. Phase 2 imported existing memory so the database was populated.
3. Phase 3 switched agents to the API; files remained read/writable but secondary.
4. Phase 4 added the MCP adapter for clean, audited agent integration.

All phases are complete. The service is production-ready.

## Known limitations

- **No authentication:** Every endpoint is open read/write. Access control happens at the network level (loopback + SSH tunnel, or firewall rules).
- **No rate limiting:** No quota or throttling on requests.
- **Document-sync root allow-list not implemented:** The service refuses the filesystem root (`/`) but still accepts any other absolute path. A full allow-list of permitted memory roots is a separate design decision.
- **Renderer not implemented:** The Phase 3 plan to generate Markdown/JSONL views from the database was deferred. The service does not write to the harness memory tree.

See `docs/architecture.md` for design rationale and `CHANGELOG.md` for per-release details.

# bishop-memory Architecture

## Overview

`bishop-memory` is a small, single-binary memory service. It is locally bound, CGo-free, and uses SQLite as its only persistence layer. The HTTP API is implemented with Gin and versioned under `/v1`.

## Layering

```text
┌──────────────────────────────────────────────────────────────┐
│                    Your local machine                        │
│                                                              │
│  .claude/memory/                                             │
│  ├── state/           Transitional/generated views           │
│  ├── missions/<id>/   Mission records (BRIEF, PROGRESS, ...)│
│  ├── findings/        Improvement ledger & service records   │
│  ├── reference/       Binding rules and conventions          │
│  ├── workspace/       Scratch working artifacts              │
│  └── graph/           Manual documentation (fixture)         │
│             │                                                │
│             ▼                                                │
│  bishop-memory (single Go binary)                            │
│  ├── Gin HTTP API                                            │
│  ├── importer                                                │
│  ├── Markdown renderer (Phase 3 stub — unimplemented)       │
│  └── SQLite access layer                                     │
│             │                                                │
│             ▼                                                │
│  data/memory.db                                              │
│  ├── missions                                                │
│  ├── mission_steps                                           │
│  ├── flight_recorder                                         │
│  ├── crew                                                    │
│  ├── findings                                                │
│  ├── patterns                                                │
│  ├── service_records                                         │
│  ├── directives                                              │
│  ├── documents                                               │
│  └── documents_fts (FTS5 virtual table)                      │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

### Component responsibilities

| Layer | Package | Responsibility |
|-------|---------|----------------|
| Entry | `cmd/memoryd` | Load config, open database, apply schema, start Gin server. Signal handling for graceful shutdown. |
| MCP Adapter | `cmd/mcpd` | Stdio Model Context Protocol server. Proxies the HTTP API with typed MCP tool schemas and agent-identity composition. |
| HTTP | `internal/api` | Gin router, route groups, handlers. SQL queries are **inline in handlers** (Phase 2 design). |
| Cross-cutting | `internal/middleware` | Request ID, structured access log, panic recovery |
| Configuration | `internal/config` | Environment loading via `godotenv`. HTTP_HOST and other runtime settings. |
| Persistence | `internal/store` | SQLite driver setup (modernc.org/sqlite, WAL, foreign keys), schema application (via `db/schema.sql`) |
| Types | `internal/model` | Request/response Go structs, validation tags |
| Import | `internal/importer` | Markdown walker, JSONL line importer. SHA-256 content hashing for incremental sync. FTS5 index building. |
| Views | `internal/renderer` | **Phase 3 stub.** Unimplemented. Planned to render generated Markdown/JSONL views from DB, but the service does not write back to the harness's memory tree. |

## Database

- **Driver:** `modernc.org/sqlite` — pure-Go, CGo-free SQLite.
- **Mode:** WAL enabled for concurrent reads/writes.
- **Tables:** `missions`, `mission_steps`, `flight_recorder`, `crew`, `findings`, `patterns`, `service_records`, `directives`, `documents`.
- **Virtual:** `documents_fts` — FTS5 full-text search index over imported documents.
- **Search:** Queried via `/v1/memory/search`.

## API Versioning

All resource endpoints live under `/v1`. `/healthz` remains unversioned so infrastructure checks stay simple. Global middleware (`RequestID`, `AccessLog`, `Recovery`) runs on every request. Future authentication or rate-limiting can be attached to the `/v1` group or to resource-specific sub-groups.

## Directory Tree

```text
bishop-memory/
├── cmd/
│   ├── memoryd/
│   │   ├── main.go
│   │   └── main_test.go
│   └── mcpd/
│       ├── main.go
│       └── main_test.go
├── db/
│   └── schema.sql
├── internal/
│   ├── api/
│   │   ├── router.go
│   │   ├── errors.go
│   │   ├── errors_test.go
│   │   ├── health.go
│   │   ├── missions.go
│   │   ├── flight_recorder.go
│   │   ├── findings.go
│   │   ├── patterns.go
│   │   ├── service_records.go
│   │   ├── directives.go
│   │   ├── search.go
│   │   ├── search_test.go
│   │   ├── events_test.go
│   │   └── errors_test.go
│   ├── middleware/
│   │   ├── request_id.go
│   │   ├── access_log.go
│   │   ├── recovery.go
│   │   └── recovery_test.go
│   ├── config/
│   │   ├── config.go
│   │   └── config_test.go
│   ├── store/
│   │   ├── sqlite.go
│   │   └── schema.go
│   ├── importer/
│   │   ├── importer.go
│   │   └── importer_test.go
│   ├── renderer/
│   │   └── renderer.go
│   └── model/
│       ├── mission.go
│       ├── flight_recorder.go
│       ├── finding.go
│       ├── pattern.go
│       ├── service_record.go
│       ├── directive.go
│       └── document.go
├── scripts/
│   ├── install-daemon.sh
│   ├── install-daemon-linux.sh
│   ├── install-opencode.sh
│   ├── reconcile-memory.py
│   ├── backfill-memory.py
│   ├── bishop-memory.service
│   ├── com.bishop-memory.memoryd.plist
│   └── templates/
│       └── memory-skill.md
├── testdata/
│   └── memory/
│       ├── state/
│       ├── missions/
│       ├── findings/
│       │   └── service-records/
│       ├── reference/
│       ├── workspace/
│       └── graph/
├── data/
│   └── .gitkeep
├── docs/
│   ├── architecture.md
│   ├── migration-plan.md
│   ├── api-contract.md
│   └── MEMORY-SETUP.md
├── README.md
├── CHANGELOG.md
├── CONTRIBUTING.md
├── LICENSE
├── go.mod
├── go.sum
├── Makefile
├── .env.example
└── .gitignore
```

## Roadmap

### Phase 1: Gin + SQLite core

**Status: Complete.** The service core (HTTP server, SQLite driver, basic handlers) is implemented and tested.

### Phase 2: Seed the database

**Status: Complete.** The importer walks `.claude/memory/` and populates the database with missions, findings, patterns, directives, and documents. The FTS5 index is built on import.

### Phase 3: API-first operation

**Status: Mostly complete.** Agents use the HTTP API as their primary interface. Mission changes go through `/v1/missions`; all activity is recorded via `/v1/flight-recorder`. Findings, patterns, and service records are written through the API.

**Limitation:** The Phase 3 plan included generating `.claude/memory/ACTIVE-TASK.md` and `EVENT-STREAM.jsonl` as compatibility views from the database (via `internal/renderer`). This was deliberately deferred. The service does not write back to the harness's memory tree — that remains the operator's or agent's responsibility. The renderer is a stub; it remains unimplemented.

### Phase 4: MCP adapter

**Status: Complete.** The `cmd/mcpd` stdio Model Context Protocol server exposes 16 tools to agents, with agent-identity composition for audit trail purposes.

## Schema Design Decisions

- **status and outcome fields:** Missions carry both a working state (`status`) and a terminal state (`outcome`). `outcome` is independently settable and not required when `status` becomes `complete` — the harness closes status at one point and records outcome at a later one.
- **No foreign key on findings.mission_id:** Findings are stored without existence checking. A finding can outlive mission-folder cleanup.
- **Append-only flight_recorder:** The audit log is append-only by application convention, not enforced at schema level. It distinguishes `occurred_at` (caller-supplied event time) from `created_at` (insert time).
- **Standalone FTS5 table:** `documents_fts` is not an external-content table; it has no triggers. The importer is the single writer to both tables and inserts into both in the same transaction.

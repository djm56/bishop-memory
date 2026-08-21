# bishop-memory Architecture

## Overview

`bishop-memory` is a small, single-binary memory service. It is locally bound, CGo-free, and uses SQLite as its only persistence layer. The HTTP API is implemented with Gin and versioned under `/v1`.

## Layering

```text
┌─────────────────────────────────────────────────────────┐
│                    Your local machine                    │
│                                                         │
│  .claude/memory/                                        │
│  ├── graph/           Human-maintained manuals          │
│  ├── reference/       Conventions and durable notes     │
│  ├── improvements/    Narrative improvement records     │
│  └── state/           Transitional/generated views      │
│             │                                           │
│             ▼                                           │
│  bishop-memory (single Go binary)                       │
│  ├── Gin HTTP API                                       │
│  ├── importer                                            │
│  ├── Markdown renderer                                  │
│  └── SQLite access layer                                │
│             │                                           │
│             ▼                                           │
│  data/memory.db                                         │
│  ├── tasks                                               │
│  ├── task_runs                                           │
│  ├── events                                              │
│  ├── agents                                              │
│  ├── improvements                                        │
│  ├── documents                                           │
│  └── FTS5 search index                                   │
│                                                         │
└─────────────────────────────────────────────────────────┘
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
| Import | `internal/importer` | Markdown walker, `ACTIVE-TASK.md` parser, JSONL line importer. SHA-256 content hashing for incremental sync. FTS5 index building. |
| Views | `internal/renderer` | **Phase 3 stub.** Will render generated Markdown/JSONL views from DB. |

## Database

- **Driver:** `modernc.org/sqlite` — pure-Go, CGo-free SQLite.
- **Mode:** WAL enabled for concurrent reads/writes.
- **Tables:** `tasks`, `task_runs`, `events`, `agents`, `improvements`, `documents`.
- **Search:** FTS5 virtual index over imported documents and sections, queried via `/v1/memory/search`.

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
│   │   ├── tasks.go
│   │   ├── events.go
│   │   ├── events_test.go
│   │   ├── search.go
│   │   └── search_test.go
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
│   │   ├── schema.go
│   │   ├── schema_test.go
│   │   ├── tasks.go
│   │   ├── events.go
│   │   └── documents.go
│   ├── importer/
│   │   ├── importer.go
│   │   └── importer_test.go
│   ├── renderer/
│   │   └── renderer.go
│   └── model/
│       ├── task.go
│       ├── event.go
│       └── document.go
├── scripts/
│   ├── install-daemon.sh
│   ├── install-daemon-linux.sh
│   ├── bishop-memory.service
│   ├── install-claude.sh
│   ├── install-opencode.sh
│   ├── com.bishop-memory.memoryd.plist
│   └── templates/
│       ├── claude-memory.md
│       └── memory-skill.md
├── testdata/
│   └── memory/
│       ├── state/
│       ├── graph/
│       ├── reference/
│       ├── improvements/
│       └── tasks/
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

Build the Gin service entry point, SQLite store with WAL mode, schema bootstrap, health endpoint, task CRUD, event append/list endpoints, and the request-ID/access-log/recovery middleware chain.

### Phase 2: Import and search

Add a Markdown walker for `.claude/memory/graph`, `reference`, `improvements`, and `agent-documents`; an `ACTIVE-TASK.md` parser; a JSONL importer for `EVENT-STREAM.jsonl`; FTS5 indexing for documents and sections; and the `/v1/memory/search` endpoint.

### Phase 3: API-first operation

Agents stop direct file writes. Task changes flow through `/v1/tasks`, all activity becomes events through `/v1/events`, and `ACTIVE-TASK.md` plus `EVENT-STREAM.jsonl` become generated compatibility views produced by the service.

### Phase 4: MCP adapter

Expose a small MCP adapter (`cmd/mcpd`) with **8 tools**: `memory_search`, `task_list`, `task_get`, `task_create`, `task_update`, `event_append`, `task_run_record`, and `documents_sync`. Raw SQL and arbitrary file-write tools are never exposed to agents; the service is the policy boundary and audit point. Agent identity is composed as `<BISHOP_HARNESS>:<agent>` on write tools that capture actors.

# bishop-memory API Contract

## Base URL and versioning

All endpoints are served from the configured host/port. Resource endpoints are prefixed with `/v1`. `/healthz` is unversioned.

## Global middleware

Every request passes through:

| Middleware | Purpose |
|------------|---------|
| `RequestID` | Generates or echoes `X-Request-ID`; sets `request_id` in Gin context |
| `AccessLog` | Emits a structured access log line with latency, status, method, path, and client IP |
| `Recovery` | Catches panics and returns a 500 response |

## Standard error format

Error responses use a JSON body with an `error` string and an optional
`details` value. `details` is either omitted, a static authored string,
or — for a struct-tag validation failure — a structured list (see
below); it never echoes a raw request-supplied value (CONV-033).

```json
{
  "error": "short error code",
  "details": "optional human-readable detail, or a structured list — see below"
}
```

Examples from `internal/api/errors.go`:

- Validation error (struct-tag failure, e.g. `oneof`/`required`/`max`):
  `400 Bad Request`
  `{ "error": "invalid request", "details": [{"field": "Status", "tag": "oneof"}] }`
- Validation error (malformed JSON body / type mismatch):
  `400 Bad Request`
  `{ "error": "invalid request", "details": "request body failed validation" }`
- Internal error (including a handler panic, caught by the `Recovery`
  middleware): `500 Internal Server Error` `{ "error": "internal server error" }`

Unknown routes return `404 Not Found` `{ "error": "route not found" }`.

## Endpoints

### Health

```text
GET /healthz
```

Purpose: Check process liveness and SQLite connectivity.

**Success — 200 OK**

```json
{
  "ok": true,
  "service": "bishop-memory",
  "storage": "sqlite"
}
```

**Failure — 503 Service Unavailable**

```json
{
  "ok": false,
  "storage": "sqlite",
  "error": "database unavailable"
}
```

### List tasks

```text
GET /v1/tasks?status=<optional>
```

Purpose: List tasks, optionally filtered by status.

**Response — 200 OK**

```json
{
  "tasks": [
    {
      "id": "task-001",
      "title": "...",
      "status": "open",
      "priority": "normal",
      "next_action": "...",
      "blockers": "...",
      "opened_at": "...",
      "closed_at": "...",
      "created_at": "...",
      "updated_at": "..."
    }
  ]
}
```

### Create task

```text
POST /v1/tasks
```

Purpose: Create a task and append a `task.created` event.

**Request body**

```json
{
  "id": "task-001",
  "title": "Task title",
  "status": "open",
  "priority": "normal",
  "next_action": "Next step",
  "blockers": "Known blockers"
}
```

- `id` and `title` are required.
- `status` defaults to `open`; allowed values: `open`, `active`, `blocked`, `complete`, `cancelled`.
- `priority` defaults to `normal`; allowed values: `low`, `normal`, `high`, `urgent`.

**Response — 201 Created**

```json
{
  "id": "task-001",
  "created": true
}
```

**Error — 409 Conflict** if the task cannot be created (for example, duplicate ID).

```json
{
  "error": "task could not be created"
}
```

### Get task

```text
GET /v1/tasks/:taskID
```

Purpose: Fetch a single task.

**Response — 200 OK** returns the task object directly.

**Error — 404 Not Found**

```json
{
  "error": "task not found"
}
```

### Update task

```text
PATCH /v1/tasks/:taskID
```

Purpose: Update status, priority, next action, or blockers.

**Request body**

All fields are optional:

```json
{
  "status": "active",
  "priority": "high",
  "next_action": "Updated next step",
  "blockers": "Updated blockers"
}
```

Setting `status` to `complete` or `cancelled` automatically sets `closed_at` to the current timestamp.

Each field uses `COALESCE(?, existing_value)` server-side, so **omitting a
key (or sending it as JSON `null`) leaves that field unchanged**, while
**sending an explicit empty string (`""`) for `next_action` or `blockers`
clears it** (stores empty string, not NULL). A whitespace-only value
(e.g. `"   "`) is trimmed server-side and treated the same as `""`.
`status` and `priority` have no such clearing behaviour — they are
constrained to the enumerated values below and an explicit `""` for
either fails `oneof` validation.

**Response — 200 OK**

```json
{
  "id": "task-001",
  "updated": true
}
```

**Error — 404 Not Found** if the task does not exist.

### Record task run

```text
POST /v1/tasks/:taskID/runs
```

Purpose: Record one agent execution attempt against a task. Verifies the
task exists (404 rather than a foreign-key error) and appends a
`task.run` event in the same transaction.

**Request body** — all fields optional:

```json
{
  "agent": "junior-developer",
  "status": "completed",
  "summary": "Step 5 deliverable shipped",
  "started_at": "2026-08-20T10:00:00Z",
  "ended_at": "2026-08-20T10:15:00Z"
}
```

`agent` max 128 chars, `status` max 64 chars, `summary` max 2000 chars.
`started_at` / `ended_at` are free-form strings (not currently parsed or
validated as timestamps).

**Response — 201 Created**

```json
{
  "task_id": "task-001",
  "recorded": true
}
```

**Error — 404 Not Found** if the task does not exist.

### Append event

```text
POST /v1/events
```

Purpose: Append a durable agent/system event.

**Request body**

```json
{
  "task_id": "task-001",
  "event_type": "task.created",
  "summary": "Task created: ...",
  "agent": "opencode:orchestrator"
}
```

- `event_type` and `summary` are required (max 64 / 2000 chars respectively,
  and rejected as invalid if empty or whitespace-only after trimming).
- `task_id` is optional; when supplied it must reference an existing
  task (404 otherwise). Omit for system/agent-scoped events.
- `agent` is optional, max 64 chars — the actor attribution
  (`"<harness>:<sub-agent>"`, composed by the `mcpd` MCP adapter).

**Response — 201 Created**

```json
{
  "id": 42,
  "appended": true
}
```

**Error — 404 Not Found** if `task_id` is supplied but does not exist.

### List events

```text
GET /v1/events
```

Purpose: Read recent events or task-specific events, newest first.

**Query parameters**

| Parameter | Required | Description |
|-----------|----------|-------------|
| `task_id` | no | Filter to events for one task (NULL-`task_id` events are excluded when set) |
| `limit` | no | Cap on results. Default 50, max 100. Invalid/zero/negative values silently fall back to the default. |

**Response — 200 OK**

```json
{
  "events": [
    {
      "id": 42,
      "task_id": "task-001",
      "event_type": "task.created",
      "summary": "Task created: ...",
      "agent": "opencode:orchestrator",
      "created_at": "..."
    }
  ]
}
```

`task_id` and `agent` are omitted (empty string) on events with no
associated task / no recorded actor.

### Search memory

```text
GET /v1/memory/search?q=<query>
```

Purpose: FTS5 search across imported memory documents.

**Query parameters**

| Parameter | Required | Description |
|-----------|----------|-------------|
| `q` | yes | FTS5 query string. Multi-word is implicit-AND; wrap phrases in double quotes; `*` is a prefix wildcard. |

Results are always capped at 20 (no `limit` override yet).

**Response — 200 OK**

```json
{
  "q": "importer path escape",
  "results": [
    {
      "id": 7,
      "source_path": "/abs/path/to/file.md",
      "title": "...",
      "kind": "reference",
      "snippet": "...<mark>escape</mark>...",
      "rank": -1.23
    }
  ]
}
```

**Error — 400 Bad Request** in two distinct cases, each with its own
`details` text (the two are NOT interchangeable — see CONV-048):

- `q` missing/empty:
  `{ "error": "invalid request", "details": "q is required" }`
- `q` present but malformed FTS5 query syntax (e.g. an unbalanced quote):
  `{ "error": "invalid request", "details": "invalid search query syntax" }`
  (a static, authored string — never the raw FTS5 driver error or any
  fragment of the submitted `q`; see CONVENTIONS.md CONV-033)

### Sync documents

```text
POST /v1/documents/sync
```

Purpose: Trigger an explicit Markdown/JSONL import of the memory tree into
the `documents` + FTS5 index tables.

**Request body** — optional:

```json
{ "root": "/absolute/path/to/memory/tree" }
```

`root` overrides the configured import root for this call only. When
omitted, the server falls back to the `MEMORY_ROOT` env var, then to
`testdata/memory`. `root` may not be `/` (or its equivalent after path
cleaning) — see "Root safety" below.

**Response — 200 OK**

```json
{
  "synced": true,
  "root": "/absolute/path/to/memory/tree"
}
```

**Error — 400 Bad Request** if `root` resolves to the filesystem root:

```json
{ "error": "invalid request", "details": "root must not be the filesystem root" }
```

**Error — 502 Bad Gateway** if the import fails for any other reason
(missing/unreadable root, path-escape detected, oversized JSONL line, ...):

```json
{ "error": "import failed", "details": "memory tree import failed; see server logs for details" }
```

`details` is a static, authored string — never the importer's own error
text, which can embed the caller-supplied `root` value and paths derived
from walking it (see CONVENTIONS.md CONV-033). The real error is logged
server-side (with the request id) for operator diagnosis.

#### Root safety

`/v1/documents/sync` accepts an arbitrary caller-supplied `root` with
**no allow-list** beyond the single minimal guard described above (a
literal `/`, or a path that cleans/resolves to `/`, is rejected). Any
other absolute path a caller supplies — including one outside the
intended memory tree — will be walked and its contents indexed. A full
allow-list of permitted roots is a deliberate, larger design decision
and is **not implemented**; this is a known, recorded gap (see
`README.md`'s Phase 3 roadmap), acceptable for a locally-bound,
single-operator service but not for anything more broadly reachable.

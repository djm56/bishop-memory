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
or — for a struct-tag validation failure — a structured list; it never echoes a raw request-supplied value (CONV-033).

```json
{
  "error": "short error code",
  "details": "optional human-readable detail, or a structured list"
}
```

Examples:

- Validation error (struct-tag failure, e.g. `oneof`/`required`/`max`):
  `400 Bad Request`
  `{ "error": "invalid request", "details": [{"field": "Status", "tag": "oneof"}] }`
- Validation error (malformed JSON body / type mismatch):
  `400 Bad Request`
  `{ "error": "invalid request", "details": "request body failed validation" }`
- Internal error (including a handler panic, caught by the `Recovery` middleware):
  `500 Internal Server Error` `{ "error": "internal server error" }`

Unknown routes return `404 Not Found` `{ "error": "route not found" }`.

## Endpoints

### Health

```text
GET /healthz
```

Purpose: Check process liveness, SQLite connectivity, and schema readiness.

**Success — 200 OK**

```json
{
  "ok": true,
  "service": "bishop-memory",
  "storage": "sqlite",
  "schema": "ok"
}
```

**Schema incomplete — 503 Service Unavailable**

```json
{
  "ok": false,
  "storage": "sqlite",
  "error": "schema incomplete",
  "missing": ["missions", "documents"]
}
```

**Database unavailable — 503 Service Unavailable**

```json
{
  "ok": false,
  "storage": "sqlite",
  "error": "database unavailable"
}
```

### Missions

#### List missions

```text
GET /v1/missions?status=<optional>
```

Purpose: List missions, optionally filtered by status.

**Query Parameters:**

- `status` (optional) — one of `not-started`, `in-progress`, `blocked`, `complete`. Returns a 400 with validation error if the value is unrecognized.

**Response — 200 OK**

```json
{
  "missions": [
    {
      "id": "mission-20260905-01",
      "title": "Refactor harness vocabulary",
      "status": "in-progress",
      "outcome": null,
      "owner": "bishop",
      "priority": "normal",
      "next_action": "Update documentation",
      "blockers": null,
      "opened_at": "2026-09-05 14:23 UTC",
      "closed_at": null,
      "created_at": "2026-09-05 14:23 UTC",
      "updated_at": "2026-09-05 14:25 UTC"
    }
  ]
}
```

#### Get a mission

```text
GET /v1/missions/:missionID
```

Purpose: Fetch a single mission by ID.

**Response — 200 OK**

Returns a single mission object (same shape as list response).

**Response — 404 Not Found**

```json
{
  "error": "mission not found"
}
```

#### Allocate a mission

```text
POST /v1/missions/allocate
```

Purpose: Allocate a centrally-unique mission ID and create the mission atomically. **Use this instead of `POST /v1/missions` when multiple harnesses share a bishop-memory instance**, so mission IDs never collide across harnesses. The allocation and creation happen inside a single database transaction, and concurrency is handled by retrying on PRIMARY KEY collision; the endpoint is safe under concurrent load.

**Request body:**

```json
{
  "harness": "claude-code",
  "title": "Refactor harness vocabulary",
  "owner": "bishop",
  "priority": "normal",
  "next_action": "Update documentation",
  "date": "20260905"
}
```

**Parameters:**

- `harness` (required) — The calling harness identifier, max 128 chars. Omitting this field returns `400 Bad Request` (this describes the HTTP endpoint contract). The MCP adapter (`mcpd`) optionally fills this from its own `BISHOP_HARNESS` environment variable if the caller omits it; supply it explicitly here whenever more than one harness shares this bishop-memory instance.
- `title` (required) — Human-readable mission title, max 500 chars.
- `owner` (optional) — Mission owner name, max 128 chars.
- `priority` (optional) — one of `low`, `normal`, `high`, `urgent`. Defaults to `normal` server-side when omitted.
- `next_action` (optional) — Free-form next-action note, max 2000 chars.
- `blockers` (optional) — Free-form blockers note, max 2000 chars.
- `date` (optional) — Override the UTC day the ID is scoped to, as YYYYMMDD. Omit in normal operation — the server's UTC clock is authoritative so harnesses in different timezones cannot disagree about what day it is. Present for testing and for backdating a mission being registered late.

**Sequence number allocation:** The endpoint computes the next free sequence number (NN in mission-YYYYMMDD-NN) by scanning ALL missions for the given date across EVERY harness that has ever used this bishop-memory, not just the caller's missions. This global view is the entire reason allocation is centralised. IDs that do not match mission-YYYYMMDD-NN exactly are ignored when computing the maximum, so hand-written or legacy IDs cannot corrupt the sequence.

**Response — 201 Created**

```json
{
  "id": "mission-20260905-01",
  "harness": "claude-code",
  "date": "20260905",
  "seq": 1,
  "created": true,
  "attempts": 1
}
```

Fields:
- `id` — The allocated mission ID.
- `harness` — Echo of the harness parameter.
- `date` — Echo of the date parameter (or the server's UTC date if omitted).
- `seq` — The sequence number NN from the allocated ID.
- `created` — Boolean flag (always true on success).
- `attempts` — How many attempts were needed to allocate (1 in most cases; >1 if concurrent callers lost races).

**Response — 400 Bad Request**

Validation failures: harness is empty, title is empty, or date is invalid (must be YYYYMMDD if supplied).

```json
{
  "error": "harness and title must be non-empty"
}
```

**Response — 503 Service Unavailable**

Concurrency limit reached: after `allocateRetryLimit` (10) consecutive PRIMARY KEY collisions, the endpoint gives up and returns 503 with a `Retry-After: 1` header. This is honest: the request is valid and will likely succeed shortly, but something is generating IDs faster than this loop can settle. Re-sending with exponential backoff is appropriate.

```json
{
  "error": "could not allocate a mission id for 20260905 after 10 attempts"
}
```

#### Create a mission

```text
POST /v1/missions
```

Purpose: Create a new mission.

**Request body:**

```json
{
  "id": "mission-20260905-01",
  "title": "Refactor harness vocabulary",
  "status": "not-started",
  "owner": "bishop",
  "priority": "normal",
  "next_action": "Update documentation",
  "blockers": null
}
```

**Parameters:**

- `id` (required) — Unique mission ID, max 128 chars.
- `title` (required) — Human-readable title, max 500 chars.
- `status` (optional) — one of `not-started`, `in-progress`, `blocked`, `complete`. Defaults to `not-started` server-side.
- `owner` (optional) — Mission owner name, max 128 chars.
- `priority` (optional) — one of `low`, `normal`, `high`, `urgent`. Defaults to `normal` server-side.
- `next_action` (optional) — Free-form next-action note, max 2000 chars.
- `blockers` (optional) — Free-form blockers note, max 2000 chars.

**Response — 201 Created**

```json
{
  "id": "mission-20260905-01",
  "created": true
}
```

**Response — 400 Bad Request**

Out-of-vocabulary values for `status` or `priority` return a 400, not a 500 from a database constraint.

```json
{
  "error": "invalid request",
  "details": [{"field": "Status", "tag": "oneof"}]
}
```

**Response — 409 Conflict**

A duplicate mission ID (PRIMARY KEY collision) returns a clean client-fixable error.

```json
{
  "error": "mission could not be created"
}
```

#### Update a mission

```text
PATCH /v1/missions/:missionID
```

Purpose: Update mission fields (status, owner, outcome, priority, next_action, blockers).

**Request body:**

```json
{
  "status": "in-progress",
  "outcome": null,
  "owner": "bishop",
  "priority": "high",
  "next_action": "Review findings",
  "blockers": ""
}
```

**Parameters:**

- `status` (optional) — one of `not-started`, `in-progress`, `blocked`, `complete`. Omitted fields are unchanged.
- `outcome` (optional) — one of `done`, `failed`. Independently settable from status; not required when status becomes `complete`.
- `owner` (optional) — New owner name, max 128 chars. Omitted fields are unchanged.
- `priority` (optional) — one of `low`, `normal`, `high`, `urgent`. Omitted fields are unchanged.
- `next_action` (optional) — New next_action text. Omitted fields are unchanged.
- `blockers` (optional) — New blockers text. Omitted fields are unchanged.

**Clearing semantics:** Omitting a field leaves the value unchanged. An explicit empty string (`""`) stores an empty string. There is **no update path back to SQL NULL** once a value is set — only another value or an empty string.

**Response — 200 OK**

```json
{
  "id": "mission-20260905-01",
  "updated": true
}
```

**Response — 404 Not Found**

```json
{
  "error": "mission not found"
}
```

### Mission Steps

#### List mission steps

```text
GET /v1/missions/:missionID/steps
```

Purpose: List all steps for a mission (PROGRESS.md view), in insertion order.

**Response — 200 OK**

```json
{
  "steps": [
    {
      "id": 1,
      "mission_id": "mission-20260905-01",
      "step": "1",
      "phase": "setup",
      "agent": "hicks",
      "status": "done",
      "notes": "Initialized repository",
      "summary": "Completed setup phase",
      "started_at": "2026-09-05T14:23:00Z",
      "ended_at": "2026-09-05T14:25:00Z",
      "created_at": "2026-09-05 14:23 UTC"
    }
  ]
}
```

**Response — 404 Not Found**

```json
{
  "error": "mission not found"
}
```

#### Record or update a mission step

```text
POST /v1/missions/:missionID/steps
```

Purpose: Record or update one agent execution attempt against a mission. Upserts on `(mission_id, step)`.

**Request body:**

```json
{
  "step": "1",
  "phase": "setup",
  "agent": "hicks",
  "status": "done",
  "notes": "Initialized repository",
  "summary": "Completed setup phase",
  "started_at": "2026-09-05T14:23:00Z",
  "ended_at": "2026-09-05T14:25:00Z"
}
```

**Parameters:**

- `step` (required) — Step label from PROGRESS.md (e.g., `1`, `3a`), max 16 chars. Absent, empty, or whitespace-only returns 400. This is the upsert key.
- `phase` (optional) — Phase label from PROGRESS.md (e.g., `Core rename`), max 64 chars.
- `agent` (optional) — Agent name (e.g., `hicks`, `bishop`), max 128 chars.
- `status` (optional) — one of `pending`, `in-progress`, `done`, `failed`.
- `notes` (optional) — Free-form step notes, max 2000 chars.
- `summary` (optional) — Free-form step summary (independently set from notes), max 2000 chars.
- `started_at` (optional) — ISO-8601 start timestamp.
- `ended_at` (optional) — ISO-8601 end timestamp.

**Upsert semantics:** The unique index on `(mission_id, step)` causes a reposted step to update the existing row. Missing fields in the payload preserve their stored values via `COALESCE(excluded.<col>, mission_steps.<col>)`, so **no field can be cleared through this endpoint** — an explicit empty string is converted to NULL and then preserved, indistinguishable from omission. Each write appends one `mission.step` audit row to the flight-recorder, capturing the step label, agent, and resulting status.

**Response — 201 Created (new step)**

```json
{
  "mission_id": "mission-20260905-01",
  "created": true
}
```

**Response — 200 OK (step updated)**

```json
{
  "mission_id": "mission-20260905-01",
  "created": false
}
```

**Response — 400 Bad Request (step missing or invalid)**

```json
{
  "error": "invalid request",
  "details": "step is required"
}
```

**Response — 404 Not Found**

```json
{
  "error": "mission not found"
}
```

### Flight Recorder

The audit log for all harness activity (append-only by application convention).

#### List flight recorder events

```text
GET /v1/flight-recorder?mission_id=<optional>&limit=<optional>
```

Purpose: List recent events, optionally filtered by mission and capped by limit.

**Query Parameters:**

- `mission_id` (optional) — Filter to events for one mission. Omit to list all events.
- `limit` (optional) — Cap at 100, default 50. The server enforces a 100-event hard cap.

**Response — 200 OK**

```json
{
  "flight_recorder": [
    {
      "id": 1,
      "mission_id": "mission-20260905-01",
      "step": "1",
      "occurred_at": "2026-09-05 14:23 UTC",
      "event": "mission.created",
      "note": "Mission created: Refactor harness vocabulary",
      "agent": "claude-code:bishop",
      "created_at": "2026-09-05 14:23 UTC"
    }
  ]
}
```

#### Append a flight recorder event

```text
POST /v1/flight-recorder
```

Purpose: Append a durable event to the audit log. Agent identity is captured for actor attribution.

**Request body:**

```json
{
  "mission_id": "mission-20260905-01",
  "step": "1",
  "event": "mission.created",
  "note": "Mission created: Refactor harness vocabulary",
  "occurred_at": "2026-09-05 14:23 UTC",
  "agent": "claude-code:bishop"
}
```

**Parameters:**

- `mission_id` (optional) — Mission ID to scope the event to. Omit for system/agent-scoped events.
- `step` (optional) — Step label from PROGRESS.md, max 16 chars.
- `event` (required) — Dotted event discriminator (e.g., `mission.created`, `agent.heartbeat`), max 64 chars.
- `note` (required) — Human-readable event description, max 2000 chars.
- `occurred_at` (optional) — Timestamp when the event occurred (YYYY-MM-DD HH:MM UTC format), max 64 chars. Defaults to insertion time.
- `agent` (optional) — Agent identity (already composed as `<harness>:<agent>` by the MCP adapter), max 64 chars.

**Response — 201 Created**

```json
{
  "id": 1,
  "appended": true
}
```

**Response — 404 Not Found**

Occurs when `mission_id` is provided but the mission does not exist.

```json
{
  "error": "mission not found"
}
```

### Findings

The improvement ledger (mirrors `.claude/memory/findings/FINDINGS.md`).

#### List findings

```text
GET /v1/findings?status=<optional>
```

Purpose: List findings, newest-first. Optional status filter.

**Query Parameters:**

- `status` (optional) — one of `proposed`, `approved`, `applied`, `rejected`, `retired`, `superseded`. Returns a 400 if unrecognized.

**Response — 200 OK**

```json
{
  "findings": [
    {
      "id": 1,
      "finding_date": "2026-09-05",
      "target": "hicks",
      "suggestion": "Hicks should handle edge cases more defensively",
      "rationale": "Three consecutive steps failed on input validation",
      "status": "proposed",
      "approver": null,
      "date_approved": null,
      "mission_id": "mission-20260905-01",
      "created_at": "2026-09-05 14:30 UTC"
    }
  ]
}
```

#### Create a finding

```text
POST /v1/findings
```

Purpose: Append a finding to the improvement ledger. Creates with `status='proposed'` (always).

**Request body:**

```json
{
  "finding_date": "2026-09-05",
  "target": "hicks",
  "suggestion": "Hicks should handle edge cases more defensively",
  "rationale": "Three consecutive steps failed on input validation",
  "mission_id": "mission-20260905-01"
}
```

**Parameters:**

- `suggestion` (required) — The finding itself, max 2000 chars.
- `finding_date` (optional) — Finding date, max 64 chars.
- `target` (optional) — Target entity (agent, skill, tool name), max 256 chars.
- `rationale` (optional) — Supporting rationale, max 2000 chars.
- `mission_id` (optional) — Mission ID to associate the finding with, max 128 chars. Stored without existence checking — findings outlive missions.

**Restrictions:** `status`, `approver`, and `date_approved` are never accepted as request parameters (silently ignored if provided). Those fields belong to the human operator alone. No update route exists — findings are created `proposed` and never modified through the API.

**Response — 201 Created**

```json
{
  "id": 1,
  "created": true
}
```

### Patterns

Advisory patterns (mirrors `.claude/memory/findings/PATTERNS.md`). Non-binding; directives win on any conflict.

#### List patterns

```text
GET /v1/patterns
```

Purpose: List advisory patterns, newest-first.

**Response — 200 OK**

```json
{
  "patterns": [
    {
      "id": 1,
      "name": "Early validation prevents cascading errors",
      "context": "When a junior developer skips type checks on input",
      "solution": "Validate inputs at the boundary before processing",
      "example": "Check status enum values against the schema constraint BEFORE inserting",
      "discovered_at": "2026-09-04",
      "discovered_mission": "mission-20260904-01",
      "created_at": "2026-09-04 16:00 UTC"
    }
  ]
}
```

#### Create a pattern

```text
POST /v1/patterns
```

Purpose: Append an advisory pattern.

**Request body:**

```json
{
  "name": "Early validation prevents cascading errors",
  "context": "When a junior developer skips type checks on input",
  "solution": "Validate inputs at the boundary before processing",
  "example": "Check status enum values against the schema constraint BEFORE inserting",
  "discovered_at": "2026-09-04",
  "discovered_mission": "mission-20260904-01"
}
```

**Parameters:**

- `name` (required) — Pattern name, max 256 chars.
- `context` (optional) — Context where the pattern applies, max 2000 chars.
- `solution` (optional) — Solution or recommendation, max 2000 chars.
- `example` (optional) — Worked example, max 2000 chars.
- `discovered_at` (optional) — Discovery timestamp, max 64 chars.
- `discovered_mission` (optional) — Mission ID where the pattern was discovered, max 128 chars.

**Response — 201 Created**

```json
{
  "id": 1,
  "created": true
}
```

### Service Records

Agent calibration notes (per-agent service history).

#### List service records

```text
GET /v1/service-records?agent=<optional>
```

Purpose: List service records, newest-first. Optional filter by agent (the subject, not the caller).

**Query Parameters:**

- `agent` (optional) — Agent name (the subject of the records). Omit to list all records.

**Response — 200 OK**

```json
{
  "service_records": [
    {
      "id": 1,
      "agent": "hicks",
      "record_date": "2026-09-05",
      "title": "Improved error handling",
      "note": "Hicks now validates enum values against schema constraints before inserting",
      "adjustment": "Continue this practice in future steps",
      "source": "bishop-observed",
      "created_at": "2026-09-05 15:00 UTC"
    }
  ]
}
```

#### Record a service observation

```text
POST /v1/service-records
```

Purpose: Record an observation about an agent's performance or behaviour.

**Request body:**

```json
{
  "agent": "hicks",
  "record_date": "2026-09-05",
  "title": "Improved error handling",
  "note": "Hicks now validates enum values against schema constraints before inserting",
  "adjustment": "Continue this practice in future steps",
  "source": "bishop-observed"
}
```

**Parameters:**

- `agent` (required) — Agent name (the crew member being recorded about), max 128 chars.
- `record_date` (optional) — Record date, max 64 chars.
- `title` (optional) — Title summarizing the observation, max 256 chars.
- `note` (optional) — Observation note, max 2000 chars.
- `adjustment` (optional) — Adjustment or recommendation, max 2000 chars.
- `source` (optional) — Source classification: `self-reported` or `bishop-observed`. Anything else is rejected with a 400 error.

**Response — 201 Created**

```json
{
  "id": 1,
  "created": true
}
```

### Directives

Binding, human-ratified rules (mirrors `.claude/memory/reference/DIRECTIVES.md`). Read-only by design — no write endpoint exists.

#### List directives

```text
GET /v1/directives
```

Purpose: List directives (the rulebook). Returned in rule order (ORDER BY id ASC), not as a feed.

**Response — 200 OK**

```json
{
  "directives": [
    {
      "id": 1,
      "directive_id": "CONV-028",
      "title": "One implementation per contract",
      "rule": "For any documented contract (API, schema, binding tags), maintain a single source of truth in the code",
      "rationale": "Divergence between documentation and enforcement leads to silent failures",
      "ratified_at": "2026-09-01",
      "created_at": "2026-09-01 10:00 UTC"
    }
  ]
}
```

### Memory Search

Full-text search over imported Markdown and JSONL documents.

#### Search memory

```text
GET /v1/memory/search?q=<required>&limit=<optional>
```

Purpose: Search imported documents via FTS5. Returns ranked hits with snippets.

**Query Parameters:**

- `q` (required) — FTS5 query string. Multi-word is implicit-AND; wrap phrases in double-quotes (`"phrase"`); `*` is a prefix wildcard. Returns 400 if the query has invalid FTS5 syntax.
- `limit` (optional) — Currently advisory only — the server always returns its internal LIMIT 20.

**Response — 200 OK**

```json
{
  "results": [
    {
      "id": 42,
      "source_path": "/path/to/harness/.claude/memory/findings/FINDINGS.md",
      "title": "# FINDINGS",
      "kind": "findings",
      "snippet": "Hicks should handle <mark>edge cases</mark> more defensively…",
      "rank": -2.5
    }
  ],
  "q": "edge cases"
}
```

**Response — 400 Bad Request**

Invalid FTS5 query syntax.

```json
{
  "error": "invalid request",
  "details": "invalid search query syntax"
}
```

### Document Sync

#### Trigger a document sync

```text
POST /v1/documents/sync
```

Purpose: Trigger an import of Markdown/JSONL files from the memory tree into the documents + FTS5 tables.

**Request body:**

```json
{
  "root": "/path/to/memory"
}
```

**Parameters:**

- `root` (optional) — Override the memory root. Omit to use the server's configured `MEMORY_ROOT` env var (or `testdata/memory` default). Must not be the filesystem root (`/` on POSIX).

**Response — 200 OK**

```json
{
  "synced": true,
  "root": "/path/to/memory"
}
```

**Response — 400 Bad Request**

The root is the filesystem root.

```json
{
  "error": "invalid request",
  "details": "root must not be the filesystem root"
}
```

**Response — 502 Bad Gateway**

The import failed (e.g., root does not exist, file read errors).

```json
{
  "error": "import failed",
  "details": "memory tree import failed; see server logs for details"
}
```

## Root safety

`POST /v1/documents/sync` accepts a caller-supplied `root` parameter. The server enforces only a **filesystem-root guard**: it rejects a root that is (or resolves to) the filesystem root (`/` on POSIX, bare volume root on Windows), so a caller cannot point this service at `/` and have the importer walk the entire filesystem.

This is a minimal, single-bounded-case defense. **No allow-list is implemented.** A full allow-list of permitted memory roots is a separate design decision tracked outside this API contract.

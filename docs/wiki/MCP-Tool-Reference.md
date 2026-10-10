# MCP Tool Reference

Every tool `mcpd` exposes, in both profiles, generated from the adapter source (`cmd/mcpd/main.go`, `cmd/mcpd/triage.go`) by `scripts/gen-mcp-reference.py`, with worked examples. An asterisk marks a required argument. Every tool returns the HTTP route's JSON body as text; a non-2xx status or transport failure comes back as an MCP error result naming the tool, never as a thrown error.

## Profiles

`mcpd` registers one of two tool sets, chosen by the `MCPD_PROFILE` environment variable in its registration:

- **harness** (default) — the 16 tools a Bishop crew uses during a mission. Registered by the harness's generated `.mcp.json` with `BISHOP_HARNESS` set to that harness's name.
- **triage** — the 13 tools the two scheduled findings-triage agents use. Registered only by `scripts/triage-run.sh`.

The two profiles share no write tool. Neither profile can change a finding's status, decide a directive proposal, or write a directive: those routes exist on HTTP for the operator and are never registered as tools.

## Agent identity

Two harness tools record who acted: `flight_recorder_append` and `mission_step_record`. Each takes an `agent` argument naming the sub-agent (`bishop`, `hicks`, …) and `mcpd` stores `<BISHOP_HARNESS>:<agent>`, for example `kirsch:hicks`. `finding_append` records the harness only. Nothing else carries identity.


## Harness profile (16 tools)


### `memory_search`

**Route:** `GET /v1/memory/search`

Search bishop-memory imported documents (FTS5) by query string. Returns ranked hits with snippets; an empty FTS5 index yields an empty results list.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `q` | string | yes | FTS5 query string. Multi-word is implicit-AND; wrap phrases in double-quotes; '*' is a prefix wildcard. Required. |
| `limit` | integer | no | Optional cap on results (the server caps at 20). Currently advisory only — the server always returns its internal LIMIT 20. |


**Example call**
```json
{
  "q": "state-sync \"positive control\"",
  "limit": 5
}
```

**Example result**
```json
{
  "query": "state-sync \"positive control\"",
  "results": [
    {
      "id": 41,
      "title": "mission-20260916-02 — DEBRIEF",
      "kind": "debrief",
      "source_path": "/…/.claude/memory/missions/mission-20260916-02/DEBRIEF.md",
      "snippet": "…paired with a <b>positive control</b> proven to return…"
    }
  ]
}
```

Multi-word is implicit AND; wrap phrases in double quotes; a single trailing `*` on a plain word is a prefix match (`verif*`). Any word containing punctuation — a hyphenated mission id, `step-sync`, `FLIGHT-RECORDER` — is matched as a literal phrase automatically, so it never needs quoting. An unterminated `"` returns a 400, and no error ever echoes your query. An empty index returns an empty list, not an error. Full rules: `[Developer: HTTP API](Developer-HTTP-API#search-query-handling)`, *Query handling*.


### `mission_list`

**Route:** `GET /v1/missions`

List missions from bishop-memory, newest-updated first. Returns {"missions":[...]}.

_No arguments._


**Example call**
```json
{}
```

**Example result**
```json
{
  "missions": [
    {
      "id": "mission-20261002-02",
      "title": "Close Milestone 2",
      "status": "in-progress",
      "outcome": null,
      "harness": "kirsch",
      "priority": "normal",
      "next_action": "",
      "updated_at": "2026-10-02 08:25 UTC"
    }
  ]
}
```

Newest-updated first. There is no status filter on the tool; filter the list yourself.


### `mission_get`

**Route:** `GET /v1/missions/:id`

Get a single mission by its ID. Returns the mission JSON or a 404 if the ID is unknown.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `id` | string | yes | Mission ID (the same value used by mission_create / mission_update). Required. |


**Example call**
```json
{
  "id": "mission-20261002-02"
}
```

**Example result**
```json
{
  "id": "mission-20261002-02",
  "title": "Close Milestone 2",
  "status": "in-progress",
  "harness": "kirsch"
}
```

`.` and `..` are refused before any request is made: they would collapse the URL onto the list route.


### `mission_allocate`

**Route:** `POST /v1/missions/allocate`

Allocate a centrally-unique mission ID and create the mission in one atomic step. Use this INSTEAD of mission_create when the harness runs in central mode, so mission IDs never collide between harnesses. The harness is read from the calling harness's .claude/bishop-memory.conf (key BISHOP_HARNESS) and passed as the harness parameter; omit it to fall back to the server's BISHOP_HARNESS env var. Supply it whenever more than one harness shares this bishop-memory, because the env var is user-scoped and identical across projects. Returns {"id":...,"harness":...,"date":...,"seq":...,"created":true}.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `title` | string | yes | Human-readable mission title, max 500 chars. Required. |
| `harness` | string | no | The owning harness, read from the calling harness's .claude/bishop-memory.conf; omit it to fall back to the server's BISHOP_HARNESS env var; supply it whenever more than one harness shares this bishop-memory, because the env var is user-scoped and identical across projects. Optional. |
| `owner` | string | no | Mission owner name, max 128 chars. Optional. |
| `priority` | string | no | One of low\|normal\|high\|urgent. Defaults to "normal" server-side when omitted. |
| `next_action` | string | no | Free-form next-action note, max 2000 chars. Optional. |
| `blockers` | string | no | Free-form blockers note, max 2000 chars. Optional. |
| `date` | string | no | Override the UTC day the ID is scoped to, as YYYYMMDD. Omit in normal use — the SERVER's UTC clock is authoritative, so that harnesses in different timezones cannot disagree about what day it is. Optional. |


**Example call**
```json
{
  "title": "Fix Linux-only CI test failures",
  "harness": "kirsch",
  "owner": "bishop"
}
```

**Example result**
```json
{
  "id": "mission-20261002-03",
  "harness": "kirsch",
  "date": "20261002",
  "seq": 3,
  "created": true,
  "attempts": 1
}
```

Use this, not `mission_create`, in central mode. The sequence is global across every harness for that UTC day, so two harnesses cannot collide. Pass `harness` from the calling harness's `.claude/bishop-memory.conf`; omit it only when one harness uses the service.


### `mission_create`

**Route:** `POST /v1/missions`

Create a new mission. Returns {"id":...,"created":true}.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `id` | string | yes | Unique mission ID, max 128 chars. Required. |
| `title` | string | yes | Human-readable mission title, max 500 chars. Required. |
| `status` | string | no | One of not-started\|in-progress\|blocked\|complete. Defaults to "not-started" server-side when omitted. |
| `owner` | string | no | Mission owner name, max 128 chars. Optional. |
| `priority` | string | no | One of low\|normal\|high\|urgent. Defaults to "normal" server-side when omitted. |
| `next_action` | string | no | Free-form next-action note, max 2000 chars. Optional. |
| `blockers` | string | no | Free-form blockers note, max 2000 chars. Optional. |


**Example call**
```json
{
  "id": "mission-20261002-03",
  "title": "Fix Linux-only CI test failures",
  "status": "in-progress"
}
```

**Example result**
```json
{
  "id": "mission-20261002-03",
  "created": true
}
```

Standalone mode only, where the harness derives the id itself. A duplicate id is a 409.


### `mission_update`

**Route:** `PATCH /v1/missions/:id`

Update an existing mission (status / owner / outcome / priority / next_action / blockers). Returns {"id":...,"updated":true} or a 404 if the mission is unknown.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `id` | string | yes | Mission ID to update. Required. |
| `status` | string | no | New status, one of not-started\|in-progress\|blocked\|complete. Omitted fields are unchanged. |
| `owner` | string | no | New owner name, max 128 chars. Omitted fields are unchanged. |
| `outcome` | string | no | Mission outcome, one of done\|failed. Optional, independently settable from status. Omitted fields are unchanged. |
| `priority` | string | no | New priority, one of low\|normal\|high\|urgent. Omitted fields are unchanged. |
| `next_action` | string | no | New next_action text. Omitted fields are unchanged. |
| `blockers` | string | no | New blockers text. Omitted fields are unchanged. |


**Example call**
```json
{
  "id": "mission-20261002-03",
  "status": "complete",
  "outcome": "done",
  "next_action": ""
}
```

**Example result**
```json
{
  "id": "mission-20261002-03",
  "updated": true
}
```

Omitted fields are unchanged. Setting `status` to `complete` stamps `closed_at`. The canonical value for an absent `next_action` is the empty string.


### `flight_recorder_append`

**Route:** `POST /v1/flight-recorder`

Append an event to the flight recorder (audit log). Agent identity is composed as "<BISHOP_HARNESS>:<agent>" (e.g. "claude-code:bishop") and stored on the event row for actor attribution. Returns {"appended":true,"id":...}.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `mission_id` | string | no | Optional mission ID to scope the event to. Omit for system / agent-scoped events. |
| `step` | string | no | Optional step label from PROGRESS.md (e.g., "4a", "—"), max 16 chars. |
| `event` | string | yes | Dotted event discriminator, e.g. "mission.created", "agent.heartbeat", max 64 chars. Required. |
| `note` | string | yes | Short human-readable note about the event, max 2000 chars. Required. |
| `occurred_at` | string | no | Optional timestamp when the event occurred (YYYY-MM-DD HH:MM UTC format), max 64 chars. |
| `agent` | string | yes | Sub-agent name (the part AFTER the harness prefix), e.g. "bishop", "hicks". Composed with BISHOP_HARNESS to form the stored actor identity ("<harness>:<agent>"); the SERVER enforces a 64-char cap on that composed string (flight_recorder.agent), so a long agent name combined with the harness prefix may be rejected as invalid. Required. |


**Example call**
```json
{
  "mission_id": "mission-20261002-03",
  "step": "4a",
  "event": "step-sync",
  "note": "Step 4a done; tests green",
  "agent": "lambert",
  "occurred_at": "2026-10-02 09:31 UTC"
}
```

**Example result**
```json
{
  "appended": true,
  "id": 3275
}
```

The stored actor is `<BISHOP_HARNESS>:<agent>`, e.g. `kirsch:lambert`. `mission_id` must exist (404 otherwise); omit it for a system-scoped event. `event` is a dotted or hyphenated discriminator the API does not validate against a list.


### `mission_step_record`

**Route:** `POST /v1/missions/:id/steps`

Record a mission step (one agent execution attempt against a mission). Agent identity is composed as "<BISHOP_HARNESS>:<agent>". Returns {"recorded":true,"mission_id":...} or a 404 if the mission is unknown.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `id` | string | yes | Mission ID the step is recorded against. Required. |
| `step` | string | yes | Step label from PROGRESS.md (e.g., "4a"), max 16 chars. Required. |
| `phase` | string | no | Optional phase label from PROGRESS.md (e.g., "Core rename"), max 64 chars. |
| `agent` | string | yes | Sub-agent name (the part AFTER the harness prefix), e.g. "hicks", "bishop". Composed with BISHOP_HARNESS to form the stored actor identity ("<harness>:<agent>"); the SERVER enforces a 128-char cap on that composed string (mission_steps.agent, per missionStepRequest's binding tag), so a long agent name combined with the harness prefix may be rejected as invalid. Required. |
| `status` | string | no | Step status, one of pending\|in-progress\|done\|failed. |
| `notes` | string | no | Free-form notes about the step, max 2000 chars. |
| `summary` | string | no | Free-form summary of the step outcome, max 2000 chars. Independently set from notes: use summary for a brief result statement, notes for detailed observations. |
| `started_at` | string | no | ISO-8601 start timestamp. Optional. |
| `ended_at` | string | no | ISO-8601 end timestamp. Optional. |


**Example call**
```json
{
  "id": "mission-20261002-03",
  "step": "4a",
  "phase": "Fix",
  "agent": "hicks",
  "status": "done",
  "summary": "Lint clean"
}
```

**Example result**
```json
{
  "recorded": true,
  "mission_id": "mission-20261002-03",
  "created": false
}
```

Upsert keyed on `(mission_id, step)`: the first call creates (201, `created: true`), later calls update and preserve omitted fields. An empty string behaves like an omitted field, so a value cannot be cleared through this tool.


### `documents_sync`

**Route:** `POST /v1/documents/sync`

Trigger a document sync / import into the FTS5 index: every registered harness memory root (and its agents/ into crew) when root is omitted, else the given root. Returns {"roots":[...],"synced":true}, {"root":...,"synced":true} for one root, or a 502 if the import failed.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `root` | string | no | Optional memory root to sync on its own. Omit to sync every registered harness (or, with none registered, the server's MEMORY_ROOT or "testdata/memory"). |


**Example call**
```json
{
  "root": "/abs/path/to/harness/.claude/memory"
}
```

**Example result**
```json
{
  "root": "/abs/path/to/harness/.claude/memory",
  "synced": true
}
```

Incremental: unchanged files are skipped by SHA-256. The filesystem root `/` is refused. This is the one tool with a five-minute timeout; everything else has thirty seconds.


### `mission_steps_list`

**Route:** `GET /v1/missions/:id/steps`

List steps for a mission. Returns {"steps":[...]}. Returns 404 if the mission is unknown.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `id` | string | yes | Mission ID. Required. |


**Example call**
```json
{
  "id": "mission-20261002-03"
}
```

**Example result**
```json
{
  "steps": [
    {
      "id": 912,
      "mission_id": "mission-20261002-03",
      "step": "4a",
      "phase": "Fix",
      "agent": "kirsch:hicks",
      "status": "done",
      "summary": "Lint clean"
    }
  ]
}
```


### `finding_list`

**Route:** `GET /v1/findings`

List findings from the improvement ledger, newest-first. Optional status filter to view findings at a particular stage (e.g., 'proposed' to see what's awaiting operator approval, or 'approved' to see binding findings). Agents can only CREATE findings; status changes belong to the human operator. Returns {"findings":[...]} or a validation error if the status value is unrecognized.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `status` | string | no | Optional status filter: one of proposed\|approved\|applied\|rejected\|retired\|superseded. Omit to list all findings regardless of status. |
| `category` | string | no | Optional triage category slug (the triage_categories tool or GET /v1/finding-categories lists them), or 'uncategorised' for findings with no classification. Omit for all categories. |
| `harness` | string | no | Optional owning-harness filter. Omit for every harness. |
| `ids` | string | no | Optional comma-separated list of finding ids. |
| `limit` | integer | no | Optional page size; omit for everything. |


**Example call**
```json
{
  "status": "approved",
  "category": "brief-writing"
}
```

**Example result**
```json
{
  "findings": [
    {
      "id": 70,
      "finding_date": "2026-09-14",
      "target": "mission-lifecycle",
      "suggestion": "State the precondition for renumbering PROGRESS.md rows…",
      "status": "approved",
      "approver": "Donovan Maidens",
      "date_approved": "02 October 2026",
      "harness": "kirsch",
      "triage": {
        "category": "state-files-and-journal",
        "confidence": 0.95,
        "summary": "Renumber only rows with no FLIGHT-RECORDER history"
      }
    }
  ]
}
```

The crew's way to read ratified findings for an area before briefing it: `status=approved` plus the category. Each row carries its triage classification and, when one is pending, its recommendation. `category=uncategorised` lists findings with no classification.


### `pattern_list`

**Route:** `GET /v1/patterns`

List advisory patterns. Patterns are non-binding; directives win on any conflict. Returns {"patterns":[...]} newest-first.

_No arguments._


**Example call**
```json
{}
```

**Example result**
```json
{
  "patterns": [
    {
      "id": 45,
      "name": "Set file modes in tests with Chmod",
      "context": "A test asserts on a file's permission bits.",
      "solution": "…",
      "discovered_mission": "mission-20261002-01"
    }
  ]
}
```

Advisory. Directives win on any conflict.


### `service_record_list`

**Route:** `GET /v1/service-records`

List service records (observations about agent performance and behaviour), newest-first. Optional agent filter to view only records about a specific crew member (returns empty list if no matches). Agents can CREATE records only; no update route exists. Returns {"service_records":[...]}.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `agent` | string | no | Optional filter: crew member name (the subject of the records, not the caller). Omit to list all service records regardless of agent. |


**Example call**
```json
{
  "agent": "lambert"
}
```

**Example result**
```json
{
  "service_records": [
    {
      "id": 83,
      "agent": "lambert",
      "record_date": "2026-10-02",
      "title": "Exact-text edits accurate; trailers still intermittent",
      "source": "bishop-observed"
    }
  ]
}
```

`agent` is the subject of the record, not the caller.


### `finding_append`

**Route:** `POST /v1/findings`

Append a finding to the findings ledger. Creates with status='proposed' — agents cannot set or advance status, approver, or date_approved. Those fields belong to the human operator. Returns {"id":...,"created":true}.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `suggestion` | string | yes | The finding itself, max 2000 chars. Required. |
| `finding_date` | string | no | Optional finding date, max 64 chars. |
| `target` | string | no | Optional target entity (agent, skill, or tool concept name), max 256 chars. Passed through unchanged — target names the subject the finding is ABOUT, not the caller. Not composed with harness prefix. |
| `rationale` | string | no | Optional supporting rationale, max 2000 chars. |
| `mission_id` | string | no | Optional mission ID to associate the finding with, max 128 chars. Stored without existence checking — findings outlive missions. |


**Example call**
```json
{
  "suggestion": "Every review brief pastes the reviewed step's own diff hunks.",
  "finding_date": "2026-10-02",
  "target": "@bishop (review briefs)",
  "rationale": "The reviewer has no shell…",
  "mission_id": "mission-20261002-01"
}
```

**Example result**
```json
{
  "id": 315,
  "created": true
}
```

Always created `proposed`; `status`, `approver` and `date_approved` are not accepted. The owning harness is taken from `BISHOP_HARNESS`, never from the caller. The reconciler creates the same row from `FINDINGS.md` using the natural key `(finding_date, target, suggestion)`, so a finding written both ways is stored once.


### `pattern_append`

**Route:** `POST /v1/patterns`

Append an advisory pattern to the patterns ledger. Patterns are non-binding; directives win on any conflict. Returns {"id":...,"created":true}.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `name` | string | yes | Pattern name, max 256 chars. Required. |
| `context` | string | no | Optional context where the pattern applies, max 2000 chars. |
| `solution` | string | no | Optional solution or recommendation, max 2000 chars. |
| `example` | string | no | Optional worked example, max 2000 chars. |
| `discovered_at` | string | no | Optional discovery timestamp, max 64 chars. |
| `discovered_mission` | string | no | Optional mission ID where the pattern was discovered, max 128 chars. |


**Example call**
```json
{
  "name": "Pair a filtered item with an unfiltered control",
  "context": "Testing that something is removed or stripped.",
  "solution": "Assert the control survives in the same test.",
  "discovered_at": "2026-09-30",
  "discovered_mission": "mission-20260930-01"
}
```

**Example result**
```json
{
  "id": 46,
  "created": true
}
```


### `service_record_append`

**Route:** `POST /v1/service-records`

Record a service observation about an agent's performance or behaviour. Agent names the subject (which crew member the record is about). Source must be 'self-reported' or 'bishop-observed'. Returns {"id":...,"created":true}.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `agent` | string | yes | Agent name (the crew member being recorded about), max 128 chars. Required. Passed through unchanged — not composed with harness prefix. |
| `record_date` | string | no | Optional record date, max 64 chars. |
| `title` | string | no | Optional title summarizing the observation, max 256 chars. |
| `note` | string | no | Optional observation note, max 2000 chars. |
| `adjustment` | string | no | Optional adjustment or recommendation, max 2000 chars. |
| `source` | string | no | Optional source classification: 'self-reported' or 'bishop-observed'. Anything else is rejected with a 400 error. |


**Example call**
```json
{
  "agent": "hicks",
  "record_date": "2026-10-02",
  "title": "Every code fix landed correct",
  "note": "…",
  "adjustment": "Keep the clause …",
  "source": "bishop-observed"
}
```

**Example result**
```json
{
  "id": 84,
  "created": true
}
```

`source` must be `self-reported` or `bishop-observed` (or omitted).


## Triage profile (13 tools)

`memory_search`, `pattern_list` and `finding_list` are the same tools as in the harness profile and are not repeated here. The rest are below.


### `triage_categories`

**Route:** `GET /v1/finding-categories`

List the active finding categories with their descriptions and examples, plus per-category counts of proposed findings and pending recommendations. Classify ONLY into slugs returned here.

_No arguments._


**Example call**
```json
{}
```

**Example result**
```json
{
  "categories": [
    {
      "slug": "brief-writing",
      "name": "Brief writing",
      "description": "How Bishop writes a brief…",
      "examples": "…",
      "proposed_count": 70,
      "pending_count": 30,
      "last_processed_at": "2026-10-02 10:52:06"
    }
  ],
  "uncategorised_count": 0
}
```

The slugs returned here are the only legal values for `triage_classify`.


### `triage_next_unclassified`

**Route:** `GET /v1/findings?unclassified=1&order=asc`

Fetch the next batch of findings that have no classification yet, oldest first. Returns {"findings":[...]}; an empty list means classification is complete.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `limit` | integer | no | Batch size, default 50, max 200. |


**Example call**
```json
{
  "limit": 50
}
```

**Example result**
```json
{
  "findings": [
    {
      "id": 12,
      "finding_date": "2026-09-03",
      "target": "mission-lifecycle skill",
      "suggestion": "…",
      "rationale": "…",
      "status": "proposed"
    }
  ]
}
```

Oldest first. An empty list means classification is complete.


### `triage_category_findings`

**Route:** `GET /v1/findings?category=&status=proposed&no_pending=1`

List the proposed findings in one category that have no pending recommendation yet (the processor's work list), oldest first, each with its classification summary.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `category` | string | yes | Category slug. Required. |
| `limit` | integer | no | Cap on findings returned (the runner passes TRIAGE_ITEMS_PER_RUN). Omit for all. |
| `include_pending` | boolean | no | Set true to also include findings that already have a pending recommendation (for context only; do not re-recommend them). |


**Example call**
```json
{
  "category": "brief-writing",
  "limit": 30
}
```

**Example result**
```json
{
  "findings": [
    {
      "id": 3,
      "status": "proposed",
      "triage": {
        "category": "brief-writing",
        "summary": "Bound how strongly a deliverable may state a claim"
      }
    }
  ]
}
```

Only proposed findings without a pending recommendation, unless `include_pending` is true.


### `triage_recent_decisions`

**Route:** `GET /v1/triage/decisions`

The operator's most recent decisions in a category, each with the recommendation that preceded it. Read these before recommending so your recommendations follow the operator's demonstrated judgement.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `category` | string | no | Category slug. Omit for every category. |
| `limit` | integer | no | Default 20. |


**Example call**
```json
{
  "category": "brief-writing",
  "limit": 20
}
```

**Example result**
```json
{
  "findings": [
    {
      "id": 71,
      "status": "rejected",
      "decision_note": "Tool roster varies per build",
      "recommendation": {
        "recommendation": "approve",
        "state": "declined"
      }
    }
  ]
}
```

What the operator decided and whether they took the earlier recommendation. Read before recommending.


### `triage_run_start`

**Route:** `POST /v1/triage/runs`

Open a triage run row. Call once at the start; pass the returned id as run_id on every write and to triage_run_finish.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `kind` | string | yes | classify, process or grade. Required. |
| `category` | string | no | For a process run: the category being processed. |
| `model` | string | no | The model id doing the work, e.g. claude-haiku-4-5-20251001. |


**Example call**
```json
{
  "kind": "process",
  "category": "brief-writing",
  "model": "claude-sonnet-5-5"
}
```

**Example result**
```json
{
  "id": 4,
  "created": true
}
```

Pass the returned id as `run_id` on every write.


### `triage_run_finish`

**Route:** `PATCH /v1/triage/runs/:id`

Close a triage run with its counts. A finished process run advances the category rotation.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `id` | integer | yes | Run id from triage_run_start. Required. |
| `status` | string | yes | done or failed. Required. |
| `considered` | integer | no | How many findings were read. |
| `written` | integer | no | How many rows were written (classifications or recommendations). |
| `notes` | string | no | One paragraph: what was done, anything the operator should know. |


**Example call**
```json
{
  "id": 4,
  "status": "done",
  "considered": 30,
  "written": 30,
  "notes": "4 groups, 26 approve, 1 supersede, 1 reject, 2 defer, 2 directive drafts."
}
```

**Example result**
```json
{
  "id": 4,
  "status": "done",
  "updated": true
}
```

A finished `process` run stamps the category's `last_processed_at`, which moves the nightly rotation on.


### `triage_classify`

**Route:** `PUT /v1/triage/classifications`

Write classifications for a batch of findings (upsert; re-classifying a finding replaces its row). The server rejects the whole batch if any category slug is not active or any finding_id is unknown.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `classified_by` | string | yes | Model id doing the classification. Required. |
| `run_id` | integer | no | Run id from triage_run_start. |
| `items` | array | yes | Array of {finding_id, category, secondary_category?, directive_candidate?, confidence (0-1), summary (<=140 chars)}. Required, 1-200 items. |


**Example call**
```json
{
  "classified_by": "claude-haiku-4-5-20251001",
  "run_id": 1,
  "items": [
    {
      "finding_id": 12,
      "category": "mission-planning",
      "confidence": 0.9,
      "directive_candidate": false,
      "summary": "Define a resume path for a partially completed step"
    }
  ]
}
```

**Example result**
```json
{
  "classified": 1
}
```

Whole batch refused with a 400 naming the item if a slug is not active or an id is unknown.


### `triage_group_create`

**Route:** `POST /v1/finding-groups`

Create a group: a cluster of findings that are instances of one underlying rule. Returns {"id":...}; pass it as group_id in triage_recommend and directive_propose.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `category` | string | yes | Category slug. Required. |
| `title` | string | yes | Short name for the rule, <=200 chars. Required. |
| `summary` | string | yes | The one rule every member is an instance of, 1-3 sentences. Required. |
| `target` | string | no | The doctrine file the fix belongs in, e.g. .claude/skills/mission-lifecycle/SKILL.md. |
| `run_id` | integer | no | Run id from triage_run_start. |


**Example call**
```json
{
  "category": "brief-writing",
  "title": "Quote literal text in a brief",
  "summary": "Where a brief depends on exact text, it quotes it verbatim.",
  "target": ".claude/agents/bishop.md",
  "run_id": 4
}
```

**Example result**
```json
{
  "id": 3,
  "created": true
}
```


### `triage_recommend`

**Route:** `POST /v1/finding-recommendations`

Write recommendations for a batch of proposed findings. Each item: {finding_id, recommendation: approve|reject|supersede|defer, rationale, group_id?, superseded_by? (required for supersede), proposed_change? (exact before/after text for an approve)}. A finding that is no longer proposed is skipped and reported. Never changes a finding's status.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `run_id` | integer | no | Run id from triage_run_start. |
| `items` | array | yes | Array of recommendation items. Required, 1-200. |


**Example call**
```json
{
  "run_id": 4,
  "items": [
    {
      "finding_id": 36,
      "group_id": 3,
      "recommendation": "approve",
      "rationale": "Not covered in bishop.md.",
      "proposed_change": "File: .claude/agents/bishop.md, under \"Writing briefs\"\nADD bullet: …"
    },
    {
      "finding_id": 33,
      "recommendation": "supersede",
      "superseded_by": 49,
      "rationale": "#49 states the same rule with more detail."
    }
  ]
}
```

**Example result**
```json
{
  "written": 2,
  "skipped": []
}
```

A finding that is no longer `proposed` is skipped and listed. An existing pending recommendation is expired and replaced.


### `directive_propose`

**Route:** `POST /v1/directive-proposals`

Draft a DIRECTIVES.md entry for the operator to ratify. Fields mirror the DIRECTIVES-TEMPLATE; the server rejects a draft whose rendered entry exceeds 1,510 characters. This creates a PROPOSAL only; a human ratifies it on the review page.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `title` | string | yes | <=60 chars. Required. |
| `applies_when` | string | yes | Observable trigger, phrased 'A change that <verb>s <object>'. Required. |
| `rule` | string | yes | One to four sentences, MUST / MUST NOT, a position not a site list. Required. |
| `rationale` | string | yes | One sentence: the failure mode this prevents. Required. |
| `reviewer_check` | string | yes | One or two yes/no questions against the change. Required. |
| `example` | string | no | Optional one-line synthetic example (the 'Illustrative —' prefix is added for you). |
| `evidence` | array | yes | Array of finding ids that are instances of this rule. Required. |
| `group_id` | integer | no | The group this directive generalises. |
| `harness` | string | no | Which harness's DIRECTIVES.md this is for. Omit when it applies to every harness. |
| `run_id` | integer | no | Run id from triage_run_start. |


**Example call**
```json
{
  "title": "Briefs quote text and records literally",
  "applies_when": "A change that writes a brief which depends on exact code, comment or state-record text",
  "rule": "A brief MUST quote such text verbatim and MUST NOT paraphrase it.",
  "rationale": "A paraphrase that does not exist in the tree gets transcribed as fact.",
  "reviewer_check": "Does every quoted string in the brief match the file byte for byte?",
  "evidence": [
    36,
    69,
    73
  ],
  "group_id": 3,
  "harness": "kirsch",
  "run_id": 4
}
```

**Example result**
```json
{
  "id": 6,
  "created": true,
  "rendered_length": 742
}
```

Creates a proposal only; a human ratifies it on the review page. Refused when the rendered entry would exceed 1,510 characters.


### `grade_claim`

**Route:** ``

Claim up to 10 finished missions that have no grade and return a grading packet for each: the mission row, signals the server counted (criteria met, steps by status, injected steps, escalations, QA steps, blocked events, findings), the brief's goal and acceptance criteria, the debrief's judgement sections, the steps, the findings and the agent notes. Everything you may grade on is in the packet. Returns {"missions":[...]}; an empty list means nothing is waiting. Call it once per run.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `run_id` | integer | no | Run id from triage_run_start. |
| `limit` | integer | no | How many missions to claim, 1-10 (default 10). |
| `mission_ids` | array | no | Optional: claim exactly these missions (the runner passes them when the operator named some). |


### `grade_write`

**Route:** ``

Write the verdicts for missions you claimed. Each item: {mission_id, grade: A|B|C|D|E|F, summary, suggestions} or {mission_id, insufficient: true, summary} when the packet does not hold enough to grade. A verdict is final; an unclaimed mission or one that already has a verdict is skipped and reported.

| Argument | Type | Required | Meaning |
|---|---|---|---|
| `graded_by` | string | yes | Model id doing the grading. Required. |
| `run_id` | integer | no | Run id from triage_run_start. |
| `items` | array | yes | Array of verdict items, 1-10. Required. |


## Errors you will see

| Message shape | Meaning | What to do |
|---|---|---|
| `<tool>: \`x\` is required and must not be empty` | The adapter refused the call before any request was made | Supply the argument |
| `<tool>: "id" must not be "." or ".."` | A dot segment would collapse the URL onto another route | Use a real id |
| `<tool>: HTTP 400: {"error":"invalid request","details":…}` | The service rejected the body; `details` names the field and tag, or carries a static message | Fix the named field |
| `<tool>: HTTP 404: …` | Unknown mission, finding, run or proposal | Check the id |
| `<tool>: HTTP 409: …` | `mission_create` with an id that exists | Use `mission_allocate` in central mode |
| `<tool>: HTTP 503: …` | `mission_allocate` lost ten id races in a row | Retry after a second |
| `<tool>: HTTP transport error: …` | The service is not running or the URL is wrong | `curl http://127.0.0.1:8787/healthz`; check `BISHOP_MEMORY_URL` in `.mcp.json` |
| `<tool>: server returned a 2xx response with a malformed JSON body` | A service-side bug | Report it with the tool name |


## Regenerating this page

```bash
scripts/gen-mcp-reference.py          # rewrite docs/wiki/MCP-Tool-Reference.md from the source
scripts/gen-mcp-reference.py --check  # exit 1 if the page is stale
```

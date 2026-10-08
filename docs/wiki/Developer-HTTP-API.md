# Developer: HTTP API

The route catalogue, authentication, the error contract, the request and response details the [MCP Tool Reference](MCP-Tool-Reference) does not cover, and the pattern for adding a route. Routes that back a tool are documented there, with arguments and example results; this page covers the rest.

## Route catalogue

Registered in `internal/api/router.go`. "Tool" is the `mcpd` tool that proxies the route, if any.

| Method | Path | Handler | Tool (profile) |
|---|---|---|---|
| GET | `/healthz` | `healthHandler` | — |
| GET | `/triage` | `ui.TriagePageHandler` | — (review page) |
| GET | `/missions` | `ui.MissionsPageHandler` | — (mission HUD) |
| GET | `/` | inline | — (302 to `/missions`) |
| GET | `/favicon.svg`, `/favicon.ico` | `ui.FaviconSVGHandler`, `ui.FaviconICOHandler` | — (both pages' icon; `.ico` serves a 32px PNG) |
| GET | `/v1/hud/missions` | `hudMissionsHandler` | — (mission HUD) |
| GET | `/v1/hud/missions/:missionID` | `hudMissionHandler` | — (mission HUD) |
| GET | `/v1/missions` | `listMissionsHandler` | `mission_list` (harness) |
| POST | `/v1/missions` | `createMissionHandler` | `mission_create` (harness) |
| POST | `/v1/missions/allocate` | `allocateMissionHandler` | `mission_allocate` (harness) |
| GET | `/v1/missions/:missionID` | `getMissionHandler` | `mission_get` (harness) |
| PATCH | `/v1/missions/:missionID` | `updateMissionHandler` | `mission_update` (harness) |
| GET | `/v1/missions/:missionID/steps` | `listMissionStepsHandler` | `mission_steps_list` (harness) |
| POST | `/v1/missions/:missionID/steps` | `createMissionStepHandler` | `mission_step_record` (harness) |
| GET | `/v1/flight-recorder` | `listFlightRecorderHandler` | — (reconciler) |
| POST | `/v1/flight-recorder` | `appendFlightRecorderHandler` | `flight_recorder_append` (harness) |
| GET | `/v1/findings` | `listFindingsHandler` | `finding_list` (both) |
| POST | `/v1/findings` | `createFindingHandler` | `finding_append` (harness) |
| POST | `/v1/findings/:findingID/decision` | `decideFindingHandler` | — (operator, reconciler) |
| PUT | `/v1/findings/:findingID/harness` | `setFindingHarnessHandler` | — (backfill) |
| GET / POST | `/v1/patterns` | `listPatternsHandler` / `createPatternHandler` | `pattern_list` (both) / `pattern_append` (harness) |
| GET / POST | `/v1/service-records` | list / create | `service_record_list` / `service_record_append` (harness) |
| GET | `/v1/directives` | `listDirectivesHandler` | — |
| PUT | `/v1/directives/:directiveID` | `upsertDirectiveHandler` | — (reconciler) |
| GET / PUT | `/v1/finding-categories[/:slug]` | list / upsert | `triage_categories` (triage) / — (seed) |
| GET / POST | `/v1/finding-groups` | list / create | — / `triage_group_create` (triage) |
| POST | `/v1/finding-recommendations` | `recommendFindingsHandler` | `triage_recommend` (triage) |
| GET / POST | `/v1/directive-proposals` | list / create | — / `directive_propose` (triage) |
| POST | `/v1/directive-proposals/:proposalID/decision` | `decideDirectiveProposalHandler` | — (operator) |
| GET / PUT | `/v1/harnesses[/:name]` | list / upsert | — (reconciler, backfill) |
| GET | `/v1/triage/pending` | `pendingTriageHandler` | — (review page) |
| GET | `/v1/triage/decisions` | `recentDecisionsHandler` | `triage_recent_decisions` (triage) |
| GET | `/v1/triage/next-category` | `nextCategoryHandler` | — (runner) |
| PUT | `/v1/triage/classifications` | `classifyFindingsHandler` | `triage_classify` (triage) |
| GET / POST | `/v1/triage/runs` | list / start | — / `triage_run_start` (triage) |
| PATCH | `/v1/triage/runs/:runID` | `finishTriageRunHandler` | `triage_run_finish` (triage) |
| GET | `/v1/memory/search` | `searchMemoryHandler` | `memory_search` (both) |
| POST | `/v1/documents/sync` | `syncDocumentsHandler` | `documents_sync` (harness) |
| POST | `/v1/documents/push` | `pushDocumentsHandler` | — (reconciler, `push-memory.py`) |
| GET | `/v1/documents/hashes` | `documentHashesHandler` | — (reconciler, `push-memory.py`, `clean-scratch.py`) |
| POST | `/v1/documents/delete` | `deleteDocumentsHandler` | — (`push-memory.py --prune`, `clean-scratch.py`) |

Routes with no tool are the operator/reconciler surface. Keeping them off both `mcpd` profiles is how agents are kept away from them, so a new route defaults to "no tool" until there is a reason. The API key (below) decides who may call the service at all; it does not separate agents from the operator, since every key has full access.

## Authentication

`internal/auth` holds the keys; `middleware.APIKey` in `internal/middleware/auth.go` guards the `/v1` group. The operator's side (creating keys, transport, the start-up rules) is in [Server Install](Server-Install).

- **Header.** `Authorization: Bearer <key>`, or `X-API-Key: <key>`. The `Bearer` scheme is matched case-insensitively.
- **Open routes.** `/healthz`, `/triage`, `/missions`, `/` (the redirect), `/favicon.svg` and `/favicon.ico` never need a key: they hold no data. Everything under `/v1` does, whenever a key is required.
- **Refusal.** 401 with `WWW-Authenticate: Bearer realm="bishop-memory"` and the body `{"error":"unauthorized","details":"send a valid API key as Authorization: Bearer <key>"}`. The same body for a missing key and a wrong one.
- **The store.** The keys file (`BISHOP_API_KEYS_FILE`, default `api-keys` beside `DB_PATH`) holds `<name> <hex sha256>` lines; blank lines and `#` comments are ignored, and a malformed line stops `memoryd` at start. `BISHOP_API_KEY` adds one key named `env`. `Store.Verify` hashes the presented key and compares it against every configured hash with `subtle.ConstantTimeCompare`. The file is re-read when its modification time or size changes, so key changes need no restart.
- **The required rule.** `NewRouterWithKeys` passes `required = HTTP_HOST is set and not loopback and BISHOP_ALLOW_NO_AUTH is not 1`. With keys configured, a valid key is always needed. With none: open when not required (the loopback default), and every `/v1` request refused when required, so revoking the last key never opens a network-facing service. `NewRouter` (tests) builds with no store, which is open. `cmd/memoryd/main.go` adds the start-up guard: a non-loopback `HTTP_HOST` with no key refuses to start unless `BISHOP_ALLOW_NO_AUTH=1`.
- **The log.** An authenticated request's access-log line ends `key=<name>`; the key itself is never logged.
- **Clients.** `mcpd` wraps its HTTP transport to send `BISHOP_MEMORY_API_KEY`; the Python scripts add it in `reconcile-memory.py`'s `api_headers()`; `triage-run.sh` passes it to `curl` through a mode-600 header file; the two pages keep it in local storage (`bishop.apiKey`) after prompting on a 401. All but the pages also read `~/.config/bishop-memory/client.env` (or `BISHOP_MEMORY_CLIENT_ENV`) for any variable the environment does not set.

## Mission HUD routes

`internal/api/hud.go`. Read-only, under `/v1/hud` so nothing the harnesses, `mcpd` or the triage agents call changed shape. Rows come back as maps keyed by column name (`queryMaps`), so the page can show a new column without a model change.

**`GET /v1/hud/missions`** — the board. Optional filters, AND-ed: `harness`, `status`, `outcome` (exact match) and `q`, which matches the mission id and title with `LIKE` and, when it sanitises to an FTS5 query, also matches missions whose brief, progress or debrief contain it. Newest first by `opened_at`. Response:

```json
{
  "missions":  [{"id", "title", "status", "outcome", "harness", "owner", "priority",
                 "opened_at", "closed_at", "updated_at",
                 "steps", "steps_done", "findings", "patterns", "has_brief", "has_debrief"}],
  "harnesses": [{"name", "missions"}]
}
```

`harnesses` counts missions per non-NULL `missions.harness` and ignores the filters.

**`GET /v1/hud/missions/:missionID`** — one mission; 404 `mission not found`. Keys:

| Key | Rows |
|---|---|
| `mission` | The `missions` row, every column |
| `steps` | `mission_steps` in step order (`CAST(step AS INTEGER), step`), with `summary`, `started_at`, `ended_at` |
| `events` | The mission's `flight_recorder` rows by id |
| `documents` | `kind`, `title`, `body`, `source_path`, `updated_at` of its `brief`, `progress` and `debrief` documents (by `documents.mission_id`) |
| `patterns` | Patterns whose `discovered_mission` is this mission |
| `directives` | Accepted `directive_proposals` whose `evidence` includes one of the mission's findings, with the directive's `ratified_at` |
| `crew` | `name`, `role`, `description` of each agent named on a step, matched through `importer.CrewName` |
| `service_records` | Those agents' service records dated from `opened_at` to `closed_at` (today while open) |

The mission's findings are not in this response. The page takes them from **`GET /v1/findings?mission_id=<id>`**, a filter on the existing route, so they arrive with the triage classification and recommendation in the shape `/triage` uses.

## Side effects added for the HUD

These routes kept their request and response shapes; what changed is what they write.

| Route | Side effect |
|---|---|
| `POST /v1/documents/sync` | Body `root`: syncs that root, tagged with the harness registered on it if any; responds `{"synced":true,"root":…}`. Body `harness`: syncs that harness's registered `memory_root` (404 `harness not found` if none); responds with `root`. Neither: syncs every registered harness root (once per root; the most recently seen name wins when two share one) and responds `{"synced":true,"roots":[…]}`; with no harness registered it falls back to `MEMORY_ROOT` (then `testdata/memory`) and answers with `root`. For every root that has a harness, it also imports the `agents/` directory beside the root into `crew`; a crew failure is logged, never returned. The walker now skips hidden directories |
| `PATCH /v1/missions/:missionID` | After the update commits, re-imports `missions/<id>/BRIEF.md`, `PROGRESS.md` and `DEBRIEF.md` from the memory root registered for the mission's harness (`importer.SyncMission`). No harness, no root, or a root that is not a directory on this machine (a remote server, where the client pushes instead): skipped. A failure is logged and never fails the update |
| `POST /v1/missions/:missionID/steps` | `stampStepTiming`: sets `started_at` the first time the step is stored `in-progress`, and `ended_at` each time it moves into `done` or `failed` from another status, unless the request carried that field. Only for a mission that is not `complete`, since a completed mission's steps arrive from a replayed `PROGRESS.md` |
| `POST /v1/flight-recorder` | Event `step-sync` with a `mission_id` and `step` copies `note` into that step's `summary`, in the same transaction. A step the mission does not have is left alone |
| `POST /v1/findings` | No `mission_id` but a `harness`: `openMissionFor` takes that harness's open mission (`in-progress` or `blocked`, most recently updated), unless `finding_date` is before the day it opened, so a replayed backlog is not pinned to whatever is open now |

## The error contract

`internal/api/errors.go` has two funnels and every handler uses them:

- `validationError(c, err)` → 400. A `validator.ValidationErrors` becomes `{"error":"invalid request","details":[{"field","tag"}]}`. A JSON syntax or type error becomes the static string `request body failed validation` (so a submitted value is never echoed). Any other error passes its text through, which is safe only when that text is fully static; never wrap a driver error into it.
- `internalError(c, err)` → 500 with the static body `{"error":"internal server error"}`; the real error is logged with the request id.

404s are hand-written per handler (`mission not found`, `finding not found`, …). 409 is used by `mission_create` on a duplicate id; 503 by `mission_allocate` after ten lost races. An unknown route returns 404 `{"error":"route not found"}`.

Every request passes three middlewares in `internal/middleware`: `RequestID` (echoes a caller's `X-Request-ID` or generates one, and puts it in the Gin context), `AccessLog` (one structured line with latency, status, method, path, client IP and, when a key was used, `key=<name>`) and `Recovery` (a panic becomes the same static 500 body). `/v1` routes also pass `APIKey` (see [Authentication](#authentication)), which answers 401 before the handler runs. Resource routes are under `/v1`; `/healthz` and the pages are unversioned.

## Request and response details

The routes below have no tool, or need more than the tool's page says. Bodies are JSON; every filter is optional unless stated.

### Health

`GET /healthz` → 200 `{"ok":true,"service":"bishop-memory","storage":"sqlite","schema":"ok"}`. 503 `{"ok":false,"storage":"sqlite","error":"schema incomplete","missing":["…"]}` when a name in `expectedSchemaObjects` is absent, and 503 `"error":"database unavailable"` when the ping fails.

### Findings

`GET /v1/findings?status=&category=&harness=&mission_id=&unclassified=1&pending=1&no_pending=1&directive_candidate=1&ids=&order=&limit=&offset=` — newest first, filters AND-ed. `status` must be one of the six ledger values (400 otherwise); `category` is a slug or `uncategorised`; `pending` / `no_pending` select findings with or without a pending recommendation; `ids` is comma-separated; `order=asc` reverses. With no `limit` every row comes back, which the reconciler depends on. Each row carries `harness`, `decision_note` and, when present, a `triage` object (the classification) and a `recommendation` object (the pending one).

`POST /v1/findings/:findingID/decision` — the operator's status change and the only writer of `status`, `approver`, `date_approved` and `decision_note`. Body `{"status","approver","note","date_approved"}`: `status` and `approver` are required; `note` is required for `rejected`, `retired` and `superseded`; `date_approved` is carried verbatim when supplied (the reconciler passes the file's value), otherwise today's UTC date. Allowed moves are `findingTransitions` in `findings.go`: `proposed` to anything; `approved` to `applied`, `rejected`, `retired`, `superseded`; `applied` to `retired`, `superseded`; and every status back to `proposed` (reopen, which clears the decision fields). A same-status decision is a 200 no-op with `"changed":false`, so the reconciler can replay a file idempotently. In the same transaction the pending recommendation is closed `accepted` when it agreed and `declined` otherwise, and a `finding.decided` row is appended to the journal. Response `{"id","status","previous","changed"}`; 400 for an illegal move or a missing note, 404 for an unknown id.

`PUT /v1/findings/:findingID/harness` — body `{"harness":"kirsch"}`. For rows mirrored before `findings.harness` existed.

### Journal and directives

`GET /v1/flight-recorder?mission_id=&limit=` — default 50, hard cap 100, and no offset. That cap is why the reconciler's `--include-journal` enumerates per mission and skips a mission at exactly 100 rows.

`GET /v1/directives` — the rulebook in rule order (`ORDER BY id`), not as a feed. No route creates one except the reconciler's `PUT /v1/directives/:directiveID` and the proposal decision below.

### Search query handling

`q` is rewritten into FTS5 syntax, not passed through raw. Any whitespace-delimited word containing something other than a Unicode letter, digit, underscore or combining mark (a hyphenated mission id, `step-sync`, `FLIGHT-RECORDER`) is matched as a literal phrase, exactly as if it were quoted; a complete `"quoted phrase"` is left as it is. A `"` inside a word, with no whitespace either side, is escaped into that word's phrase (`abc"def"ghi` is one literal phrase). Only a `"` after whitespace or at the start opens a phrase, and one never closed is a 400 before the database is reached. FTS5's `AND` / `OR` / `NOT`, `( )` grouping and `column:` filters still work in unpunctuated words. The tokenizer is `porter unicode61`, so `improve`, `improved` and `improving` match each other. Results are ranked by BM25 and capped at 20; `limit` is advisory. A 400 says `invalid search query syntax` and never echoes `q`.

### Document sync

`POST /v1/documents/sync` — besides the shapes in [Side effects added for the HUD](#side-effects-added-for-the-hud): 400 `root must not be the filesystem root`, and 502 `{"error":"import failed"}` when the walk fails (missing root, read errors; the detail is in the server log). The filesystem-root refusal is the only guard on `root`: there is no allow-list of memory roots. A path sync reads the server's own disk, so it only finds files when the server runs on the harness machine; a remote server gets documents by push instead.

### Document push

`internal/api/documents_push.go`. A memory tree lives on the harness machine; when `memoryd` runs elsewhere the client sends the files. `scripts/push-memory.py` and the reconciler drive these routes, and a pushed file is imported exactly as a local sync would import it (`importer.ImportMarkdown`: same upsert, hashing, kind, mission id and FTS handling).

**`POST /v1/documents/push`**

```json
{
  "harness": "kirsch",
  "files":  [{"path": "/abs/path/on/client/.claude/memory/missions/m-1/BRIEF.md",
              "rel_path": "missions/m-1/BRIEF.md",
              "content": "# Brief\n…"}],
  "agents": [{"path": "/abs/path/on/client/.claude/agents/hicks.md", "content": "…"}]
}
```

`path` is the file's absolute path on the client and becomes `source_path`, so rows match those a local sync of the same file made. `rel_path` is relative to the memory root and decides the kind and mission id, as the walker does. `agents` become `crew` rows (`importer.ImportAgent`). Response `{"harness","files":N,"agents":M}`.

Limits and refusals: 413 `request too large` over 8 MiB per request; 400 over 500 files and agents together, for a file over 1 MiB, for a `path` not ending `.md` (Markdown only; JSONL is not pushed), and for a `rel_path` that is empty, absolute, contains a backslash, is not already clean (`path.Clean`), or has an empty, `..` or hidden (leading `.`) segment. `harness` is required (up to 128 characters), `path` up to 1024, `rel_path` up to 512. A bad agent definition is a 400.

**`GET /v1/documents/hashes?harness=<name>`** — `{"harness","hashes":{"<source_path>":"<sha256>",…}}` for every document of that harness, so a client sends only what changed. JSONL documents appear as `<file>:<line>`. 400 without `harness`.

**`POST /v1/documents/delete`** — body `{"paths":[…]}` (required, at most 5,000). Deletes each document whose `source_path` equals a path, or starts with `<path>:` (a JSONL file's lines), with its `documents_fts` row, in one transaction. Response `{"deleted":N}`.

The mission update's re-import (`syncMissionDocuments`) stays, but returns silently when the harness's registered memory root is not a directory on the server; the client's next push carries those files instead.

### Findings triage

| Route | Body or query | Response and rules |
|---|---|---|
| `GET /v1/finding-categories?include_inactive=1` | | `{"categories":[…],"uncategorised_count":N}`; each category has `slug`, `name`, `description`, `examples`, `sort_order`, `active`, `last_processed_at`, `proposed_count`, `pending_count` |
| `PUT /v1/finding-categories/:slug` | `{"name","description","examples","sort_order","active"}` | Slug must be lower-case kebab-case and not `uncategorised` |
| `POST /v1/triage/runs` | `{"kind":"classify"\|"process","category","model"}` | 201 `{"id"}` |
| `PATCH /v1/triage/runs/:runID` | `{"status":"done"\|"failed","considered","written","notes"}` | A finished `process` run stamps its category's `last_processed_at` |
| `GET /v1/triage/runs?limit=50` | | Each run with `accepted` and `declined` counts from the recommendations it wrote |
| `GET /v1/triage/next-category` | | `{"category","waiting"}`, or 404 when nothing waits |
| `PUT /v1/triage/classifications` | `{"classified_by","run_id","items":[{"finding_id","category","secondary_category","directive_candidate","confidence","summary"}]}` | 1–200 items, upsert (re-classifying replaces). The whole batch is refused, naming the item, if a slug is not active or a finding id is unknown. `{"classified":N}` |
| `GET` / `POST /v1/finding-groups` | `?category=` / `{"category","title","summary","target","run_id"}` | 201 `{"id"}` |
| `POST /v1/finding-recommendations` | `{"run_id","items":[{"finding_id","group_id","recommendation","superseded_by","rationale","proposed_change"}]}` | `supersede` needs `superseded_by`. An existing pending recommendation is expired and replaced; a finding no longer `proposed` is skipped. `{"written":N,"skipped":[ids]}` |
| `GET /v1/triage/decisions?category=&limit=20` | | Decided findings in the category, each with the last recommendation it received |
| `GET /v1/triage/pending` | | `{"categories":[{"slug","groups":[…],"ungrouped":[…]}],"directive_proposals":[…],"pending_findings":N,"decided_findings":N}` |
| `GET /v1/directive-proposals?state=` | | `pending`, `accepted`, `declined` or `expired` |
| `POST /v1/directive-proposals` | `{"title" (≤60),"applies_when","rule","rationale","reviewer_check","example","evidence":[ids],"group_id","harness","run_id"}` | Rendered with an 80-character Evidence placeholder and refused over 1,510 characters. 201 `{"id","rendered_length"}` |
| `POST /v1/directive-proposals/:proposalID/decision` | `{"state":"accepted"\|"declined","decided_by","note", …edited fields}` | Declining needs a note. Accepting allocates the next `DIR-NNN` across `directives` and every accepted proposal, stores the possibly edited text, upserts `directives`, marks every evidence finding `applied` and appends `directive.ratified`. `{"id","state","directive_id","rendered"}` |
| `GET /v1/harnesses`, `PUT /v1/harnesses/:name` | `{"memory_root":"/abs/path/.claude/memory"}` | `memory_root` must be absolute |

The pages and their icons (`/triage`, `/missions`, `/favicon.svg`, `/favicon.ico`) are embedded in the binary from `internal/ui/`; the icons are cached for a day, and `.ico` is a 32-pixel PNG so a browser's automatic request is not logged as a 404.

## Handler pattern

Read `decideFindingHandler` in `internal/api/findings.go` as the template:

1. Parse path parameters; `validationError` on a malformed id.
2. `c.ShouldBindJSON(&request)`; the struct's binding tags do the shape validation. Trim free-form strings. Post-trim empties that the column forbids are checked explicitly so they become a 400, not a driver 500.
3. `db.BeginTx` with `defer tx.Rollback()`. Read what you need inside the transaction.
4. Validate business rules (allowed transitions, active slugs) and return 400 with a static message.
5. Write, then append the audit row to `flight_recorder` in the same transaction. Audit notes describe the resulting state, not the request delta.
6. `tx.Commit()`, then respond with a small JSON object: ids and booleans, not the whole row, unless the caller needs it.

Shared helpers: `isOneOf`, `nullIfEmpty`, `nullStringPtr`, `nullInt64Ptr`, `parseLimitOffset`, `queryFindings` with the `findingSelect` prefix and one of two recommendation joins.

## Adding a route

1. Add the request/response structs to `internal/model/` with binding tags that match the CHECK constraints.
2. Write the handler in the matching `internal/api/*.go` file following the pattern above.
3. Register it in `router.go`. Decide and write down in a comment whether it is an agent route or an operator route.
4. If it is an agent route, add the tool in `cmd/mcpd` (see [Developer: MCP Adapter](Developer-MCP-Adapter)) and the row in `TestMCPDRoutePathsMatchServerRoutes`.
5. Add a test in `internal/api/` that boots the real schema (`newTriageTestRouter` shows how) and exercises the failure paths, not only the happy one.
6. Add it to the route catalogue above. If no tool proxies it, give its body and response under [Request and response details](#request-and-response-details); if a tool does, the [MCP Tool Reference](MCP-Tool-Reference) documents it once `scripts/gen-mcp-reference.py` is re-run.

## Health

`/healthz` pings the database and checks that every name in `expectedSchemaObjects` exists. Add any new table there, or a fresh install will report healthy with the table missing while its routes return 500.

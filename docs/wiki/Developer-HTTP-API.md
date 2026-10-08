# Developer: HTTP API

The route catalogue, the error contract, and the pattern for adding a route. Request and response bodies for every route are in `docs/api-contract.md` in the repository; this page is the developer's view.

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

Routes with no tool are the operator/reconciler surface. Keeping them off both `mcpd` profiles is the whole access-control model, so a new route defaults to "no tool" until there is a reason.

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
| `PATCH /v1/missions/:missionID` | After the update commits, re-imports `missions/<id>/BRIEF.md`, `PROGRESS.md` and `DEBRIEF.md` from the memory root registered for the mission's harness (`importer.SyncMission`). No harness or no root: skipped. A failure is logged and never fails the update |
| `POST /v1/missions/:missionID/steps` | `stampStepTiming`: sets `started_at` the first time the step is stored `in-progress`, and `ended_at` each time it moves into `done` or `failed` from another status, unless the request carried that field. Only for a mission that is not `complete`, since a completed mission's steps arrive from a replayed `PROGRESS.md` |
| `POST /v1/flight-recorder` | Event `step-sync` with a `mission_id` and `step` copies `note` into that step's `summary`, in the same transaction. A step the mission does not have is left alone |
| `POST /v1/findings` | No `mission_id` but a `harness`: `openMissionFor` takes that harness's open mission (`in-progress` or `blocked`, most recently updated), unless `finding_date` is before the day it opened, so a replayed backlog is not pinned to whatever is open now |

## The error contract

`internal/api/errors.go` has two funnels and every handler uses them:

- `validationError(c, err)` → 400. A `validator.ValidationErrors` becomes `{"error":"invalid request","details":[{"field","tag"}]}`. A JSON syntax or type error becomes the static string `request body failed validation` (so a submitted value is never echoed). Any other error passes its text through, which is safe only when that text is fully static; never wrap a driver error into it.
- `internalError(c, err)` → 500 with the static body `{"error":"internal server error"}`; the real error is logged with the request id.

404s are hand-written per handler (`mission not found`, `finding not found`, …). 409 is used by `mission_create` on a duplicate id; 503 by `mission_allocate` after ten lost races.

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
6. Document it in `docs/api-contract.md`.

## Health

`/healthz` pings the database and checks that every name in `expectedSchemaObjects` exists. Add any new table there, or a fresh install will report healthy with the table missing while its routes return 500.

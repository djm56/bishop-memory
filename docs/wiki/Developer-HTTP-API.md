# Developer: HTTP API

The route catalogue, the error contract, and the pattern for adding a route. Request and response bodies for every route are in `docs/api-contract.md` in the repository; this page is the developer's view.

## Route catalogue

Registered in `internal/api/router.go`. "Tool" is the `mcpd` tool that proxies the route, if any.

| Method | Path | Handler | Tool (profile) |
|---|---|---|---|
| GET | `/healthz` | `healthHandler` | — |
| GET | `/triage` | `ui.TriagePageHandler` | — (review page) |
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

# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
This project has not yet made a tagged release, so every change to date
lives under `[Unreleased]` — no version numbers or dates are invented
here ahead of an actual release.

## [Unreleased]

### Added

- **Stale missions on the HUD** — `GET /v1/hud/missions` returns `last_activity` (the later of the mission's `updated_at` and its newest journal event) and `stale` (not complete, idle 48 hours), and takes a `stale` filter. The mission list shows a `stale` pill, the header counts stale missions, and the status menu has a **stale** option.
- **Missions record their harness on create** — `POST /v1/missions` takes `harness`; `PATCH` fills it only while it is empty, never moving a mission between harnesses; `GET /v1/missions` returns it. `mission_create` sends the mcpd's `BISHOP_HARNESS`, and the reconciler sends `--harness` on create and claims harness-less missions it owns. `scripts/backfill-mission-links.py` gains a `mission-harness` part (from the mission's own documents) and `--only` (`make mission-links ONLY=…`).
- **Plan:** `docs/plans/MEMORY-INSIGHT-PLAN.md`.
- **Directive proposal reopen and amendment** — operators can now reopen accepted, declined or expired directive proposals, which moves them back to pending. A decided proposal that carries a `directive_id` can be re-decided: accepting it amends the existing `DIR-NNN` (preserves the original `ratified_at`), declining it retires the directive. The triage page's Directives tab shows a Reopen button on decided rows and cards on reopened proposals showing "reopened — amends DIR-xxx" with buttons to Ratify as amendment or Retire. Route `POST /v1/directive-proposals/:id/reopen` with `{decided_by, note}` required; 400 for a pending proposal, 404 for unknown id; prior state recorded in `directive.reopened` flight row. The `/v1/directives` route now returns a derived `status` field: `active`, `retired` or `under-review`.
- **Stale triage run detection and operator-close** — runs are now tracked for activity. `GET /v1/triage/runs` adds `last_activity_at` (newest of started_at and child-table timestamps) and `stale` (running with no activity ≥30 minutes). The triage page's Runs tab shows a Last activity column and a `stale` pill. Operators can close a stale run with `POST /v1/triage/runs/:id/operator-close` (body: `{status: done|failed, decided_by, note}`); returns 409 if already finished or not stale, 404 if unknown; appends an operator note and writes `triage_run.operator_closed` flight row. `PATCH /v1/triage/runs/:id` now returns 409 if the run is no longer running, preventing overwrites after an operator close.
- **`export-decisions.py` now amends and retires in place** — beyond appending new ratifications, the exporter now amends existing `DIR-NNN` entries and retires directives in-place in `DIRECTIVES.md`. It only touches entries whose Added line has `**Source:** bishop-memory triage` (service-owned); hand-authored entries are byte-preserved. Exit code 0 ok, 1 failure, 2 missing harness or file, 3 conflicts only (1 and 2 outrank 3). An entry exported for the first time after amendment records the amendment date as Added. Calling `make triage-export` before amending preserves the original date. `scripts/reconcile-memory.py` no longer PUTs directives that have a proposal, so a UI amendment is never reverted by stale Markdown.

- **`scripts/rename-harness.py`** renames a harness across every table that records it (registry, missions, findings, directive proposals, documents, and `harness:agent` actors in the journal, steps and service records). Dry run by default; `--apply` backs up first; safe to repeat after a session that still used the old name restarts.
- **Mission grading** (wiki Mission-Grading): a third nightly triage agent, `mission-grader` (21:40; `opencode-go/glm-5.2` or Sonnet), grades every finished mission A to F with a short summary and suggestions for the next mission. It works from the database alone: `grade_claim` returns a compact packet per mission — the brief's criteria, the debrief's judgement sections, one line per step, finding and agent note, and signals the service counts itself — and `grade_write` records the verdicts. One pass of at most ten missions per run; a verdict is final, an unanswered claim is retried once and then skipped, and only `make triage-grade MISSIONS=… REGRADE=1` (or `DELETE /v1/mission-grades/:id`) reopens a mission.
  - New table `mission_grades`; `triage_runs.kind` admits `grade`, widened on an existing database by a one-time table rebuild at boot that never stops the service from starting.
  - Routes `/v1/mission-grades` (list, write), `/waiting`, `/claim`, `/performance` and `DELETE /:missionID`; `make triage-grade`; `install-triage-schedule.sh --grade-at`.
  - The mission HUD shows each mission's grade, a Grade section and a grade filter; the new `/performance` page rolls grades up per harness with averages, an A–F distribution, the latest grades and a trend.

- **Network deployment** (docs/plans/NETWORK-DEPLOYMENT-PLAN.md, wiki Server-Install):
  - **API keys.** Keys are named and stored hashed (`memoryd keys add|list|revoke`) and are required on every `/v1` route once one exists. Off loopback they are always required: memoryd refuses to start on a non-loopback address without a key unless `BISHOP_ALLOW_NO_AUTH=1`. The access log names the key behind each request.
  - **Built-in HTTPS** via `TLS_CERT_FILE` and `TLS_KEY_FILE`.
  - **Standalone binary.** The schema is embedded in memoryd, so the binary runs from any directory.
  - **Document push by content:** `POST /v1/documents/push`, `GET /v1/documents/hashes` and `POST /v1/documents/delete`, plus `scripts/push-memory.py`. The reconciler pushes changed files instead of asking for a path sync, and falls back to the old path sync against an older memoryd.
  - **Clients send the key:** mcpd, the reconciler, the triage scripts, `triage-run.sh` and both pages. They read `~/.config/bishop-memory/client.env`; the pages ask for the key on a 401.
  - **Install and operations:** `scripts/install.sh server|client` (systemd on Linux, launchd on macOS, with a settings file and the first key), `make dist` release archives for linux and darwin on amd64 and arm64, and `memoryd backup <file>`.
- **docs/ folder** now holds only plans and the roadmap (`docs/README.md`, `docs/ROADMAP.md`, `docs/plans/`). The user and developer documentation lives in the wiki. Includes a PostgreSQL backend plan (docs/plans/POSTGRES-PLAN.md).

### Changed

- **The pages have the MU/TH/UR design**: a MU/TH/UR wordmark in the header, a blue in-progress colour, solid status pills, mission rows edged by status, and a tighter mission header and tiles. `/performance` is reorganised into a summary bar, one row per harness (click to filter), a paged grade list with grade buttons, and **Display…** to hide harnesses. Headers fit at every width: on `/triage` the summary counts give way below 1800px and the header wraps to two rows below 1600px, and on phones long text wraps instead of widening the page.
- **Search ranks scratch lower** — `GET /v1/memory/search` (and `memory_search`) counts `workspace` documents at half their bm25 score, so findings, patterns, directives, briefs and debriefs come first for the same match; `rank` is the weighted value.
- **`mission.updated` journal notes say what changed** — each changed field as `field: "old" → "new"`, or `Mission updated (no change)`, instead of the bare "Mission updated".
- **The triage page and the mission HUD have a new look**: a green terminal style in IBM Plex Mono and VT323, with the same light, dark and auto themes, and the wiki screenshots regenerated to match. The pages' behaviour is unchanged, except that the HUD now scrolls its mission list, not the whole page, to show the selected mission.
- `scripts/clean-scratch.py` works through the API and runs on the machine that holds the harness checkouts. Its `--url` option replaces `--db`.
- **HUD page redesign with shared shell** — crew, missions, performance, and triage pages rebuilt from operator designs with a unified header, in-page sub-tabs, and pagination pager shared across all four pages. Crew page is a sortable, filterable, paginated leaderboard with expandable rows. Missions detail is organized into tabs. Triage Browse and Runs tabs are paginated; Runs table condensed with expandable model ID, notes, and timestamps. All pages support keyboard-accessible rows and cards with focus retained on re-render. Performance settings dialog closes on Escape with focus trap and restore. Light-theme faint text raised to 4.74:1 contrast. Animations respect prefers-reduced-motion. Fixed unescaped finding-status text on missions summary. Tests: TestEmbeddedScriptsParse runs node --check on inline scripts; shared-shell identity and source-text tests documented. No API changes.

### Breaking Changes

- **Complete vocabulary refactor: harness vocabulary replaces legacy task/event model.** The 10-table schema, 18 HTTP routes, and 15 MCP tools adopted unified harness terminology. **Every database would need rebuilding — no migration script exists because there is no legacy data; the service is new and this was chosen as a clean break.** All changes are breaking:
  - `tasks` table → `missions`; task ID format → mission ID format (mission-YYYYMMDD-NN)
  - `task_runs` table → `mission_steps`
  - `events` table → `flight_recorder`
  - `agents` table → `crew`
  - `improvements` table → split into `findings` + `patterns`
  - New tables: `service_records` (per-agent calibration), `directives` (read-only binding rules)
  - Old routes `/v1/tasks`, `/v1/events` → `/v1/missions`, `/v1/flight-recorder`; old tools `task_*`, `event_*` → `mission_*`, `flight_recorder_*`
  - Updated MCP tool count: 8 → 15 tools (added finding/pattern/service-record read/write, clarified mission/step terminology)
  - `docs/MEMORY-SETUP.md` completely rewritten: 7 tables → 10 tables, document-kind mapping updated for new structure, day-to-day workflows rewritten for mission/flight-recorder terminology, Phase 3 section clarified (renderer deferred, service remains read-only)
  - `README.md` MCP tool table rewritten with correct tool names and argument schemas
  - `CONTRIBUTING.md` updated: stub file references (`internal/store/tasks.go` → `internal/store/missions.go`, etc.)
  - `.env.example` security warning updated: endpoint names changed, substance preserved (no authentication, open read/write, filesystem sync accepts caller root)

### Security

- **HTTP listener now defaults to loopback-only (`127.0.0.1`).**
  `internal/config/config.go` previously composed the listen address as
  `":" + PORT`, which binds every interface on the host. bishop-memory
  has no authentication of any kind — every mission/flight-recorder/findings
  endpoint is open read/write to any caller that can reach it, and
  `POST /v1/documents/sync` accepts a caller-supplied filesystem root —
  so binding all interfaces contradicted the "locally-bound" security
  model every doc in this repository already describes. A new
  `HTTP_HOST` variable (default `127.0.0.1`) lets an operator still
  choose a different bind explicitly; `cmd/memoryd/main.go` logs a
  loud startup warning naming exactly what becomes reachable whenever
  the configured bind is not loopback. See `.env.example` and
  `internal/config/config.go`'s `Config.HTTPHost` docblock.

### Fixed

- **HUD journal times** — events show when they happened (`occurred_at`, the harness's `YYYY-MM-DD HH:MM UTC` now parsed) rather than when the service stored them, with the stored time on hover.
- **HUD agent roles** — a step by the operator shows `human operator`, and a name no agent definition has (such as `senior (general-purpose)`) shows a **not in crew** tag instead of a blank role.
- **The reconciler skipped findings and never linked them to missions.**
  `scripts/reconcile-memory.py` read only `**Suggestion**:`, so entries
  written `**Suggestion:**` were dropped without a word; it now reads
  both. A `**Mission**: mission-…` line in a finding is sent as its
  `mission_id`, so the mission HUD shows it. A finding an agent sent
  through `finding_append` without a date is matched on target and
  suggestion, instead of being created a second time.

- **`validationError`'s CONV-033 fix had over-corrected and collapsed
  every hand-authored error message into one generic string.** An
  earlier fix in this same task closed a real defect — a JSON
  type-mismatch error's `Error()` text could echo a raw
  request-supplied scalar back to the caller — but its fallback branch
  caught every other error too, discarding distinct, already-safe
  messages (`"q is required"`, `"status must be one of: ..."`,
  `"root must not be the filesystem root"`, the `event_type`/`summary`
  empty-value messages, and the wrapped FTS5 syntax-error text) across
  every handler in `internal/api`. `validationError` now distinguishes
  three cases explicitly: `validator.ValidationErrors` (structured
  field/tag list, unchanged), a genuine `*json.UnmarshalTypeError` /
  `*json.SyntaxError` (the one case CONV-033 actually applies to — kept
  as a static message), and everything else (passed through verbatim,
  since none of those messages are request-derived). `docs/api-contract.md`'s
  search-endpoint error example was also corrected — it had conflated
  the "`q` missing" and "malformed FTS5 syntax" cases under one shared
  example, when they return two distinct messages.

- **Linux installer overly-broad permission grant.** `scripts/install-daemon-linux.sh`
  previously ran `chmod -R o+rX "$BISHOP_ROOT"` on every invocation,
  recursively making the entire checkout world-readable — including any
  `.env` file the operator may have placed there (documented in
  `.gitignore` as holding secrets like PORT, DB_PATH, and future API
  keys). The revised version grants **only the dedicated service account**
  (`bishop-memory` group) **exactly the permissions it needs**: read/execute on
  `bin/memoryd`, read on `db/schema.sql`, and traversal on their parent
  directories. The recursive, "other"-class, and re-widening-on-every-run
  approaches are all removed.

- **Linux installer fails silently if the service crashes on startup.**
  `scripts/install-daemon-linux.sh` previously ran `systemctl restart`
  and returned success if systemd accepted the request, but for `Type=simple`
  units, systemd does not wait for the process to prove itself before
  returning — a unit that starts and immediately exits (hardening
  interaction, permission problem, bind failure) would silently report
  success. The revised version includes a post-start health check that
  polls `systemctl is-active` with a retry loop (up to 10 attempts), and
  on failure logs the unit's recent `journalctl` output before failing
  the entire install.

### Added

- **Findings triage on OpenCode.** `scripts/triage-run.sh` can run either
  agent with `opencode run` instead of `claude -p`: `TRIAGE_ENGINE=opencode`
  for both jobs, `TRIAGE_CLASSIFY_ENGINE` / `TRIAGE_PROCESS_ENGINE` for one,
  `--engine` (or `make triage-* ENGINE=`) for a single run. The provider and
  key come from the user's own OpenCode config, so nothing goes in `.env`.
  The defaults are `opencode-go/glm-5.3-flash` (classify) and
  `opencode-go/glm-5.2` (process). `.opencode/agents/` holds twins of the two
  agents that deny every tool except reads and their own `bishop-triage`
  tools. Each run gets the MCP server, the step cap and read access to the
  harness checkouts through `OPENCODE_CONFIG_CONTENT`, so no file is written,
  plus `--pure` and with sharing, snapshots, LSP and auto-update off. OpenCode
  falls back to its default (editing) agent on an unknown `--agent`, so the
  runner checks the resolved agent with `opencode debug agent` and refuses
  to start unless it is the twin. The runner also refuses a model id that
  does not fit the engine. It logs `ERROR run closed as failed`, and exits
  1, when the agent gives up and closes its run as failed, even though the
  CLI itself exits cleanly.
- **A run timeout for both engines.** `TRIAGE_TIMEOUT_MIN` (default 45) stops
  an agent that is still running at the limit, so one hung run cannot hold
  the launchd job and block every later night. Agents now get stdin from
  `/dev/null`; `opencode run` otherwise waits for end of input before its
  first request.
- **Documentation screenshots, regenerated by `make screenshots`.**
  `scripts/screenshots/run.sh` starts a throwaway `memoryd` on a fresh
  database, loads made-up findings (`scripts/screenshots/demo.json`) through
  the HTTP API, and captures the review page's tabs in light, dark and phone
  width with headless Chrome into `docs/wiki/images/`. No real ledger appears
  in a published image. The README shows the Pending tab in the reader's
  GitHub theme; the Review Page Guide shows every tab. `make wiki-publish`
  now uploads the images with the pages.
- **A favicon for the review page.** A "B" in the page's accent blue, served
  at `/favicon.svg` with a 32px PNG at `/favicon.ico`, both embedded in the
  binary. It replaces the 404 every page load used to log for `/favicon.ico`.
- **Review page restyle.** `internal/ui/triage.html` is replaced with a new
  design: a warm neutral palette, a card layout, segmented tabs, a category rail
  with count badges, and keyboard hints on the buttons. A header control
  switches between Auto, Light and Dark. Auto follows the operating system, the
  choice is saved in `localStorage` as `triage.theme`, and `?theme=` in the URL
  overrides it for one load. API calls, routes and payloads are unchanged.
  Keyboard shortcuts now ignore Cmd, Ctrl and Alt; previously Cmd+A, meant as
  select-all, approved the focused finding. The directive form's length meter
  turns amber above 1,300 characters and red above 1,510, and Copy on a
  proposed change confirms with a toast.
- **Documentation split into end-user and developer sets, published to the
  GitHub wiki.** `docs/wiki/` holds fifteen pages — a User Guide, a generated
  MCP Tool Reference covering both `mcpd` profiles with worked examples, a
  Review Page Guide, Commands and Scripts, Troubleshooting, and eight
  developer pages (overview, HTTP API, database, MCP adapter, scripts and
  reconciler, triage agents, testing and CI, harness integration).
  `scripts/publish-wiki.sh` / `make wiki-publish` push them to the wiki
  repository; `docs/wiki/` is the source of truth. `README.md` was rewritten
  as the entry point to both sets. The harness profile's `finding_list` tool
  now actually registers the `category`, `harness`, `ids` and `limit`
  arguments its handler already forwarded.
- **Findings triage.** Nightly classification of the findings ledger into a
  17-category taxonomy, per-category grouping and recommendations, directive
  drafts, an operator review page at `/triage`, and a write-back of decisions
  into each harness's `FINDINGS.md` / `DIRECTIVES.md`. New tables `harnesses`,
  `finding_categories`, `triage_runs`, `finding_triage`, `finding_groups`,
  `finding_recommendations`, `directive_proposals`; additive columns
  `findings.harness` and `findings.decision_note` (applied at boot by
  `EnsureColumns`, with the `idx_findings_harness` index created there rather
  than in `db/schema.sql` so a pre-triage database still boots). New routes
  under `/v1/finding-categories`, `/v1/finding-groups`,
  `/v1/finding-recommendations`, `/v1/directive-proposals`, `/v1/harnesses`,
  `/v1/triage/*`, the operator-only `POST /v1/findings/:id/decision` (the one
  write path for `findings.status`), `PUT /v1/findings/:id/harness`, and
  `PUT /v1/directives/:id` for the reconciler's mirror of `DIRECTIVES.md`.
  `mcpd` gains `MCPD_PROFILE=triage` with 13 tools for the two scheduled agents
  (`.claude/agents/findings-classifier.md`, `.claude/agents/findings-processor.md`,
  doctrine in `.claude/skills/findings-triage/SKILL.md`); the harness profile's
  `finding_list` gains `category`/`harness`/`ids`/`limit` filters and
  `finding_append` carries `BISHOP_HARNESS` as the finding's harness. Scripts:
  `triage-run.sh`, `triage-seed-categories.py`, `triage-backfill-harness.py`,
  `export-decisions.py`, `install-triage-schedule.sh`; Makefile `triage-*`
  targets. The reconciler now registers the harness's memory root, sends
  `harness` on findings, mirrors a hand-set `Status` in `FINDINGS.md` into the
  service through the decision route, and mirrors `DIRECTIVES.md` into the
  `directives` table. Guide: `docs/FINDINGS-TRIAGE.md`; design record:
  `docs/FINDINGS-TRIAGE-PLAN.md`.

- **systemd unit + Linux installer** (`scripts/bishop-memory.service`,
  `scripts/install-daemon-linux.sh`) — the Linux/systemd counterpart to
  the existing macOS/launchd installer. Runs as a dedicated, installer-created
  non-root system account; binds loopback only; every hardening
  directive in the unit carries its own justification comment; the
  installer detects Linux, systemd (via `/run/systemd/system`), and CPU
  architecture rather than assuming any of them, and never elevates its
  own privilege — it prints the exact root-requiring commands rather
  than running `sudo` itself.
- **Cross-compiled Linux binaries.** `make dist-linux-amd64`,
  `make dist-linux-arm64`, and `make dist-linux` build static Linux
  binaries for both architectures from any host, with no C toolchain —
  `modernc.org/sqlite`, this project's only otherwise-cgo-shaped
  dependency, is pure Go, so `CGO_ENABLED=0` cross-compilation is
  clean.
- **Repository hygiene for the first commit**: `LICENSE` (MIT),
  `CONTRIBUTING.md` (build/test/verification-gate instructions and the
  project's docblock convention), and `.github/workflows/ci.yml`
  (`go build`, `go vet`, `gofmt -l`, `go test`, `go test -race` on every
  push/PR, matching the Go version pinned in `go.mod`).
- **Central mission-ID allocation.** `POST /v1/missions/allocate` endpoint and
  `mission_allocate` MCP tool enable multiple harnesses to safely allocate
  unique mission IDs across UTC dates, eliminating collision windows in
  concurrent allocation. The allocation and mission creation happen
  atomically in a single transaction; concurrency is handled by retrying on
  PRIMARY KEY collision. Brings MCP tool count from 15 to 16 and HTTP route
  count from 18 to 19. A `harness` column on `missions` attributes each
  mission to the harness that allocated it, enabling shared bishop-memory
  instances to track multi-harness work. The harness identity is read from
  the caller's `.claude/bishop-memory.conf` and passed via the `harness`
  parameter to `mission_allocate`.
- **Live mission step mirroring.** `POST /v1/missions/{id}/steps` now uses
  upsert semantics keyed on the composite `(mission_id, step)` pair, backed
  by a unique index, so step records from multiple sources can update
  independently without collision. The `step` field becomes required; audit
  rows (`flight_recorder` entries) now carry both the step label and agent
  identity read back from the upserted row, preserving resulting state rather
  than the actor's intended state alone. This closes a gap where concurrent
  step updates would collide or be lost entirely.
- **`next_action` kept current.** The `next_action` mission field is now
  synchronized with the harness CURRENT-MISSION.md and cleared on mission close
  (status → `complete`). On creation, omitted `next_action`, `owner`, and
  `blockers` fields are stored as SQL NULL; the reconciler treats NULL and empty
  string as equivalent.

### Changed

- **The triage processor drafts far fewer directives.** After the first runs
  produced 43 pending drafts from about 130 findings, most of them built on a
  single finding, `.claude/skills/findings-triage/SKILL.md` now caps a run
  at two drafts. A draft needs three or more members recommended `approve`,
  or two or more with a `directive_candidate`. A lone finding is never
  grouped and never drafted; its `proposed_change` carries the fix. The
  processor drafts nothing for a rule that a ratified directive in the
  harness's `DIRECTIVES.md` already states, or that the operator has rejected
  before. The classifier sets `directive_candidate` only when a finding
  itself asks for a binding, project-wide rule. Flags already set are kept
  until the category is re-classified.
- `.gitignore` extended to cover the new cross-compiled Linux binaries
  and other build/test artefacts ahead of this project's first commit.

---

Earlier in this task (not previously changelogged): a three-part code
review (persistence, HTTP, MCP/scripts) found and fixed five CRITICAL
defects — an empty-body 500 from `gin.Recovery`, `createTaskHandler`
returning 409 for every insert failure instead of only real conflicts,
the `validationError` echo-scalar issue this file's own "Fixed" section
above continues the story of, `mcpd`'s HTTP adapter not guarding
dot-segment task IDs before building upstream URLs, and `memoryd` never
installing a signal handler so its deferred `db.Close()` was
unreachable on every real exit path. All five now have regression
tests; `cmd/memoryd/main.go` now shuts down gracefully on
`SIGINT`/`SIGTERM` via `signal.NotifyContext` + `http.Server.Shutdown`.

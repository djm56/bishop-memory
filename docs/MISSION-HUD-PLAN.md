# Mission HUD — Plan

Status: phases 1 and 3 built on branch `feat/mission-hud`; phase 2 deferred
(decision D3). The live database still needs the sync and backfill in §2.4.
Date: 2026-10-08.

The goal is a mission head-up display next to the triage review page. It shows
the full history of any mission in one place: what it set out to do, how each
step went, how it ended, and the findings, patterns and directives that came
out of it. Before that page is worth building, the database has to be able to
answer "what belongs to this mission?" — and today, for most of it, it cannot.

## 1. What exists today

Read before planning: `db/schema.sql`, `internal/importer/importer.go`,
`internal/api/*`, `cmd/mcpd/main.go`, `scripts/reconcile-memory.py`,
`data/memory.db`, the kirsch mission tree (`.claude/memory/missions`, 30
missions; `.opencode/memory/missions`, 5 missions) and the kirsch mission
templates (`.claude/templates/mission/`).

A kirsch mission folder holds three files:

| File | Content | Already in the DB? |
|---|---|---|
| `BRIEF.md` | Goal, acceptance criteria, key files, notes | As a `documents` row for the 13 anomalous and 30 kirsch Claude Code missions; the 5 kirsch OpenCode missions are filed under kind `.opencode` (see below). Linked to its mission by file path alone. |
| `PROGRESS.md` | One table: step, phase, agent, status, notes | Yes. `mission_steps` has the same columns and the reconciler mirrors the table into it. No separate storage needed. |
| `DEBRIEF.md` | Fixed sections from `DEBRIEF-TEMPLATE.md`: summary, criteria outcome, step recap, deliverables, tracker check, wrong assumptions, sub-agent mistakes, linked findings and patterns, similar future missions | As `BRIEF.md`. |

Facts that shape the design (counts from `data/memory.db` on 2026-10-08):

| Fact | Consequence |
|---|---|
| `documents` has no `mission_id`. Brief, progress and debrief rows are tied to a mission only through `source_path`. | Nothing can join a mission to its brief or debrief without string-matching paths. |
| `documents` is filled only by `POST /v1/documents/sync`, which defaults to `MEMORY_ROOT`. The launchd plist sets that to the anomalous checkout. Nothing calls it automatically. | Documents are only as fresh as the last manual sync of each root. The kirsch OpenCode tree was only ever imported by a sync rooted at the repository, so its mission files carry kind `.opencode` instead of `brief`/`progress`/`debrief`. |
| 136 of 462 `documents` rows (1.2 MB) lie outside every registered harness memory root, with kinds `.claude`, `.opencode`, `plan`, `cmd`, `doc`, `testdata`, `document`. | A sync was once run against a repository root rather than `<repo>/.claude/memory`. The walker skips hidden files but not hidden directories, and nothing deletes rows. |
| `findings.mission_id` is NULL on 331 of 331 rows. | Agents do not pass it to `finding_append`, and `FINDINGS.md` has no mission field for the reconciler to read. The debrief's "Findings and Patterns Linked" section does name them, by date and target. |
| `mission_steps.started_at` and `ended_at` are NULL on 770 of 770 rows; `summary` is set on 18. | The API accepts all three, but neither the hook nor the reconciler sends them. `flight_recorder` has the timing (`mission.step` events, 1,839 rows) and the summary (`step-sync` events, 649 rows). |
| `step-sync` rows in kirsch's `FLIGHT-RECORDER.md` are attributed to `@lambert` (the journal writer), not the agent that ran the step. | Not a database bug. The HUD takes a step's agent from `mission_steps.agent` and uses the `step-sync` row only for its note. |
| `crew` has 0 rows. Nothing writes it. | Agent identity lives as text on `mission_steps.agent` and `flight_recorder.agent`. See decision D1. |
| Harness names are free text. `kirsch-opencode` (3 findings, one `harnesses` row) and `kirschopencode` (5 missions, the name kirsch's `opencode.json` and `.opencode/bishop-memory.conf` use) both exist for the same memory root. | Fix the 3 findings; drop the stray row. |
| `patterns.discovered_mission` is set on 50 of 52 rows. `service_records` has no mission column. | Patterns link directly. Service records can only be linked by agent plus the mission's date range. |
| `missions.harness` is NULL on 10 of 50 rows. | Missions created through `POST /v1/missions` (the reconciler) rather than `mission_allocate` never get one. |

What the `documents` tables are, for reference: `documents` holds one row per
imported Markdown file (or JSONL line), with its full body and a content hash.
`documents_fts` is the FTS5 full-text index behind `/v1/memory/search`. The
five `documents_fts_*` tables are SQLite's internal storage for that index and
are never touched directly.

## 2. Phase 1 — link what is already there

All schema changes are additive nullable columns, applied by
`internal/store/migrate.go`.

### 2.1 Documents know their mission and harness

- Add `documents.mission_id TEXT` and `documents.harness TEXT`, with an index
  on `mission_id`.
- The importer sets `mission_id` from the path for `missions/<id>/BRIEF.md`,
  `PROGRESS.md` and `DEBRIEF.md`, and `harness` from the harness whose memory
  root is being synced. An unchanged file (same hash) still gets the two
  columns filled and its kind corrected, so a re-sync backfills existing rows
  without rewriting the FTS index.
- The walker skips hidden directories (`.claude`, `.opencode`, `.git`), so a
  sync pointed at the wrong level can no longer import a whole repository.

### 2.2 Sync every harness, and sync on mission change

- `POST /v1/documents/sync` with no `root` syncs every registered harness
  memory root, each tagged with its harness name. A body `harness` syncs just
  that harness's root. A body `root` still works; its harness is looked up by
  memory root. `MEMORY_ROOT` remains the fallback when no harness is
  registered.
- `PATCH /v1/missions/:id` re-imports that mission's three files after the
  update commits, when the mission's harness has a registered memory root.
  It costs three file reads and a hash compare. A failure is logged and never
  fails the mission update.

### 2.3 New writes fill the empty columns

- `POST /v1/findings` without a `mission_id` takes the harness's open mission
  (status `in-progress` or `blocked`, most recently updated), provided the
  finding's date is not before the mission opened. A backlog replayed by the
  reconciler therefore does not get pinned to whatever mission is open now.
- `POST /v1/missions/:id/steps` stamps `started_at` the first time a step is
  stored as `in-progress`, and `ended_at` the first time it is stored as
  `done` or `failed`, unless the caller supplied them. A step first seen as
  already `done` gets no `started_at`; there is no honest value for it.
- `POST /v1/flight-recorder` with event `step-sync`, a mission and a step
  copies the note into that step's `summary`.

### 2.4 One-off backfill — `scripts/backfill-mission-links.py`

Dry run by default; `--apply` writes, after copying the database to
`data/memory.bak-<timestamp>.db`. Each part reports what it would change.

1. **Harness names.** Move the 3 `kirsch-opencode` findings to
   `kirschopencode` and delete the stray `harnesses` row.
2. **Orphan documents.** Delete `documents` rows (and their `documents_fts`
   rows) whose `source_path` is under no registered harness memory root.
3. **Step timing and summaries.** From `flight_recorder`: `started_at` from
   the first `mission.step` event whose note says `status: in-progress`,
   `ended_at` from the first saying `done` or `failed`, and `summary` from the
   latest `step-sync` note. Only NULL columns are filled.
4. **Finding to mission.** First, the debrief's "Findings entry refs" lines
   (`[YYYY-MM-DD] — <target>`), matched to a finding of the same harness with
   that date and target. Then, for findings still unlinked, a date-window
   match: the one mission of the same harness that was open on the finding's
   date. A finding that matches several missions is left NULL and listed.

Run order: deploy the new binary, `POST /v1/documents/sync` (imports kirsch,
fills `documents.mission_id`), then the backfill.

## 3. Phase 2 — structure the debrief (optional)

The debrief template has fixed sections. The importer parses them into small
tables when it imports a `DEBRIEF.md`. Each table is replaced wholesale on
re-import, keyed by `mission_id`.

| Table | Columns | From section |
|---|---|---|
| `mission_criteria` | `mission_id, ord, criterion, met (0/1), evidence` | Acceptance Criteria Outcome (`- [x] text — pass: evidence`) |
| `mission_deliverables` | `mission_id, path, note` | Deliverables Changed |
| `mission_lessons` | `mission_id, kind ('wrong-assumption', 'agent-mistake', 'similar-mission'), text` | Wrong Assumptions; Sub-Agent Mistakes and Corrections; Similar Future Missions |

The brief's criteria go into `mission_criteria` too, with `met` NULL, so an
in-flight mission shows its criteria before it has a debrief.

What this buys: questions the page can filter on, not only text to read —
"missions with an unmet criterion", "every wrong assumption about the
provider package", "which files change most often".

## 4. Phase 3 — the mission HUD

Built on branch `feat/mission-hud`, without phase 2 (decision D3).

### 4.1 Endpoints

New read-only routes under `/v1/hud`, so nothing the harnesses, `mcpd` or the
triage agents already call changes shape. Rows come back keyed by column name.

- `GET /v1/hud/missions?harness=&status=&outcome=&q=` — the board: every
  mission with step, done-step, finding and pattern counts and whether it has
  a brief and a debrief, plus the harness list with mission counts. `q`
  matches the id and title, and runs a full-text search over the mission's
  brief, progress and debrief.
- `GET /v1/hud/missions/:id` — the mission row; its steps in step order; its
  flight-recorder events; its brief, progress and debrief bodies; patterns
  where `discovered_mission` matches; directives accepted from proposals whose
  evidence includes its findings; the crew rows of the agents on its steps;
  those agents' service records dated within the mission.
- `GET /v1/findings?mission_id=` — a new filter on the existing route. The
  page takes a mission's findings from here, so they arrive with their triage
  category and recommendation exactly as the triage page shows them.

### 4.2 Pages

The HUD is its own page, `/missions`, served by memoryd beside `/triage`
(decision D4). `/` redirects to `/missions`.

- Both pages carry a Triage / Missions switch in the header and share the
  theme setting.
- A finding card on `/triage` links to its mission (`/missions#<id>`).
- A finding on `/missions` links to `/triage?finding=<id>`, which opens
  Browse on just that finding, with a "show all" link back to the full list.

`/missions` has the mission list on the left (search, harness and status
filters) and the selected mission on the right:

- Header: id, harness, owner, status and outcome, opened and closed times.
- Tiles: steps done, duration, findings by status, acceptance criteria met,
  patterns, directives.
- Brief: the goal, and the acceptance criteria ticked or crossed from the
  debrief's "Acceptance Criteria Outcome" lines, with their evidence; the full
  brief on demand.
- Steps: a timeline with each step's agent and crew role, status, start time,
  duration and summary; expanding a step shows its notes and journal events.
- Findings: category, status and recommendation, each linking to triage.
- Debrief: the Markdown, rendered.
- Patterns, directives, crew and service records, where there are any.
- Journal: every flight-recorder event, and PROGRESS.md as written.

Both pages remain single embedded HTML files with no build step.

### 4.3 Crew

Document sync also reads each registered harness's agent definitions
(`<checkout>/.claude/agents/*.md`, `<checkout>/.opencode/agents/*.md` — the
`agents/` directory beside the memory root) into `crew` (decision D1). The
agent name is the key, lower-cased and without `@`, so an agent defined in
several harnesses is one row carrying the definition synced last. `role` is
the description's first sentence; the new `description` and `source_path`
columns hold the full line and the file. The HUD matches `@hicks`, `hicks`
and `harness:hicks` on steps and service records to the same crew row.

### 4.4 Scratch cleanup

`make scratch-clean [DAYS=30] [HARNESS=name] [APPLY=1]`
(`scripts/clean-scratch.py`) clears harness workspace scratch (decision D2).
It moves every file under `<memory root>/workspace/` older than `DAYS` (by
modification time) into the macOS Trash under `bishop-scratch-<timestamp>/`,
and deletes those files' search documents. `workspace/README.md` and hidden
files such as `.gitkeep` stay. It is a dry run unless `APPLY=1`, and runs
only when the operator runs it.

## 5. Rollout

1. Phase 1 code, tests, deploy, sync, backfill. Check: every kirsch mission
   has three `documents` rows with `mission_id` set; most findings have a
   mission; most finished steps have `ended_at`.
2. Phase 3 endpoints and page against phase 1 data. The page is useful
   without phase 2.
3. Phase 2 parsing, if and when the debrief format settles (D3).

## 6. Decisions (settled 2026-10-08)

- **D1. `crew`.** Filled from the harness agent definitions, keyed on the
  agent name so there is one row per agent across harnesses (§4.3).
- **D2. `workspace/` documents.** Kept and searchable. Old scratch is cleared
  by a manual command with a day threshold, default 30 (§4.4).
- **D3. Phase 2.** Not now. Debriefs stay plain Markdown while their format
  may still change; the HUD reads the criteria lines directly.
- **D4. Where the HUD lives.** A separate page, `/missions`, on the same
  memoryd as `/triage`, with links between them (§4.2).

## 7. Risks

- **Debrief format drift.** Phase 2 and the finding backfill parse Markdown
  written by agents. Parsers skip what they do not recognise and report it;
  they never fail a sync.
- **Wrong finding attribution.** The date-window match can pick the wrong
  mission when a harness runs missions back to back on one day. The backfill
  only links when exactly one mission matches, and lists the rest.
- **Sync cost.** Syncing every harness walks every memory tree, including
  workspace scratch. This is acceptable on demand. The per-mission sync on
  `PATCH` reads only three files.

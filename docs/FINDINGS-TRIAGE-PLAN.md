# Findings Triage — Plan

Status: implemented on 2026-10-02 (phases 1–3 of §9; phase 4 items remain
open). The operator guide is `docs/FINDINGS-TRIAGE.md`; this document is kept
as the design record. Where the two differ, the guide describes what shipped.
An earlier, unbuilt design from 2026-09-15 is in `TRIAGE-PLAN.md` at the
repository root. Its update section compares the two designs and records how
its open decisions were settled.
Date: 2026-10-02.

## 1. What exists today, and what gets in the way

Read before planning: `db/schema.sql`, `internal/api/*`, `cmd/mcpd/main.go`,
`scripts/reconcile-memory.py`, `data/memory.db` (all 314 findings, 45 patterns,
83 service records, 3,273 flight-recorder rows), the kirsch harness doctrine
(`bishop.md`, `mission-lifecycle/SKILL.md`, `self-improvement/SKILL.md`,
`FINDINGS-TEMPLATE.md`, `DIRECTIVES-TEMPLATE.md`), and the kirsch and
anomalous memory trees.

Facts that shape the design:

| Fact | Consequence |
|---|---|
| All 314 findings are `proposed`. The kirsch `FINDINGS.md` has 2 entries the operator moved to `approved`, but the DB still shows `proposed`. | The reconciler only creates findings. It never syncs status. Approval today is invisible to the service. |
| `findings.status` is a `CHECK` constraint. SQLite cannot extend a CHECK without rebuilding the table. | New states like `pre-approved` must not go in `findings.status`. They live in a separate table. This also keeps the doctrine intact: `status` stays human-only. |
| `findings` has no `harness` column, and `mission_id` is NULL on 314 of 314 rows (`FINDINGS.md` has no mission field; the reconciler cannot supply one). | A shared ledger cannot say which harness a finding came from, so a decision cannot be written back to the right `FINDINGS.md`. Findings 1–64 are the anomalous harness; 65–314 are kirsch (250 entries, matches the file). |
| The `directives` table is empty, `DIRECTIVES.md` is empty in every harness, and there is no write route and no reconciler parse for directives. | A directive pipeline is greenfield. Proposal → human ratification → `DIRECTIVES.md` → mirror. |
| The agent boundary is **the mcpd tool list**, not HTTP auth. There is no auth at all; mcpd only registers 16 tools. | New operator routes stay off the default mcpd tool set. A second mcpd profile exposes the triage tools to the triage agents only. |
| `state-continuity.sh` runs the reconciler on every memory-tree write made through Claude Code's Write/Edit tools. | External scripts that write `FINDINGS.md` do not trigger the hook, so a write-back cannot loop. The next reconcile (hook or step H) must tolerate the new status lines. |
| The harness Write tool guard refuses filenames containing `findings` (findings #96, #120). | Triage never uses the harness's Write tool. All file writes are Python scripts run outside a harness session. |
| `.claude/memory/` is git-excluded in kirsch (#34, #146). | Write-back has no git safety net. The exporter must back up, write atomically, and re-read to verify. |
| Finding natural key is `(finding_date, target, suggestion)`. | Classification and recommendations key on `findings.id`; status write-back locates an entry in Markdown by natural key. |

## 2. Shape of the solution

Three loops, all outside the mission state machine:

```
  nightly 02:00  classify   Haiku   unclassified findings  → finding_triage (category, confidence)
  nightly 02:30  process    Sonnet  one category per run   → finding_groups, finding_recommendations,
                                                              directive_proposals   (all "pending")
  whenever       review     human   http://127.0.0.1:8787/triage → decisions
                                     ↓
                            findings.status (human-only field, set by the decision route)
                            scripts/export-decisions.py → FINDINGS.md / DIRECTIVES.md in the owning harness
                            reconciler (already runs) mirrors Markdown back; status now compared
```

Recommendation: keep this in **bishop-memory**, not in bishop-harness. It spans
harnesses, it has no mission to track, and the harness guards (Write guard,
state-sync contract, learning pass) would fight a batch job. The harness gets at
most one optional read-only slash command later (§8).

## 3. Category taxonomy (proposed, 17 + fallback)

Derived from reading all 314 findings. Each row shows example finding ids from
the current database so you can sanity-check the fit. Edit freely before
seeding; the classifier reads descriptions from the table, not from its prompt.

| # | slug | What belongs here | Example ids |
|---|---|---|---|
| 1 | `brief-writing` | How Bishop writes briefs: name the artefact, quote verbatim, absolute paths, provenance of figures, hypothesis vs fact, no denied commands | 4, 24, 33, 36, 49, 58, 91, 104, 111, 116, 122, 172, 179, 185, 273, 297, 308 |
| 2 | `evidence-and-reporting` | What a completion report must contain: raw output pasted, described result counts as not done, report slots, trailers | 10, 20, 142, 158, 268, 294, 303 |
| 3 | `check-design` | Designing a verification or gate so it can fail: positive controls, falsifying command, zero-result checks, calibration, "laziest conforming work" | 9, 20, 44, 90, 99, 128, 141, 189, 199, 200, 267, 274, 280, 289, 307 |
| 4 | `review-practice` | The code-review skill and @apone: severity definitions, scope, carrying findings forward, grading vs repairing, reviewer builds own table | 30, 78, 93, 101, 102, 105, 106, 110, 125, 129, 166, 169, 174, 175, 187, 195, 238, 275, 310, 311, 314 |
| 5 | `fix-rounds-and-escalation` | Rule 3 budgets, triggers, non-code fix rounds, re-planning when a phase balloons | 14, 57, 79, 164, 186, 265, 276, 304 |
| 6 | `class-closure` | Fix the class not the instance: sweeps, enumerate before briefing, both-direction diffs, re-walk the set | 1, 7, 81, 118, 136, 147, 153, 157, 173, 178, 299 |
| 7 | `state-files-and-journal` | FLIGHT-RECORDER / PROGRESS / CURRENT-MISSION integrity: append mechanics, renumbering, timestamps, pipe escaping, who writes | 11, 12, 17, 18, 28, 31, 63, 69, 70, 100, 121, 131, 150, 163, 171, 180, 183, 184, 188, 286, 300 |
| 8 | `mission-planning` | Plan shape, terminal states, partial steps, deployed-state tracking, commit cadence, archive naming, init checklist | 13, 29, 43, 133, 146, 162, 181, 211, 272, 277, 309 |
| 9 | `agent-definitions` | Proposals to change `.claude/agents/*.md`: tools lists, report contracts, standing clauses | 2, 76, 77, 124, 135, 144, 148, 293, 301 |
| 10 | `harness-mechanics` | Hooks, `settings.json`, permission classifier, Write guard, PostToolUse timing, MCP instruction injection | 16, 19, 21, 22, 23, 27, 45, 51, 65, 67, 89, 96, 115, 120, 182, 194, 258 |
| 11 | `documentation-accuracy` | Counts in prose, stale facts, docs sourced from code, comments as claims | 6, 47, 50, 80, 143, 149, 160, 206, 283, 287 |
| 12 | `testing-discipline` | Tests that cannot fail, mutation testing, calibration artefacts, deleted tests, sentinels, fixtures | 84, 85, 103, 108, 109, 112, 114, 117, 134, 137, 138, 159, 201, 203, 228, 233, 237, 239–252, 263, 266, 271, 292, 298, 302, 312 |
| 13 | `product-defect` | A defect in the product repository itself (kirsch `internal/tui`, `internal/app`, `cmd/kirsch`, …), not in the harness | 196–198, 207–210, 212–218, 220–227, 229–232, 234–236, 241, 254–257, 259–262, 264, 269, 270 |
| 14 | `repo-tooling-and-ci` | Lint/CI config, `go mod tidy`, tool install paths, gate derivation from CI | 52, 87, 97, 113, 132 |
| 15 | `shell-portability` | Shell conventions: sourcing guards, `stat -f`, exit codes, redirects, locks | 35, 40, 41, 42, 54, 55, 56 |
| 16 | `bishop-memory-service` | This service: API shape, MCP tools, reconciler, hook mirror | 32, 37, 38, 39, 48, 53, 74, 88, 98 |
| 17 | `operator-evidence` | Manual walkthroughs and files holding the operator's own observations: entry/exit state, ticks, verbatim copies | 281, 285, 288, 290, 295, 296, 305 |
| — | `uncategorised` | Classifier confidence below threshold. Reviewed by hand, then re-classified. | — |

Cross-cutting flag, not a category: `directive_candidate` (#80, #89, #90, #292,
#312 say so explicitly; the processor can set it on others). A secondary
category is allowed because many findings name two targets ("@bishop /
code-review skill").

Seed file: `db/finding-categories.json` (slug, name, description, examples).
Loaded by `scripts/triage-seed-categories.py`; idempotent upsert by slug.

## 4. Schema changes (all additive; no existing CHECK touched)

New tables in `db/schema.sql` (`CREATE TABLE IF NOT EXISTS`, so existing
databases pick them up at next boot):

```sql
CREATE TABLE IF NOT EXISTS harnesses (
    name            TEXT PRIMARY KEY,
    memory_root     TEXT NOT NULL,          -- absolute path to .claude/memory; upserted by the reconciler
    last_seen_at    TEXT NOT NULL DEFAULT (CURRENT_TIMESTAMP)
);

CREATE TABLE IF NOT EXISTS finding_categories (
    slug              TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    description       TEXT NOT NULL,        -- written for the classifier
    examples          TEXT,                 -- two or three one-liners
    sort_order        INTEGER NOT NULL DEFAULT 0,
    active            INTEGER NOT NULL DEFAULT 1,
    last_processed_at TEXT,                 -- rotation pointer for the processor
    created_at        TEXT NOT NULL DEFAULT (CURRENT_TIMESTAMP)
);

CREATE TABLE IF NOT EXISTS triage_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    kind        TEXT NOT NULL CHECK (kind IN ('classify','process')),
    category    TEXT,
    model       TEXT,
    status      TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','done','failed')),
    considered  INTEGER NOT NULL DEFAULT 0,
    written     INTEGER NOT NULL DEFAULT 0,
    notes       TEXT,
    started_at  TEXT NOT NULL DEFAULT (CURRENT_TIMESTAMP),
    finished_at TEXT
);

CREATE TABLE IF NOT EXISTS finding_triage (          -- one row per finding
    finding_id          INTEGER PRIMARY KEY REFERENCES findings(id) ON DELETE CASCADE,
    category            TEXT NOT NULL REFERENCES finding_categories(slug),
    secondary_category  TEXT REFERENCES finding_categories(slug),
    directive_candidate INTEGER NOT NULL DEFAULT 0,
    confidence          REAL,
    summary             TEXT,               -- one-line restatement, <=140 chars, for the review page
    classified_by       TEXT NOT NULL,      -- model id
    run_id              INTEGER REFERENCES triage_runs(id),
    classified_at       TEXT NOT NULL DEFAULT (CURRENT_TIMESTAMP)
);

CREATE TABLE IF NOT EXISTS finding_groups (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    category   TEXT NOT NULL REFERENCES finding_categories(slug),
    title      TEXT NOT NULL,
    summary    TEXT NOT NULL,               -- the one underlying rule the members share
    target     TEXT,                        -- the skill/agent file a fix belongs in
    run_id     INTEGER REFERENCES triage_runs(id),
    created_at TEXT NOT NULL DEFAULT (CURRENT_TIMESTAMP)
);

CREATE TABLE IF NOT EXISTS finding_recommendations (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    finding_id      INTEGER NOT NULL REFERENCES findings(id) ON DELETE CASCADE,
    group_id        INTEGER REFERENCES finding_groups(id),
    recommendation  TEXT NOT NULL CHECK (recommendation IN ('approve','reject','supersede','defer')),
    superseded_by   INTEGER REFERENCES findings(id),
    rationale       TEXT NOT NULL,
    proposed_change TEXT,                   -- exact before/after text for the target file (approve only)
    state           TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','accepted','declined','expired')),
    decided_by      TEXT,
    decided_at      TEXT,
    run_id          INTEGER REFERENCES triage_runs(id),
    created_at      TEXT NOT NULL DEFAULT (CURRENT_TIMESTAMP)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_recommendation_pending
    ON finding_recommendations(finding_id) WHERE state = 'pending';

CREATE TABLE IF NOT EXISTS directive_proposals (   -- mirrors the DIRECTIVES-TEMPLATE fields
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    group_id       INTEGER REFERENCES finding_groups(id),
    harness        TEXT,                    -- which DIRECTIVES.md; NULL = every harness
    title          TEXT NOT NULL,
    applies_when   TEXT NOT NULL,
    rule           TEXT NOT NULL,
    rationale      TEXT NOT NULL,
    reviewer_check TEXT NOT NULL,
    example        TEXT,
    evidence       TEXT NOT NULL,           -- JSON array of finding ids
    state          TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','accepted','declined','expired')),
    directive_id   TEXT,                    -- DIR-NNN once ratified
    decided_by     TEXT,
    decided_at     TEXT,
    run_id         INTEGER REFERENCES triage_runs(id),
    created_at     TEXT NOT NULL DEFAULT (CURRENT_TIMESTAMP)
);
```

Additive columns via `internal/store/migrate.go` `additiveColumns`:

- `findings.harness TEXT` — set by the reconciler from `--harness` and by
  mcpd from `BISHOP_HARNESS`. Backfilled once by
  `scripts/triage-backfill-harness.py`, which matches each row's natural key
  against each registered harness's `FINDINGS.md`.
- `findings.decision_note TEXT` — the operator's one-line reason on reject or
  retire. Written only by the decision route.

The `directives` table gains nothing; it is populated by a new reconciler parse
of `DIRECTIVES.md` (§6).

## 5. HTTP routes

Two groups. The **agent group** is what mcpd's default profile proxies today.
The **operator group** is new and is never registered as a harness MCP tool.
The **triage group** is proxied only by `mcpd` when `MCPD_PROFILE=triage`.

Operator (human, UI, scripts):

| Route | Purpose |
|---|---|
| `GET /v1/findings?status=&category=&harness=&unclassified=1&limit=&offset=` | Extend the existing list with filters and paging. Include `harness`, `category`, `recommendation` in each row. |
| `POST /v1/findings/:id/decision` `{status, approver, note}` | **The only writer of `findings.status`.** Sets status, approver, date_approved, decision_note; marks the pending recommendation accepted or declined; appends `finding.decided` to the flight recorder. Validates the transition (`proposed→approved/rejected/retired/superseded`, `approved→applied`). |
| `POST /v1/directive-proposals/:id/decision` `{state, decided_by, note}` | Accept allocates the next `DIR-NNN`, stores it on the proposal, and marks every evidence finding `applied` in the same transaction. |
| `GET /v1/finding-categories`, `PUT /v1/finding-categories/:slug` | Read and edit the taxonomy. |
| `GET /v1/triage/pending` | Everything awaiting a decision, grouped by category then group. The review page's data source. |
| `GET /v1/triage/runs` | Run history with acceptance counts. |
| `GET /triage` (HTML) | The review page (§7). |

Triage (classifier and processor, through mcpd triage profile):

| Route | mcpd tool |
|---|---|
| `GET /v1/finding-categories` | `triage_categories` |
| `GET /v1/findings?unclassified=1&limit=N` | `triage_next_unclassified` |
| `PUT /v1/triage/classifications` (batch upsert into `finding_triage`) | `triage_classify` |
| `GET /v1/findings?category=X&status=proposed&include_triage=1` | `triage_category_findings` |
| `GET /v1/triage/decisions?category=X&limit=20` (recent human decisions, for calibration) | `triage_recent_decisions` |
| `POST /v1/finding-groups` | `triage_group_create` |
| `POST /v1/finding-recommendations` (batch) | `triage_recommend` |
| `POST /v1/directive-proposals` | `directive_propose` |
| `POST /v1/triage/runs`, `PATCH /v1/triage/runs/:id` | `triage_run_start`, `triage_run_finish` |
| existing `GET /v1/patterns`, `GET /v1/memory/search` | `pattern_list`, `memory_search` (reused read tools) |

Harness profile additions (read-only, safe for the crew): `finding_list` gains
`category` and `harness` filters so Bishop can ask for "approved findings in
`brief-writing`" at mission start.

## 6. Reconciler and write-back

`scripts/reconcile-memory.py` changes:

1. Send `harness` in every finding payload (it already has `--harness`).
2. Upsert a `harnesses` row (`name`, `memory_root`) on every run. This is how
   bishop-memory learns where each harness's Markdown lives without a config
   file of its own.
3. **Status sync, Markdown → DB only.** When an entry's Markdown `Status` is not
   `proposed` and differs from the DB, call the decision route with
   `approver` = the Markdown `Approver` and `note` = `reconciled from FINDINGS.md`.
   Markdown stays the source of truth; a hand edit in the file wins.
4. Parse `DIRECTIVES.md` (`### DIR-NNN — title` entries and the index) and
   upsert `directives` by `directive_id` through a new `PUT /v1/directives/:id`
   that is **not** registered in either mcpd profile. The "no write route"
   guarantee becomes "no agent-reachable write route", which is what it was
   protecting.

New `scripts/export-decisions.py --harness <name> [--dry-run]`:

- For every finding with `harness = <name>` whose DB status differs from the
  Markdown status: locate the entry by natural key, replace exactly the three
  lines `**Status**`, `**Approver**`, `**Date approved**`, and append a
  `**Disposition (triage):** <note>` line where a note exists (the template
  already allows amendment lines after `Date approved`).
- For every accepted directive proposal for that harness without a Markdown
  entry: append the entry in template form below the last entry, add the index
  row, and check the length budget (≤1,510 chars) before writing.
- Write atomically (temp file + rename), keep `FINDINGS.md.bak-<timestamp>` in
  `.claude/memory/workspace/triage-backups/`, re-read and diff to confirm only
  the intended lines changed, and refuse if the file has more than the
  expected number of changed lines.
- Conflict rule: Markdown non-`proposed` and different from the DB decision →
  do not write, flag the finding on the review page as "conflict".

The export runs at the end of every review session (a button on the page and
`make triage-export`). The next hook-triggered reconcile sees matching status
and does nothing.

## 7. The review page

Served by memoryd at `http://127.0.0.1:8787/triage`. Go `html/template`, no
build step, no JavaScript framework; a few lines of inline JS for keyboard
shortcuts and bulk actions. Loopback-only like everything else.

Layout:

- Left rail: categories with pending counts, plus "uncategorised" and
  "conflicts".
- Main: one category at a time. Groups first, each with its summary, the
  target file, the member findings (summary line expands to full suggestion
  and rationale), the model's recommendation and rationale, and the
  `proposed_change` as a before/after block with a copy button.
- Per finding: **Approve** / **Reject** (reason required) / **Defer** /
  **Supersede** (pick the surviving finding). Keyboard `a`, `r`, `d`, `s`, `j`/`k`
  to move.
- Per group: **Accept all recommendations** / **Decline all**. This is the
  one-click path when the model got a group right.
- Directive proposals: rendered exactly as the `DIRECTIVES.md` entry will
  appear, with the character count against the budget, editable in place
  before **Ratify** / **Decline**.
- Header: "N decisions not yet exported" and an **Export to Markdown** button
  that runs the exporter for every harness and shows the diff summary.
- Runs tab: each run with considered, written, accepted, declined; the
  acceptance rate per category is the quality signal to watch.

Approver name comes from a `TRIAGE_APPROVER` env var (default: the OS user), so
the Markdown `Approver` line reads as it does today ("Donovan Maidens").

## 8. Agents, skill, and trigger

Files in **this** repository:

```
.claude/
  agents/findings-classifier.md     model: haiku     tools: mcp__bishop-triage__triage_categories,
                                                            triage_next_unclassified, triage_classify,
                                                            triage_run_start, triage_run_finish
  agents/findings-processor.md      model: sonnet    tools: the triage set + pattern_list + memory_search
                                                            + Read (harness checkouts, via --add-dir)
  skills/findings-triage/SKILL.md   the doctrine both agents load
  triage.mcp.json                   registers mcpd with MCPD_PROFILE=triage (project scope, gitignored,
                                    generated like the harness's .mcp.json)
scripts/
  triage-run.sh                     the single entry point for launchd, make, and by hand
  triage-seed-categories.py
  triage-backfill-harness.py
  export-decisions.py
  com.bishop-memory.triage-classify.plist   StartCalendarInterval 02:00
  com.bishop-memory.triage-process.plist    StartCalendarInterval 02:30
  install-triage-schedule.sh                renders and loads both plists (same conventions as install-daemon.sh)
```

`scripts/triage-run.sh classify|process [--category SLUG] [--limit N] [--dry-run]`:

1. `curl /healthz`; exit 2 if the service is down (never run against nothing).
2. `classify`: if no unclassified findings, exit 0 with a log line.
3. `process`: pick the category by rotation — active, has `proposed` findings
   without a pending recommendation, oldest `last_processed_at` first.
   `TRIAGE_CATEGORIES_PER_RUN` (default 1) and `TRIAGE_ITEMS_PER_RUN` (default
   30) bound the run. During the backlog you can set 2 and 40; at ~300 proposed
   findings that clears in about a week, then it is maintenance.
4. Run Claude Code headless from the repo root so project-scope agents and
   `.claude/triage.mcp.json` are discovered:

   ```bash
   claude -p "$PROMPT" \
     --agent findings-classifier \
     --model haiku \
     --mcp-config .claude/triage.mcp.json --strict-mcp-config \
     --allowedTools 'mcp__bishop-triage__*' \
     --permission-mode dontAsk \
     --max-turns 40 \
     --output-format json \
     --add-dir "$HARNESS_CHECKOUT"          # processor only
   ```

5. Append one line to `~/Library/Logs/bishop-memory/triage.log`; the run row in
   `triage_runs` is written by the agent through `triage_run_start/finish`, and
   the script marks it `failed` if the process exits non-zero without finishing.

The skill (`findings-triage/SKILL.md`) holds the rules so the agent files stay
short:

- Classification: one primary category, optional secondary, confidence 0–1;
  below 0.6 → `uncategorised`. Write a ≤140-char summary. Never touch status.
  Batch of ≤50 per call. Categories and their descriptions come from
  `triage_categories` at run time, never from the prompt.
- Processing, in order: (1) load the category's proposed findings and the last
  20 human decisions in that category; (2) cluster by the single underlying rule
  each finding is an instance of; (3) for each member recommend `approve` only
  when the suggestion is implementable without a question and not already in
  the target doctrine (read the actual skill or agent file through `Read` and
  quote the covering sentence when rejecting as "already covered"); `reject`
  when the target no longer exists, the suggestion is obsolete, or the doctrine
  already says it; `supersede` when a later finding restates it better; `defer`
  when it needs the operator's judgement; (4) for an `approve`, draft
  `proposed_change` as exact before/after text against the current target file;
  (5) a group of three or more approve-able members with one rule → draft a
  directive proposal in the template's six fields inside the 1,300-char
  typical budget, evidence = member ids; (6) stop at the item cap and finish
  the run with counts. The processor never writes outside the triage tools.

Why Claude Code agents rather than a plain API script: you asked for agent +
skill, and the processor genuinely needs tools (read the live skill file,
search patterns). If headless `claude -p` under launchd proves awkward
(keychain, PATH), the classifier alone is a 40-line Python script against the
Messages API with Haiku and a JSON schema; the processor should stay an agent.

Harness side, optional and later: a read-only `/findings-digest` command in
bishop-harness that calls `finding_list status=approved category=…` so Bishop
sees ratified findings for the area it is about to brief. No mission-loop
integration: the user's instinct to keep this out of the state loop is right.

## 9. Rollout

Each phase is one mission-sized unit with a clean stopping point.

**Phase 1 — Classification (useful on its own).**
Schema tables `harnesses`, `finding_categories`, `triage_runs`,
`finding_triage`; additive `findings.harness`; category seed and backfill
scripts; list-route filters; mcpd triage profile with the five classify tools;
classifier agent + skill; `triage-run.sh classify`; launchd plist; Makefile
`triage-classify`. Exit: 314 findings carry a category; `uncategorised` count
reviewed and the taxonomy adjusted once.

**Phase 2 — Decisions without the model.**
Decision routes for findings; reconciler status sync (Markdown → DB) and
`harnesses` upsert; `export-decisions.py` for findings; the review page with
category browsing and approve/reject/defer. Exit: the two already-approved
kirsch findings show `approved` in the DB, and a decision made on the page
lands in `FINDINGS.md` with the right Approver and date.

**Phase 3 — Processing.**
`finding_groups`, `finding_recommendations`, `directive_proposals` tables and
routes; processor agent; rotation and caps in `triage-run.sh process`; the
page gains groups, recommendations, bulk accept, and the directive card;
directive decision route; reconciler parse of `DIRECTIVES.md`; exporter writes
directive entries. Exit: one category processed end to end, one directive
ratified from the page and visible in both `DIRECTIVES.md` and the
`directives` table.

**Phase 4 — Improve over time.**
Recent decisions fed back as calibration (already in the tool list, wired here);
acceptance-rate per category on the runs tab; `--reclassify` for a category
whose description changed; expiry of pending recommendations older than N days;
optional `scripts/triage-apply.py --recommendation N` that applies an accepted
`proposed_change` to the harness file and shows the git-free diff for the
operator to confirm; optional categorisation of patterns so the processor can
cite the pattern that already answers a finding.

## 10. Decisions needed from you

1. **Taxonomy.** Edit §3 before Phase 1 seeds it. Merge or split as you like;
   the classifier adapts because it reads the table.
2. **Markdown write-back** (recommended, §6) versus DB-only approval. DB-only is
   less code, but the crew reads `FINDINGS.md` for approved findings, so without
   write-back approvals never reach them.
3. **Processor model.** Sonnet 5.5 (`claude-sonnet-5-5`) by default; Opus 5.5
   for directive drafting if the first ratifications read as thin. Classifier
   stays Haiku 4.5 (`claude-haiku-4-5-20251001`).
4. **Cadence and caps.** One category and 30 items per night, or two and 40
   while the backlog stands.
5. **Where the agents live.** This repository (recommended) or the harness.
6. **Auto-apply.** Whether accepting an `approve` may also patch the skill or
   agent file (§9 Phase 4). Recommended: no in the first three phases; the
   page gives you the exact text to paste, and applying doctrine stays a human
   act as the harness doctrine already requires.

## 11. Risks

- **Headless Claude under launchd.** User agents normally have keychain
  access, but PATH and HOME must be set in the plist. Verify with
  `--dry-run` runs before enabling the schedule. Fallback for the classifier
  is the plain API script noted in §8.
- **Write-back on an untracked tree.** No git history under `.claude/memory/`
  in kirsch. Backups, atomic writes, and the changed-line count check in §6 are
  the guard. Keep the exporter's edit surface to the three status lines and
  appends.
- **Two harnesses, one ledger.** Until `findings.harness` is backfilled, the
  exporter must refuse to run. Rows that match neither `FINDINGS.md` stay
  NULL and are shown on the page as "orphan" for a manual assignment.
- **Reconciler and exporter touching the same file.** The hook only fires on
  Claude Code tool writes, and the reconciler never writes Markdown, so there
  is no cycle. The one ordering rule: run the export only when no mission in
  that harness is mid-sync; the page checks `CURRENT-MISSION.md` status for
  the harness and warns if a mission is `in-progress`.
- **Model drift in recommendations.** The acceptance rate per category on the
  runs tab is the early signal. If a category's rate drops below about half,
  tighten that category's description or move it to `defer`-only until the
  rules in the skill are revised.

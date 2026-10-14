# Memory Insight — Plan

Status: phase 0 (data health) merged to main (ef23e11) and live; phase 1 slice 1
(mission_criteria) merged to main (c325080) and live; decision P1 settled yes
(2026-10-14); remaining phase 1 tables (deliverables, lessons, agent_mistakes,
step_reviews) planned; phases 2–4 planned, not started. Decisions P2–P5 in §7 are open.
Date: 2026-10-14.

The service captures missions well, and the HUD shows one mission well. What
neither does yet is answer questions across missions: which agents are
reliable and where they fail, what a mission costs, and whether anything the
memory holds is read before the mistake it warns about happens again. This
plan takes the HUD from a per-mission viewer to that, in five phases, each
useful on its own.

Read with: `MISSION-HUD-PLAN.md` (phase 2 there, structured debriefs, is
phase 1 here), `../ROADMAP.md` (items already listed there are not repeated),
`db/schema.sql`, `internal/api/hud.go`, `internal/api/grades.go`,
`internal/ui/missions.html`.

## 1. What the live database shows (2026-10-14)

| Fact | Consequence |
|---|---|
| 68 missions, 1,089 steps, 5,712 journal rows, 420 findings (351 linked to a mission), 65 grades (A 4, B 19, C 26, D 15, F 1) | Enough history for cross-mission questions to have answers. |
| 10 missions had no `harness`. `POST /v1/missions` (the reconciler's path and `mission_create`) never set one. | Off the harness filter and the performance page. Fixed in phase 0. |
| `mission-20261009-02` sat `not-started` for five days. | Nothing flags an abandoned mission. Fixed in phase 0. |
| 283 of 580 documents (3.15 of 6.14 MB) are `workspace` scratch, and for "calibration" 3 of the top 5 search hits were scratch. | Agents' `memory_search` reads scratch before curated memory. Fixed in phase 0. |
| All 1,025 `mission.updated` journal notes read "Mission updated". | The audit trail cannot say what changed. Fixed in phase 0. |
| The HUD journal showed `created_at`, and the page could not parse the harness's `occurred_at` format (`YYYY-MM-DD HH:MM UTC`). | Replayed events showed their insert time. Fixed in phase 0. |
| Steps by `senior (general-purpose)` and `operator` match no crew row. | The HUD showed no role. Phase 0 labels them; the harness should use crew names (§6). |
| Debriefs follow a fixed template with mandatory tables: criteria outcome, step recap, deliverables, wrong assumptions, and **Sub-Agent Mistakes** (agent, mistake, impact, correction, prevention). | The richest reliability data in the system, held only as text. Phase 1. |
| Reviewer step notes start `APPROVED` or `NOT APPROVED` and count CRITICAL and warning items; @apone alone has 457 steps. | First-pass approval and escalation rates are computable but not computed. Phase 1. |
| No token, model or cost data anywhere. Claude Code transcripts exist under `~/.claude/projects/<encoded checkout path>/*.jsonl`. | Cost per mission, per grade and per harness is unknown. Phase 3. |
| No record of what agents read from memory. | Whether the memory changes outcomes cannot be measured. Phase 4. |

## 2. Phase 0 — data health (built)

Merged to main (ef23e11) and live; nothing here changes a route's existing
fields or anything the triage agents call, apart from search order.

| Fix | Where |
|---|---|
| `POST /v1/missions` accepts `harness`; `PATCH` fills it only while NULL, never moving a mission between harnesses; `GET /v1/missions[/:id]` returns it. `mcpd`'s `mission_create` sends `BISHOP_HARNESS`; the reconciler sends `--harness` on create and claims harness-less missions it owns. | `internal/api/missions.go`, `internal/model/mission.go`, `cmd/mcpd/main.go`, `scripts/reconcile-memory.py` |
| Backfill part `mission-harness`: sets `missions.harness` from the mission's own documents when they name exactly one harness (all 10 on the live database). `--only` runs one part. | `scripts/backfill-mission-links.py` |
| The board returns `last_activity` (later of `updated_at` and the newest journal event) and `stale` (unfinished, idle 48 hours), with a `stale` filter. The page shows a stale pill, a header count and a status-menu option. | `internal/api/hud.go`, `internal/ui/missions.html` |
| Search weights `workspace` documents at half their bm25 score. | `internal/api/search.go` |
| `mission.updated` notes list each changed field, old and new. | `internal/api/missions.go` |
| The journal shows `occurred_at` (parsed, with the stored time on hover); unmatched agents show `human operator` or a **not in crew** tag. | `internal/ui/missions.html` |

Rollout: deploy memoryd and mcpd (local redeploy rules: build to `.new`,
`mv`, `kickstart`; not while a triage run is open), then
`make mission-links ONLY=mission-harness APPLY=1`. Rehearsed on a copy of
the live database on 2026-10-14: 10 missions set, one stale mission, the
kirsch reconciler dry run unchanged.

## 3. Phase 1 — structure what the debriefs and reviews already say

Unblocks `MISSION-HUD-PLAN.md` phase 2 (decision D3): the template now has
fixed mandatory sections, and the grader already parses the criteria section
(`criteriaOutcome` in `grades.go`), so the parser exists in part.

The importer parses a `DEBRIEF.md` when it imports one and replaces that
mission's rows wholesale. A section it cannot read is skipped and counted,
never a sync failure.

| Table | Columns | From |
|---|---|---|
| `mission_criteria` | `mission_id, ord, criterion, result ('met','partial','unmet','unclear', NULL before a debrief), evidence` | Brief's Acceptance Criteria; debrief's Acceptance Criteria Outcome |
| `mission_deliverables` | `mission_id, path, note` | Deliverables Changed |
| `mission_lessons` | `mission_id, kind ('wrong-assumption','similar-mission','reuse-step'), text, correction` | Wrong Assumptions; Similar Future Missions |
| `agent_mistakes` | `mission_id, agent, mistake, impact ('low','med','high'), correction, prevention` | Sub-Agent Mistakes and Corrections |

Criteria are read only from `-` and `*` bullet lines; numbered lists (`1.`, `2.`) are not read and their section counts as skipped. The table is created via `db/schema.sql` `CREATE TABLE IF NOT EXISTS`, applied on every memoryd boot (not a separate migrate.go change).

Review outcomes are parsed when a step is written (and backfilled), into
`step_reviews(mission_id, step, reviewer, verdict ('approved','not-approved'),
critical, warnings, reviews_step, fix_round)`, from a reviewer step's notes or
summary. Free text is the fallback; see decision P3.

What it buys: first-pass approval rate per implementer, fix rounds per
mission, which mistakes recur per agent, "every wrong assumption about X",
and the files that change most often.

## 4. Phase 2 — cross-mission pages and operator actions

All pages stay single embedded HTML files with no build step, beside
`/triage`, `/missions` and `/performance`.

- **Crew** (`/crew`): one card per agent — steps run, first-pass approval
  rate, fix rounds caused, recurring mistakes by impact, service records, and
  the average grade of missions it worked on, each over time.
- **Library** (`/library`): patterns and directives with where they came
  from and, after phase 4, how often each is read and cited.
- **Activity**: one feed across every harness, newest first, filterable by
  harness and event; the in-progress missions at the top with their current
  step and time in step, refreshed by polling.
- **Trends** on `/performance`: grade, fix rounds and findings per week,
  per harness.
- **Global search**: missions, findings, patterns and directives in one box,
  with a type facet.
- **Filters in the URL** instead of local storage, so a view can be shared.
- **Operator actions on the HUD**, keeping the rule that agents never decide:
  - agree with or override a grade, with a note: new columns
    `mission_grades.operator_grade, operator_note, reviewed_by, reviewed_at`;
    the agreement rate is the grader's calibration signal, as accepted and
    declined recommendations are the processor's;
  - park or close a stale mission (a `PATCH` with a journal note);
  - an operator note on a mission: `mission_notes(id, mission_id, author,
    note, created_at)`.

## 5. Phase 3 — cost and usage

`agent_runs(id, harness, mission_id, step, agent, model, session_id,
input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
cost_usd, started_at, ended_at)` and `model_prices(model, input, output,
cache_read, cache_write, effective_from)`.

`scripts/import-usage.py` reads Claude Code transcripts
(`~/.claude/projects/<encoded checkout>/*.jsonl`, sub-agent runs included)
and OpenCode's session store, sums usage per sub-agent run, and matches a run
to a mission by checkout and time window and to a step by agent and time. It
stores counts only, never message content. In network mode it runs on the
harness machine and posts aggregates, as `push-memory.py` does for files.

What it buys: cost per mission, per grade letter, per agent and per harness;
kirsch against kirsch-oc becomes a like-for-like comparison of Claude Code
and OpenCode on one project.

## 6. Phase 4 — feed the memory forward

- **Retrieval log.** `retrieval_log(id, at, harness, actor, mission_id,
  tool, query, result_ids, result_count)`, written by the server for
  `memory_search`, `pattern_list` and `finding_list` calls; `mcpd` sends the
  harness and agent in headers. Answers whether memory is read at all, and
  whether a pattern was read before the mistake it describes recurred.
- **Brief prep.** `GET /v1/brief-prep?goal=&harness=` and an MCP tool
  `brief_prep` (harness profile): the most similar past missions by their
  briefs, their wrong assumptions and reuse checklists (phase 1), and the
  directives and patterns that match. Generalises the roadmap's
  `/findings-digest` idea. Measured by comparing grades and fix rounds of
  missions briefed with and without it.
- **Directive citations.** `directive_refs(directive_id, mission_id, step,
  source)` from `DIR-NNN` mentions in step notes, reviews and debriefs:
  which directives come up, and which never do (candidates to retire).
- **Git deliverables.** `mission_commits(mission_id, repo, sha, branch,
  files_changed, committed_at)`, collected by the reconciler from the
  checkout between a mission's open and close; flags a mission closed with
  uncommitted deliverables.
- **Operator interventions.** A journal event `operator.decision` (re-scope,
  extra fix round, deferral) written by the harness, counted per harness as
  a measure of how independently it works.

Semantic (embedding) search is left out: at 600 documents FTS5 with the
phase 0 weighting is adequate; revisit if `retrieval_log` shows searches
returning nothing useful.

## 7. Decisions (open)

- **P1. Un-defer debrief parsing (D3).** Settled yes by the operator on 2026-10-14; `mission_criteria` shipped (merged c325080, live). All 66 imported
  debriefs, across ten harnesses, carry the Acceptance Criteria Outcome,
  Wrong Assumptions, Sub-Agent Mistakes and Deliverables Changed sections;
  65 carry Similar Future Missions.
- **P2. Usage granularity.** Per sub-agent run (recommended) or per message.
  Per run is enough for every question in §5 and keeps the table small.
- **P3. Review verdicts.** Parse free text, or have the harness reviewer
  write a fixed line (`Verdict: NOT APPROVED; critical=1; warnings=4`).
  Recommended: ask the harness for the fixed line and parse free text as the
  fallback for history.
- **P4. Stale threshold.** 48 hours, fixed in code (built). Make it a
  setting only if missions start running longer than a day between syncs.
- **P5. Agent names.** Ask the harnesses to write crew names on steps
  (`@vasquez`, not `senior (general-purpose)`), or add an alias table.
  Recommended: the harness fix; an alias table hides drift.

## 8. Risks

- **Parsing agent-written Markdown.** Format drift breaks parsers silently.
  Every parser counts what it skipped and the sync reports it.
- **Transcript locations and formats** belong to Claude Code and OpenCode and
  can change. The importer is a script outside the service, so a change
  breaks one script, not memoryd.
- **Triage.** Phases 1 and 4 add tables the triage agents do not read, and
  the retrieval log is written alongside their reads; nothing changes what
  they call or receive. Search order changed in phase 0, and the processor
  uses `memory_search`; watch its next runs.
- **Retrieval log volume.** One row per read call; prune after 90 days if it
  grows.

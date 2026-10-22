# Memory Insight — Plan

**Status as of 2026-10-20:**

- **Phase 0, data health (§2): done.** Merged (ef23e11) and live.
- **Phase 1, structure what the debriefs and reviews say (§3): done** for
  slices 1 to 4 and stage 1b (c325080, 2347527, a53c1ac, 1e78378, 06e4f8a).
  Stage 1a, the fixed verdict line in the agent and skill definitions, is
  **partial**: the parser accepts the line, no definition carries it yet, and
  decision P3 awaits ratification. The "Found while building" candidates are
  **open**.
- **Phase 2, cross-mission pages and operator actions (§4): partial.** The
  Crew page is done (f0da6af) and the grade trend on `/performance` shipped
  (c83fdbf). Library, Activity, global search, operator grade override and
  operator notes are **not started**. Filters in the URL and park or close
  from the HUD are **unverified**.
- **Phase 3, cost and usage (§5): not started.**
- **Phase 4, feed the memory forward (§6): partial.** Brief prep is done
  (3112a24). The retrieval log, directive citations, git deliverables and
  operator interventions are **not started**.
- **Build order (§9):** stages 1 to 3 are done or partial as above; stages 4
  to 8 are **not started**.

Status: phase 0 (data health) merged to main (ef23e11) and live; phase 1 slice 1
(mission_criteria) merged to main (c325080) and live; phase 1 slice 2 (agent_mistakes)
merged to main (2347527) and live; phase 1 slice 3 (mission_deliverables) merged to main (a53c1ac) and live (2026-10-15); phase 1 slice 4 (mission_lessons) merged to main (1e78378) and live (2026-10-16); phase 1 stage 1b (review verdicts, parsed on read) merged to main (06e4f8a) and live (2026-10-16); stage 1a (verdict line) awaits ratification;
phase 2 partly built (the Crew page shipped), phases 3 and 4 not started; see
the status block above. Decisions P2, P3 and P5 settled by the operator on 2026-10-16; P4 built. Build order in §9.
Date: 2026-10-16.

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

*Status: done (ef23e11, 8426304; live).*

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

*Status: done for slices 1 to 4 and stage 1b; stage 1a partial; candidates open.*

Unblocks `MISSION-HUD-PLAN.md` phase 2 (decision D3): the template now has
fixed mandatory sections, and the grader already parses the criteria section
(`criteriaOutcome` in `grades.go`), so the parser exists in part.

The importer parses a `DEBRIEF.md` when it imports one and replaces that
mission's rows wholesale. A section it cannot read is skipped and counted,
never a sync failure.

| Table | Columns | From |
|---|---|---|
| `mission_criteria` | `mission_id, ord, criterion, result ('met','partial','unmet','unclear', NULL before a debrief), evidence` | Brief's Acceptance Criteria; debrief's Acceptance Criteria Outcome |
| `mission_deliverables` | `mission_id, ord, path (NULL when none), note` | Deliverables Changed |
| `mission_lessons` | `mission_id, kind ('wrong-assumption','similar-mission','reuse-step'), ord, text, why, correction` | Wrong Assumptions; Similar Future Missions |
| `agent_mistakes` | `mission_id, ord, agent, agent_raw, mistake, impact ('low','med','high'), impact_note, correction, prevention` | Sub-Agent Mistakes and Corrections |

Criteria and deliverables are read only from `-` and `*` bullet lines; numbered lists (`1.`, `2.`) are not read and their section counts as skipped. For deliverables, `path` comes from a leading backtick span, used as is, or a leading bold span, from which one trailing `:` is trimmed, one pair of surrounding backticks stripped, then one more trailing `:` trimmed; a result still containing a backtick is not a path, and either span must have no whitespace and contain `/` or `.`, otherwise `path` is NULL. Rows are replaced when a section with bullets is found; a section of only none-bullets deletes the rows; a missing heading or failed read keeps the rows. The tables are created via `db/schema.sql` `CREATE TABLE IF NOT EXISTS`, applied on every memoryd boot (not a separate migrate.go change).

Review outcomes are parsed on read, not stored: a reviewer step's notes (then
summary) in `mission_steps` give verdict ('approved', 'not-approved', or
unknown), critical and warning counts, the step reviewed and the fix round.
A fixed verdict line (decision P3) is read first; free text is the fallback
for history. No `step_reviews` table is kept, because `mission_steps` is
written by three paths (step upsert, step PATCH, summary update) and a stored
copy would drift from it; about 520 reviewer rows make a read-time parse
cheap.

What it buys: first-pass approval rate per implementer, fix rounds per
mission, which mistakes recur per agent, "every wrong assumption about X",
and the files that change most often.

### Found while building (candidates)

Each bullet below is a candidate only, not a commitment.

- **Multi-path bullets.** A bullet such as ``- `a.go`, `b.go` — note`` stores only the first path, and its note begins with `, \`b.go\``. That is 20 of 592 rows on a copy of the live database (2026-10-15). Options: one row per path, or strip the leading path list from the note.
- **Label-prefixed bullets.** Bullets such as `Tests: \`…\`` and `Docs: …` get a NULL path. That is 14 of the 82 NULL-path rows. Option: read paths after a leading `Label:`.
- **HUD page sections.** Shipped 2026-10-15 (3a0ba79): the mission page renders `deliverables` and `agent_mistakes` as sections before the debrief.
- **Commit-failure tests.** The commit-failure path of the replace transactions has no test for deliverables, mistakes or criteria (checked in internal/importer tests, 2026-10-15). A deferred foreign key in a test schema can force a COMMIT failure.
- **Parser edge cases, accepted for now.** Bullets inside fenced code blocks are read as rows. A bold span such as `**Done.**` or `**v1.2**` counts as a path because it contains a `.`. Neither occurs in the live data: a check of the staged copy on 2026-10-15 found no bullets inside fenced blocks and no such bold spans stored as paths.
- **Staging figures.** 592 rows over 77 missions, 82 with NULL path (14%), sync skipped 0 and errors 0 (copy of the live database, 2026-10-15).
- **Lessons shipped.** 2026-10-16 (1e78378): the mission page renders `lessons` as a Lessons section after Agent mistakes. Staging figures on a copy of the live database: 303 wrong-assumption, 81 similar-mission and 411 reuse-step rows over 84 missions; sync skipped 2, errors 0.
- **Lessons page guards untested.** The page test is a source-text check, so the `trig ?`, `w.why ?` and `w.correction ?` guards are not pinned; removing `trig ?` would break the page for a mission with steps but no trigger (mission-20260922-03 is one). Option: a JS-executing page test.

## 4. Phase 2 — cross-mission pages and operator actions

*Status: partial. See the bracketed status on each item below.*

All pages stay single embedded HTML files with no build step, beside
`/triage`, `/missions` and `/performance`.

- **Crew** *[done, f0da6af]* (`/crew`): one card per agent — steps run, first-pass approval
  rate, fix rounds caused, recurring mistakes by impact, service records, and
  the average grade of missions it worked on, each over time.
- **Library** *[not started]* (`/library`): patterns and directives with where they came
  from and, after phase 4, how often each is read and cited.
- **Activity** *[not started]*: one feed across every harness, newest first, filterable by
  harness and event; the in-progress missions at the top with their current
  step and time in step, refreshed by polling.
- **Trends** *[partial: grade trend shipped in c83fdbf; fix rounds and findings per week not started]* on `/performance`: grade, fix rounds and findings per week,
  per harness.
- **Global search** *[not started]*: missions, findings, patterns and directives in one box,
  with a type facet.
- **Filters in the URL** *[unverified]* instead of local storage, so a view can be shared.
- **Operator actions on the HUD** *[not started]*, keeping the rule that agents never decide:
  - agree with or override a grade *[not started]*, with a note: new columns
    `mission_grades.operator_grade, operator_note, reviewed_by, reviewed_at`;
    the agreement rate is the grader's calibration signal, as accepted and
    declined recommendations are the processor's;
    - an N/A (`excluded`) state, or an override with a note, for a test mission that should not count *[shipped: feat/grade-excluded, mission-20261022-05]*: settable by the operator only, labelled N/A on the HUD and the Performance page. The N/A state and its operator routes (`PUT` and `DELETE /v1/mission-grades/:missionID/exclusion`) are built; the override with a note is not started. The workaround of setting `mission_grades.state = 'insufficient'` and `grade = NULL` directly in the database is superseded by the route. Evidence for the need, from the code before the branch: averages, the grade distribution and the trend count only `state = 'graded'` (`internal/api/grades.go`, `performanceHandler`); the nightly claim does not pick up `insufficient` or `skipped` rows (`internal/api/grades.go`, claim query); the UI labelled such a row "not enough evidence" (`internal/ui/missions.html:687`, `internal/ui/performance.html:540`, at HEAD), not N/A; and the CHECK allowed only `pending`, `graded`, `insufficient` and `skipped` (`db/schema.sql`, `mission_grades`).
  - park or close a stale mission *[unverified in the UI]* (a `PATCH` with a journal note);
  - an operator note on a mission *[not started]*: `mission_notes(id, mission_id, author,
    note, created_at)`.

## 5. Phase 3 — cost and usage

*Status: not started.*

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

*Status: partial. Brief prep is done; the rest is not started.*

- **Retrieval log** *[not started]*. `retrieval_log(id, at, harness, actor, mission_id,
  tool, query, result_ids, result_count)`, written by the server for
  `memory_search`, `pattern_list` and `finding_list` calls; `mcpd` sends the
  harness and agent in headers. Answers whether memory is read at all, and
  whether a pattern was read before the mistake it describes recurred.
- **Brief prep** *[done, 3112a24]*. `GET /v1/brief-prep?goal=&harness=` and an MCP tool
  `brief_prep` (harness profile): the most similar past missions by their
  briefs, their wrong assumptions and reuse checklists (phase 1), and the
  directives and patterns that match. Generalises the roadmap's
  `/findings-digest` idea. Measured by comparing grades and fix rounds of
  missions briefed with and without it.
- **Directive citations** *[not started]*. `directive_refs(directive_id, mission_id, step,
  source)` from `DIR-NNN` mentions in step notes, reviews and debriefs:
  which directives come up, and which never do (candidates to retire).
- **Git deliverables** *[not started]*. `mission_commits(mission_id, repo, sha, branch,
  files_changed, committed_at)`, collected by the reconciler from the
  checkout between a mission's open and close; flags a mission closed with
  uncommitted deliverables.
- **Operator interventions** *[not started]*. A journal event `operator.decision` (re-scope,
  extra fix round, deferral) written by the harness, counted per harness as
  a measure of how independently it works.

Semantic (embedding) search is left out: at 600 documents FTS5 with the
phase 0 weighting is adequate; revisit if `retrieval_log` shows searches
returning nothing useful.

## 7. Decisions

- **P1. Un-defer debrief parsing (D3).** Settled yes by the operator on 2026-10-14; `mission_criteria` shipped (merged c325080, live). All 66 imported
  debriefs, across ten harnesses, carry the Acceptance Criteria Outcome,
  Wrong Assumptions, Sub-Agent Mistakes and Deliverables Changed sections;
  65 carry Similar Future Missions.
- **P2. Usage granularity.** Settled 2026-10-16: one row per sub-agent run.
  Per run is enough for every question in §5 and keeps the table small.
- **P3. Review verdicts.** Settled 2026-10-16: the reviewer writes a fixed
  line (`Verdict: NOT APPROVED; critical=1; warnings=4; reviews_step=3;
  fix_round=1`), and free text is parsed as the fallback for history. The
  line is proposed for ratification (FINDINGS 2026-10-16) before the agent
  and skill definitions change.
- **P4. Stale threshold.** 48 hours, fixed in code (built). Make it a
  setting only if missions start running longer than a day between syncs.
- **P5. Agent names.** Settled 2026-10-16: the harnesses write crew names on
  steps (`@vasquez`, not `senior (general-purpose)`); no alias table, which
  would hide drift.

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

## 9. Build order (agreed 2026-10-16)

Each stage ships on its own: exact-code briefs, a review after every coding
step, staging on a copy of the database, deploy, then the operator's HUD
test. One stage per mission; stage 1 is split in two.

1. **Review verdicts.** *[done: 1b (06e4f8a); 1a partial, awaiting ratification of P3]*
   - 1a. A fixed verdict line in the review output format (decision P3),
     drafted as a proposed finding for the operator to ratify; agent and
     skill definitions change only after ratification.
   - 1b. Review verdicts parsed on read from `mission_steps` (see §3) and
     shown on the mission page. Shipped 2026-10-16 (06e4f8a): on a copy of
     the live database, 526 reviews read as 290 approved, 185 not approved
     and 51 unknown (mostly reviews not run); none yet use the verdict line.
     Known gaps: "NOT APPROVE" and "NOT yet APPROVED" read as approved, and
     the page test checks source text, not rendering.
2. **Crew page** (`/crew`) *[done, f0da6af]*, with crew names on steps (decision P5): steps
   run, first-pass approval rate, fix rounds caused, recurring mistakes by
   impact, service records and average mission grade per agent. Shipped
   2026-10-17 (f0da6af). Live: 9 agents; hicks 399 steps (first pass 58 of
   110, 53%); vasquez 94 steps (22 of 29, 76%); lambert 232 steps (15 of
   20, 75%); apone 2 of 2; known gaps: page tests check source text, not
   rendering; "over time" trends deferred; whole tables read per request;
   three non-crew names visible (P5 drift).
3. **Brief prep** (§6) *[done, 3112a24]*: similar past missions, their wrong assumptions and
   reuse checklists, and matching directives and patterns, called by Bishop
   while framing a mission. Shipped 2026-10-17 (3112a24). Live: GET /v1/brief-prep
   returns 5 missions, 0 directives and 1 pattern for goal "crew page metrics"
   (top: mission-20261017-01); endpoint and MCP tool live (mcpd reinstalled 2026-10-17; sessions pick it up on MCP reconnect). Known gaps: directives scored on token match only, not
   on related rule semantics; hostile-goal test in brief_prep_test.go:597–600
   overstates which goals discriminate; the harness echo and the title, status, outcome and rank passthrough fields have no killing assertion.
4. **Retrieval log and directive citations** (§6) *[not started]*: whether memory is read,
   and which directives are cited or never come up.
5. **Operator actions on the HUD** (§4) *[not started]*: agree with or override a grade,
   park or close a stale mission, mission notes.
6. **Cross-harness views** (§4) *[not started]*: activity feed, trends on `/performance`,
   global search, filters in the URL, `/library`.
7. **Cost and usage** (§5) *[not started]*, one row per sub-agent run (decision P2).
   Independent of stages 2–6; it can move earlier.
8. **Git deliverables and operator interventions** (§6) *[not started]*.

Small fixes, taken by whichever stage touches the same code *[open]*:

- A page test that runs the mission page's JavaScript, so runtime guards
  such as the Lessons `trig ?` check are tested, not only present.
- Commit-failure tests for the replace transactions (see "Found while
  building").
- Deliverables: multi-path bullets and label-prefixed bullets (see "Found
  while building").
- The mutation harness keeps every failing line of a kill, not only the
  first (FINDINGS 2026-10-16).

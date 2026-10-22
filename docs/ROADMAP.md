# Roadmap — bishop-memory

The single list of outstanding work. Each item says where it stands and links
its plan where one exists. How the shipped system works is in the wiki
(`docs/wiki/`), not here. When an item ships, move it to [Shipped](#shipped)
with its commit or PR; git history keeps the detail. Item-by-item progress,
with evidence, is in [PROGRESS.md](PROGRESS.md).

Status words: **in progress**, **planned** (a plan exists, not started),
**open** (agreed, no plan yet), **deferred** (decided not now),
**decision** (waiting on the operator), **check** (may already be done;
confirm before working on it), **unverified** (the repository cannot confirm
it; a run or a harness-side check is needed), **accepted** (decided as it
stands, with no fix planned).

Last checked against the code: 2026-10-20 (status audit of `main` at
`1c4def9` and the live database). Statuses changed on 2026-10-21 by the live
redeploy and by operator decisions; they are recorded under Reconciler,
Harness portability and Shipped. Statuses changed on 2026-10-22 through PR #12
(squash-merge `7fc76ba`, the OpenCode v2 triage runner); they are recorded under
Findings triage and Shipped.

## Planned work

**Network deployment.** Run memoryd on any Unix server and reach it over the
network from harness machines, the triage agents and the operator's browser:
named API keys, transport (Tailscale, built-in TLS or a reverse proxy),
clients pushing memory files instead of the server reading paths, and a
one-step installer. Status: in progress. Phase 1 is built and on `main`; the
server installer has not been run for real on Linux or macOS, and TLS has not
been run with a real certificate. Phases 2 and 3 not started. Plan:
[plans/NETWORK-DEPLOYMENT-PLAN.md](plans/NETWORK-DEPLOYMENT-PLAN.md).

**Network deployment: the rest of the open items.** Each is also a line in
[PROGRESS.md](PROGRESS.md#network-deployment).

- Browser prompt on 401 and its Sign out control. Status: unverified; the HTML
  has not been inspected.
- The reconciler pushes files: with `--harness` the push is the default. Its
  fallback to server-side path sync (`POST /v1/documents/sync`) remains for
  runs without `--harness` and against an older memoryd. Status: partial.
- `--data-dir` is named in the plan but is not among the installer flags.
  Status: decision; change the plan or the scripts.
- Harness-side edits in each harness repository (plan §5.4). Status:
  unverified; outside this repository.
- Phase 2: backup timer set up by the installer, LaunchDaemon option on macOS,
  Homebrew formula and `.deb`, CI building release archives on tags. Status:
  planned.
- Phase 3: read-only keys, and an audit of key use in the flight recorder.
  Status: planned.

**Optional PostgreSQL backend.** Let the operator choose SQLite (the default)
or PostgreSQL with the same API, pages, tools and behaviour. Status: planned,
not started. Plan: [plans/POSTGRES-PLAN.md](plans/POSTGRES-PLAN.md).

**Memory insight.** Take the HUD from one mission at a time to questions
across missions: data health, structured debriefs and review verdicts, crew,
library and activity pages with operator grade review, cost and usage from
transcripts, and a retrieval log with a brief-prep tool that feeds past
lessons into new briefs. Status: phase 0, stages 1b, 2 and 3 shipped (see
[Shipped](#shipped)). Open: stage 1a (the fixed verdict line in the agent and
skill definitions, waiting on ratification of decision P3); phase 2's
Library, Activity, global search, filters in the URL, operator grade override
with a note (not started; the N/A state is shipped, see [Shipped](#shipped)),
operator notes, park or close from the HUD, and the rest of the Trends work;
phase 3 (cost
and usage) and phase 4 (retrieval log, directive citations, git deliverables,
operator interventions) not started. Plan:
[plans/MEMORY-INSIGHT-PLAN.md](plans/MEMORY-INSIGHT-PLAN.md).

**Mission HUD: outstanding items.** The HUD's own phases 1 and 3 and its
structured-debrief phase have shipped (see [Shipped](#shipped)). Still open
from [plan §8](plans/MISSION-HUD-PLAN.md#8-outstanding):

- **Browser check of the triage close controls** (look, focus-visible, pill
  fills). Verified by code reading only. Status: unverified.
- **The triage category rail is mouse-only.** No keyboard access. Status: open.
- **Pager focus edge cases** on the HUD pages. Status: open.
- **The `step-sync` note is not copied into the step summary** (plan §2.3).
  Status: unverified; not traced.
- **Live-system checks the repository cannot confirm.** The §2.4 backfill on
  the live database; the running memoryd process (the binary's sha1 matches
  `b15baa93`; the process has not been inspected); and the closure of triage
  run 89 on the live database. Status: unverified.

## Findings triage

The rest of phase 4 of the shipped design, plus what the earlier, unbuilt
design covered and the shipped one does not. Shipped items from this area are
listed under [Shipped](#shipped). Calibration on the last 20 decisions and
per-run accepted and declined counts are recorded as done in earlier notes,
but were not found in the code on 2026-10-20. Status: check, before relying
on either.

**Grade runs can exit 0 without a grade.** `scripts/triage-run.sh` exits 0
when a grade run ends with bishop-triage tool errors and writes no grade. The
2026-10-21 grade runs at 15:35, 17:21 and 18:00 UTC looked successful for this
reason. Source: mission-20261021-05 DEBRIEF (harness repository), and FINDINGS
2026-10-22 (`scripts/triage-run.sh`, grade success check). Status: open. The
operator wants it handled as a follow-up mission.

**The launchd triage jobs run the development checkout.** The jobs run
`/Users/djm56/bishop-memory`, so a `make mutate` run or an in-progress edit
can reach a scheduled run. Fix: a dedicated deploy checkout or worktree.
Source: FINDINGS 2026-10-22 (triage launchd jobs, deployment checkout). Status:
open.

**Live classify and process runs on the OpenCode v2 config shape are not
observed.** Only `make triage-grade` has run live on that shape (step 2k of
mission-20261021-05). Classify and process share the builder. Status:
unverified; the scheduled runs are the first check.

**The 1-second real-binary tests are timing-sensitive under load.** The tests
in `scripts/test_triage_run.py` use 1-second timeouts, and review 4b judged
them flaky under load. Source: review 4b, mission-20261021-05. Status: open,
low.

**Review follow-ups from mission-20261021-05.** Listed in its DEBRIEF, under
Tracker And Reality, Follow-ups (harness repository:
`.claude/memory/missions/mission-20261021-05/DEBRIEF.md`). Status: open, low.

**Triage run 99 is still `running`.** Started 2026-10-19 19:20:18 UTC on
`opencode-go/glm-5.2` (process) with an expired model key, so it is far past
the 30-minute stale rule. Status: decision. Confirm the key has been replaced,
then close the run as failed with the operator-close route
(`POST /v1/triage/runs/:id/operator-close`). Only the operator closes it.

**Pending recommendations never expire by age.** A recommendation is expired
only when a re-run replaces it. A sweep that expires pending ones older than N
days (the `expired` state exists) would keep the review page honest after a
long gap. Status: open.

**No acceptance rate per category over time.** The Runs tab shows accepted and
declined counts per run. Nothing yet shows the rate per category across runs,
which is the signal for a category description that needs tightening or a
model that is not good enough. The data is present: on 2026-10-20 the
recommendations were 252 accepted, 76 declined, 24 expired and 86 pending.
Status: open.

**Directive drafts are compared with ratified directives only loosely.** The
processor reads the harness's `DIRECTIVES.md` and drafts nothing when a
ratified directive already states the rule. It cannot propose an amendment,
a supersession or a duplicate for a draft, and it does not read the
`directives` table. The earlier design proposed four outcomes per draft: new,
amend, supersede, duplicate. The operator-side path (reopen a decided proposal
and ratify it as an amendment, or retire the directive) has shipped. Status:
open, partly done. This matters more as directives accumulate.

**`CONVENTIONS.md` is outside the pipeline.** The `CONV-` entries in the
Bishop harness's `reference/CONVENTIONS.md` are human-ratified rules, but they
are not in the `directives` table and the processor does not read them.
Whether to import them as directives is undecided. Status: decision.

**Patterns are not linked to findings.** The processor can search patterns
(`pattern_list`, `memory_search`), but nothing records which pattern already
answers a finding. Status: open.

**No one-step apply of an accepted `proposed_change`.** The review page offers
the text to copy; applying it to a skill or agent file is the operator's own
edit, as the harness doctrine requires. A `scripts/triage-apply.py` that
applies an accepted change and shows the diff was deliberately left out.
Status: deferred (decision 6 of the triage plan: no auto-apply).

**Taxonomy tuning.** After the first full run `class-closure` received no
findings and `brief-writing` received 70. On 2026-10-20 `brief-writing` holds
116 of 524 triaged findings (22%) and `class-closure` holds 1, so the skew
persists. Tighten the descriptions in `db/finding-categories.json`, or merge
categories, then re-classify. Status: decision, then re-classify. Check
against the current distribution on the Runs tab first.

**The exporter does not check for a mission in progress.** Running
`make triage-export` while a mission in that harness is mid-sync is safe from
loops but can interleave with the crew's own writes. The design had the page
warn when the harness's `CURRENT-MISSION.md` is `in-progress`; today the rule
is only written down. Status: open, low.

**Export conflicts are reported only by the exporter.** A conflict (file and
service disagree, both decided) is printed and gives exit 3; the review page
does not show it. Status: open, low.

**Sub-themes across runs and recurrence detection.** Groups are formed within
one processor run, so nothing carries a sub-theme from one run to the next or
notices a rule that keeps coming back. Neither design built this. Status:
open, idea only.

**A read-only `/findings-digest` command in the harness.** Lets Bishop see
ratified findings for the area it is about to brief. Status: open, harness
side; not checkable from this repository.

## Reconciler

**Reconcile behaviour (operator decision, 2026-10-21).** The journal
summary's buckets sum to `parsed`, with an `out of scope` bucket for events of a
mission the run does not know. Each mission with journal events gets a
`[flight-recorder] <id>:` line. A run whose only problem is journal events
skipped at the cap exits 3; a failed operation or fetch exits 1 and outranks it.
The step WARN is printed only after that mission's existing-steps fetch
succeeds. `JOURNAL_MAX_PAGES` stays 1000, and the known-id set is the ids
`parse_missions` returns. Status: decided, and shipped with the change that
carries this record (`reconcile_journal` and `main()` in
`scripts/reconcile-memory.py`).

**`normalize_empty` unescapes `\|` without a reason to.** It copies `clean()`,
which needs it for Markdown tables; a value from the JSON API does not. It is
applied to both sides and only for comparison, so it cannot cause a false
mismatch, but a `next_action` containing a literal `\|` is altered for no
reason. Status: open, low.

**A malformed non-string `next_action` from the API triggers a corrective
PATCH.** Self-healing in the normal case; it becomes a repeat-write loop only
if the service keeps returning a malformed value, which would be a service
defect. Status: open, watch only; unverified.

**Replaying FINDINGS.md into an empty database fails some status changes.**
Against a fresh database the reconciler's status mirroring sends decisions the
decision route refuses (HTTP 400): 38 of 263 kirsch findings on 2026-10-08,
for example a finding already `applied` in Markdown that the service has never
seen `approved`. A live database is unaffected because it saw each step. It
matters when moving to a new server: copy the database (Server-Install wiki
page), do not rebuild it by reconciling. Status: open, not reproduced on
2026-10-20; fix by replaying the transition path, or by an import route that
sets status directly.

## Harness side (bishop-harness repository)

Recorded here because they affect this service; the fixes belong in the
harness's `state-continuity.sh` hook and `connect-bishop-memory.sh`. None of
these can be checked from this repository.

**`stat -f '%m'` is BSD and macOS syntax.** On Linux the lock's mtime comes
back empty and stale-lock reclamation never fires. This matters directly to
running harnesses on Linux (see the network deployment plan). Status: open;
unverified here.

**The hook runs the reconciler from the working tree.** An edit to
`reconcile-memory.py` is live on the next memory-tree write, before the review
that would catch a defect in it, and a step briefed as dry-run-only cannot be
held to that while the hook is armed. Status: open; unverified here.

**The hook leaks its `mktemp` file when the subshell is signalled.** The TERM
trap releases only the lock; the older mirror section already uses trap-based
cleanup. Status: open; unverified here.

**One hook comment claims more than the code guarantees.** It says the
winning fire's post-reconcile check will pick up a marker touched after that
check ran. The invariant holds (that fire or the next answers it); the
sentence does not. Status: open; unverified here.

**The hook's function variables are globals.** POSIX `sh` has no locals. No
collision exists today, and no naming convention prevents one. Status: open,
low; unverified here.

**`SIGKILL` mid-reconcile leaves the lock directory** until the next
staleness check reclaims it. A delay, not a loss; step H is the backstop.
Status: accepted.

**Stderr is still suppressed during capability probing** in
`connect-bishop-memory.sh`. A missing interpreter is now diagnosed, but a
present-but-broken one could be masked. Status: open; unverified here.

## Service

**Rate limiting and lockout.** None today. The network deployment plan puts
lockout after repeated 401s in its phase 3. Status: planned.

**`POST /v1/documents/sync` accepts any absolute root except `/`.** There is
no allow-list of memory roots. The network plan replaces server-side reads
with clients pushing files (decision N3); if that lands, the `root` parameter
should be narrowed or removed. Status: planned with network deployment.

**Typed store helpers and the Markdown renderer.** `internal/store/missions.go`
and `internal/renderer/renderer.go` are deliberate placeholders: SQL stays
inline in the handlers, and the service does not write views back to the
harness tree. Either needs its own design first (see `CONTRIBUTING.md`).
Status: deferred.

**Global request body cap.** The 8 MiB cap covers the push route only; other
routes have no cap yet. Plan §3.4, phase 3. Status: planned.

## Harness portability

**Make bishop-memory installable into harnesses other than Bishop.** Plain
Claude Code, OpenCode, pi and the operator's Kirsch harness, through a generic
core, a neutral integration contract and one adapter per harness. Bishop stays
the reference harness and must not break. Status: in progress. Phase 1 is on
`main` through PR #8 (`8437f32`), and the reconciler-bug part of phase 2 is on
`main` through PR #9 (`1c4def9`). The research and the coupling inventory are
done. The layout profile and every later phase are not started. Plan:
[plans/HARNESS-PORTABILITY-PLAN.md](plans/HARNESS-PORTABILITY-PLAN.md). Items
per phase are in [PROGRESS.md](PROGRESS.md#harness-portability).

- Phase 1, compatibility guard: golden tests on the shipped fixture, and a seam
  for the Bishop values. Status: shipped, PR #8 (`8437f32`); listed under Shipped.
- Phase 2, layout profile: the memory layout moves into a profile, and the
  mission-ID mismatch is fixed. Status: the layout profile is not started. The
  reconciler-bug part (exact-match mission-ID links and the unknown-status
  warning) shipped in PR #9 (`1c4def9`); listed under Shipped.
- Phase 3, integration contract and generic names. Status: planned.
- Phase 4, plain Claude Code adapter. Status: planned.
- Phase 5, OpenCode adapter. Status: planned.
- Phase 6, pi adapter (pi is now maintained by Earendil and its MCP stance has
  reversed; see the plan's §3.3). Status: planned.
- Phase 7, Kirsch adapter. Status: deferred by the operator on 2026-10-21
  (plan §9); the K-questions in plan §11 wait with it.
- Phase 8, packaging and distribution. Status: planned.

On 2026-10-21 the operator adopted the recommendations in plan §9 for Claude
Code, OpenCode and pi, and deferred Kirsch.

## Docs and README

Drift in the README and the docs index, found on 2026-10-20. Each is also a
line in [PROGRESS.md](PROGRESS.md#docsreadme). The fixes to the README, the
docs index, the HUD plan's §8 and the plans' status blocks are on `main`
through PR #7 (merge `250202c`) and are listed under
[Shipped](#shipped).

What is still open:

- The CHANGELOG has no entries for `mission_lessons`, `mission_deliverables`,
  `agent_mistakes`, review verdicts or brief prep, and no entry of its own for
  the Crew page.

## Before starting work

1. Read the harness's `.claude/bishop-memory.conf` for mode, URL and
   `BISHOP_MEMORY_HOME`.
2. Confirm the service answers: `curl -s <url>/healthz`.
3. In both repositories, `git log main..HEAD --oneline` shows work not yet on
   `main`.
4. See whether the derived copy is in step:
   `$BISHOP_MEMORY_HOME/scripts/reconcile-memory.py --root .claude/memory --harness <name> --dry-run`.

## Shipped

Listed by area, newest work first within each. Entries are commits on `main`,
except the operational ones, which name their evidence instead.

**Harness portability**

- Phase 2, reconciler-bug part: a mission-ID candidate links only on an exact
  match with a known mission id, and an unknown step status is warned and
  counted rather than dropped (PR #9, `1c4def9`).
- Phase 1, compatibility guard: golden tests for the importer's database state
  and the reconciler's dry-run output, the `internal/profile` seam, the
  `BISHOP_LAYOUT` and `APPROVER_DEFAULT` seam in the reconciler, and a CI step
  for the Python script tests (PR #8, `8437f32`).

**Reconciler**

- The `[flight-recorder]` summary sums to `parsed`: the buckets in `JOURNAL_SUMMED_BUCKETS` add up to it, an `out of scope` bucket names events of a mission the run does not know, and each mission with journal events gets a `[flight-recorder] <id>:` detail line. Shipped with the change that carries this entry (`scripts/reconcile-memory.py`: `reconcile_journal`, `main()`). Not run against the live service.
- Operational, no commit (live system, 2026-10-21, operator-reported): memoryd
  was redeployed at 06:35 UTC with the flight-recorder paging from PR #9
  (`1c4def9`). A full journal reconcile of the live service was clean, with 13
  rows added.
- The journal's `warned` counter goes up once per capped mission, and the cap
  has its own summary category, `skipped (journal cap)`, apart from fetch
  failures. The run exits 1 only when events were skipped at the cap (PR #9,
  `1c4def9`). **Superseded 2026-10-21** (see the Reconciler decision above):
  a run whose only problem is events skipped at the cap exits 3, and a failed
  operation or fetch exits 1.
- The journal is read to the end when the server pages: the reconciler follows
  `has_more` up to `JOURNAL_MAX_PAGES`, and a read still reporting `has_more`
  at the ceiling is a fetch failure. `GET /v1/flight-recorder` takes `offset`
  and returns `has_more` (PR #9, `1c4def9`). The live service was redeployed
  on 2026-10-21, as recorded above.

**Docs and README**

- Docs drift on the README, the docs index, the HUD plan's §8 and the plan
  status blocks corrected, on `main` through PR #7 (merge `250202c`). The
  item-by-item evidence is in [PROGRESS.md](PROGRESS.md#docsreadme).

**Mission HUD and structured debriefs**

- Operational, no commit (live system, 2026-10-20): the stray `bin/memoryd.new`
  is removed, with an identical rollback kept at
  `~/.local/libexec/bishop-memory/memoryd.pre-mission-20261018-03` (removed in
  mission-20261020-02 step 1; the rollback is verified identical to the removed
  file, both sha1 `1f9e921b143981614691705920ce60e44d2490c5`). `mission-20261009-02` is closed complete/failed
  (abandoned by operator) via PATCH on 2026-10-20; a live read confirms the
  state, and the PATCH method is operator-reported.
- Mission HUD phase 2, structured debriefs: `mission_criteria` (c325080),
  `agent_mistakes` (2347527), `mission_deliverables` (a53c1ac),
  `mission_lessons` (1e78378). Review verdicts parsed on read (06e4f8a).
- Mission HUD phases 1 and 3: documents carry `mission_id` and `harness`,
  sync covers every harness, the `/missions` page and the `/v1/hud` endpoints
  (af53c3d), crew rows from agent definitions, `make scratch-clean`.
- Mission HUD merged to `main` through PR #5 (6772e85): the page redesign with
  the shared shell, and the triage close-run controls.
- MU/TH/UR green-terminal design across the four pages: Triage, Missions,
  Performance and Crew (942b52d, 1d9b01a).

**Memory insight**

- Phase 0, data health (ef23e11, 8426304): missions record their harness, the
  stale board (48 hours), workspace scratch ranked lower in search,
  field-level `mission.updated` notes, `occurred_at` on the journal.
- Stage 1b, review verdicts parsed on read (06e4f8a).
- Stage 2, Crew page and `GET /v1/crew` (f0da6af).
- Stage 3, brief prep: `GET /v1/brief-prep` and the `brief_prep` MCP tool
  (3112a24).

**Mission grading and performance**

- Operator N/A for missions that should not count: `mission_grades.state`
  admits `excluded`, set and lifted by the operator's `PUT` and `DELETE` on
  `/v1/mission-grades/:missionID/exclusion`; the grader skips such missions;
  N/A on the HUD and the Performance page (merged from branch
  feat/grade-excluded, mission-20261022-05).
- Mission grading: `mission_grades`, the `mission-grader` agent, the
  `/performance` page, `make triage-grade` (c83fdbf, merged in ae14630).
  Grader packets as readable text (5538d6d).

**Findings triage**

- Triage runner on OpenCode v2 (PR #12, squash-merge `7fc76ba`, 2026-10-22).
  The guard lists agents with `opencode debug agents`, checks the definition
  file first and applies a last-match-wins allowlist; a check that cannot
  finish exits 4. Runs use `opencode run --standalone` without `--pure`, so
  operator plugins load. The MCP config uses the `mcp.servers` shape with
  `codemode: false`, which fixed tools that OpenCode 2.0.32 hid behind Code
  Mode; one live grade run confirmed it on 2026-10-22. `--help` works anywhere
  in the arguments, and the docs require OpenCode v2. Evidence: CHANGELOG
  `[Unreleased]`, Changed; mission-20261021-05 DEBRIEF.
- Stale triage-run detection and operator close, and directive proposal reopen
  with amendment (9756373).
- `export-decisions.py` amends and retires directives in place
  (`scripts/export-decisions.py`).
- OpenCode triage engine, `TRIAGE_ENGINE=opencode` (PR #4, 7943ac8; 54058ff).
- Classifier batch size 20 (PR #6, f9d8f1f; 2df2f46).
- `--reclassify` in `scripts/triage-run.sh`.
- Category taxonomy in `db/finding-categories.json`.

**Harness registry and tooling**

- Harness registry: `GET /v1/harnesses`, `PUT /v1/harnesses/:name`
  (`internal/api/harness_roots.go`).
- `scripts/rename-harness.py`, renaming a harness across every table (0967cbc).
- `MCPD_PROFILE=triage`, a separate MCP tool set (`cmd/mcpd/triage.go`).
- Documentation tooling: `make screenshots`, `make wiki-publish`,
  `scripts/gen-mcp-reference.py`.

**Service**

- Document push body limit of 8 MiB (`internal/api/documents_push.go`).
- Mission-scoped findings: a finding takes its harness's open mission
  (`internal/api/findings.go`).

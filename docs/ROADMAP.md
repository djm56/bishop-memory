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
`f9d8f1f` and the live database).

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
Library, Activity, global search, filters in the URL, operator grade override,
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
- **Stray `bin/memoryd.new`** (about 36 MB, gitignored) left in the checkout.
  Status: open; remove it.
- **`mission-20261009-02`** is still `not-started` in the live database and
  stale under the 48-hour rule. Abandon it or keep it. Status: decision.
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

**An unrecognised step status is dropped silently.** `parse_steps` in
`scripts/reconcile-memory.py` turns a status outside `pending`,
`in-progress`, `done`, `failed` into an empty string and removes it from the
payload, so the run reports `unchanged` while the database keeps a stale
value. Seen when a harness wrote the mission status `blocked` into a step row.
It should warn and count the row instead. Status: open (confirmed on the
2026-10-20 read).

**The `[flight-recorder]` summary does not add up to `parsed`.** Events for
missions outside the parsed set are skipped with no counter. Behind the
opt-in `--include-journal`. Status: open; code reading only, not run.

**The journal's `warned` counter is never incremented.** So the "at cap"
summary branch cannot run, and a mission skipped at the 100-row cap is
reported as a generic fetch failure. Behind `--include-journal`. Status: open
(confirmed on the 2026-10-20 read).

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

Plan in progress — see docs/plans/HARNESS-PORTABILITY-PLAN.md (to be written).

## Docs and README

Drift in the README and the docs index, found on 2026-10-20. Each is also a
line in [PROGRESS.md](PROGRESS.md#docsreadme). The items below are fixed on
`docs/roadmap-progress` and close when that branch merges to `main`.

- `docs/README.md` says phase 2 of the HUD is deferred; it has shipped.
- The README table list is stale against `db/schema.sql`.
- The README describes the HUD switch as three pages; Crew is the fourth.
- The README repository map omits `/crew` and `/performance`.
- The README configuration table omits `BISHOP_MEMORY_CA_FILE`.
- The README network section omits the push routes and `scripts/push-memory.py`.
- The HUD plan's §8 and the status blocks on the four plans (memory insight,
  HUD, network, PostgreSQL), dated 2026-10-20, are on `docs/roadmap-progress`
  only, not yet on `main`.
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

Listed by area, newest work first within each. Commits are on `main`.

**Mission HUD and structured debriefs**

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

- Mission grading: `mission_grades`, the `mission-grader` agent, the
  `/performance` page, `make triage-grade` (c83fdbf, merged in ae14630).
  Grader packets as readable text (5538d6d).

**Findings triage**

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

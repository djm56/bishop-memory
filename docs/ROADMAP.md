# Roadmap — bishop-memory

The single list of outstanding work. Each item says where it stands and links
its plan where one exists. How the shipped system works is in the wiki
(`docs/wiki/`), not here. Remove an item when it ships; git history keeps it.

Status words: **in progress**, **planned** (a plan exists, not started),
**open** (agreed, no plan yet), **deferred** (decided not now),
**decision** (waiting on the operator), **check** (may already be done;
confirm before working on it).

Last checked against the code: 2026-10-08.

## Planned work

**Network deployment.** Run memoryd on any Unix server and reach it over the
network from harness machines, the triage agents and the operator's browser:
named API keys, transport (Tailscale, built-in TLS or a reverse proxy),
clients pushing memory files instead of the server reading paths, and a
one-step installer. Status: in progress (phase 1). Plan:
[plans/NETWORK-DEPLOYMENT-PLAN.md](plans/NETWORK-DEPLOYMENT-PLAN.md).

**Optional PostgreSQL backend.** Let the operator choose SQLite (the default)
or PostgreSQL with the same API, pages, tools and behaviour. Status: planned.
Plan: [plans/POSTGRES-PLAN.md](plans/POSTGRES-PLAN.md).

**Mission HUD phase 2: structured debriefs.** Parse each `DEBRIEF.md` into
`mission_criteria`, `mission_deliverables` and `mission_lessons`, so the HUD
can filter on unmet criteria, wrong assumptions and files that change often.
Status: deferred (decision D3) until the debrief format settles. Plan:
[plans/MISSION-HUD-PLAN.md](plans/MISSION-HUD-PLAN.md) §3.

## Findings triage

The rest of phase 4 of the shipped design, plus what the earlier, unbuilt
design covered and the shipped one does not. Already done and not listed:
`--reclassify`, calibration on the last 20 decisions, per-run accepted and
declined counts.

**Pending recommendations never expire by age.** A recommendation is expired
only when a re-run replaces it. A sweep that expires pending ones older than N
days (the `expired` state exists) would keep the review page honest after a
long gap. Status: open.

**No acceptance rate per category over time.** The Runs tab shows accepted and
declined counts per run. Nothing yet shows the rate per category across runs,
which is the signal for a category description that needs tightening or a
model that is not good enough. Status: open.

**Directive drafts are compared with ratified directives only loosely.** The
processor now reads the harness's `DIRECTIVES.md` and drafts nothing when a
ratified directive already states the rule. It cannot propose an amendment
or a supersession, and it does not read the `directives` table. The earlier
design proposed four outcomes per draft: new, amend, supersede, duplicate.
Status: open, partly done. This matters more as directives accumulate.

**`CONVENTIONS.md` is outside the pipeline.** The 28 `CONV-` entries in the
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
findings and `brief-writing` received 70. Tighten the descriptions in
`db/finding-categories.json`, or merge categories, then re-classify. Status:
check against the current distribution on the Runs tab.

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
side.

## Reconciler

**An unrecognised step status is dropped silently.** `parse_steps` in
`scripts/reconcile-memory.py` turns a status outside `pending`,
`in-progress`, `done`, `failed` into an empty string and removes it from the
payload, so the run reports `unchanged` while the database keeps a stale
value. Seen when a harness wrote the mission status `blocked` into a step row.
It should warn and count the row instead. Status: open.

**The `[flight-recorder]` summary does not add up to `parsed`.** Events for
missions outside the parsed set are skipped with no counter. Behind the
opt-in `--include-journal`. Status: open.

**The journal's `warned` counter is never incremented.** So the "at cap"
summary branch cannot run, and a mission skipped at the 100-row cap is
reported as a generic fetch failure. Behind `--include-journal`. Status: open.

**`normalize_empty` unescapes `\|` without a reason to.** It copies `clean()`,
which needs it for Markdown tables; a value from the JSON API does not. It is
applied to both sides and only for comparison, so it cannot cause a false
mismatch, but a `next_action` containing a literal `\|` is altered for no
reason. Status: open, low.

**A malformed non-string `next_action` from the API triggers a corrective
PATCH.** Self-healing in the normal case; it becomes a repeat-write loop only
if the service keeps returning a malformed value, which would be a service
defect. Status: open, watch only.

**Replaying FINDINGS.md into an empty database fails some status changes.**
Against a fresh database the reconciler's status mirroring sends decisions the
decision route refuses (HTTP 400): 38 of 263 kirsch findings on 2026-10-08,
for example a finding already `applied` in Markdown that the service has never
seen `approved`. A live database is unaffected because it saw each step. It
matters when moving to a new server: copy the database (Server-Install wiki
page), do not rebuild it by reconciling. Status: open; fix by replaying the
transition path, or by an import route that sets status directly.

## Harness side (bishop-harness repository)

Recorded here because they affect this service; the fixes belong in the
harness's `state-continuity.sh` hook and `connect-bishop-memory.sh`.

**`stat -f '%m'` is BSD and macOS syntax.** On Linux the lock's mtime comes
back empty and stale-lock reclamation never fires. This matters directly to
running harnesses on Linux (see the network deployment plan). Status: open.

**The hook runs the reconciler from the working tree.** An edit to
`reconcile-memory.py` is live on the next memory-tree write, before the review
that would catch a defect in it, and a step briefed as dry-run-only cannot be
held to that while the hook is armed. Status: open.

**The hook leaks its `mktemp` file when the subshell is signalled.** The TERM
trap releases only the lock; the older mirror section already uses trap-based
cleanup. Status: open.

**One hook comment claims more than the code guarantees.** It says the
winning fire's post-reconcile check will pick up a marker touched after that
check ran. The invariant holds (that fire or the next answers it); the
sentence does not. Status: open.

**The hook's function variables are globals.** POSIX `sh` has no locals. No
collision exists today, and no naming convention prevents one. Status: open,
low.

**`SIGKILL` mid-reconcile leaves the lock directory** until the next
staleness check reclaims it. A delay, not a loss; step H is the backstop.
Status: accepted.

**Stderr is still suppressed during capability probing** in
`connect-bishop-memory.sh`. A missing interpreter is now diagnosed, but a
present-but-broken one could be masked. Status: open.

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

## Before starting work

1. Read the harness's `.claude/bishop-memory.conf` for mode, URL and
   `BISHOP_MEMORY_HOME`.
2. Confirm the service answers: `curl -s <url>/healthz`.
3. In both repositories, `git log main..HEAD --oneline` shows work not yet on
   `main`.
4. See whether the derived copy is in step:
   `$BISHOP_MEMORY_HOME/scripts/reconcile-memory.py --root .claude/memory --harness <name> --dry-run`.

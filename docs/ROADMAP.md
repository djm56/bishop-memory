# Roadmap — bishop-memory

## What Exists Now

Bishop-memory provides the HTTP service, the MCP adapter, the reconciler, and daemon installers for running the memory service as a managed system service. The service tracks missions, mission steps, findings, patterns, service records, and journal events in SQLite with an FTS5 search index.

A bishop-harness opts in through its own `.claude/bishop-memory.conf`; with no configuration file, or with `BISHOP_MEMORY_MODE=standalone`, the harness remains entirely self-contained and bishop-memory need not be installed or running. The harness generates its own `.mcp.json` from its configuration, keeping both the service and the harness self-contained and preventing reach from one into the other.

## How The Derived Copy Is Kept Current

State this precisely:

The harness's `state-continuity.sh` hook mirrors each new `FLIGHT-RECORDER.md` row as it is written, guarded by a cursor file so the same row is not re-posted and a file lock so two concurrent invocations cannot both post it. This keeps the journal (audit trail) current during normal operation.

The hook also mirrors mission steps live via the `POST /v1/missions/{id}/steps` endpoint, which uses upsert semantics keyed on `(mission_id, step)`. The first write returns 201; a repost of the same step returns 200. This allows steps to be mirrored while `in-progress` and updated to `done`, keeping the database current as steps run.

The closing sequence's step H runs the reconciler, which brings all structured tables — missions, mission steps, findings, patterns, and service records — into line with the Markdown as a backstop. A full reconcile measures approximately 0.2 seconds, well inside the hook's 10-second timeout.

## Outstanding Items

**The reconciler's `[flight-recorder]` summary does not sum to `parsed`.** Events for missions outside the parsed set are skipped with no counter accounting for them. Behind the opt-in `--include-journal`.

**A `warned` counter in the journal reconciliation is never incremented.** So its "at cap" summary branch is unreachable and a cap hit reports as a generic fetch failure. Also behind `--include-journal`.

**The hook leaks its `mktemp` temp file when the subshell is signalled.** Because the TERM trap releases only the lock. The file's older mirror section already uses trap-based cleanup for its own temp file and this does not reuse that convention.

**One hook comment claims a narrower guarantee than the code gives.** It asserts the winning fire's own post-reconcile check will pick up a marker touched after that check has already run. The higher-level invariant holds — either that fire answers it or the next does — but the sentence does not.

**The hook's function-scoped variables are globals.** POSIX `sh` having no locals. No collisions exist today and no naming convention guards against a future one.

**`stat -f '%m'` is BSD and macOS syntax.** On a Linux host the lock mtime comes back empty and stale-lock reclamation silently never triggers. This predates this mission and appears in the older mirror section too, but it matters directly to this project's cross-platform intent.

**`SIGKILL` cannot be trapped.** A hard kill mid-reconcile leaves the lock directory until the next staleness check reclaims it. A delay rather than a loss, with step H as backstop.

**The hook runs the reconciler from the working tree.** `state-continuity.sh` invokes `reconcile-memory.py` by path, so any edit to that script is in force on the next write under the memory tree — which a state-sync guarantees within seconds. Consequences: an edit reaches the live database before the review that would have caught a defect in it, and a step briefed as dry-run-only cannot be held to that while the hook is armed. Observed on this mission, where the change was applied unattended before its review completed.

**Decision recorded for clarity: the canonical value for an absent next action is the empty string.** It is the contract's own clearing mechanism under CONV-030, and the API deliberately has no path back to SQL `NULL` once a value is set. The literal `none` was considered and rejected as the canonical value — it is a non-empty string that the reconciler simultaneously treats as a placeholder, so a consumer unaware of its special meaning would display it as a real next action, reproducing the very problem the field had. Six historical rows were normalised once through the API, so the writes appear in the audit journal. New rows receive the empty string naturally, so this is not enforced by ongoing reconciler logic — the inconsistency was historical, and encoding a permanent check would leave the reconciler evaluating a condition only legacy data could satisfy.

**The `PLACEHOLDERS` set recognises `none` and `None` but not `NONE`, and widening it was declined.** This decision stands because the canonical stored value is the empty string, which leaves the case gap a narrow cosmetic exposure — a literal `NONE` typed into one bullet field, self-correcting at the next sync. Had `none` been chosen as the canonical value, `none` versus `NONE` would have become the difference between canonical absence and real data, one keystroke apart, and widening `PLACEHOLDERS` would have had to be revisited. This is a recorded dependency: a future reader who changes the canonical value knows this decision is downstream of it.

**A borrowed rationale in `normalize_empty`.** It unescapes `\|` because `clean()` does, but `clean()` needs that for the Markdown table format and a value arriving from a JSON API has no such requirement. Applied symmetrically to both sides of the comparison so it cannot introduce a false mismatch, and comparison-only rather than persisted — but a `next_action` containing a literal `\|` is altered for comparison purposes with no reason to be.

**A malformed non-string `next_action` from the API is corrective, not inert.** The guard stops it raising, but unequal types never compare equal, so it produces a PATCH sending the harness's proper string. Self-healing in the ordinary case; a genuine repeat-write loop only if the service keeps emitting a malformed value after being corrected, which would be a service-side defect.

**Decision recorded for clarity: the endpoint's `max=16` on `step` validates before trimming.** A label exceeding 16 characters only through surrounding whitespace is rejected. Left as it is, because changing it would make `step` the only field validated unlike its siblings, and it fails safe.

**Proposal for human ratification: control-flow and lock/loop changes should require comment rewrites.** Six findings on one mission were comments asserting a property the code did not have, across four different agents, which points at how changes are briefed rather than at any one agent.

**Two findings still await operator ratification:** one concerns guarding a sourced file on readability as well as existence (a present file that is not readable can appear missing and cause silent failure); the other concerns quoting code verbatim in briefs rather than paraphrasing (agent behaviours around code citation vary, and consistency strengthens clarity).

**The conf parser is shared and sourced by both consumers** — the MCP registration generator and the post-mission hook — which closes the drift risk between them.

**The hook's mirror does not attribute a harness.** Rows the hook posts carry the agent name from the Markdown, while rows written through the MCP tools compose `<harness>:<agent>`. The mission's own `harness` column makes it derivable, so this is a provenance design decision rather than a defect. Changing it would make new rows inconsistent with those already imported from earlier harnesses.

**Residual stderr suppression in the harness's generator remains necessary.** The `.claude/connect-bishop-memory.sh` generator suppresses stderr during capability probing. Capability probing now diagnoses a missing interpreter correctly, but a present-but-broken interpreter could still be masked by the suppression. The suppression was originally added to hide an error that no longer occurs, but the underlying risk persists.

**opencode support was removed,** along with both harness installers, so nothing in this repository patches another repository.

## How To Pick This Up

Before starting work:

1. Read the harness configuration to find the runtime environment. This is stored in `.claude/bishop-memory.conf` in the harness checkout; check it for mode, URL, and `BISHOP_MEMORY_HOME`.
2. Confirm the service answers. Run `curl -s <url>/healthz` substituting the URL from the config.
3. Check which branches are unmerged in both repositories. In the harness, run `git log main..HEAD --oneline` to see work not yet on main. In bishop-memory, run the same.
4. Run the reconciler with `--dry-run` to see whether the derived copy is currently in step. The command is: `$BISHOP_MEMORY_HOME/scripts/reconcile-memory.py --root .claude/memory --dry-run`, substituting `BISHOP_MEMORY_HOME` from the config.

# Roadmap — bishop-memory

## What Exists Now

Bishop-memory provides the HTTP service, the MCP adapter, the reconciler, and daemon installers for running the memory service as a managed system service. The service tracks missions, mission steps, findings, patterns, service records, and journal events in SQLite with an FTS5 search index.

A bishop-harness opts in through its own `.claude/bishop-memory.conf`; with no configuration file, or with `BISHOP_MEMORY_MODE=standalone`, the harness remains entirely self-contained and bishop-memory need not be installed or running. The harness generates its own `.mcp.json` from its configuration, keeping both the service and the harness self-contained and preventing reach from one into the other.

## How The Derived Copy Is Kept Current — And The Gap

State this precisely, because it is the heart of the roadmap:

The harness's `state-continuity.sh` hook mirrors each new `FLIGHT-RECORDER.md` row as it is written, guarded by a cursor file so the same row is not re-posted and a file lock so two concurrent invocations cannot both post it. This keeps the journal (audit trail) and the mission registry current during normal operation.

The closing sequence's step H runs the reconciler, which brings all structured tables — missions, mission steps, findings, patterns, and service records — into line with the Markdown. A full reconcile measures approximately 0.2 seconds, well inside the hook's 10-second timeout, so most of that work can also run live rather than only at close.

**The gap:** mission steps are currently excluded from live mirroring. They land in the database only at mission close, via the full reconcile. This is deliberate, not accidental.

## Why Steps Cannot Mirror Live Today

The reconciler's step dedupe key is `(mission_id, step)`, and status is not part of the key. A step mirrored while it is `in-progress` becomes a row in the database. Because there is no unique constraint on that pair, the same row can be written again — and is treated as already-present by dedupe, so it freezes at its original status while the Markdown continues to `done`. 

The API currently offers `POST` and `GET` for mission steps and no `PATCH`, `PUT`, or `DELETE`. There is no mechanism to update a step once written or to delete a stale one. Mirroring steps live would therefore be actively worse than the present arrangement, where they land at close with every status final and certain.

## What Would Need To Change

To enable live mission step mirroring, either approach works:

1. **Add a step update endpoint** — `PATCH /v1/missions/{id}/steps/{step}` to update a step in place.
2. **Add upsert semantics to the existing `POST`** — keyed on `(mission_id, step)`, so a reposted step overwrites the previous record rather than failing or being skipped.

Either choice requires a schema change on a populated table. The `mission_steps` table currently has no unique constraint on `(mission_id, step)`, so upsert semantics require adding one. This is a schema change under the additive-migration path in `internal/store/migrate.go`, not a table rewrite.

Note also: each step write appends its own `mission.step` audit row to the flight-recorder. If upsert semantics are added, consider whether an update of an already-present step should append a second audit row or leave the journal entry alone.

## The Interim Arrangement

The hook reconciles live with steps excluded. Mission close runs the full reconcile, including steps. This keeps live mirroring accurate at the cost of step records landing only at close. When a step update endpoint or upsert exists, the hook can be extended to mirror steps as they transition from `in-progress` to `done`, rather than holding all step writes until the closing sequence.

## Other Outstanding Items

**Two findings await operator ratification.** Findings are created `proposed` and only the human operator advances their status; there is no API write path for status changes. The findings are:

- One concerns guarding a sourced file on readability as well as existence. A present file that is not readable can appear to be missing and cause a silent failure.
- The other concerns quoting code verbatim in briefs rather than paraphrasing. Agent behaviours around code citation vary, and consistency strengthens clarity.

**A recorded proposal, not a finding.** The pattern of agents describing verification results instead of pasting them verbatim has been observed across more than one agent and more than one mission. This points at the brief template rather than any single agent. It is recorded as a proposal for operator ratification.

**An unapplied review suggestion.** A pre-check for sourced files using `sh -n` would catch syntax errors cheaply before sourcing. Not applied because the case proved recoverable on the tested shell, but it is the guard to reach for on a stricter interpreter.

**Residual stderr suppression** in the harness's `.mcp.json` generator. Capability probing now diagnoses a missing interpreter correctly, but a present-but-broken one could still be masked. The suppression was once necessary to hide a `KeyError` that now does not occur.

**The conf parser is shared; the readers are not enforced to agree.** The harness now has one parser in `.claude/lib/`, sourced by both consumers (the MCP registration generator and the post-mission hook), which closes the drift risk between them. Note it as done rather than outstanding — a reader of an older note may expect otherwise.

**The hook's mirror does not attribute a harness.** Rows the hook posts carry the agent name from the Markdown, while rows written through the MCP tools compose `<harness>:<agent>`. The mission's own `harness` column makes it derivable, so this is a provenance design decision rather than a defect. Changing it would make new rows inconsistent with those already imported from earlier harnesses.

**opencode support was removed**, along with both harness installers, so nothing in this repository patches another repository. Record it so a reader does not go looking for the feature or the removal path.

## How To Pick This Up

Before starting work:

1. Read the harness configuration to find the runtime environment. This is stored in `.claude/bishop-memory.conf` in the harness checkout; check it for mode, URL, and `BISHOP_MEMORY_HOME`.
2. Confirm the service answers. Run `curl -s <url>/healthz` substituting the URL from the config.
3. Check which branches are unmerged in both repositories. In the harness, run `git log main..HEAD --oneline` to see work not yet on main. In bishop-memory, run the same.
4. Run the reconciler with `--dry-run` to see whether the derived copy is currently in step. The command is: `$BISHOP_MEMORY_HOME/scripts/reconcile-memory.py --root .claude/memory --dry-run`, substituting `BISHOP_MEMORY_HOME` from the config.

# Developer: Harness Integration

What crosses the boundary between a Bishop harness and bishop-memory, and why the boundary is where it is. The full operator text is `docs/HARNESS-INTEGRATION.md` in the repository; this is the developer's summary.

## The switch lives in the harness

A harness opts in with one file, `.claude/bishop-memory.conf`:

```
BISHOP_MEMORY_MODE=central          # or standalone (the default when the file is absent)
BISHOP_MEMORY_URL=http://127.0.0.1:8787
BISHOP_HARNESS=kirsch               # unique per harness
BISHOP_MEMORY_HOME=/path/to/bishop-memory
```

`.claude/connect-bishop-memory.sh` in the harness renders `.mcp.json` (project scope) from it. The harness doctrine (`agents/bishop.md`, `skills/mission-lifecycle/SKILL.md`) branches on the file's presence and mode. Nothing in bishop-memory writes into a harness except the triage exporter, and nothing in a harness patches bishop-memory. Two consumers read the conf — the MCP registration and the hook — through one shared parser (`.claude/lib/bishop-memory-conf.sh`), so they cannot disagree about the harness's identity.

## Standalone versus central

| | Standalone | Central |
|---|---|---|
| Mission id | derived locally from folders and logs | `mission_allocate`, global sequence per UTC day |
| Journal and steps | Markdown only | mirrored live by `state-continuity.sh` |
| Mission close | Markdown only | step H runs `reconcile-bishop-memory.sh` → the reconciler |
| Service down | irrelevant | mission start is blocked, by design: a duplicate id in an append-only journal is worse than a delayed start |

## What crosses, in which direction

| Flow | Mechanism | Direction |
|---|---|---|
| Mission id | `mission_allocate` tool | service → harness |
| Journal rows, steps | hook posts each new row; `POST /v1/missions/:id/steps` upserts | harness → service |
| Findings, patterns, records, directives, missions | reconciler at close and on memory-tree writes | harness → service |
| Hand-set finding status | reconciler calls the decision route | harness → service |
| Operator decisions, ratified directives | `export-decisions.py` | service → harness (decision lines and appended entries only) |
| Documents for search | `documents_sync` / reconciler | harness → service |

## Identity

Rows written through `mcpd` carry `<harness>:<agent>` as the actor. Rows the hook mirrors carry the bare agent name from the Markdown. The mission's own `harness` column makes the harness derivable either way; this inconsistency is recorded as a provenance decision in `docs/ROADMAP.md`, not a defect.

## The hook, briefly

`state-continuity.sh` is a PostToolUse hook on Write/Edit. It validates the newest journal row's shape, mirrors it and the current step, and fires a detached reconcile under a `mkdir` lock with a pending marker so a burst of writes coalesces into at most two reconciles. It never blocks. It fires only on Claude Code tool writes, which is why the exporter's own writes cannot trigger it.

## Things a developer should not break

- `.mcp.json` must stay project-scope; user-scope would merge every harness into one identity.
- `--harness` on the reconciler has no environment fallback on purpose; a stale shell export would misattribute a whole run.
- The reconciler's `[findings]` natural key is what the exporter and backfill rely on; change it in one place (`reconcile-memory.py`) and the others follow through `triage_common`.
- The exporter touches three lines per entry and appends. Widening that is a design change, because the harness tree is git-excluded in at least one harness and the backup directory is the only history.

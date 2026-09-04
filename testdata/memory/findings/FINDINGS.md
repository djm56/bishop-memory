# Findings

The findings ledger: specific, observed findings, one entry per observation, each proposing a change to an agent, a skill, a tool, or a candidate directive. Findings may cite code, paths, and symbols.

Append-only. New entries go below the marker at the bottom of this file, after every entry already there. The marker never moves and nothing already written is displaced.

Status is human-controlled: `proposed` → `approved` → `applied`, with `rejected`, `retired`, and `superseded` as terminal branches. Approver and Date approved are set by a human only when status becomes approved. When a finding generalises, a human ratifies the broader rule into `.claude/memory/reference/DIRECTIVES.md` and the originating entry becomes `applied`.

<!-- Append new entries below this line -->

### 2026-09-04 — Fixture structure alignment
**Suggestion**: Keep fixture structures aligned with the actual harness memory tree to ensure importer tests remain representative.
**Rationale**: The old fixture used `tasks/<id>/` layout while the harness uses `missions/<id>/`. Keeping them in sync prevents regressions.
**Status**: proposed
**Approver**: —
**Date approved**: —

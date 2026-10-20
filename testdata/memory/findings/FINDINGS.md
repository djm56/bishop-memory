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

### 2026-10-09 — Plain mission id link
**Suggestion**: Link a finding to its mission when the Mission field holds a plain id.
**Rationale**: Control entry. The plain id is the one shape every reader already handles.
**Mission**: mission-20261009-10
**Status**: proposed
**Approver**: —
**Date approved**: —

### 2026-10-09 — Three-digit counter link
**Suggestion**: Link a finding whose Mission field holds a three-digit counter to that mission.
**Rationale**: Counters past 99 occur. The -100 mission must not be linked to the -10 mission.
**Mission**: mission-20261009-100
**Status**: proposed
**Approver**: —
**Date approved**: —

### 2026-10-09 — Retry suffix link
**Suggestion**: Link a finding whose Mission field names a retry to the retry's own id.
**Rationale**: A retry is a distinct mission. The retry id must not be linked to the original mission.
**Mission**: mission-20261009-02-retry
**Status**: proposed
**Approver**: —
**Date approved**: —

### 2026-10-09 — Harness-qualified id link
**Suggestion**: Link a finding whose Mission field holds a harness-qualified id to that mission.
**Rationale**: Harness-qualified ids exist in the archive. The anomaly mission must be linked, not dropped.
**Mission**: mission-20260907-anom-01
**Status**: proposed
**Approver**: —
**Date approved**: —

### 2026-10-09 — Trailing text link
**Suggestion**: Link a finding whose Mission field carries trailing text to the id before the text.
**Rationale**: Control entry for trailing text. The id ends at the space, so the link is to -02.
**Mission**: mission-20261009-02 (retry)
**Status**: proposed
**Approver**: —
**Date approved**: —

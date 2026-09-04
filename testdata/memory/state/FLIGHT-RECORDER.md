# Flight Recorder

Append-only audit journal. One row per state-sync and per completion event. Never edit or reorder existing rows; never rewrite the header. Timestamps are `YYYY-MM-DD HH:MM UTC` and are never invented. Escape every literal `|` in the Note as `\|`, and read each appended row back before reporting the sync done.

| Timestamp | Mission ID | Step | Agent | Event | Note |
|-----------|-----------|------|-------|-------|------|
| 2026-09-04 05:12 UTC | mission-20260904-01 | 1 | @hicks | step-sync | Initial setup and config loaded |
| 2026-09-04 05:45 UTC | mission-20260904-01 | 2 | @apone | step-sync | Review of step 1 — APPROVED, no CRITICAL |
| 2026-09-04 06:09 UTC | mission-20260904-01 | — | @bishop | complete | Mission closed done — fixture structure aligned with harness memory tree |

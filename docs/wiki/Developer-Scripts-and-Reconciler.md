# Developer: Scripts and Reconciler

The Python and shell layer that moves data between a harness's Markdown and the service. All of it is operator-side: it runs from a terminal, a hook or launchd, never from inside an agent.

## `scripts/reconcile-memory.py`

**Direction:** Markdown → service, with one exception described below.

**Parsers** (`parse_missions`, `parse_steps`, `parse_journal`, `parse_findings`, `parse_patterns`, `parse_service_records`, `parse_directives`) read the harness tree: `state/MISSION-ARCHIVE.md`, `state/CURRENT-MISSION.md`, `missions/*/PROGRESS.md`, `state/FLIGHT-RECORDER.md`, `findings/FINDINGS.md`, `findings/PATTERNS.md`, `findings/service-records/*.md`, `reference/DIRECTIVES.md`. Shared helpers: `entries` splits a ledger on `### ` headings; `entry_heading`; `entry_field` pulls `**Name**: value` (multi-line) through `clean`, which collapses placeholders (`—`, `none`, `None`) to the empty string; `split_heading_date` accepts both `2026-09-02 — target` and `[2026-09-02] — target`; `truncate` applies the API's length caps; `directive_field` pulls the `- **Name:** value` bullet form.

**Natural keys** decide what already exists:

| Entity | Key |
|---|---|
| missions | `id` |
| mission_steps | `(mission_id, step)` |
| findings | `(finding_date, target, suggestion)` after truncation |
| patterns | `name` |
| service_records | `(agent, record_date, title)` |
| directives | `directive_id` |

**Reconcile functions** fetch the existing rows, index them by key and create what is missing. Missions and steps also PATCH drifted fields. Findings do two more things: the create payload carries `harness` (from `--harness`), and when the file's `**Status**` is not `proposed` and differs from the stored status, `POST /v1/findings/:id/decision` is called with the file's `Approver` and `Date approved` carried verbatim and the note `reconciled from FINDINGS.md`. That is the only way a hand edit in the file reaches the service, and it is one-directional: the reconciler never writes Markdown.

**Harness registration:** with `--harness`, every run first upserts `PUT /v1/harnesses/<name>` with the absolute `--root`. The exporter reads this table to find the file to write.

**Known gaps** (recorded in `docs/ROADMAP.md`): an unrecognised step status in `PROGRESS.md` is dropped silently; the journal cannot be enumerated past 100 rows per mission so `--include-journal` skips a mission at the cap.

**Exit codes:** 0 success, 1 any failed operation, 66 bad `--root`.

## `scripts/export-decisions.py`

**Direction:** service → Markdown, decisions only. The deliberate exception to "Markdown wins".

For a harness: `GET /v1/findings?harness=<name>`, keep rows with status ≠ `proposed`, index by natural key. Read `findings/FINDINGS.md`, split into preamble + entry blocks (`triage_common.split_entries`, byte-exact round trip), compute each block's key with the reconciler's parser (`block_key`). For a matching block:

- file status equals service status → skip;
- file status is not `proposed` and differs → **conflict**: report, skip, exit 3 at the end;
- otherwise rewrite the `**Status**`, `**Approver**`, `**Date approved**` lines (approver and date are written only for `approved`/`applied`; the others keep `—`) and append or replace one `**Disposition (triage):**` line.

Directives: `GET /v1/directive-proposals?state=accepted` filtered to this harness or no harness; for each `DIR-NNN` absent from `reference/DIRECTIVES.md`, render the entry (same text as the service's `renderDirectiveEntry`, with the Evidence field resolved to `[date] — target (+N)` ≤ 80 chars), insert it before `## Retired Entries`, add the index row at the end of the index table, refuse if over 1,510 characters or if the file lacks the index table or the retired heading.

Every write: backup to `<memory_root>/workspace/triage-backups/<file>.bak-<utc stamp>`, `atomic_write` (temp file + `os.replace`), re-read and compare.

## `scripts/triage-backfill-harness.py`

Parses every registered harness's `FINDINGS.md` into keys, lists all findings, and for each row with no harness sets it via `PUT /v1/findings/:id/harness` when exactly one harness owns the key. `--register NAME=/abs/path` upserts harnesses first for a fresh service. Reports unmatched and ambiguous rows and leaves them NULL.

## `scripts/triage-seed-categories.py`

Upserts `db/finding-categories.json` through `PUT /v1/finding-categories/:slug`. `--deactivate-missing` sets `active=false` on slugs absent from the file.

## `scripts/triage_common.py`

Loads `reconcile-memory.py` through `importlib` (hyphenated file name) and re-exports its parsers, so `natural_key`, `block_key` and `key_of_api_row` are guaranteed to agree with the reconciler's dedupe. Also `api()` (one JSON request), `split_entries`, `atomic_write`, `load_harnesses`, `die`.

## `scripts/triage-run.sh`

See [Developer: Triage Agents](Developer-Triage-Agents) for what it runs; mechanically it sources `.env`, checks `/healthz`, checks for work, builds `bin/mcpd` if absent, composes the `--mcp-config` JSON inline (so no file is written), opens every registered harness checkout with `--add-dir` for the processor, runs `claude -p` from the checkout root so project-scope agents resolve, and logs. With `TRIAGE_ENGINE=opencode` it composes `OPENCODE_CONFIG_CONTENT` instead (the same server, the step cap, `external_directory` allows for the checkouts), verifies the agent with `opencode debug agent`, and runs `opencode run --pure`. Every run gets stdin from `/dev/null` and a `TRIAGE_TIMEOUT_MIN` alarm. It is bash-3.2 compatible (no `mapfile`, no associative arrays) because macOS ships bash 3.2.

## Installers

`install-daemon.sh` (launchd) and `install-daemon-linux.sh` (systemd) build, stage and (re)load the service; `install-triage-schedule.sh` renders `com.bishop-memory.triage.plist` twice. All three are idempotent and have `--dry-run`. The macOS installers refuse or warn on paths under `/Volumes` because launchd cannot open logs or binaries there without a TCC grant nobody can answer.

## Conventions for new scripts

- Python 3 standard library only; no third-party packages, so a bare server can run them.
- Talk to the API, never to the database file. A one-off data fix goes through a route so it appears in the audit journal.
- `--dry-run` on anything that writes. Distinct exit codes for "precondition failed" versus "the work failed", and never reuse an exit code of a command you wrap.
- Reuse `triage_common` for anything that parses a ledger; do not re-implement the natural key.

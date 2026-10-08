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

**Recorded decisions.** An absent `next_action` is stored as the empty string, which is the API's own way of clearing a field; there is no path back to SQL `NULL` once a value is set. The literal `none` was rejected as the canonical value because it is a non-empty string the reconciler also treats as a placeholder, so a consumer unaware of that would show it as a real next action. `PLACEHOLDERS` recognises `none` and `None` but not `NONE`, and widening it was declined: with the empty string canonical, the gap is cosmetic and corrects itself at the next sync. That decision depends on the first one, so revisit both together. On the API side, `step`'s `max=16` is checked before trimming, so a label over 16 characters only because of surrounding whitespace is refused; it fails safe and is left alone.

**Flags worth knowing.** `--url` falls back to `BISHOP_MEMORY_URL`; `--harness` deliberately does not, because `BISHOP_HARNESS` may be exported in an operator's shell for something else and a stale value would attribute a whole run to the wrong harness. Without `--harness` a run is unattributed and registers no harness. `--include-journal` is off by default: the hook mirrors journal rows live, and `GET /v1/flight-recorder` caps at 100 rows with no offset, so the reconciler enumerates per mission and skips (with a warning) any mission at exactly 100 rows rather than guess and risk duplicates. `--skip-steps` is an escape hatch. `--no-sync-documents` skips the documents step that otherwise runs first so search is current in the same pass. URL and API key also come from `~/.config/bishop-memory/client.env` (`load_client_env`, at import, for any variable the environment does not set), and every request carries `api_headers()`. On an empty database the first run creates everything; later runs create what is missing and patch what drifted.

**The documents step.** With `--harness` (and not `--dry-run`), `push_memory` pushes the tree by content: `GET /v1/documents/hashes?harness=` for the server's hashes, then `POST /v1/documents/push` for every Markdown file whose SHA-256 differs (walked like the importer: hidden files and directories and symlinks skipped; files over 1 MiB skipped with a message), in batches of at most 200 files or 6 MiB, with the agent definitions in `<root>/../agents/` on the last batch. That works whether the service runs here or on another machine. Without `--harness`, or when the hashes route answers 404 (a server older than the client), it falls back to `POST /v1/documents/sync {"root": …}`, which only finds the files when the service shares this disk. The reconciler never prunes; `push-memory.py --prune` does.

**Exit codes:** 0 success, 1 any failed operation, 66 bad `--root`.

## `scripts/push-memory.py`

**Direction:** harness tree → service, by content. Loads `reconcile-memory.py` as a module and calls the same `push_memory`, after registering the harness with `--root`. For a first upload to a remote server, or to push without reconciling. `--prune` sends `POST /v1/documents/delete` for documents of this harness under this root whose files are gone, in chunks of 1,000.

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

## `scripts/backfill-mission-links.py`

**Direction:** database → database. One-off, for rows written before the mission HUD; see `docs/plans/MISSION-HUD-PLAN.md` §2.4.

Four parts in one `BEGIN IMMEDIATE` transaction, rolled back on a dry run: `rename_harnesses` (each `--rename-harness OLD=NEW` across `missions`, `findings`, `directive_proposals`, `documents`, then the `OLD` harnesses row is deleted); `delete_orphan_documents` (documents and their `documents_fts` rows whose `source_path` lies under no `harnesses.memory_root`); `backfill_steps` (`started_at` from the first `mission.step` event whose note says `(status: in-progress)`, `ended_at` from the last saying `done` or `failed`, only events before the mission closed; `summary` from the latest `step-sync` note); `link_findings` (debrief `[YYYY-MM-DD] — target` references in the **Findings and Patterns Linked** section, matched on date and a target prefix in either direction, then a time window `opened_at` to `closed_at` + 2 hours on `findings.created_at`). Every update has `AND <column> IS NULL`. A finding is linked only when exactly one candidate matches; `--verbose` lists the ambiguous ones.

`--apply` takes a backup through `sqlite3.Connection.backup` to `<db stem>.bak-<timestamp>.db` (the `.db` suffix keeps it under `.gitignore`), and exits 1 while any `triage_runs` row is `running`.

## `scripts/clean-scratch.py`

**Direction:** harness tree → Trash on this machine, and deletes from the service through the API. Run by hand only (`docs/plans/MISSION-HUD-PLAN.md` decision D2), on the machine that holds the harness checkouts.

Takes the harnesses from `GET /v1/harnesses` and skips any memory root that is not a directory here. For each (deduplicated by resolved workspace path), walks `<memory root>/workspace/` without following directory symlinks, skips hidden files and the top-level `README.md`, and takes files whose `mtime` is older than `--days`; the search-document count comes from `GET /v1/documents/hashes`. With `--apply` each file is moved to `<Trash>/bishop-scratch-<timestamp>/<harness>/<relative path>` (`~/.Trash` on macOS, `~/.local/share/Trash/files` elsewhere), then its documents (matched by `source_path`, or `source_path:<line>` for JSONL) and their search rows are deleted with `POST /v1/documents/delete` in chunks of 1,000, and empty directories are pruned. If the delete fails after the move, it exits 1 naming the `push-memory.py --prune` command that finishes the job.

`scripts/backfill-mission-links.py` is now the one exception to "talk to the API" below: it opens `data/memory.db` with `sqlite3` because no route bulk-fills nullable columns, and a busy timeout of 30 seconds lets it run beside `memoryd`. It therefore runs on the server.

## `scripts/triage-seed-categories.py`

Upserts `db/finding-categories.json` through `PUT /v1/finding-categories/:slug`. `--deactivate-missing` sets `active=false` on slugs absent from the file.

## `scripts/triage_common.py`

Loads `reconcile-memory.py` through `importlib` (hyphenated file name) and re-exports its parsers, so `natural_key`, `block_key` and `key_of_api_row` are guaranteed to agree with the reconciler's dedupe. Also `api()` (one JSON request), `split_entries`, `atomic_write`, `load_harnesses`, `die`.

## `scripts/triage-run.sh`

See [Developer: Triage Agents](Developer-Triage-Agents) for what it runs; mechanically it sources `.env`, then `~/.config/bishop-memory/client.env` for any variable neither set, writes the API key (if any) to a mode-600 header file that every `curl` reads with `-H @file`, checks `/healthz`, checks for work, builds `bin/mcpd` if absent, composes the `--mcp-config` JSON inline (so no file is written), opens every registered harness checkout with `--add-dir` for the processor, runs `claude -p` from the checkout root so project-scope agents resolve, and logs. With `TRIAGE_ENGINE=opencode` it composes `OPENCODE_CONFIG_CONTENT` instead (the same server, the step cap, `external_directory` allows for the checkouts), verifies the agent with `opencode debug agent`, and runs `opencode run --pure`. Every run gets stdin from `/dev/null` and a `TRIAGE_TIMEOUT_MIN` alarm. It is bash-3.2 compatible (no `mapfile`, no associative arrays) because macOS ships bash 3.2.

## Installers

`install.sh` is the front door for a server (`server`, which runs the platform installer and writes `memoryd.env` and the first key) and its clients (`client`, which writes `client.env`); see [Server Install](Server-Install). `install-daemon.sh` (launchd) and `install-daemon-linux.sh` (systemd) build (or use the shipped binary of a release archive), stage and (re)load the service; `install-triage-schedule.sh` renders `com.bishop-memory.triage.plist` twice. All three are idempotent and have `--dry-run`. The macOS installers refuse or warn on paths under `/Volumes` because launchd cannot open logs or binaries there without a TCC grant nobody can answer.

## Conventions for new scripts

- Python 3 standard library only; no third-party packages, so a bare server can run them.
- Talk to the API, never to the database file. A one-off data fix goes through a route so it appears in the audit journal.
- `--dry-run` on anything that writes. Distinct exit codes for "precondition failed" versus "the work failed", and never reuse an exit code of a command you wrap.
- Reuse `triage_common` for anything that parses a ledger; do not re-implement the natural key.

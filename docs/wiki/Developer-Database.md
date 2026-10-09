# Developer: Database

SQLite through `modernc.org/sqlite` (pure Go, no CGo). One file, WAL mode, foreign keys on, one open connection so writers serialise instead of hitting `database is locked`. The schema is `db/schema.sql`; additive changes go through `internal/store/migrate.go`.

## Boot sequence

`cmd/memoryd/main.go` runs, in order:

1. `store.Open` — DSN pragmas `busy_timeout(5000)`, `foreign_keys(1)`, `journal_mode(WAL)`; `SetMaxOpenConns(1)`; chmod the file to 0600.
2. `store.ApplySchema(db, "db/schema.sql")` — splits the file on top-level `;` (comment- and string-aware; no trigger bodies) and executes each statement. Everything is `CREATE … IF NOT EXISTS`, so this is a no-op on an existing database **and cannot add a column to an existing table**.
3. `store.EnsureColumns` — for each entry in `additiveColumns`, `ALTER TABLE … ADD COLUMN` if `PRAGMA table_info` lacks it; then each statement in `additiveIndexes`.
4. `store.EnsureMissionStepsIndex` — creates the unique index on `mission_steps(mission_id, step)` after checking for duplicates and producing an actionable error if any exist.

Consequence: a fresh database and an upgraded one end up identical, by different paths. `TestUpgradeFromPreTriageDatabase` in `internal/store/migrate_test.go` proves the upgrade path.

## Tables

### Harness vocabulary

| Table | Key | Notes |
|---|---|---|
| `missions` | `id` text (`mission-YYYYMMDD-NN`) | `status` CHECK not-started/in-progress/blocked/complete; `outcome` done/failed or NULL, set independently of `status` because the harness closes the status at one point and records the outcome later; `harness` additive; `closed_at` stamped on complete |
| `mission_steps` | autoincrement; unique `(mission_id, step)` | `step` is text so `3a` works; `status` CHECK pending/in-progress/done/failed; FK cascade; `started_at`/`ended_at` stamped by the steps route, `summary` copied from `step-sync` journal rows |
| `flight_recorder` | autoincrement | append-only by convention; `occurred_at` (caller time) separate from `created_at`; `agent` free text; FK to missions, nullable |
| `crew` | `name` unique | one row per agent, filled by document sync from each harness's `agents/*.md` (`importer.SyncAgents`); `name` lower-cased without `@`, so an agent defined in several harnesses is one row carrying the definition synced last; `role` is the description's first sentence; `description`, `source_path` additive |
| `findings` | autoincrement | `status` CHECK of six values; `harness`, `decision_note` additive; `mission_id` free text, no FK, defaulted to the harness's open mission on create; natural key `(finding_date, target, suggestion)` enforced by the reconciler, not the schema |
| `patterns` | autoincrement | natural key `name` |
| `service_records` | autoincrement | `source` CHECK self-reported/bishop-observed; natural key `(agent, record_date, title)` |
| `directives` | `directive_id` unique (`DIR-NNN`) | written by the reconciler and the proposal decision route |
| `documents` | `source_path` unique | `sha256` for incremental sync; `mission_id` (indexed) and `harness` additive, set by the importer |
| `documents_fts` | FTS5 virtual, standalone | `tokenize='porter unicode61'`; the importer writes both tables in one transaction; no triggers |

### Findings triage

| Table | Key | Notes |
|---|---|---|
| `harnesses` | `name` | `memory_root` absolute path; upserted by the reconciler |
| `finding_categories` | `slug` | description read by the classifier; `last_processed_at` is the rotation pointer |
| `triage_runs` | autoincrement | `kind` CHECK classify/process; `status` running/done/failed |
| `finding_triage` | `finding_id` (one row per finding) | FK cascade; `category` FK to categories |
| `finding_groups` | autoincrement | `category` FK |
| `finding_recommendations` | autoincrement; partial unique on `finding_id WHERE state='pending'` | `recommendation` CHECK approve/reject/supersede/defer; `state` pending/accepted/declined/expired |
| `directive_proposals` | autoincrement | six template fields; `evidence` JSON array of finding ids; `directive_id` once accepted |

## Mission links

Four additive columns and one index, in `additiveColumns` and `additiveIndexes`, back the mission HUD:

| Column | Set by | Meaning |
|---|---|---|
| `documents.mission_id` | the importer | `<id>` for `missions/<id>/BRIEF.md`, `PROGRESS.md` and `DEBRIEF.md` (exactly one directory under `missions/`); NULL otherwise |
| `documents.harness` | the importer | The harness whose memory root was synced; NULL for a root no harness is registered on. A sync with no harness leaves an existing value alone |
| `crew.description` | `importer.SyncAgents` | The agent definition's full `description` line |
| `crew.source_path` | `importer.SyncAgents` | The agent definition file |

`idx_documents_mission_id` indexes `documents(mission_id)`. An unchanged file (same hash) still gets its `mission_id`, `harness` and `kind` corrected on the next sync, so a re-sync backfills existing rows without rewriting the FTS index.

Document kinds are no longer polluted by hidden directories: the walker skips `.claude`, `.opencode`, `.git` and every other directory whose name starts with `.`, so a sync pointed at a repository instead of its memory root cannot file the repository under kinds like `.claude`. Rows imported that way before the change are removed by `scripts/backfill-mission-links.py` (orphan documents), which also fills `findings.mission_id` and the step columns for older rows.

## Documents and their kinds

`POST /v1/documents/sync` walks a memory root (`internal/importer`), imports every Markdown file whole and every JSONL file line by line (one document per line, kind `flight-recorder`, `source_path` `<file>:<line>`), and writes `documents` and `documents_fts` in one transaction. Each file's SHA-256 is stored; on a re-sync an unchanged hash is skipped and a changed one is updated in place with its FTS row deleted and re-inserted, so a sync is incremental and safe to repeat. `POST /v1/documents/push` imports files a client sends by content through the same function (`importer.ImportMarkdown`), with the client's absolute path as `source_path`, so a pushed file and a synced one land in the same row. Nothing deletes a document just because its file has gone; the deleters are `POST /v1/documents/delete` (used by `scripts/clean-scratch.py` and `scripts/push-memory.py --prune`) and the orphan pass of `scripts/backfill-mission-links.py`. `CURRENT-MISSION.md` gets its structured fields (mission id, status, owner, next action, last updated, blockers) copied into a block at the top of the body, so they are searchable as one block; the file itself follows unchanged.

`kindFromPath` in `importer.go` sets `kind` from the path relative to the root:

| Path | Kind |
|---|---|
| `state/CURRENT-MISSION.md`, `state/FLIGHT-RECORDER.md`, `state/MISSION-ARCHIVE.md` | `state` |
| `missions/<id>/BRIEF.md`, `PROGRESS.md`, `DEBRIEF.md` | `brief`, `progress`, `debrief` (exactly one directory under `missions/`) |
| `findings/FINDINGS.md`, `findings/PATTERNS.md` | `findings`, `patterns` |
| `findings/service-records/<name>.md` | `service-record` (exactly that depth) |
| `reference/DIRECTIVES.md` | `directives` |
| any other `<dir>/…` | the top-level directory name: `graph`, `reference`, `workspace`, `missions` for a deeper or bare mission file |
| a file at the root | `document` |

## Why triage state is not in `findings`

`findings.status` is a CHECK constraint. SQLite cannot widen a CHECK without rebuilding the table, and the harness doctrine makes status human-only. So model-written state lives in its own tables and the operator's decision route is the single writer of status. Adding a "pre-approved" value to the ledger would have broken both the migration story and the doctrine.

## Adding a column

1. Add it to the `CREATE TABLE` in `db/schema.sql` so a fresh database has it.
2. Add an entry to `additiveColumns` in `migrate.go`: table, column, definition. The definition must be valid for a populated table: nullable with no default, or a constant default. Never remove an entry once shipped.
3. If it needs an index, add the `CREATE INDEX IF NOT EXISTS` to `additiveIndexes`, **not** to `schema.sql`: `ApplySchema` runs first and would fail on a database that does not have the column yet. This exact failure is what `TestUpgradeFromPreTriageDatabase` guards.
4. Thread it through the model struct, the SELECT lists and the scanner.
5. Add it to the tables above, and to [Developer: HTTP API](Developer-HTTP-API) if a route without a tool returns it. A route with a tool is documented by re-running `scripts/gen-mcp-reference.py`.

## Adding a table

1. `CREATE TABLE IF NOT EXISTS` in `db/schema.sql`, with CHECK constraints for every enumerated column and FKs with explicit `ON DELETE`.
2. Add its name to `expectedSchemaObjects` in `internal/api/health.go`.
3. No triggers: the statement splitter does not parse `BEGIN … END`. Put the behaviour in the handler's transaction instead.

## Things that cannot change cheaply

Changing a CHECK, dropping a column, or making a nullable column NOT NULL all need a table rebuild and a data decision. There is no migration framework for that; if one becomes necessary, write it as a one-off, versioned script and record it in `CHANGELOG.md`, the way the `events.agent` migration was recorded.

## Inspecting by hand

```bash
sqlite3 data/memory.db '.tables'
sqlite3 data/memory.db 'PRAGMA table_info(findings)'
sqlite3 data/memory.db "SELECT status, COUNT(*) FROM findings GROUP BY status"
sqlite3 data/memory.db "SELECT t.category, COUNT(*) FROM finding_triage t GROUP BY 1 ORDER BY 2 DESC"
sqlite3 data/memory.db ".backup '/tmp/memory-backup.db'"     # safe while the daemon runs
```

One-off data fixes go through the API, not direct SQL, so they appear in the audit journal. The triage backfill route (`PUT /v1/findings/:id/harness`) exists for exactly that reason.

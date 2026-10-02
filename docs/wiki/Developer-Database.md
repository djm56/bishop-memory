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
| `missions` | `id` text (`mission-YYYYMMDD-NN`) | `status` CHECK not-started/in-progress/blocked/complete; `outcome` done/failed or NULL; `harness` additive; `closed_at` stamped on complete |
| `mission_steps` | autoincrement; unique `(mission_id, step)` | `step` is text so `3a` works; `status` CHECK pending/in-progress/done/failed; FK cascade |
| `flight_recorder` | autoincrement | append-only by convention; `occurred_at` (caller time) separate from `created_at`; `agent` free text; FK to missions, nullable |
| `crew` | `name` unique | registered agents; currently unused by handlers |
| `findings` | autoincrement | `status` CHECK of six values; `harness`, `decision_note` additive; natural key `(finding_date, target, suggestion)` enforced by the reconciler, not the schema |
| `patterns` | autoincrement | natural key `name` |
| `service_records` | autoincrement | `source` CHECK self-reported/bishop-observed; natural key `(agent, record_date, title)` |
| `directives` | `directive_id` unique (`DIR-NNN`) | written by the reconciler and the proposal decision route |
| `documents` | `source_path` unique | `sha256` for incremental sync |
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

## Why triage state is not in `findings`

`findings.status` is a CHECK constraint. SQLite cannot widen a CHECK without rebuilding the table, and the harness doctrine makes status human-only. So model-written state lives in its own tables and the operator's decision route is the single writer of status. Adding a "pre-approved" value to the ledger would have broken both the migration story and the doctrine.

## Adding a column

1. Add it to the `CREATE TABLE` in `db/schema.sql` so a fresh database has it.
2. Add an entry to `additiveColumns` in `migrate.go`: table, column, definition. The definition must be valid for a populated table: nullable with no default, or a constant default. Never remove an entry once shipped.
3. If it needs an index, add the `CREATE INDEX IF NOT EXISTS` to `additiveIndexes`, **not** to `schema.sql`: `ApplySchema` runs first and would fail on a database that does not have the column yet. This exact failure is what `TestUpgradeFromPreTriageDatabase` guards.
4. Thread it through the model struct, the SELECT lists and the scanner.
5. Add it to `docs/api-contract.md` and `docs/MEMORY-SETUP.md`.

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

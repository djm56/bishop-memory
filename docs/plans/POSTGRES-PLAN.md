# PostgreSQL Backend — Plan

Status: planned, not started.
Date: 2026-10-08.

The goal: let an operator choose SQLite (the default, as today) or PostgreSQL
as memoryd's database, with the same API, pages, MCP tools and behaviour on
both.

## 1. Recommendation: PostgreSQL only, not MySQL

Recorded decision **P1**. Support SQLite and PostgreSQL; do not add MySQL.

- **Full-text search.** PostgreSQL has `tsvector`, `ts_rank` and
  `ts_headline`, which map onto today's FTS5 search, ranking and snippets.
  MySQL's FULLTEXT has no snippet function and weaker query syntax.
- **Upserts.** The code uses `INSERT … ON CONFLICT … DO UPDATE` in eight places,
  and PostgreSQL has the same syntax. MySQL needs `ON DUPLICATE KEY UPDATE`,
  which behaves differently.
- **JSON.** The HUD reads directive evidence with `json_each`; PostgreSQL's
  `jsonb_array_elements_text` is a direct match.
- **Partial unique indexes** (one pending recommendation per finding) exist in
  PostgreSQL and not in MySQL.
- **Cost.** One more dialect doubles the test matrix. Two is manageable.

## 2. Why bother

SQLite is the right default: one file, no server, fast enough for this load.
PostgreSQL earns its place when:

- memoryd runs on a server that already runs PostgreSQL, with its backups,
  replication and monitoring;
- more than one memoryd process should share a database (a hot standby, or
  splitting the triage page from the API);
- the operator wants point-in-time recovery.

None of these is needed for a single server, so this plan comes after
[NETWORK-DEPLOYMENT-PLAN.md](NETWORK-DEPLOYMENT-PLAN.md).

## 3. What is SQLite-specific today

Counted in non-test Go code (about 9,400 lines) on 2026-10-08:

| Construct | Uses | Where | PostgreSQL equivalent |
|---|---|---|---|
| `?` placeholders | every query | everywhere | `$1, $2, …` — rebind at one choke point |
| `LastInsertId()` | 9 | findings, flight recorder, importer, patterns, service records, triage | `INSERT … RETURNING id` |
| FTS5 (`documents_fts`, `MATCH`, `bm25`, `snippet`, `rowid`) | ~30 | search, importer, HUD | `documents.search tsvector` (generated), GIN index, `websearch_to_tsquery`, `ts_rank`, `ts_headline` |
| `CURRENT_TIMESTAMP` stored as TEXT | 19 + schema defaults | handlers, schema | keep TEXT columns with a default of `to_char(now() at time zone 'utc', 'YYYY-MM-DD HH24:MI:SS')`, so every comparison and the JSON shape stay identical |
| `date(?)`, `instr`, `substr`, `json_each` | 7 | findings, HUD | `?::date`, `strpos`, `substr`, `jsonb_array_elements_text` |
| `PRAGMA` (WAL, foreign keys, table_info) | 5 | store | not needed; `information_schema.columns` for column checks |
| `modernc.org/sqlite` error codes (constraint, busy) | 12 | allocate, errors, missions, search | `pgconn.PgError` codes `23505` (unique), `23503` (foreign key) |
| `INTEGER PRIMARY KEY AUTOINCREMENT` | schema | schema | `BIGINT GENERATED ALWAYS AS IDENTITY` |
| `IS NOT ?` (null-safe compare) | 3 | importer | `IS DISTINCT FROM` |
| Single-writer connection pool (`SetMaxOpenConns(1)`) | store | store | a normal pool; transactions already wrap every multi-statement write |

Python scripts that open the SQLite file directly
(`backfill-mission-links.py`) stay SQLite-only. They are one-off tools.
After the network plan's phase 1, `clean-scratch.py` goes through the API.

## 4. Design

### 4.1 A dialect, not an ORM

- `internal/store/dialect.go` defines a `Dialect` interface. It has
  `Name()` and `Rebind(query)` (`?` → `$n`), helpers for
  `InsertReturningID`, `IsUniqueViolation`, `IsForeignKeyViolation`,
  `Now()`, `DateParam()` and `JSONArrayElements()`, and the search query
  builder.
- Handlers keep writing plain SQL with `?`. A thin wrapper around `*sql.DB`
  and `*sql.Tx` (`store.DB`) rebinds every query. That is the one choke
  point; no handler changes its SQL for placeholders.
- The few genuinely different statements, mostly search, call the dialect
  explicitly.
- Driver: `github.com/jackc/pgx/v5/stdlib`, which stays CGo-free like
  modernc SQLite.

### 4.2 Configuration

- `DB_DRIVER=sqlite|postgres` (default `sqlite`).
- `DB_PATH` for SQLite, as today.
- `DATABASE_URL=postgres://user:pass@host:5432/bishop_memory?sslmode=require`
  for PostgreSQL.
- `/healthz` reports `storage: sqlite|postgres`.

### 4.3 Schema and migrations

- `db/schema.sql` stays the SQLite schema. A new `db/schema.postgres.sql`
  holds the PostgreSQL one, with the same tables, columns, CHECKs and
  indexes. A test compares the two by column list and fails when they drift.
- Both are embedded in the binary.
- The additive column list in `internal/store/migrate.go` gets a definition
  per dialect, and column checks use `information_schema` on PostgreSQL.
- Replace "CREATE IF NOT EXISTS plus additive columns" with numbered
  migrations (`schema_migrations` table) at this point. Two dialects make the
  current approach too easy to get wrong. Recorded as decision **P2**.

### 4.4 Search

- SQLite: unchanged (FTS5).
- PostgreSQL: a `documents.search` column,
  `tsvector GENERATED ALWAYS AS (setweight(to_tsvector('english', coalesce(title,'')), 'A') || setweight(to_tsvector('english', body), 'B')) STORED`,
  with a GIN index. The query uses `websearch_to_tsquery('english', $1)`,
  ranks with `ts_rank`, and builds snippets with `ts_headline(…,
  'StartSel=<mark>, StopSel=</mark>')`.
- The importer no longer writes a separate FTS table on PostgreSQL; the
  generated column follows the body.
- Ranking and stemming differ slightly between the two. The API promises
  "ranked hits with a snippet", not identical ranks. A test checks that the
  same queries find the same documents on both.

### 4.5 Moving data

`memoryd migrate-db --from sqlite:data/memory.db --to postgres://…` copies
every table in dependency order inside one transaction, resets the identity
sequences to `max(id)+1`, rebuilds search, and compares row counts. It is
re-runnable into an empty database only.

### 4.6 Tests and CI

- Every store and API test runs against SQLite, as today.
- With `BISHOP_TEST_DATABASE_URL` set, the same tests also run against
  PostgreSQL, each in its own schema.
- CI adds a `postgres:16` service container and runs both.

## 5. Phases

1. The dialect wrapper and rebinding on SQLite alone, with no behaviour
   change. All tests stay green.
2. `InsertReturningID`, the error helpers, the date and JSON helpers, and
   numbered migrations.
3. The PostgreSQL schema, driver, configuration, and search.
4. The double test run and the CI service.
5. `migrate-db`, the docs (wiki Database and Server Install pages), and a
   `make` target.

Estimate: a focused week for phases 1–4. Most of the risk is in search and
in the migration runner.

## 6. Decisions

- **P1.** PostgreSQL only; no MySQL (§1).
- **P2.** Numbered migrations replace the additive-column list when the
  second dialect lands (§4.3).
- **P3.** Timestamps stay TEXT in UTC `YYYY-MM-DD HH:MM:SS` on both, so the
  JSON API does not change (§3).
- **P4.** SQLite stays the default and the recommended choice for a single
  server.

## 7. Risks

- **Search behaves differently.** Users may notice different ordering.
  Mitigated by the shared-results test and by documenting it.
- **Drift between two schema files.** Mitigated by the column-comparison test.
- **Hidden SQLite assumptions**, such as rowid order and type affinity
  letting a string compare as a number. The double test run is what finds
  these, which is why it is phase 4 and not optional.

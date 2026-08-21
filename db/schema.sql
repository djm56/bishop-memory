-- db/schema.sql — bishop-memory SQLite schema (Phase 2)
--
-- Tables described by the plan (see project_memory.md §"Updated
-- architecture" and §"Phase 2: Import and search"):
--
--   * tasks         — durable task records (the API's primary object)
--   * task_runs     — one row per agent execution attempt against a task
--   * events        — audit/activity log (task.*, agent.*, improvement.*);
--                      append-only BY APPLICATION CONVENTION ONLY, not
--                      enforced at the schema level — see the table's
--                      own comment below (Step 4 review, Review A W1)
--   * agents        — registered agents (for actor attribution on events)
--   * improvements  — durable improvement records (mirrors .claude/memory/improvements)
--   * documents     — imported Markdown/JSONL files
--   * documents_fts — FTS5 virtual table indexing documents for /v1/memory/search
--
-- All access goes through modernc.org/sqlite (CGo-free); the service runs
-- in WAL mode so reads do not block writes. ApplySchema
-- (internal/store/schema.go) executes this file once at startup; all
-- CREATE statements use IF NOT EXISTS so repeated boots are idempotent.

-- Journal mode is also set at runtime by store.Open (Phase 2 Step 3); the
-- duplicate pragma here keeps `sqlite3 data/memory.db < db/schema.sql`
-- (Makefile init-db target) producing the same on-disk layout as a
-- service boot.
PRAGMA journal_mode=WAL;

-- Enable foreign-key enforcement for this connection. SQLite ships with
-- FKs disabled by default per-connection; without this pragma the
-- ON DELETE CASCADE clauses on task_runs / events would silently no-op.
PRAGMA foreign_keys=ON;

-- tasks — durable task records.
--
-- Column order MUST stay aligned with the SELECT list in
-- internal/api/tasks.go (scanTask, listTasksHandler, getTaskHandler):
--
--     id, title, status, priority, next_action, blockers,
--     opened_at, closed_at, created_at, updated_at
--
-- Adding columns here requires updating those handlers; the INSERT
-- (id, title, status, priority, next_action, blockers) and UPDATE SET
-- list (status, priority, next_action, blockers, closed_at, updated_at)
-- are subsets of the columns below.
--
-- CHECK constraints mirror the oneof validators on
-- model.CreateTaskRequest / model.UpdateTaskRequest:
--   status   IN ('open','active','blocked','complete','cancelled')
--   priority IN ('low','normal','high','urgent')
CREATE TABLE IF NOT EXISTS tasks (
    id          TEXT    PRIMARY KEY,
    title       TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'open',
    priority    TEXT    NOT NULL DEFAULT 'normal',
    next_action TEXT,
    blockers    TEXT,
    opened_at   TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),
    closed_at   TEXT,
    created_at  TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),
    updated_at  TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),

    CHECK (status   IN ('open', 'active', 'blocked', 'complete', 'cancelled')),
    CHECK (priority IN ('low', 'normal', 'high', 'urgent'))
);

-- task_runs — one row per agent execution attempt against a task.
--
-- Inserted by createTaskRunHandler (relocated into internal/api/tasks.go
-- in Phase 2 Step 3). agent and status are intentionally free-form
-- (agents register dynamically; run status vocabulary will tighten once
-- the Phase 3 renderer lands).
CREATE TABLE IF NOT EXISTS task_runs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id    TEXT    NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent      TEXT,
    status     TEXT,
    started_at TEXT,
    ended_at   TEXT,
    summary    TEXT,
    created_at TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP)
);

-- events — audit log of task / agent / improvement activity.
--
-- "Append-only" describes application behaviour, not a schema
-- guarantee (Step 4 review, Review A WARNING 1): no handler in
-- internal/api currently exposes an UPDATE or DELETE against this
-- table, but nothing here — no CHECK, no trigger — would stop one from
-- a future code path or an ad hoc query. A true DB-level guarantee (a
-- BEFORE UPDATE/DELETE trigger that raises) is deliberately NOT added
-- here: it needs a trigger body with an embedded ";" inside
-- BEGIN...END, which internal/store/schema.go's splitSQLStatements
-- explicitly documents it does not handle — adding the trigger without
-- first extending the splitter would silently break schema bootstrap
-- on the next ApplySchema call. Recorded as a follow-up; not attempted
-- in this pass.
--
-- Inserted by every state-mutating handler (createTaskHandler,
-- updateTaskHandler, appendEventHandler, createTaskRunHandler). The
-- INSERT list in internal/api/tasks.go (task_id, event_type, summary)
-- is a subset of the columns below; id is autoincrement and created_at
-- defaults to CURRENT_TIMESTAMP.
--
-- agent (Phase 3) is the actor attribution for the event — populated
-- by the mcpd HTTP proxy as "<harness>:<sub-agent>" (e.g.
-- "opencode:orchestrator") and optional so non-agent / system callers
-- can still post events. task.created / task.updated / task.run events
-- emitted by the task handlers leave agent NULL — the agent identity
-- is captured indirectly via the surrounding event_append / task_run
-- calls the agent makes.
CREATE TABLE IF NOT EXISTS events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id    TEXT    REFERENCES tasks(id) ON DELETE CASCADE,
    event_type TEXT    NOT NULL,
    summary    TEXT    NOT NULL,
    agent      TEXT,
    created_at TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP)
);

-- agents — registered agents for actor attribution on events.
CREATE TABLE IF NOT EXISTS agents (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT,
    role       TEXT,
    created_at TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP)
);

-- improvements — durable improvement records.
CREATE TABLE IF NOT EXISTS improvements (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    title      TEXT,
    status     TEXT,
    body       TEXT,
    created_at TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP)
);

-- documents — imported Markdown / JSONL files from the agent's memory tree.
--
-- source_path is the post-Clean absolute path; UNIQUE so the importer
-- can upsert by path. sha256 is the hex-encoded content hash used to
-- skip unchanged files on re-sync (incremental import).
CREATE TABLE IF NOT EXISTS documents (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    source_path TEXT    NOT NULL UNIQUE,
    title       TEXT,
    kind        TEXT,
    body        TEXT    NOT NULL,
    sha256      TEXT    NOT NULL,
    imported_at TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),
    updated_at  TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP)
);

-- documents_fts — FTS5 search index over imported documents.
--
-- Design choice: STANDALONE FTS5 table (no external-content table, no
-- AFTER INSERT / UPDATE / DELETE triggers). The importer
-- (internal/importer/importer.go, Phase 2 Step 5) is the single writer
-- to both `documents` and `documents_fts`, and it inserts into both
-- tables in the same transaction — so an FTS5 update is always paired
-- with a documents row, and there is no need for trigger-driven sync.
--
-- Why not external-content + triggers?
--   * Triggers add a maintenance surface: silent breakage if a trigger
--     is dropped, harder to reason about during schema migrations.
--   * External-content tables are read-only by default; updates need
--     the special "INSERT INTO fts(fts) VALUES('rebuild')" dance.
--   * The importer is already in control of the documents lifecycle,
--     so the denormalised storage cost (one extra copy of title + body)
--     is a small price for much simpler SQL in the search handler.
--
-- tokenize='porter unicode61' gives English stemming plus Unicode-aware
-- tokenisation; agents are typically English-text but Markdown often
-- contains Unicode quotes / dashes that the default tokenizer mishandles.
CREATE VIRTUAL TABLE IF NOT EXISTS documents_fts USING fts5(
    title,
    body,
    tokenize = 'porter unicode61'
);

-- idx_events_task_id — supports the ?task_id= filter on
-- GET /v1/events and the task-scoped event lookups done by the task
-- handlers (Step 2 review warning #2: an index on events.task_id avoids
-- a full table scan for the common task-scoped read path).
--
-- Superseded in practice by idx_events_task_id_created_at below (a
-- composite index whose leading column also satisfies a task_id-only
-- lookup), but kept so a query that filters on task_id without the
-- ORDER BY still has a narrower, single-column index to use.
CREATE INDEX IF NOT EXISTS idx_events_task_id ON events(task_id);

-- idx_events_task_id_created_at — Step 4 review addition (Review A
-- WARNING 3): listEventsHandler filters WHERE task_id = ? and always
-- ORDER BY created_at DESC. The single-column index above supports the
-- filter but leaves the sort to a post-filter step; this composite
-- index lets SQLite satisfy both the filter and the ordering from the
-- index directly for the task-scoped case.
CREATE INDEX IF NOT EXISTS idx_events_task_id_created_at ON events(task_id, created_at DESC);

-- idx_tasks_status, idx_tasks_updated_at — Step 4 review addition
-- (Review A WARNING 3): listTasksHandler filters on ?status= and always
-- ORDER BY updated_at DESC; neither column was previously indexed. At
-- the service's stated scale (a local, single-operator memory service)
-- a full scan of `tasks` is very unlikely to matter in practice — this
-- is a low-cost, low-risk addition rather than a response to an
-- observed performance problem.
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_updated_at ON tasks(updated_at DESC);

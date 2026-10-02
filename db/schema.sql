-- db/schema.sql — bishop-memory SQLite schema (Phase 3)
--
-- Harness vocabulary: 10 objects replacing the legacy task/event model.
--
-- Objects created by this schema:
--   1. missions         — durable mission records
--   2. mission_steps    — PROGRESS.md rows (Step, Phase, Agent, Status, Notes)
--   3. flight_recorder  — audit log for the harness (append-only, separate occurred_at/created_at)
--   4. crew             — registered agents (for actor attribution)
--   5. findings         — improvement ledger (mirrors .claude/memory/findings/FINDINGS.md)
--   6. patterns         — advisory patterns (mirrors .claude/memory/findings/PATTERNS.md)
--   7. service_records  — per-agent service history
--   8. directives       — binding rules (never written by agents; mirrored from DIRECTIVES.md)
--   9. documents        — imported Markdown/JSONL files
--  10. documents_fts    — FTS5 virtual table indexing documents for /v1/memory/search
--
-- Findings triage objects (appended at the end of this file; see
-- docs/FINDINGS-TRIAGE.md): harnesses, finding_categories, triage_runs,
-- finding_triage, finding_groups, finding_recommendations, directive_proposals.
--
-- All access goes through modernc.org/sqlite (CGo-free); the service runs
-- in WAL mode so reads do not block writes. ApplySchema
-- (internal/store/schema.go) executes this file once at startup; all
-- CREATE statements use IF NOT EXISTS so repeated boots are idempotent.

-- Journal mode is also set at runtime by store.Open (Phase 3); the
-- duplicate pragma here keeps `sqlite3 data/memory.db < db/schema.sql`
-- (Makefile init-db target) producing the same on-disk layout as a
-- service boot.
PRAGMA journal_mode=WAL;

-- Enable foreign-key enforcement for this connection. SQLite ships with
-- FKs disabled by default per-connection; without this pragma the
-- ON DELETE CASCADE clauses on mission_steps / flight_recorder would silently no-op.
PRAGMA foreign_keys=ON;

-- missions — durable mission records (replaces legacy tasks).
--
-- Splits the old task.status into two fields:
--   status: a working state (not-started, in-progress, blocked, complete)
--   outcome: a terminal state (done, failed, null while in-progress)
--
-- owner mirrors the Owner field from CURRENT-MISSION.md.
-- priority is retained for mission prioritization (service-only metadata).
CREATE TABLE IF NOT EXISTS missions (
    id          TEXT    PRIMARY KEY,
    title       TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'not-started',
    outcome     TEXT,
    owner       TEXT,
    priority    TEXT    NOT NULL DEFAULT 'normal',
    next_action TEXT,
    blockers    TEXT,
    -- Which harness owns this mission. NULL on rows created before
    -- central allocation existed, and on any mission created by a
    -- harness running in standalone mode. Set by POST /v1/missions/allocate.
    harness     TEXT,
    opened_at   TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),
    closed_at   TEXT,
    created_at  TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),
    updated_at  TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),

    CHECK (status   IN ('not-started', 'in-progress', 'blocked', 'complete')),
    CHECK (outcome IS NULL OR outcome IN ('done', 'failed')),
    CHECK (priority IN ('low', 'normal', 'high', 'urgent'))
);

-- mission_steps — one row per entry in PROGRESS.md; merges the old task_runs
-- execution record with the row shape (Step, Phase, Agent, Status, Notes).
--
-- step is TEXT (not INTEGER) so injected labels like '3a' are representable.
-- status is NULL-safe so missing/omitted steps are distinct from an explicit status.
CREATE TABLE IF NOT EXISTS mission_steps (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    mission_id TEXT    NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
    step       TEXT,
    phase      TEXT,
    agent      TEXT,
    status     TEXT,
    notes      TEXT,
    started_at TEXT,
    ended_at   TEXT,
    summary    TEXT,
    created_at TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),

    CHECK (status IS NULL OR status IN ('pending', 'in-progress', 'done', 'failed'))
);

-- flight_recorder — audit log for the harness, append-only BY APPLICATION
-- CONVENTION ONLY (not enforced at schema level). No triggers exist because
-- internal/store/schema.go's splitSQLStatements cannot parse semicolons inside
-- BEGIN...END blocks, so adding a trigger would break schema bootstrap. Mirrors
-- the structure of FLIGHT-RECORDER.md with two discriminator kinds:
--
-- Journal rows (harness state machine):
--   event: 'step-sync', 'complete', 'blocked'
--   Validating this vocabulary is the API layer's job on the journal-append
--   path, not the database. mission_id, step, agent are populated from
--   PROGRESS.md context. note describes the state change.
--
-- Audit rows (service-internal handlers):
--   event: 'mission.created', 'mission.updated', 'mission.step'
--   These trace what the API handlers did and feed observability logs.
--   No CHECK over event values to avoid rejecting the handler audit trail.
--
-- occurred_at is the journal's caller-supplied event time (authoritative).
-- created_at is row insert time. Separating them makes a back-dated or
-- replayed row visible instead of indistinguishable.
CREATE TABLE IF NOT EXISTS flight_recorder (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    mission_id TEXT,
    step       TEXT,
    agent      TEXT,
    event      TEXT    NOT NULL,
    note       TEXT    NOT NULL,
    occurred_at TEXT,
    created_at TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),

    FOREIGN KEY (mission_id) REFERENCES missions(id) ON DELETE CASCADE
);

-- crew — registered agents for actor attribution on flight_recorder events.
-- Replaces the legacy agents table; same structure, new name.
CREATE TABLE IF NOT EXISTS crew (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    UNIQUE,
    role       TEXT,
    created_at TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP)
);

-- findings — improvement ledger (mirrors .claude/memory/findings/FINDINGS.md).
-- Replaces the legacy improvements table with a richer structure.
--
-- status belongs to the human operator alone — no agent ever sets or changes it.
-- Starts at 'proposed' when appended by an agent; status transitions are
-- human-driven only. The only write path for status is the operator decision
-- route POST /v1/findings/:id/decision, which no MCP profile registers.
--
-- harness and decision_note are additive columns applied by
-- internal/store/migrate.go (EnsureColumns) on databases created before they
-- existed; they are declared here so a fresh database has them from the start.
--
-- Vocabulary: proposed, approved, applied, rejected, retired, superseded
CREATE TABLE IF NOT EXISTS findings (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    finding_date  TEXT,
    target        TEXT,
    suggestion    TEXT    NOT NULL,
    rationale     TEXT,
    status        TEXT    NOT NULL DEFAULT 'proposed',
    approver      TEXT,
    date_approved TEXT,
    mission_id    TEXT,
    harness       TEXT,
    decision_note TEXT,
    created_at    TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),

    CHECK (status IN ('proposed', 'approved', 'applied', 'rejected', 'retired', 'superseded'))
);

-- patterns — advisory patterns (mirrors .claude/memory/findings/PATTERNS.md).
-- Non-binding; directives win on any conflict.
CREATE TABLE IF NOT EXISTS patterns (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    name             TEXT    NOT NULL,
    context          TEXT,
    solution         TEXT,
    example          TEXT,
    discovered_at    TEXT,
    discovered_mission TEXT,
    created_at       TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP)
);

-- service_records — per-agent service history.
-- Records observations about agent performance and behaviour.
--
-- source tracks the origin: 'self-reported' (from the agent's own
-- IMPROVEMENT-NOTE) or 'bishop-observed' (from Bishop-level observations).
CREATE TABLE IF NOT EXISTS service_records (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    agent       TEXT    NOT NULL,
    record_date TEXT,
    title       TEXT,
    note        TEXT,
    adjustment  TEXT,
    source      TEXT,
    created_at  TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),

    CHECK (source IS NULL OR source IN ('self-reported', 'bishop-observed'))
);

-- directives — binding, human-ratified rules.
-- No MCP profile registers a write tool for this table. The only writers are
-- the reconciler (PUT /v1/directives/:directive_id, mirroring DIRECTIVES.md)
-- and the operator's directive-proposal decision route. Agents read only.
--
-- directive_id is the natural key for a future sync mirror with
-- .claude/memory/reference/DIRECTIVES.md; UNIQUE so callers can safely
-- upsert by id on ingest.
CREATE TABLE IF NOT EXISTS directives (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    directive_id TEXT    UNIQUE,
    title        TEXT,
    rule         TEXT,
    rationale    TEXT,
    ratified_at  TEXT,
    created_at   TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP)
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
-- (internal/importer/importer.go) is the single writer to both `documents`
-- and `documents_fts`, and it inserts into both tables in the same
-- transaction — so an FTS5 update is always paired with a documents row,
-- and there is no need for trigger-driven sync.
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

-- Indexes supporting common query patterns.

-- idx_flight_recorder_mission_id — supports the ?mission_id= filter on
-- GET /v1/flight-recorder and the mission-scoped event lookups.
CREATE INDEX IF NOT EXISTS idx_flight_recorder_mission_id ON flight_recorder(mission_id);

-- idx_flight_recorder_mission_id_created_at — composite index supporting
-- mission_id filter + ORDER BY created_at DESC (most recent first).
CREATE INDEX IF NOT EXISTS idx_flight_recorder_mission_id_created_at ON flight_recorder(mission_id, created_at DESC);

-- idx_missions_status — supports the ?status= filter on GET /v1/missions.
CREATE INDEX IF NOT EXISTS idx_missions_status ON missions(status);

-- idx_missions_updated_at — supports ORDER BY updated_at DESC on mission lists.
CREATE INDEX IF NOT EXISTS idx_missions_updated_at ON missions(updated_at DESC);

-- idx_mission_steps_mission_id — supports the mission-scoped step lookups.
CREATE INDEX IF NOT EXISTS idx_mission_steps_mission_id ON mission_steps(mission_id);

-- idx_mission_steps_unique_key is NOT declared here. It is created by the
-- internal/store/migrate.go EnsureMissionStepsIndex function instead, which
-- must pre-check for duplicate (mission_id, step) pairs and produce an
-- actionable error before applying the constraint. See that function's docblock
-- for why this diverges from schema.sql's usual layout: cmd/memoryd calls
-- ApplySchema before EnsureMissionStepsIndex. A unique index declared in
-- schema.sql would fail at boot with a raw SQLite constraint error (if the
-- database held duplicates); the migration path allows a pre-check to produce
-- guidance before the constraint is applied.
--
-- Consequence: `sqlite3 data/memory.db < db/schema.sql` (Makefile init-db)
-- produces a schema lacking the unique index. That is intentional and expected:
-- the index is created only by cmd/memoryd at boot (via the migration path).
-- A subsequent cmd/memoryd boot creates the index and applies the constraint.

-- idx_findings_status — supports filtering findings by status.
CREATE INDEX IF NOT EXISTS idx_findings_status ON findings(status);

-- idx_service_records_agent — supports per-agent record lookups.
CREATE INDEX IF NOT EXISTS idx_service_records_agent ON service_records(agent);

-- ---------------------------------------------------------------------------
-- Findings triage (2026-10-02). Everything below is additive and lives beside
-- the ledger rather than inside it: findings.status stays the human-only
-- field it always was, and the model-written state (category, grouping,
-- recommendation, directive draft) sits in its own tables so no CHECK
-- constraint on the ledger has to change. See docs/FINDINGS-TRIAGE.md.
-- ---------------------------------------------------------------------------

-- harnesses — where each harness's Markdown memory tree lives. Upserted by
-- scripts/reconcile-memory.py on every run (it already knows --root and
-- --harness), so bishop-memory can write decisions back to the right
-- FINDINGS.md / DIRECTIVES.md without a config file of its own.
CREATE TABLE IF NOT EXISTS harnesses (
    name         TEXT    PRIMARY KEY,
    memory_root  TEXT    NOT NULL,
    last_seen_at TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP)
);

-- finding_categories — the taxonomy the classifier assigns from. Seeded from
-- db/finding-categories.json by scripts/triage-seed-categories.py; the
-- classifier reads descriptions from here at run time, never from its prompt.
-- last_processed_at is the rotation pointer the nightly processor uses to pick
-- the next category.
CREATE TABLE IF NOT EXISTS finding_categories (
    slug              TEXT    PRIMARY KEY,
    name              TEXT    NOT NULL,
    description       TEXT    NOT NULL,
    examples          TEXT,
    sort_order        INTEGER NOT NULL DEFAULT 0,
    active            INTEGER NOT NULL DEFAULT 1,
    last_processed_at TEXT,
    created_at        TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP)
);

-- triage_runs — one row per scheduled or manual classifier/processor run.
CREATE TABLE IF NOT EXISTS triage_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    kind        TEXT    NOT NULL,
    category    TEXT,
    model       TEXT,
    status      TEXT    NOT NULL DEFAULT 'running',
    considered  INTEGER NOT NULL DEFAULT 0,
    written     INTEGER NOT NULL DEFAULT 0,
    notes       TEXT,
    started_at  TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),
    finished_at TEXT,

    CHECK (kind IN ('classify', 'process')),
    CHECK (status IN ('running', 'done', 'failed'))
);

-- finding_triage — one row per classified finding. Re-classification
-- overwrites the row (upsert on finding_id).
CREATE TABLE IF NOT EXISTS finding_triage (
    finding_id          INTEGER PRIMARY KEY REFERENCES findings(id) ON DELETE CASCADE,
    category            TEXT    NOT NULL REFERENCES finding_categories(slug),
    secondary_category  TEXT    REFERENCES finding_categories(slug),
    directive_candidate INTEGER NOT NULL DEFAULT 0,
    confidence          REAL,
    summary             TEXT,
    classified_by       TEXT    NOT NULL,
    run_id              INTEGER REFERENCES triage_runs(id),
    classified_at       TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP)
);

-- finding_groups — a cluster of findings the processor judged to be instances
-- of one underlying rule.
CREATE TABLE IF NOT EXISTS finding_groups (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    category   TEXT    NOT NULL REFERENCES finding_categories(slug),
    title      TEXT    NOT NULL,
    summary    TEXT    NOT NULL,
    target     TEXT,
    run_id     INTEGER REFERENCES triage_runs(id),
    created_at TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP)
);

-- finding_recommendations — the processor's pre-decision on one finding.
-- state is the recommendation's own lifecycle; the finding's status is
-- changed only by POST /v1/findings/:id/decision, which also closes the
-- pending recommendation. The partial unique index keeps at most one
-- pending recommendation per finding.
CREATE TABLE IF NOT EXISTS finding_recommendations (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    finding_id      INTEGER NOT NULL REFERENCES findings(id) ON DELETE CASCADE,
    group_id        INTEGER REFERENCES finding_groups(id),
    recommendation  TEXT    NOT NULL,
    superseded_by   INTEGER REFERENCES findings(id),
    rationale       TEXT    NOT NULL,
    proposed_change TEXT,
    state           TEXT    NOT NULL DEFAULT 'pending',
    decided_by      TEXT,
    decided_at      TEXT,
    run_id          INTEGER REFERENCES triage_runs(id),
    created_at      TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),

    CHECK (recommendation IN ('approve', 'reject', 'supersede', 'defer')),
    CHECK (state IN ('pending', 'accepted', 'declined', 'expired'))
);

-- directive_proposals — a drafted DIRECTIVES.md entry awaiting ratification.
-- Fields mirror .claude/templates/reference/DIRECTIVES-TEMPLATE.md. evidence
-- is a JSON array of finding ids. directive_id is DIR-NNN once accepted.
CREATE TABLE IF NOT EXISTS directive_proposals (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    group_id       INTEGER REFERENCES finding_groups(id),
    harness        TEXT,
    title          TEXT    NOT NULL,
    applies_when   TEXT    NOT NULL,
    rule           TEXT    NOT NULL,
    rationale      TEXT    NOT NULL,
    reviewer_check TEXT    NOT NULL,
    example        TEXT,
    evidence       TEXT    NOT NULL,
    state          TEXT    NOT NULL DEFAULT 'pending',
    directive_id   TEXT,
    decided_by     TEXT,
    decided_at     TEXT,
    decision_note  TEXT,
    run_id         INTEGER REFERENCES triage_runs(id),
    created_at     TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),

    CHECK (state IN ('pending', 'accepted', 'declined', 'expired'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_recommendation_pending
    ON finding_recommendations(finding_id) WHERE state = 'pending';
CREATE INDEX IF NOT EXISTS idx_recommendations_group ON finding_recommendations(group_id);
CREATE INDEX IF NOT EXISTS idx_finding_triage_category ON finding_triage(category);

-- idx_findings_harness is NOT declared here. findings.harness is an additive
-- column applied by internal/store/migrate.go AFTER this file runs, and an
-- index on a column that does not exist yet fails the whole bootstrap on a
-- database created before the column. The index is created by EnsureColumns
-- once the column is guaranteed present.

# Developer Overview

Where things are, how the layers relate, and the rules that keep the service small. Each subsystem has its own page; this one is the map.

## Repository layout

```
cmd/memoryd/        the HTTP daemon: config, start-up guard, open database, apply schema, migrations, serve (HTTP or TLS),
                    graceful shutdown; commands.go has memoryd keys add|list|revoke and memoryd backup
cmd/mcpd/           the MCP adapter: main.go (harness profile), triage.go (triage profile); a pure HTTP client;
                    clientenv.go reads client.env and sends the API key
internal/api/       Gin router and handlers; SQL inline in handlers by design
internal/auth/      API keys: the hashed keys file, constant-time verification, add and revoke
internal/model/     request/response structs with binding tags
internal/store/     SQLite open (WAL, FKs, busy timeout, one writer), schema bootstrap, additive migrations
internal/importer/  Markdown/JSONL walker and FTS5 indexer behind POST /v1/documents/sync and /v1/documents/push
internal/middleware/ request id, access log, panic recovery, the API key check
internal/ui/        the embedded pages (triage.html, the review page; missions.html, the mission HUD) and their handlers
internal/renderer/  deliberate stub; the service does not render Markdown views
db/schema.sql       the schema, CREATE … IF NOT EXISTS throughout; embedded in memoryd by db/embed.go
db/finding-categories.json  the triage taxonomy
scripts/            installers (install.sh for a server and its clients), the reconciler, push-memory.py,
                    the triage scripts, the wiki publisher
.claude/agents/     findings-classifier, findings-processor (project-scope agents for headless runs)
.claude/skills/     findings-triage/SKILL.md, the agents' doctrine
docs/               build material only: ROADMAP.md (outstanding work) and plans/ (active design plans)
docs/wiki/          these pages
testdata/memory/    a fixture memory tree used by importer and reconciler tests
```

## Layering

```
harness (.claude/memory Markdown)  ──reconciler──▶  memoryd HTTP API  ──▶  SQLite
          ▲                                             ▲      ▲
          │ exporter (decisions only)                   │      │
          └─────────────────────────────────────────────┘      │
crew (Claude Code) ──▶ mcpd harness profile ──▶ HTTP ──────────┘
triage agents      ──▶ mcpd triage profile  ──▶ HTTP ──────────┘
operator           ──▶ /triage, /missions   ──▶ HTTP ──────────┘
```

- `mcpd` never touches the database; it is an HTTP client with typed tool schemas. That keeps one code path for every mutation and one place to validate.
- Handlers hold their SQL. There is no repository layer; the handlers are the contract, documented in [Developer: HTTP API](Developer-HTTP-API) and the [MCP Tool Reference](MCP-Tool-Reference).
- The database is the derived copy. Markdown wins, except for the exporter's narrow write-back of operator decisions.
- Files reach the service by push. The reconciler and `push-memory.py` run where the harness lives and send changed Markdown by content (`POST /v1/documents/push`), so the service never needs to read a harness's disk and can run on another machine. The older path sync (`POST /v1/documents/sync`) still works when the service shares the harness's disk.

## Rules that are load-bearing

1. **Keys decide who reaches the service; tool lists decide what agents can do.** On loopback with no keys the service is open, as it always was. Once a key exists, or whenever it listens on a network address, every `/v1` call needs one (`internal/auth`, [Developer: HTTP API](Developer-HTTP-API#authentication)). A key grants full access, so the agent boundary is still `mcpd`'s tool list per profile: operator routes are never registered as tools.
2. **`findings.status` has one writer**, `decideFindingHandler`. Every other write path is refused at the binding layer (fields not in the request struct) or does not exist.
3. **Additive schema changes only.** New tables go in `db/schema.sql`; new columns on existing tables go through `internal/store/migrate.go`, and so do their indexes. See [Developer: Database](Developer-Database).
4. **Errors never echo request text.** `validationError` renders validator failures structurally and static messages verbatim; handlers never wrap a driver error into a message that reaches the client.
5. **One implementation per contract.** Status vocabularies, natural keys and the directive entry format each have one definition that both sides use (for example the exporter imports the reconciler's parser rather than re-implementing it).
6. **Idempotent everything.** Schema bootstrap, migrations, the reconciler, the seed script, the backfill, the exporter and the installers can all be run again without harm.

## Where to read next

| Change you want to make | Page |
|---|---|
| Add or change a route | [Developer: HTTP API](Developer-HTTP-API) |
| Add a table or column | [Developer: Database](Developer-Database) |
| Expose something to the crew or the triage agents | [Developer: MCP Adapter](Developer-MCP-Adapter) |
| Change what the reconciler or exporter does with Markdown | [Developer: Scripts and Reconciler](Developer-Scripts-and-Reconciler) |
| Change how findings are classified or recommended | [Developer: Triage Agents](Developer-Triage-Agents) |
| Know what the tests prove and how to run the gate | [Developer: Testing and CI](Developer-Testing-and-CI) |
| Understand the harness side of the boundary | [Developer: Harness Integration](Developer-Harness-Integration) |

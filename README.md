# bishop-memory

A locally bound memory service for the Bishop agent harness. One Go binary, one SQLite database with full-text search, an MCP adapter the crew talks to, and a nightly findings-triage loop with a review page for the operator.

- **Go + Gin + SQLite (FTS5)**, `modernc.org/sqlite`, so there is no C toolchain and the binary cross-compiles statically.
- **Loopback-only, no authentication.** Reach it from elsewhere over an SSH tunnel.
- **Markdown is the source of truth.** The service is a derived copy that several harnesses can share; the reconciler keeps it in step.

## Documentation

| Audience | Start here |
|---|---|
| **End users** — run a harness against it, review findings | [User Guide](docs/wiki/User-Guide.md) · [MCP Tool Reference](docs/wiki/MCP-Tool-Reference.md) · [Review Page Guide](docs/wiki/Review-Page-Guide.md) · [Commands and Scripts](docs/wiki/Commands-and-Scripts.md) · [Troubleshooting](docs/wiki/Troubleshooting.md) |
| **Developers** — change the service | [Overview](docs/wiki/Developer-Overview.md) · [HTTP API](docs/wiki/Developer-HTTP-API.md) · [Database](docs/wiki/Developer-Database.md) · [MCP Adapter](docs/wiki/Developer-MCP-Adapter.md) · [Scripts and Reconciler](docs/wiki/Developer-Scripts-and-Reconciler.md) · [Triage Agents](docs/wiki/Developer-Triage-Agents.md) · [Testing and CI](docs/wiki/Developer-Testing-and-CI.md) · [Harness Integration](docs/wiki/Developer-Harness-Integration.md) |
| **Reference** | [API contract](docs/api-contract.md) · [Architecture](docs/architecture.md) · [Install](docs/INSTALL.md) · [Harness integration](docs/HARNESS-INTEGRATION.md) · [Memory setup](docs/MEMORY-SETUP.md) · [Findings triage](docs/FINDINGS-TRIAGE.md) · [Roadmap](docs/ROADMAP.md) · [Changelog](CHANGELOG.md) |

The pages under `docs/wiki/` are also published to the [GitHub wiki](https://github.com/djm56/bishop-memory/wiki) with `make wiki-publish`; `docs/wiki/` is the source of truth.

## Quick start

```bash
git clone git@github.com:djm56/bishop-memory.git && cd bishop-memory

# Run in the foreground (127.0.0.1:8787, data/memory.db)
make run
make health                      # {"ok":true,"schema":"ok",...}

# Or install as a service that starts at login and restarts on exit
scripts/install-daemon.sh --memory-root /abs/path/to/a/harness/.claude/memory    # macOS, launchd
sudo scripts/install-daemon-linux.sh                                              # Linux, systemd

# Build the MCP adapter every harness .mcp.json points at
make build-mcpd
```

Re-running the installer after `git pull` is the upgrade path: it rebuilds, restages and restarts, and the database schema upgrades itself at boot.

## Connect a harness

The connection is configured from the harness, not from here. In the harness checkout: copy `.claude/bishop-memory.conf.example` to `.claude/bishop-memory.conf`, set `BISHOP_MEMORY_MODE=central`, a unique `BISHOP_HARNESS`, and `BISHOP_MEMORY_HOME` pointing at this checkout; run `.claude/connect-bishop-memory.sh`; restart Claude Code and approve the `bishop-memory` MCP server. Then reconcile once:

```bash
/path/to/bishop-memory/scripts/reconcile-memory.py --root .claude/memory --harness <name>
```

Without the conf file a harness runs standalone and never calls the service. Details: [docs/HARNESS-INTEGRATION.md](docs/HARNESS-INTEGRATION.md).

## What it stores

Ten harness-vocabulary tables — `missions`, `mission_steps`, `flight_recorder` (the audit journal), `crew`, `findings`, `patterns`, `service_records`, `directives`, `documents`, `documents_fts` — plus seven findings-triage tables. Schema: [db/schema.sql](db/schema.sql); walkthrough: [docs/MEMORY-SETUP.md](docs/MEMORY-SETUP.md).

Two rules are enforced by what is registered where rather than by authentication:

1. **A finding's status belongs to the operator.** Agents create findings as `proposed`; the one route that changes status is `POST /v1/findings/:id/decision`, which no MCP profile exposes.
2. **Directives are read-only to agents.** Only the reconciler (mirroring `DIRECTIVES.md`) and the operator's ratification route write them.

## MCP tools

`cmd/mcpd` is a stdio MCP server that proxies the HTTP API. It registers one of two tool sets, chosen by `MCPD_PROFILE`:

**Harness profile** (default, 16 tools) — what a Bishop crew uses during a mission.

| Read | Write |
|---|---|
| `memory_search`, `mission_list`, `mission_get`, `mission_steps_list`, `finding_list`, `pattern_list`, `service_record_list` | `mission_allocate`, `mission_create`, `mission_update`, `flight_recorder_append`, `mission_step_record`, `finding_append`, `pattern_append`, `service_record_append`, `documents_sync` |

`flight_recorder_append` and `mission_step_record` record the actor as `<BISHOP_HARNESS>:<agent>`; `finding_append` records the harness. `finding_list` filters by status, triage category, harness and ids.

**Triage profile** (`MCPD_PROFILE=triage`, 13 tools) — what the two scheduled findings-triage agents use: `triage_categories`, `triage_next_unclassified`, `triage_classify`, `triage_category_findings`, `triage_recent_decisions`, `triage_group_create`, `triage_recommend`, `directive_propose`, `triage_run_start`, `triage_run_finish`, plus the read-only `finding_list`, `pattern_list`, `memory_search`. The two profiles share no write tool.

Every tool, with arguments, examples and errors: [MCP Tool Reference](docs/wiki/MCP-Tool-Reference.md).

## Findings triage

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/wiki/images/pending-dark.png">
  <img alt="The findings triage review page: a category rail on the left, and on the right a group of findings that share a rule, each with the processor's recommendation and Approve, Reject and Defer buttons." src="docs/wiki/images/pending-light.png">
</picture>

Findings accumulate faster than anyone reads them. bishop-memory classifies the ledger into a 17-category taxonomy nightly (Haiku), groups one category's findings by the rule they share and recommends a decision on each (Sonnet), on Claude Code or, with `TRIAGE_ENGINE=opencode`, on any model your OpenCode config reaches, and drafts `DIRECTIVES.md` entries — all for you to decide on a review page at `http://127.0.0.1:8787/triage`. Decisions are written back into the owning harness's Markdown. No agent ever changes a finding's status.

```bash
printf 'ANTHROPIC_API_KEY=sk-ant-...\n' >> .env && chmod 600 .env   # scheduled jobs have no Claude login
make triage-seed                                                     # load db/finding-categories.json
make triage-backfill REGISTER="kirsch=/abs/path/kirsch/.claude/memory"
make triage-classify                                                 # classify everything now
make triage-process                                                  # recommend on the next category
make triage-review                                                   # open the review page
make triage-export                                                   # write decisions to FINDINGS.md / DIRECTIVES.md
make triage-install                                                  # nightly launchd jobs (21:00 / 21:20)
make screenshots                                                     # regenerate the screenshots in the docs
```

The screenshots are produced from made-up demo data on a throwaway service (`scripts/screenshots/`), never from a real ledger, because they are published.

Guide: [docs/FINDINGS-TRIAGE.md](docs/FINDINGS-TRIAGE.md). Design record: [docs/FINDINGS-TRIAGE-PLAN.md](docs/FINDINGS-TRIAGE-PLAN.md).

## Configuration

Environment variables, with optional `.env` in the working directory (gitignored).

| Variable | Default | Read by | Purpose |
|---|---|---|---|
| `HTTP_HOST` | `127.0.0.1` | memoryd | Bind address. Anything else exposes an unauthenticated service and logs a loud warning |
| `PORT` | `8787` | memoryd, scripts | Listen port |
| `DB_PATH` | `data/memory.db` | memoryd | SQLite file; relative to the working directory |
| `APP_ENV` | `development` | memoryd | Gin mode |
| `LOG_LEVEL` | `info` | memoryd | Advisory |
| `MEMORY_ROOT` | `testdata/memory` | memoryd | Default root for `documents_sync` when the caller omits one |
| `BISHOP_MEMORY_URL` | `http://127.0.0.1:8787` | mcpd, scripts | Service URL |
| `BISHOP_HARNESS` | `claude-code` | mcpd | Harness identity composed into actors and stored on findings |
| `MCPD_PROFILE` | `harness` | mcpd | `harness` or `triage` tool set |
| `ANTHROPIC_API_KEY` or `CLAUDE_CODE_OAUTH_TOKEN` | — | triage runner | Required for scheduled agent runs on the claude engine; keep it in `.env`, mode 0600 |
| `TRIAGE_*` | see guide | triage runner | Engine (`claude` or `opencode`), item caps, models, budgets, timeout, log directory |

## Network and security model

The service has **no authentication, no rate limiting**, and `POST /v1/documents/sync` walks any filesystem path a caller supplies (except `/`). It is safe because it binds loopback. The supported remote topology is an SSH tunnel: `ssh -L 8787:127.0.0.1:8787 user@server` gives you encryption, your SSH key as authentication, and isolation, with no change to the service. Do not bind `0.0.0.0` on a host you do not fully control.

## Development

```bash
make test            # go test ./...
make vet             # go vet ./...
make fmt             # gofmt
go test -race ./...  # CI also runs this
```

CI (`.github/workflows/ci.yml`) runs build, vet, gofmt, test and test -race on Linux for every push. Conventions and the verification gate: [CONTRIBUTING.md](CONTRIBUTING.md) and [Developer: Testing and CI](docs/wiki/Developer-Testing-and-CI.md).

## Repository map

```
cmd/memoryd         HTTP daemon          internal/api        handlers and router
cmd/mcpd            MCP adapter          internal/store      SQLite, schema, migrations
db/schema.sql       schema               internal/importer   Markdown → FTS5
db/finding-categories.json  taxonomy     internal/ui         the /triage review page
scripts/            installers, reconciler, triage, wiki     .claude/agents, .claude/skills   the triage agents
                                                             .opencode/agents                 their OpenCode twins
docs/               reference docs       docs/wiki/          user and developer pages (published to the wiki)
```

## License

See [LICENSE](LICENSE).

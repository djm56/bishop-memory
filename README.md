# bishop-memory

A locally-bound memory service for AI agents. Built with **Go + Gin + SQLite + FTS5**, using `modernc.org/sqlite` for a CGo-free, portable binary.

## Quick Start

### Prerequisites

- **Go 1.25+** (for building from source).
- **No C toolchain required** — `modernc.org/sqlite` is pure Go, so you can run `make build` on any platform.
- **For systemd (Linux):** The installer script detects this automatically.
- **For macOS:** No additional setup required.

**Optional tools** (required for specific operations, not for daemon installation):
- **`curl`** — needed for `make health` (HTTP health checks) and manual migration verification.
- **`jq`** — needed for `make health` (JSON query of health endpoint).
- **`sqlite3`** — needed for manual database inspection, migration checks, and troubleshooting queries.

### Run Locally

```bash
# Start the service (listening on 127.0.0.1:8787 by default)
make run

# Build binary to bin/memoryd
make build

# Initialize the database
make init-db

# Check service health
make health

# View all Makefile targets
make help  # (or just `make` to see the PHONY targets)
```

### Development

```bash
# Run all tests
make test

# Format code
make fmt

# Lint
make vet

# Update dependencies
make tidy

# Reset database (destructive)
make reset-db
```

## Configuration

Configuration is loaded from environment variables (with optional `.env` file support via `godotenv`). Unset variables fall back to documented defaults.

| Variable | Default | Purpose |
|----------|---------|---------|
| `HTTP_HOST` | `127.0.0.1` | Interface to bind HTTP listener to. **⚠️ Loopback-only by default — see Security below.** |
| `PORT` | `8787` | HTTP listen port. Combined with `HTTP_HOST` as the listen address. |
| `DB_PATH` | `data/memory.db` | Path to SQLite database file. Relative paths are resolved from the working directory. |
| `APP_ENV` | `development` | Runtime environment mode. Passed to Gin (`gin.SetMode`). |
| `LOG_LEVEL` | `info` | Structured log verbosity. Currently advisory — full filtering is Phase 3. |

**Example `.env` file:**

```bash
HTTP_HOST=127.0.0.1
PORT=8787
DB_PATH=data/memory.db
APP_ENV=development
LOG_LEVEL=info
```

`.env` is optional — if absent or unreadable, environment variables and defaults apply. The `.env` file is **not checked into version control** (see `.gitignore`).

### `HTTP_HOST` and Security

⚠️ **Critical:** bishop-memory has **no authentication of any kind**. Every endpoint is open read/write to any caller that can reach it. The service deliberately defaults to loopback-only (`127.0.0.1`) for this reason.

- **Loopback-only (recommended):** Bind to `127.0.0.1` or `::1`. Only local processes can reach the service.
- **Non-loopback:** If you set `HTTP_HOST` to anything else (e.g., `0.0.0.0`, a LAN address), the service logs a **loud warning on startup** naming exactly what becomes exposed. This is your explicit choice, not a silent default.

See [Network Model](#network-model) below for the recommended topology.

## Installing as a Service

**See `docs/INSTALL.md` for the complete installation guide** — it covers build, service setup (daemon installation), MCP registration, index seeding, verification, and troubleshooting end-to-end.

Quick start for the impatient:

### macOS (launchd)

```bash
scripts/install-daemon.sh
```

### Linux (systemd)

```bash
sudo scripts/install-daemon-linux.sh
```

For full details, options, preview modes, and troubleshooting, see `docs/INSTALL.md`.

## Deploying to a Remote Server

**See `docs/INSTALL.md` for the complete remote deployment guide** — it covers building cross-compiled binaries, transfer options, and running the installer on the server.

Quick reference:

```bash
# Build for your target
make dist-linux-amd64  # or dist-linux-arm64
make dist-linux        # both architectures

# Transfer and install (see INSTALL.md for full details)
# Then on the server:
sudo scripts/install-daemon-linux.sh
```

## Network Model

`bishop-memory` is designed for **loopback-only operation** with access via **SSH local port forwarding** (the recommended arrangement).

### Recommended: SSH Tunnel

**On your local machine:**

```bash
# Forward localhost:8787 on your machine to localhost:8787 on the server
# over the SSH tunnel. The server-side memoryd listens on loopback.
ssh -L 8787:127.0.0.1:8787 user@server
```

Now your local applications can reach `http://127.0.0.1:8787` as if the service were running locally.

### Why Not Network-Bound?

The alternative — binding to `0.0.0.0` or a non-loopback address — is **not recommended** because:

1. **No authentication.** Any client on the network can call any endpoint.
2. **Arbitrary filesystem read.** `POST /v1/documents/sync` accepts a caller-supplied root path and walks it to import documents.
3. **No isolation.** Task state, events, and memory are exposed directly to the network.

The loopback + SSH-tunnel topology provides **encryption** (SSH), **authentication** (your SSH key), and **isolation** (only local processes can reach the service) without any changes to the service itself.

## Connecting a Harness

bishop-memory exposes an **MCP adapter** (`cmd/mcpd`) that a bishop-harness can call as a tool-providing server. Connection is configured **from the harness**, not from bishop-memory.

**See `docs/INSTALL.md` and `docs/HARNESS-INTEGRATION.md` for the complete registration and integration guide.**

### Claude Code (bishop-harness)

To connect an existing bishop-harness to this bishop-memory instance:

1. Create `.claude/bishop-memory.conf` from the example in your harness repo (`.claude/bishop-memory.conf.example`).
2. Set `BISHOP_MEMORY_HOME` to the path of this checkout.
3. Run `.claude/connect-bishop-memory.sh` in your harness.

This creates `.mcp.json` (project-scope MCP registration) and registers `mcpd` to run under your harness's project scope, not user scope. Your harness can then operate in either mode:

- **Standalone** — each harness owns its memory entirely. Mission IDs are derived locally. No network calls. Default, and safe when bishop-memory is absent.
- **Central** — mission IDs are allocated centrally, and the journal is mirrored to bishop-memory. Requires the service to be running.

The connection is idempotent — re-running the generator detects what's already in place and skips it.

## MCP Tool Surface

The bishop-memory MCP adapter (`cmd/mcpd`) exposes **16 tools**:

### Read Tools (no agent identity required)

| Tool | Purpose | Arguments |
|------|---------|-----------|
| `memory_search` | Search imported documents (FTS5 index) by query string. Returns ranked hits with snippets. Empty index = empty results. | `q` (required): FTS5 query string. Multi-word is implicit AND; wrap phrases in `"quotes"`; `*` is a prefix wildcard. `limit` (optional): cap on results (server always caps at 20). |
| `mission_list` | List all missions, newest-updated first. Returns `{"missions":[...]}`. | None. |
| `mission_get` | Fetch a single mission by ID. Returns mission JSON or HTTP 404. | `id` (required): Mission ID. |
| `mission_steps_list` | List steps for a mission (PROGRESS.md rows). Returns `{"steps":[...]}`. | `id` (required): Mission ID. |
| `finding_list` | List findings from the ledger, newest-first. Optional status filter. Returns `{"findings":[...]}`. | `status` (optional): Filter by status (proposed/approved/applied/rejected/retired/superseded). |
| `pattern_list` | List advisory patterns, newest-first. Returns `{"patterns":[...]}`. | None. |
| `service_record_list` | List service records (observations about agent performance). Optional agent filter. Returns `{"service_records":[...]}`. | `agent` (optional): Filter by agent name (subject of the records). |

### Write Tools (HTTP method varies; agent identity composed from `BISHOP_HARNESS:<agent>` where noted)

| Tool | Purpose | Arguments | HTTP Method |
|------|---------|-----------|-------------|
| `mission_allocate` | Allocate a centrally-unique mission ID and create the mission atomically. **Use this INSTEAD of `mission_create` in central mode** so mission IDs never collide between harnesses. Returns `{"id":...,"harness":...,"date":...,"seq":...,"created":true,"attempts":...}`. | `title` (required, max 500 chars): Human-readable mission title. `harness` (optional): Owning harness name; read from caller's `.claude/bishop-memory.conf`, fall back to `BISHOP_HARNESS` env var. `owner`, `priority`, `next_action`, `blockers` (all optional): Mission fields. `date` (optional, YYYYMMDD): Override UTC day for ID scope; defaults to server's UTC clock. | POST |
| `mission_create` | Create a new mission. Returns `{"id":...,"created":true}`. | `id` (required, max 128 chars): Unique mission ID. `title` (required, max 500 chars): Human-readable title. `status`, `owner`, `priority`, `next_action`, `blockers` (all optional): Mission fields. | POST |
| `mission_update` | Update an existing mission (status, owner, outcome, priority, next_action, blockers). Returns `{"id":...,"updated":true}` or HTTP 404. | `id` (required): Mission ID. Omitted fields are unchanged. | PATCH |
| `flight_recorder_append` | Append an event to the flight recorder (audit log). Agent identity is composed as `<BISHOP_HARNESS>:<agent>`. Returns `{"appended":true,"id":...}`. | `mission_id` (optional): Scope the event to a mission. `step` (optional): Step label from PROGRESS.md. `event` (required, max 64 chars): Dotted event discriminator (e.g. `step.complete`, `agent.heartbeat`). `note` (required, max 2000 chars): Human-readable note. `occurred_at` (optional): Event timestamp in `YYYY-MM-DD HH:MM UTC` format. `agent` (required): Sub-agent name (e.g. `bishop`, `hicks`). Composed with `BISHOP_HARNESS`. | POST |
| `mission_step_record` | Record a mission step (one agent execution). Agent identity is composed. Returns `{"recorded":true,"mission_id":...}` or HTTP 404. | `id` (required): Mission ID. `step` (optional, max 16 chars): Step label from PROGRESS.md (e.g. `4a`, `—`). `phase` (optional, max 64 chars): Phase label. `agent` (required): Sub-agent name. Composed with `BISHOP_HARNESS`. `status` (optional): Step status (pending/in-progress/done/failed). `notes`, `summary` (optional, max 2000 chars): Free-form notes and summary. `started_at`, `ended_at` (optional): ISO-8601 timestamps. | POST |
| `finding_append` | Append a finding to the findings ledger. Status is always 'proposed'; only humans can advance it. Returns `{"id":...,"created":true}`. | `suggestion` (required, max 2000 chars): The finding itself. `finding_date` (optional, max 64 chars): Finding date. `target` (optional, max 256 chars): Target entity (agent, skill, tool name). `rationale` (optional, max 2000 chars): Supporting rationale. `mission_id` (optional, max 128 chars): Associated mission. | POST |
| `pattern_append` | Append an advisory pattern. Returns `{"id":...,"created":true}`. | `name` (required, max 256 chars): Pattern name. `context`, `solution`, `example` (optional, max 2000 chars): Context, solution, worked example. `discovered_at` (optional, max 64 chars): Discovery timestamp. `discovered_mission` (optional, max 128 chars): Discovery mission ID. | POST |
| `service_record_append` | Record a service observation about an agent. Returns `{"id":...,"created":true}`. | `agent` (required, max 128 chars): Agent name (subject of the record). `record_date` (optional, max 64 chars): Record date. `title`, `note`, `adjustment` (optional, max 2000 chars): Title, note, adjustment. `source` (optional): 'self-reported' or 'bishop-observed'. | POST |
| `documents_sync` | Trigger an import of Markdown/JSONL files into the search index. Returns `{"root":...,"synced":true}` or HTTP 502 if import fails. | `root` (optional): Override the memory root. Falls back to `MEMORY_ROOT` env var, then `testdata/memory` default. | POST |

For full request/response details, see `docs/api-contract.md`.

## Memory System Setup

See **`docs/MEMORY-SETUP.md`** for comprehensive guidance on:

- What the service stores (database schema).
- Seeding the index from existing agent memory via `/v1/documents/sync`.
- How search and content hashing work.
- Day-to-day agent usage patterns.

## Migration: `events.agent` Column

**Skip this section if you are doing a fresh install.** Clean installs created from the current `db/schema.sql` already include the `events.agent` column and do not require migration.

⚠️ **Critical:** Any bishop-memory database created **before task-20260820-03** lacks the `events.agent` column needed for agent-identity capture. Before using the service, check:

```bash
sqlite3 data/memory.db '.schema events'
```

If the output does NOT show an `agent TEXT` column, run this once to add it:

```bash
sqlite3 data/memory.db 'ALTER TABLE events ADD COLUMN agent TEXT;'
```

Then restart the service.

## Security Posture

### Current (Safe)

- **Loopback-only by default** — no network exposure without explicit configuration.
- **HTTP listener port** is configurable (`PORT`, default `8787`).
- **Database is single-writer, WAL-enabled** — concurrent reads don't block writes.
- **Document import** checks for path traversal (symlink escape, `../` escape) and refuses to walk the filesystem root.

### Known Limitations (Phase 3 and beyond)

- **No authentication.** Every endpoint is open read/write. Access control happens at the network level (loopback + SSH tunnel, or firewall rules).
- **No rate limiting.** No quota or throttling on requests.
- **No audit trail beyond the database.** Service logs are not archived separately.
- **Document-sync root allow-list not yet implemented** — the service currently refuses `/` (the filesystem root) but still accepts any other absolute path. A full allow-list of permitted memory roots is a Phase 3 design decision.
- **Response bodies forwarded with truncation** — if a handler returns a non-2xx response, the response body is sent directly to the MCP caller (up to 512 bytes; longer responses are truncated with "…"). No scrubbing is performed, so a malicious or buggy handler could leak information within that 512-byte window.

### Operational Recommendations

1. **Always use SSH tunneling** for remote access.
2. **Run on loopback only** — never set `HTTP_HOST` to `0.0.0.0` or a non-loopback address in production.
3. **Firewall the machine** — restrict SSH access to authorized users only.
4. **Monitor logs** — watch for unexpected `/v1/documents/sync` requests with unusual root paths.

## Troubleshooting

### Service Won't Start

**Symptom:** `systemctl status bishop-memory.service` shows "inactive" or "failed".

**Check:**

1. Logs:
   ```bash
   journalctl -u bishop-memory.service -n 50
   ```

2. Permissions (Linux):
   ```bash
   ls -la /var/lib/bishop-memory
   ls -la /path/to/checkout/bin/memoryd
   ```

3. Database:
   ```bash
   sqlite3 data/memory.db '.tables'
   ```

### HTTP_HOST Not Loopback Warning

**Symptom:** Service logs a warning like:

```
[http] warning: HTTP_HOST=0.0.0.0 is not loopback-only — ...
```

**Meaning:** The service is accessible from the network. Verify this is intentional and use firewall rules or SSH tunneling to control access.

### Cannot Connect to Service

**Symptom:** `make health` fails with connection refused.

**Check:**

1. Service is running:
   ```bash
   lsof -i :8787  # macOS
   ss -tulpn | grep 8787  # Linux
   ```

2. Listen address is correct:
   ```bash
   grep HTTP_HOST .env  # or check env vars
   ```

3. SSH tunnel (if remote):
   ```bash
   ssh -L 8787:127.0.0.1:8787 user@server  # keep this open
   ```

### FTS5 Search Returns No Results

**Symptom:** `memory_search` queries return empty results despite imported documents.

**Check:**

1. Documents are imported:
   ```bash
   sqlite3 data/memory.db 'SELECT COUNT(*) FROM documents;'
   ```

2. FTS5 index is built:
   ```bash
   sqlite3 data/memory.db 'SELECT COUNT(*) FROM documents_fts;'
   ```

3. Re-sync documents (force rebuild):
   ```bash
   curl -X POST http://127.0.0.1:8787/v1/documents/sync -H 'Content-Type: application/json' -d '{}'
   ```

4. Query syntax (FTS5 is strict):
   ```bash
   # OK: simple word
   # OK: "phrase in quotes"
   # OK: word*  (prefix wildcard)
   # NOT OK: wor* (FTS5 requires at least one complete word before the wildcard)
   ```

## References

- **`docs/INSTALL.md`** — Complete installation, registration, and MCP setup guide.
- **`docs/architecture.md`** — Architecture overview, layering, component responsibilities.
- **`docs/migration-plan.md`** — Completion record of the four-phase migration (Phase 1–4 all complete).
- **`docs/api-contract.md`** — Full HTTP API contract (request/response examples).
- **`docs/MEMORY-SETUP.md`** — Memory system setup and usage guide.
- **`CHANGELOG.md`** — Version history and notable changes.
- **`CONTRIBUTING.md`** — Contribution guidelines and development process.

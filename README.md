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

### macOS (launchd)

Register the service to start at login and restart on exit:

```bash
scripts/install-daemon.sh
```

To preview without installing:

```bash
scripts/install-daemon.sh --dry-run
```

The script:
- Builds `memoryd` into `./bin/memoryd`.
- Renders and installs the plist to `~/Library/LaunchAgents/com.bishop-memory.memoryd.plist`.
- Backs up any existing plist to `*.bak` (first run only).
- Loads the service with `launchctl`.

Check status with:

```bash
launchctl list | grep com.bishop-memory.memoryd
log stream --predicate 'process == "memoryd"'  # tail logs
```

### Linux (systemd)

Install as a system service running under a dedicated non-root account:

```bash
sudo scripts/install-daemon-linux.sh
```

Or preview first:

```bash
scripts/install-daemon-linux.sh --dry-run
```

The script:
- **Detects platform, init system, and CPU architecture** rather than assuming (checks for `/run/systemd/system`, maps `uname -m` to Go `GOARCH`).
- **Never elevates its own privilege** — it prints the exact `useradd`, `groupadd`, and `systemctl` commands that need root and exits if not already root.
- Builds `memoryd` into `./bin/memoryd` (or uses `./bin/memoryd-linux-<arch>` if Go is not available).
- Creates a dedicated `bishop-memory` system user and group.
- Installs the systemd unit to `/etc/systemd/system/bishop-memory.service`.
- Grants the service account **exactly** the permissions it needs: read/execute on `./bin/memoryd`, read on `./db/schema.sql`, and traversal on the directory chain leading to both.
- Runs a **post-start health check** (polls `systemctl is-active` + `journalctl` for logs on failure).

**Note:** The installer does not grant traversal *above* the checkout root, so if the checkout is in a restrictive home directory (e.g., `/root/...` with narrow permissions on `/root`), the service may fail to start. The health check will catch this and log the error.

Check status with:

```bash
systemctl status bishop-memory.service
journalctl -u bishop-memory.service -f  # tail logs
```

## Deploying to a Remote Server

### Step 1: Build for your target platform

Build the binary on any host with Go installed:

```bash
# For x86-64 Linux (from any host)
make dist-linux-amd64

# For ARM64 Linux (from any host)
make dist-linux-arm64

# For both
make dist-linux
```

The resulting binaries in `./bin/` are **statically linked** with no runtime dependency — they do not require Go, libc, or any shared libraries on the target host. Only the Linux kernel and systemd are required.

### Step 2: Transfer the checkout to your server

The installer script (`scripts/install-daemon-linux.sh`) requires the full checkout directory to be present on the server. Choose one of these approaches:

**Option A: Clone on the server, then transfer the prebuilt binary** (for servers without Go)

**Prerequisite:** `/opt` is typically root-owned, so the initial `git clone` (run interactively on the server, where you can escalate) needs either `sudo mkdir -p /opt/bishop-memory && sudo chown "$USER" /opt/bishop-memory` run first, or an existing `/opt/bishop-memory` already writable by your account. The `scp` step below runs from your local machine as `user@server` with no chance to escalate mid-transfer, so that account must already own (or have write access to) `/opt/bishop-memory/bin` — i.e. it must be the same account that ran the clone, or one granted write access to it.

```bash
# On the server:
git clone <your-repo-url> /opt/bishop-memory
mkdir -p /opt/bishop-memory/bin

# On your local machine (after building):
# Transfer for x86-64:
scp ./bin/memoryd-linux-amd64 user@server:/opt/bishop-memory/bin/memoryd-linux-amd64
# Or for ARM64:
scp ./bin/memoryd-linux-arm64 user@server:/opt/bishop-memory/bin/memoryd-linux-arm64
```

The installer detects your architecture and uses the matching prebuilt binary if Go is not installed.

**Option B: Transfer the entire checkout with rsync** (for servers without Go)

**Prerequisite:** unlike Option A, this is a single non-interactive command run from your local machine over SSH — there is no interactive session on the server in which to `sudo`. `/opt/bishop-memory` MUST already exist and be writable by the `user` account named in the command before you run `rsync`; create it and hand it over first with a separate step such as `ssh user@server 'sudo mkdir -p /opt/bishop-memory && sudo chown user /opt/bishop-memory'`.

```bash
rsync -a --exclude='.git' . user@server:/opt/bishop-memory/
```

This transfers the entire source tree **including the prebuilt binaries** that you built in Step 1. The installer will use them if Go is not installed on the server.

**Option C: Clone and build on the server** (requires Go 1.25+)

**Prerequisite:** as in Option A, the initial `git clone` runs interactively on the server, so `/opt` being root-owned means you need either a prior `sudo mkdir -p /opt/bishop-memory && sudo chown "$USER" /opt/bishop-memory`, or an already-writable `/opt/bishop-memory`. Unlike Option A, write access must persist past the clone: `make build` writes the compiled binary into that same tree, so the account doing the clone must keep write access through the build step too.

```bash
# On the server:
git clone <your-repo-url> /opt/bishop-memory
cd /opt/bishop-memory
make build
```

This is the simplest option if the server already has Go installed — it rebuilds the binary locally from the current source.

The installer needs the following files and directories to be present:
- `scripts/install-daemon-linux.sh` — the installer script
- `scripts/bishop-memory.service` — the systemd unit template
- `db/schema.sql` — the database schema (read at runtime by the daemon)
- One of: `bin/memoryd` (from `make build` on the server) OR `bin/memoryd-linux-amd64` or `bin/memoryd-linux-arm64` (prebuilt binaries from Step 1)

### Step 3: Run the installer

Once the checkout and binary are on the server:

```bash
cd /opt/bishop-memory
sudo scripts/install-daemon-linux.sh
```

Then follow the "Linux (systemd)" section above for status checks and log tailing.

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

## Registering the MCP

bishop-memory exposes an **MCP adapter** (`cmd/mcpd`) that agents such as Claude Code and opencode can call as a tool-providing server.

### Claude Code

```bash
scripts/install-claude.sh --project-root /path/to/your/project
```

The script:
- Builds `mcpd` into `./bin/mcpd`.
- Registers bishop-memory with Claude Code via the `claude` CLI (if available) or by patching `claude.json` directly.
- Appends a `## bishop-memory` section to your project's `CLAUDE.md` (or creates it).
- Is idempotent — re-runs detect existing registrations and skip.

Set these environment variables for the MCP to work:

- `BISHOP_MEMORY_URL`: Base URL of the bishop-memory HTTP API (default: `http://127.0.0.1:8787`).
- `BISHOP_HARNESS`: Harness prefix for agent identity (default: `claude-code`). The MCP composes actor as `<BISHOP_HARNESS>:<agent>`, so agents must pass an `agent` argument to identify themselves.

### opencode

```bash
scripts/install-opencode.sh --opencode-root /path/to/.opencode
```

The script:
- Builds `mcpd` into `./bin/mcpd`.
- Registers the MCP under `mcp.bishop-memory` in `opencode.json`.
- Installs the `memory` skill to `skills/memory/SKILL.md`.
- Patches the 5 standard agent AGENT.md files (orchestrator, junior-developer, senior-developer, code-reviewer, documentation-writer).
- Is idempotent — re-runs detect existing entries and skip.

Environment variables:

- `BISHOP_MEMORY_URL`: Base URL of the bishop-memory HTTP API (default: `http://127.0.0.1:8787`).
- `BISHOP_HARNESS`: Harness prefix (default: `opencode`). Agents compose identity as `<BISHOP_HARNESS>:<agent>`.

## MCP Tool Surface

The bishop-memory MCP adapter (`cmd/mcpd`) exposes **8 tools**:

### Read Tools (no agent identity required)

| Tool | Purpose | Arguments |
|------|---------|-----------|
| `memory_search` | Search imported documents (FTS5 index) by query string. Returns ranked hits with snippets. Empty index = empty results. | `q` (required): FTS5 query string. Multi-word is implicit AND; wrap phrases in `"quotes"`; `*` is a prefix wildcard. `limit` (optional): cap on results (server always caps at 20). |
| `task_list` | List all tasks, newest-updated first. Returns `{"tasks":[...]}`. | None. |
| `task_get` | Fetch a single task by ID. Returns task JSON or HTTP 404. | `id` (required): Task ID. |

### Write Tools (HTTP method varies; agent identity composed from `BISHOP_HARNESS:<agent>`)

| Tool | Purpose | Arguments | HTTP Method |
|------|---------|-----------|-------------|
| `task_create` | Create a new task. Returns `{"id":...,"created":true}`. | `id` (required, max 128 chars): Unique task ID. `title` (required, max 500 chars): Human-readable title. `status`, `priority`, `next_action`, `blockers` (all optional): Task fields. | POST |
| `task_update` | Update an existing task (status, priority, next_action, blockers). Returns `{"id":...,"updated":true}` or HTTP 404. | `id` (required): Task ID. Omitted fields are unchanged. | PATCH |
| `event_append` | Append an event to the audit log. Agent identity is composed as `<BISHOP_HARNESS>:<agent>`. Returns `{"appended":true,"id":...}`. | `event_type` (required, max 64 chars): Dotted discriminator (e.g. `task.created`, `agent.heartbeat`). `summary` (required, max 2000 chars): Human-readable summary. `agent` (required): Sub-agent name (e.g. `orchestrator`, `junior-developer`). Composed with `BISHOP_HARNESS` to form the stored actor identity. `task_id` (optional): Scope the event to a task. | POST |
| `task_run_record` | Record an agent execution attempt against a task. Agent identity is composed. Returns `{"recorded":true,"task_id":...}` or HTTP 404. | `id` (required): Task ID. `agent` (required): Sub-agent name. `status` (optional, max 64 chars): Run status (e.g. `started`, `completed`, `failed`). `summary` (optional, max 2000 chars): Free-form summary. `started_at`, `ended_at` (optional): ISO-8601 timestamps. | POST |
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

- **`docs/architecture.md`** — Architecture overview, layering, component responsibilities.
- **`docs/migration-plan.md`** — Phase 2/3/4 roadmap.
- **`docs/api-contract.md`** — Full HTTP API contract (request/response examples).
- **`docs/MEMORY-SETUP.md`** — Memory system setup and usage guide.
- **`CHANGELOG.md`** — Version history and notable changes.
- **`CONTRIBUTING.md`** — Contribution guidelines and development process.
- **Authoritative plan:** tracked outside this repository; `docs/migration-plan.md` is the in-tree phase roadmap.

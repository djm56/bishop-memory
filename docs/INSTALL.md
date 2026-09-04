# Installing bishop-memory with bishop-harness

This guide walks an operator through installing bishop-memory as a queryable memory service, registering it with Claude Code, and integrating it into the bishop-harness workflow.

## What You're Installing

**bishop-memory** is a local single-binary HTTP service — Go + Gin + SQLite + FTS5 — that indexes and queries your harness's memory tree. It exposes two entry points:

- **HTTP API** (`cmd/memoryd`) — the core service. Listens on loopback (`127.0.0.1:8787` by default).
- **MCP adapter** (`cmd/mcpd`) — a stdio Model Context Protocol server that proxies the HTTP API, so Claude Code agents can call the 15 memory tools as native MCP functions.

Together, they give your crew a searchable, queryable view of your harness memory **without changing how the harness records state**. The harness remains the authoritative owner of `.claude/memory/`; bishop-memory imports and reads it but never writes back.

## Prerequisites

### Required

- **Go 1.25.5** or later (needed for building from source).
- **No C toolchain required.** bishop-memory uses `modernc.org/sqlite`, a pure-Go SQLite implementation. `make build` works on any platform without a C compiler.

### Optional

These tools are needed only for specific operations, not for daemon installation:

- **`curl`** — required for manual health checks (`make health`) and testing the HTTP API.
- **`jq`** — required for parsing JSON in `make health`.
- **`sqlite3`** — required only for manual database inspection, migration checks, and troubleshooting queries. The service itself does not require it.

## Build and Initialize

### Step 1: Build the binary

```bash
cd /path/to/bishop-memory
make build
```

This compiles `cmd/memoryd` into `./bin/memoryd`. The binary is statically linked — it depends only on the Go runtime and the Linux kernel (or macOS kernel) with no dynamic libc or shared libraries.

### Step 2: Initialize the database

```bash
make init-db
```

This creates `data/memory.db` (the default path) and applies the schema from `db/schema.sql`. The database uses WAL (Write-Ahead Logging) mode for concurrent read/write access. Three files will be created:

- `data/memory.db` — the main SQLite database.
- `data/memory.db-shm` — shared memory file (WAL coordination).
- `data/memory.db-wal` — write-ahead log.

All three are essential; never delete the `-shm` or `-wal` files manually. They are cleaned up automatically when the database is closed.

### Step 3: Verify the build

```bash
./bin/memoryd --version  # if supported, or just:
ls -lh bin/memoryd
```

You should see the binary is roughly 20–30 MB (pure static Go binary).

## Run Locally (First Look)

Start the service in the foreground to verify it works:

```bash
make run
```

Output should look like:

```
[http] listening on 127.0.0.1:8787
```

In another terminal, check health:

```bash
make health
```

Expected output (200 OK):

```json
{
  "ok": true,
  "service": "bishop-memory",
  "storage": "sqlite",
  "schema": "ok"
}
```

Press Ctrl-C in the first terminal to stop.

## Install as a Managed Service

### macOS (launchd)

Register `com.bishop-memory.memoryd` to start at login and restart on exit:

```bash
scripts/install-daemon.sh
```

To preview what the script will do without installing:

```bash
scripts/install-daemon.sh --dry-run
```

The script:
- Builds `memoryd` into `./bin/memoryd`.
- Renders the plist template `scripts/com.bishop-memory.memoryd.plist` (substituting `__BISHOP_ROOT__` with your checkout path).
- Backs up any existing plist to `~/Library/LaunchAgents/com.bishop-memory.memoryd.plist.bak` (first run only).
- Installs the plist to `~/Library/LaunchAgents/com.bishop-memory.memoryd.plist`.
- Loads the agent with `launchctl load -w`.

Check status:

```bash
launchctl list | grep com.bishop-memory.memoryd
log stream --predicate 'process == "memoryd"'  # tail logs
```

Unload it with:

```bash
launchctl unload ~/Library/LaunchAgents/com.bishop-memory.memoryd.plist
```

### Linux (systemd)

**Root privileges required.** Install as a system service running under a dedicated non-root account:

```bash
sudo scripts/install-daemon-linux.sh
```

To preview without making changes:

```bash
scripts/install-daemon-linux.sh --dry-run
```

The script performs these steps:

1. **Detects platform and architecture** — checks that `/run/systemd/system` exists (systemd guard), maps `uname -m` to a Go `GOARCH`, and refuses on unsupported architectures (currently amd64 and arm64 only).

2. **Builds the binary** (no root needed) — prefers a fresh local build with `go build` if Go is installed, falls back to a prebuilt `bin/memoryd-linux-<arch>` cross-compiled binary otherwise. This lets you build on one machine and deploy to another without a Go toolchain.

3. **Prints privileged commands** — creates a dedicated `bishop-memory` system user and group, installs the systemd unit to `/etc/systemd/system/bishop-memory.service`, and grants exact permissions (never world-readable, never recursive). **These steps require root and will be printed before execution.**

4. **If not root**, prints the exact `useradd`, `groupadd`, and `systemctl` commands you need to run. Copy/paste them or re-run the whole script with `sudo`.

5. **If root**, performs the install and runs a post-start health check (polls `systemctl is-active` and prints logs if the service fails to reach "active" within 10 seconds).

Check status:

```bash
systemctl status bishop-memory.service
journalctl -u bishop-memory.service -f  # tail logs
```

**Important:** The installer does not grant traversal *above* your checkout directory. If the checkout is in a restrictive parent directory (e.g., a narrow home directory), the service may fail to start. The health check catches this and prints the error.

## Deploying to a Remote Server

Build for your target platform, transfer the checkout, and run the installer on the server.

### Step 1: Build cross-compiled binaries

```bash
make dist-linux-amd64    # For x86-64
make dist-linux-arm64    # For ARM64
make dist-linux          # For both
```

Binaries land in `./bin/memoryd-linux-amd64`, etc. They are statically linked and require only the Linux kernel and systemd on the target host.

### Step 2: Transfer to the server

**Option A: Clone on server, transfer binary**

```bash
# On the server (interactive, you can sudo if needed):
git clone <your-repo-url> /opt/bishop-memory
mkdir -p /opt/bishop-memory/bin

# On your local machine (after building):
scp ./bin/memoryd-linux-amd64 user@server:/opt/bishop-memory/bin/memoryd-linux-amd64
```

**Option B: Transfer entire checkout with rsync**

```bash
rsync -a --exclude='.git' . user@server:/opt/bishop-memory/
```

**Option C: Clone and build on server**

```bash
# On the server:
git clone <your-repo-url> /opt/bishop-memory
cd /opt/bishop-memory
make build
```

### Step 3: Run the installer

```bash
cd /opt/bishop-memory
sudo scripts/install-daemon-linux.sh
```

## Registering with Claude Code

The MCP adapter (`cmd/mcpd`) registers bishop-memory as an MCP server so Claude Code agents can call memory tools.

```bash
scripts/install-claude.sh --project-root /path/to/bishop-harness
```

**⚠️ Important: Pass `--project-root` explicitly.** The script refuses to default to the current directory silently because a stray `CLAUDE.md` created in the wrong directory (e.g., a subproject) will never be loaded by Claude Code, and you may not notice. If you want to use the current directory, pass `--yes` instead:

```bash
cd /path/to/bishop-harness
scripts/install-claude.sh --yes
```

The script:

1. **Builds `mcpd`** into `./bin/mcpd`.

2. **Registers the MCP server** via one of two paths:
   - **CLI path:** If `command -v claude` succeeds, uses `claude mcp add --scope user` (idempotent — checks `claude mcp get bishop-memory` first).
   - **JSON fallback:** If no `claude` CLI, patches `~/.claude/claude.json` (or `~/.claude.json` on legacy installs) under the `mcpServers` key (idempotent — detects existing entries).

3. **Appends a section to `<project-root>/CLAUDE.md`** with the `## bishop-memory` heading and configuration details. Backs up any existing `CLAUDE.md` to `CLAUDE.md.bak` (first run only). Is idempotent — re-runs detect the `memory_search` marker and skip the append.

### Configuration

The script sets two environment variables that control the MCP adapter:

- **`BISHOP_MEMORY_URL`** (default: `http://127.0.0.1:8787`) — Base URL of the bishop-memory HTTP API.
- **`BISHOP_HARNESS`** (default: `claude-code`) — Harness prefix for composing agent identity. When an agent calls a write tool like `flight_recorder_append`, mcpd composes the stored actor as `<BISHOP_HARNESS>:<agent>` (e.g., `claude-code:bishop`).

Both are set automatically by the install script. If you need to change them later, edit `~/.claude/claude.json` or re-run `install-claude.sh`.

## Seeding the Index from Existing Memory

When bishop-memory first starts, the database is empty. To populate it from your harness's `.claude/memory/` tree:

### Step 1: Ensure the service is running

```bash
# If running locally:
make run

# If installed as a service:
systemctl start bishop-memory.service  # Linux
launchctl load ~/Library/LaunchAgents/com.bishop-memory.memoryd.plist  # macOS
```

### Step 2: Trigger an import

```bash
curl -X POST http://127.0.0.1:8787/v1/documents/sync \
  -H 'Content-Type: application/json' \
  -d '{"root": "/path/to/.claude/memory"}'
```

The importer walks the memory tree, computes SHA-256 hashes of each file, and populates the FTS5 search index. Re-running the sync is cheap — it uses content hashing to skip unchanged files, so only modified documents are re-indexed.

### Step 3: Verify

```bash
# Count imported documents
curl -s http://127.0.0.1:8787/v1/documents/sync -X GET | jq '.documents'

# Or via sqlite3:
sqlite3 data/memory.db 'SELECT COUNT(*) FROM documents;'

# Try a search
curl -s 'http://127.0.0.1:8787/v1/memory/search?q=mission' | jq
```

## Verifying Installation

### Health Check

```bash
curl http://127.0.0.1:8787/healthz | jq
```

Three outcomes:

**Success (200 OK):**
```json
{
  "ok": true,
  "service": "bishop-memory",
  "storage": "sqlite",
  "schema": "ok"
}
```

**Schema incomplete (503 Service Unavailable):**
```json
{
  "ok": false,
  "storage": "sqlite",
  "error": "schema incomplete",
  "missing": ["missions", "documents"]
}
```
This means the database exists but the schema was not applied. Run `make init-db` and try again.

**Database unavailable (503 Service Unavailable):**
```json
{
  "ok": false,
  "storage": "sqlite",
  "error": "database unavailable"
}
```
This means the database file could not be opened or locked. Check that the path in `DB_PATH` is writable and the database is not corrupted.

### Search and Mission Read

Once seeded, test search through the MCP adapter. In Claude Code or a compatible MCP client:

```
Tool: memory_search
Args:
  q: "mission"
  limit: 10
```

Or read a specific mission:

```
Tool: mission_get
Args:
  id: "mission-20260821-01"
```

## What Your Crew Gets

The MCP adapter exposes **15 tools** grouped by use case. All tools are composed from the HTTP API with agent identity handled automatically.

### Search and Discovery (no agent identity required)

- **`memory_search`** — Search imported Markdown/JSONL documents via FTS5 (full-text search). Multi-word is implicit AND; phrases in quotes; prefix wildcard `*`. Returns ranked snippets.
- **`mission_list`** — List all missions, newest-updated first. Optional status filter.
- **`mission_get`** — Fetch a single mission by ID.
- **`mission_steps_list`** — List steps in a mission (PROGRESS.md rows).
- **`finding_list`** — List findings from the ledger, optional status filter (proposed/approved/applied/rejected/retired/superseded).
- **`pattern_list`** — List advisory patterns.
- **`service_record_list`** — List performance/calibration observations about agents, optional agent filter.

### Recording and Proposing (agent identity composed automatically)

- **`mission_create`** — Create a new mission.
- **`mission_update`** — Update mission status, owner, outcome, priority, blockers, next action.
- **`mission_step_record`** — Record a step execution (agent name, status, notes, timestamps).
- **`flight_recorder_append`** — Append an event to the audit log. Agent identity captured automatically.
- **`finding_append`** — Propose a finding. Always created with status='proposed'; only the operator can advance status.
- **`pattern_append`** — Record an advisory pattern (non-binding).
- **`service_record_append`** — Record a calibration note about an agent. Source is 'self-reported' or 'bishop-observed'.
- **`documents_sync`** — Trigger a re-import of the memory tree (incremental via SHA-256 hashing).

### Two Governance Rules

1. **Findings are operator-controlled.** A finding's `status`, `approver`, and `date_approved` cannot be set through the API. Agents can only *create* findings (always with status='proposed'). Status advancement stays the operator's job, outside the service. This prevents agents from advancing their own proposals.

2. **Directives are read-only via the API.** Agents can read `directives` to learn the binding rules that govern them, but cannot create or modify directives. Directives are human-ratified only and live in `.claude/memory/reference/DIRECTIVES.md`.

For full request/response details, see `docs/api-contract.md`.

## Security Posture

### Current State

- **Loopback-only by default.** Binds to `127.0.0.1:8787`. Only local processes can reach the service. Configurable via `HTTP_HOST` env var, but the service logs a loud warning on startup if you bind to a non-loopback address.
- **HTTP listener port configurable** via `PORT` env var (default `8787`).
- **Single-writer, WAL-enabled database.** Concurrent reads don't block writes.
- **Document import path guarded.** The `/v1/documents/sync` endpoint checks for path traversal (symlink escapes, `../` escapes) and refuses to walk the filesystem root.

### Known Limitations

**No authentication.** Every endpoint is open read/write to any caller that can reach the service. Access control happens at the network level:

- **Loopback-only (recommended)** — bind to `127.0.0.1` or `::1`. Only your local machine can reach it.
- **SSH tunnel (for remote)** — tunnel `localhost:8787` on your machine over SSH to the remote server. This provides encryption and authentication via your SSH key.
- **Firewall rules (if exposed)** — restrict access at the network level. This is less safe than loopback or SSH tunnel.

**No rate limiting, audit archival, or allow-list.** See the Security Posture section in `README.md` for the full picture.

### Operational Recommendations

1. **Always use loopback** on the server — never set `HTTP_HOST` to `0.0.0.0` or a non-loopback address in production.
2. **Use SSH tunneling** for remote access: `ssh -L 8787:127.0.0.1:8787 user@server`.
3. **Firewall the server** — restrict SSH access to authorized users only.
4. **Monitor logs** — watch `journalctl -u bishop-memory.service` for unexpected `/v1/documents/sync` requests with unusual root paths.

## Troubleshooting

### The service won't start

**Symptom:** `systemctl status bishop-memory.service` shows "inactive" or "failed".

**Check:**

```bash
# View recent logs
journalctl -u bishop-memory.service -n 50

# Verify file permissions
ls -la /var/lib/bishop-memory
ls -la /path/to/checkout/bin/memoryd
```

**Possible causes:**

- Database file is corrupted or locked — try `make reset-db` (destructive).
- Parent directory traversal denied — the service account cannot reach the checkout. Check parent directory permissions.
- Port already in use — another process is listening on `8787`. Change `PORT` env var or stop the conflicting service.

### Health check returns "schema incomplete"

**Symptom:** `/healthz` returns 503 with `"error": "schema incomplete"` and lists missing tables.

**Cause:** Database exists but schema was not applied, or the schema is stale.

**Fix:**

```bash
make init-db      # Re-apply schema (safe — idempotent)
systemctl restart bishop-memory.service
curl http://127.0.0.1:8787/healthz | jq
```

### Port 8787 already in use

**Symptom:** Service logs "bind: address already in use" or similar.

**Check:**

```bash
# macOS
lsof -i :8787

# Linux
ss -tulpn | grep 8787
```

**Fix:** Either stop the conflicting process or change `PORT`:

```bash
PORT=8788 make run
```

Or set it in `.env`:

```bash
cat > .env <<EOF
PORT=8788
HTTP_HOST=127.0.0.1
DB_PATH=data/memory.db
EOF
```

### Cannot connect to the service

**Symptom:** Agents or tests fail with "connection refused" or timeout.

**Check:**

1. Service is running:
   ```bash
   systemctl status bishop-memory.service  # Linux
   launchctl list | grep memoryd            # macOS
   ```

2. Listening on the right address:
   ```bash
   curl -v http://127.0.0.1:8787/healthz
   ```

3. SSH tunnel is open (if remote):
   ```bash
   ssh -L 8787:127.0.0.1:8787 user@server
   # Keep this session open; in another terminal:
   curl http://127.0.0.1:8787/healthz
   ```

### Search returns no results

**Symptom:** `memory_search` queries return empty results even though documents were imported.

**Check:**

```bash
# Are documents imported?
sqlite3 data/memory.db 'SELECT COUNT(*) FROM documents;'

# Is the FTS5 index built?
sqlite3 data/memory.db 'SELECT COUNT(*) FROM documents_fts;'

# Try a simple search
curl 'http://127.0.0.1:8787/v1/memory/search?q=mission'
```

**Cause:** Documents not yet imported, or FTS5 index not rebuilt.

**Fix:**

```bash
# Re-sync (incremental — only re-indexes changed files)
curl -X POST http://127.0.0.1:8787/v1/documents/sync \
  -H 'Content-Type: application/json' \
  -d '{"root": "/path/to/.claude/memory"}'

# If still empty, check query syntax (FTS5 is strict)
# OK: word, "phrase in quotes", word*
# NOT OK: wor* (prefix requires at least one whole word first)
```

### The MCP adapter is registered but returns nothing

**Symptom:** Claude Code recognizes the bishop-memory MCP, but calling tools returns no results or an error.

**Check:**

1. HTTP API is running:
   ```bash
   curl http://127.0.0.1:8787/healthz
   ```

2. MCP environment variables are set:
   ```bash
   # In ~/.claude/claude.json or the project CLAUDE.md:
   grep -A 5 "bishop-memory" ~/.claude/claude.json | head -10
   ```

3. The configured URL is reachable:
   ```bash
   curl -v http://127.0.0.1:8787/v1/missions
   ```

**Fix:** Restart the MCP adapter or re-run the installer:

```bash
scripts/install-claude.sh --project-root /path/to/bishop-harness
```

## Next Steps

- See **`docs/MEMORY-SETUP.md`** for day-to-day usage patterns and how the importer works.
- See **`docs/api-contract.md`** for the full HTTP API request/response contract.
- See **`docs/architecture.md`** for the service layering and design.
- See **`README.md`** for configuration, development, and additional troubleshooting.

## Note on Installation

**No install script was executed during the creation of this guide.** Every script in this codebase was edited and verified via `bash -n` (syntax check) only. The scripts are syntactically valid and ready to run, but they are unexercised end-to-end. You are the first person to run them in a real environment. Watch the output carefully on the first run, particularly the daemon install — it may reveal environment-specific issues that testing could not catch.

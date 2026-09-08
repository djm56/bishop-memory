# Harness Integration — bishop-memory

## Overview

Bishop-memory can operate in two modes:

- **Standalone** — each harness owns its memory entirely. Mission IDs are derived locally from folders and logs. No network calls, and bishop-memory doesn't need to be installed. This is the default.
- **Central** — mission IDs are allocated centrally by bishop-memory so they are unique across every harness. Journal rows are also registered with the central service. Requires bishop-memory to be running.

The `scripts/connect-harness.sh` installer makes a harness aware of bishop-memory and configures it for either mode. The connection is idempotent — re-running the installer detects what's already in place and skips it.

## What "Connected" Means — Four Changes

When you connect a harness, four files are created or patched:

### 1. `.claude/bishop-memory.conf`

A per-harness configuration file containing:

- `BISHOP_MEMORY_MODE` — `standalone` or `central`
- `BISHOP_MEMORY_URL` — base URL of the bishop-memory HTTP API
- `BISHOP_HARNESS` — unique identity for this harness (required in central mode)

**Why it exists.** The MCP server registration (`.mcp.json`, step 2 below) carries `BISHOP_HARNESS` as an environment variable. But the post-mission hooks are separate processes that don't inherit the MCP server's environment — they read from this config file instead. Two readers, one identity, and they must agree. The config file lives outside `.claude/` (except it starts there in the name) because it is **live, per-installation state** — not doctrine or tooling — and is gitignored so each clone carries its own identity rather than inheriting a committed one.

The file is created from `scripts/templates/bishop-memory.conf.example` in bishop-memory, with `BISHOP_HARNESS` set to the harness name you provide.

### 2. `.mcp.json` at the harness root

The project-scope MCP server registration. A **project-scope** server (not user-scope) is essential because user-scope is one global value shared by every project running on the workstation. If several harnesses registered at user scope, they would be indistinguishable, which defeats central ID allocation.

Shape of the entry:

```json
{
  "mcpServers": {
    "bishop-memory": {
      "type": "stdio",
      "command": "/path/to/bishop-memory/bin/mcpd",
      "args": [],
      "env": {
        "BISHOP_HARNESS": "harness-name",
        "BISHOP_MEMORY_URL": "http://127.0.0.1:8787"
      }
    }
  }
}
```

The installer merges into an existing `.mcp.json` if one is already present, rather than overwriting it — a harness may have other MCP servers registered.

**Why project-scope.** User-scope `~/.claude.json` applies to every project on the workstation, and every harness would read the same `BISHOP_HARNESS` value. That makes several harnesses indistinguishable at the API level, so they collide on the same mission IDs. Project-scope ensures each harness can carry its own identity.

**Approval required after restart.** In Claude Code, newly-created `.mcp.json` servers are inert until approved. After the installer runs, restart Claude Code. When prompted, approve the `bishop-memory` server. Until then, the MCP tools are unavailable and missions cannot use central ID allocation.

### 3. `.gitignore` additions

Three exclusion rules so the live config and the hook's sync state are never committed:

```
.claude/bishop-memory.conf
.claude/memory/state/.bishop-memory-cursor
.claude/memory/state/.bishop-memory-lock
```

The first is the live config — each clone needs its own. The other two are state files used by the post-mission hook to track sync position without colliding when multiple harnesses update the same central journal.

### 4. Two doctrine file patches

Central mode requires changes to how missions are set up. Two skeleton files are patched:

#### `.claude/skills/mission-lifecycle/SKILL.md`

A new subsection `### Central Mode — When bishop-memory Owns The ID` is inserted after `### Working Out The Next One`. It explains:

- When to use local derivation (standalone mode or no config file) vs. central allocation (central mode)
- How central allocation works: call `mission_allocate` to get an ID atomically
- Why falling back to local derivation in central mode is a hard stop: two harnesses would compute the same `NN`, reusing an ID in an append-only journal makes it ambiguous

#### `.claude/agents/bishop.md`

The `## Standing Up A New Mission` section opening paragraph is updated to branch on `.claude/bishop-memory.conf`. Instead of always deriving locally, it now says:

- **Standalone or no file** — derive the ID locally as before
- **Central mode** — call `mission_allocate` with the mission title

Then it points to the Mission IDs section of SKILL.md for the full rules.

## Why The Config File When MCP Already Carries `BISHOP_HARNESS`

The MCP server registration (`.mcp.json`) sets `BISHOP_HARNESS` in the MCP process's environment. That works fine for MCP tools like `mission_allocate`. But post-mission hooks — the processes that run after a mission closes to mirror journal rows to bishop-memory — are separate shell processes invoked by Claude Code. They don't inherit the MCP server's environment.

Those hooks read from `.claude/bishop-memory.conf` directly. So two processes, two readers:

1. **MCP server** — reads `BISHOP_HARNESS` from `.mcp.json` environment
2. **Post-mission hook** — reads from `.claude/bishop-memory.conf`

They must agree, so both values are kept in sync. The config file is the source of truth; the MCP registration echoes it.

## Why Project-Scope Rather Than User-Scope

Claude Code supports two registration scopes:

- **User-scope** (`~/.claude.json`) — applies to every project on the workstation
- **Project-scope** (`.mcp.json` at project root) — applies to this project only

If several harnesses registered bishop-memory at user scope, they would all read the same `BISHOP_HARNESS` value from the same environment variable. Central ID allocation depends on each harness having a unique identity — if they all have the same name, they collide on the same mission IDs, and ID reuse corrupts the journal.

Project-scope ensures each harness carries its own registration with its own `BISHOP_HARNESS` value, so ID collision is impossible.

## Central Mode Requires A Running Service

In central mode, an unreachable bishop-memory service is a hard stop for mission startup, not a fallback to local derivation. The reason: local derivation is right for one harness and wrong for several. Two harnesses both computing the same `NN` locally would reuse the same ID, and a reused ID in an append-only journal is ambiguous — the one thing the journal exists to prevent.

A blocked mission start is recoverable (restart the service, try again). A duplicate ID is not.

The installer checks if bishop-memory is reachable (`curl -s <url>/healthz`) and warns if it isn't in central mode.

## Usage

### Basic Connection — Central Mode

```bash
/path/to/bishop-memory/scripts/connect-harness.sh \
  --harness-root /path/to/my-harness \
  --harness-name my-harness-id
```

This sets `BISHOP_MEMORY_MODE=central` and `BISHOP_HARNESS=my-harness-id`.

### Standalone Mode

```bash
/path/to/bishop-memory/scripts/connect-harness.sh \
  --harness-root /path/to/my-harness \
  --harness-name my-harness-id \
  --mode standalone
```

Standalone mode doesn't require bishop-memory to be running and doesn't need a unique `--harness-name` (it's ignored). The harness derives mission IDs locally as before.

### Dry Run

```bash
/path/to/bishop-memory/scripts/connect-harness.sh \
  --harness-root /path/to/my-harness \
  --harness-name my-harness-id \
  --dry-run
```

Prints every intended change without modifying files. Use this to inspect what the installer will do before running for real.

### Custom bishop-memory URL

```bash
/path/to/bishop-memory/scripts/connect-harness.sh \
  --harness-root /path/to/my-harness \
  --harness-name my-harness-id \
  --url http://bishop-memory.example.com:8787
```

The URL defaults to `http://127.0.0.1:8787`. Set a different one if bishop-memory is running on a different host or port.

## Idempotency

Re-running the installer against an already-connected harness detects what's in place and skips it:

```bash
$ ./connect-harness.sh --harness-root /path/to/harness --harness-name my-harness
[connect-harness] step 1: .claude/bishop-memory.conf
[connect-harness]   [skip] already exists
[connect-harness] step 2: .mcp.json
[connect-harness]   [skip] bishop-memory server already registered in /path/to/harness/.mcp.json
[connect-harness] step 3: .gitignore
[connect-harness]   [skip] .gitignore already has required rules
[connect-harness] step 4: .claude/skills/mission-lifecycle/SKILL.md
[connect-harness]   [skip] already has central-mode section
[connect-harness] step 5: .claude/agents/bishop.md
[connect-harness]   [skip] already has central-mode section
```

## Undoing the Connection

To disconnect a harness from bishop-memory:

1. Remove `.claude/bishop-memory.conf` (or rename it to `.claude/bishop-memory.conf.bak` to preserve it).
2. Remove the `bishop-memory` server entry from `.mcp.json` at the harness root.
3. Remove the three `.gitignore` rules added by the installer (or revert to a prior commit).
4. Remove or revert the patches to `.claude/skills/mission-lifecycle/SKILL.md` and `.claude/agents/bishop.md`.

**Backup files**: The installer creates `.bak` files of any modified file the first time it runs. You can use those to restore the original state.

```bash
# Restore all backed-up files
cd /path/to/harness
for f in $(find . -name '*.bak'); do
  mv "$f" "${f%.bak}"
done
```

## After Running The Installer

1. **Restart Claude Code.** The new `.mcp.json` server registration requires a restart to be discovered.
2. **Approve the server when prompted.** Claude Code will ask to approve the new project-scope `bishop-memory` server. Until you do, MCP tools are unavailable.
3. **In central mode, verify bishop-memory is running.** The installer checks reachability and warns if the service is not running. Ensure it is before starting a mission.

If you're switching from standalone to central mode on an existing harness, any missions that exist locally will keep their locally-derived IDs. New missions will be allocated from the central service.

## Memory Reconciliation — `scripts/reconcile-memory.py`

### What the Reconciler Does

The harness writes its memory as Markdown. Bishop-memory holds a derived copy of that memory in its SQLite database so it can serve MCP tools and provide search. The two must stay in sync, but they can drift in two ways:

1. **During development** — if you edit FINDINGS.md, PATTERNS.md, or PROGRESS.md directly in the harness, those changes exist in Markdown but not yet in the database.
2. **On harness reconnection** — if you migrate a harness from standalone mode (where it accumulated memory locally) to central mode (where bishop-memory becomes the source of truth for some operations), the accumulated history must be copied into bishop-memory.

`scripts/reconcile-memory.py` syncs the Markdown truth into the database using natural-key dedupe so it is **idempotent**: running it twice changes nothing the second time.

The reconciler parses:

- `state/MISSION-ARCHIVE.md` and `state/CURRENT-MISSION.md` → creates/updates missions
- `missions/*/PROGRESS.md` → creates mission steps
- `findings/FINDINGS.md` → creates findings
- `findings/PATTERNS.md` → creates patterns
- `findings/service-records/*.md` → creates service records
- `state/FLIGHT-RECORDER.md` → creates journal events (opt-in, see below)

### Natural Key Dedupe

Each entity type has a natural key — the minimal set of fields that uniquely identifies it:

| Entity | Natural Key |
|--------|-------------|
| missions | `id` |
| mission_steps | `(mission_id, step)` |
| findings | `(finding_date, target, suggestion)` |
| patterns | `name` |
| service_records | `(agent, record_date, title)` |

The reconciler fetches the existing rows from the API, indexes them by natural key, and only creates rows that are missing. For missions, it also **updates** if `status` or `outcome` has drifted, so a Markdown edit to a mission's status is reflected in the database.

### Why Markdown Is The Source of Truth

The harness mission files (BRIEF.md, PROGRESS.md) are the primary record. They are checked into git and survive a database reset, a service crash, or a harness migration. The database is a secondary, derived copy optimized for search and MCP tool access. Syncing from Markdown → database preserves that hierarchy.

The journal (FLIGHT-RECORDER.md) is a partial exception: a post-mission hook mirrors journal rows live to the API, so the journal and database stay current during normal operation. The reconciler's journal sync is opt-in via `--include-journal` and defaults off precisely because the hook keeps it current and reconciling it can be expensive (per-mission enumeration with a 100-row cap).

### Typical Usage

After connecting a harness or editing memory Markdown directly:

```bash
cd /path/to/harness
$BISHOP_MEMORY_HOME/scripts/reconcile-memory.py --root .claude/memory
```

Verify the changes with a dry run first:

```bash
$BISHOP_MEMORY_HOME/scripts/reconcile-memory.py --root .claude/memory --dry-run
```

### Flags

- `--root <path>` (required) — Path to the `.claude/memory` tree.
- `--url <url>` (optional) — Base URL of the bishop-memory API. Defaults to `http://127.0.0.1:8787`, also honoring `BISHOP_MEMORY_URL` env var.
- `--dry-run` — Parse and report what would be written; send nothing.
- `--include-journal` — Reconcile the flight-recorder (journal) as well. Default: off (the post-mission hook keeps it current).
- `--no-sync-documents` — Skip `POST /v1/documents/sync`. Default: sync documents first so the full-text index is current in the same pass.

### The Journal Flag — Why It's Opt-In

The flight-recorder journal cannot be fully enumerated through the API (hard cap of 100 rows per request, no offset or pagination). Global dedupe is therefore impossible.

The flag is opt-in with a safety mechanism: when `--include-journal` is given, the reconciler uses per-mission enumeration with `?mission_id=<id>&limit=100`. If a mission's journal reaches exactly 100 rows, it skips that mission and warns — at the cap you cannot tell if the list is complete or truncated, and guessing risks duplicates.

In normal operation, the post-mission hook mirrors journal rows live, so the journal is already current and this flag is unnecessary. It exists only to catch rows that were missed while the service was down.

### Exit Codes

- 0 — Success (nothing to do, or all reconciliation succeeded).
- 1 — One or more operations failed (HTTP errors, validation errors).

### Idempotent First-Time Run

`scripts/reconcile-memory.py` subsumes the old `scripts/backfill-memory.py` for first-time backfill operations. It works the same way on an empty database as on a populated one: fetches existing rows, indexes them by natural key, and creates anything missing. The natural-key dedupe ensures it is safe to run against a freshly-initialized database where all tables are empty.

This means:
- First-time setup (empty database) — works as expected, creates everything
- Subsequent runs (populated database) — creates missing rows, updates drifted fields, all idempotent

### Backward Compatibility — `scripts/backfill-memory.py`

The old `scripts/backfill-memory.py` is retained as a compatibility passthrough. It forwards its arguments directly to `reconcile-memory.py` and execs it, so the old invocation name continues to work without modification. The passthrough carries no parsing or reconciliation logic of its own — a deliberate choice to prevent the two files from drifting again as they did before the consolidation.

When you call `backfill-memory.py --root <path>`, it behaves exactly as `reconcile-memory.py --root <path>` does. For new code, call the reconciler directly; for existing scripts that reference `backfill-memory.py`, the old name keeps working.

### `BISHOP_MEMORY_HOME` Configuration

The reconciler lives in bishop-memory, not in the harness. The harness finds it via the `BISHOP_MEMORY_HOME` environment variable set in `.claude/bishop-memory.conf`, written there by `scripts/connect-harness.sh`.

If you need to call the reconciler from a harness closing sequence, read `BISHOP_MEMORY_HOME` from the config and invoke it:

```bash
# In a harness post-mission hook
BISHOP_MEMORY_HOME=$(grep ^BISHOP_MEMORY_HOME= .claude/bishop-memory.conf | cut -d= -f2)
"$BISHOP_MEMORY_HOME/scripts/reconcile-memory.py" --root .claude/memory
```

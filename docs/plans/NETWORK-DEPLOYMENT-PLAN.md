# Network Deployment — Plan

Status: phase 1 built on branch `feat/network-deploy` (§6 lists what is in
it and what was tested). Phases 2 and 3 are not started.
Date: 2026-10-08.

The goal: run memoryd on any Unix machine — an Ubuntu server, another Linux
box, a Mac — and reach it across the network from the machines that run
harnesses, the triage agents and the operator's browser, safely.

## 1. What exists today, and what stops it working over a network

| Fact | Consequence |
|---|---|
| memoryd binds `127.0.0.1` by default. `HTTP_HOST` can bind elsewhere, and it only logs a warning. | Nothing stops it being exposed. |
| There is no authentication at all. The agent boundary is the mcpd tool list, not the HTTP API. | Anyone who can reach the port can read every finding and change any record. Fine on loopback; unacceptable on a network. |
| memoryd reads `db/schema.sql` relative to its working directory. | The binary only runs from a checkout. A server install needs the repository on the server. |
| Document sync reads memory trees from the server's own disk (`harnesses.memory_root`, `POST /v1/documents/sync {"root": ...}`). The reconciler asks for a sync by path; a mission update re-imports by path. | On a remote server those paths do not exist, so briefs, debriefs, search and crew stay empty. |
| `scripts/clean-scratch.py` and `scripts/backfill-mission-links.py` open the SQLite file directly. | They only work on the server. `clean-scratch` also moves files, which live on the harness machine. |
| The triage processor gives its agents read access to each harness checkout, derived from `harnesses.memory_root`. | Triage must run where the harness checkouts are. |
| Installers exist for macOS (`scripts/install-daemon.sh`, launchd user agent) and Linux (`scripts/install-daemon-linux.sh`, systemd system service). | A good base; they need host, port, TLS and key options. |

Clients that call the API: `mcpd` (`BISHOP_MEMORY_URL`), the harness hook
`state-continuity.sh` (curl, and the reconciler through
`BISHOP_MEMORY_HOME`), `scripts/triage_common.py` (every triage script),
`scripts/reconcile-memory.py`, `scripts/triage-run.sh` (curl), and the two
pages in the browser.

## 2. Shape of the solution

```
  harness machine(s)                       server (Ubuntu, any Linux, or a Mac)
  ─────────────────                        ────────────────────────────────────
  Claude Code / OpenCode ─ mcpd ──┐
  hook ── reconciler (push files) ┼── HTTPS or private network ──▶ memoryd ── SQLite
  triage agents ── triage-run.sh ─┤        Authorization: Bearer <key>
  browser ── /triage, /missions ──┘
```

- **One server, many clients.** memoryd runs on the server. Every client
  sends an API key. Files never need to be on the server: harness machines
  push them.
- **Default stays as it is.** On loopback with no keys configured, memoryd
  behaves exactly as today, so an existing single-machine install and the
  nightly triage keep working untouched.

## 3. Security

### 3.1 API keys

- **Named keys, stored hashed.** A keys file (`BISHOP_API_KEYS_FILE`,
  default `<data dir>/api-keys`) holds one line per key:
  `<name> <sha256 of key>`. memoryd never stores a key in clear.
- **`memoryd keys add <name>`** generates a key (32 random bytes,
  `bm_` + base64url), appends its hash, and prints the key once.
  `memoryd keys list` shows names; `memoryd keys revoke <name>` removes one.
  The file is re-read on change, so adding or revoking needs no restart.
- **One key per client.** `kirsch-mac`, `triage`, `browser-laptop`, … A lost
  machine is revoked without touching the others, and the access log names
  the key behind every request.
- **`BISHOP_API_KEY`** in memoryd's environment is accepted as one extra key
  named `env`, for a quick single-key setup and for tests.
- **Header:** `Authorization: Bearer <key>` (also `X-API-Key: <key>`).
  Comparison is constant-time over the SHA-256.
- **What needs a key:** every `/v1` route. `/healthz` stays open and says
  only ok/not ok. The two pages (`/triage`, `/missions`) and the icons are
  static and hold no data, so they load without a key; the data they fetch
  is under `/v1`.
- **The browser:** when a `/v1` call answers 401, the page asks for a key,
  keeps it in the browser's local storage, and sends it on every call. A
  "Sign out" control forgets it.

Recommendation, recorded: no user accounts, sessions or roles in phase 1.
One scope (full access) per key. Read-only keys are phase 3.

### 3.2 When keys are required

| Bind | Keys configured | Result |
|---|---|---|
| loopback | none | Open, as today. |
| loopback | some | Keys required. |
| non-loopback | some | Keys required. |
| non-loopback | none | **memoryd refuses to start**, unless `BISHOP_ALLOW_NO_AUTH=1` (logged loudly at every start). |

### 3.3 Transport

An API key over plain HTTP can be read by anyone on the path.

**Operator's setup (decided 2026-10-09):** one Ubuntu server on the
operator's own network, used by the operator alone, no Tailscale. Built:
built-in TLS with a certificate from the operator's own mkcert CA; clients
trust it through `BISHOP_MEMORY_CA_FILE` in `client.env`
(`install.sh client --ca`), which only bishop-memory's clients read, so
nothing else on the Mac changes what it trusts; `--allow-from MAC_IP/32` on
the systemd address filter (the server's own address is added
automatically) plus ufw admitting only the Mac; memoryd under
`/opt/bishop-memory` as a systemd service. Step by step: wiki
[Home Server Setup](../wiki/Home-Server-Setup.md).

The general options, for other layouts:

1. **A private network: Tailscale (or plain WireGuard).** Encrypted end to
   end, nothing exposed to the LAN or internet, works from anywhere. Bind
   memoryd to the server's tailnet address. **Recommended.**
2. **Built-in TLS.** `TLS_CERT_FILE` and `TLS_KEY_FILE` make memoryd serve
   HTTPS itself. Use a certificate from your own CA, mkcert, or Let's
   Encrypt (DNS challenge for a LAN name).
3. **A reverse proxy** (Caddy, nginx) terminating TLS in front of a
   loopback-bound memoryd. Good when the server already runs one.
4. **An SSH tunnel** per client (`ssh -N -L 8787:127.0.0.1:8787 server`).
   No change to memoryd at all; awkward for more than one machine.

memoryd warns at start when it binds a non-loopback address without TLS,
since it cannot tell whether the network itself is encrypted.

### 3.4 Other hardening

- The document push route limits its body (8 MiB per request, 1 MiB per
  file, 500 items). Other routes take small JSON bodies and have no explicit
  limit yet; a global body cap is phase 3.
- Repeated 401s from one address are logged; a lockout is phase 3.
- The systemd unit runs as a dedicated `bishop-memory` user with
  `ProtectSystem=strict`, `ReadWritePaths=<data dir>`, `NoNewPrivileges`.
- Firewall: allow the port only from the private network or LAN
  (`ufw allow from 100.64.0.0/10 to any port 8787` for Tailscale).

## 4. Files over the network: push instead of pull

- **`POST /v1/documents/push`** takes `{"harness", "files": [{"path",
  "rel_path", "content"}], "agents": [{"path", "content"}]}`. `path` is the
  file's absolute path on the client (kept as `source_path`, so rows match
  those a local sync made); `rel_path` is relative to the memory root and
  drives kind and mission id exactly as the walker does. Agents become crew
  rows. Same upsert, hashing and FTS handling as a local sync.
- **`GET /v1/documents/hashes?harness=<name>`** returns `source_path → sha256`
  for one harness, so a client sends only what changed.
- **`POST /v1/documents/delete`** takes `{"paths": [...]}` and removes those
  documents and their search rows.
- **`scripts/push-memory.py --root <memory root> --harness <name>`** walks the
  tree like the importer (same skips), compares hashes, and pushes changes,
  plus the agent definitions beside the root. `--prune` deletes documents of
  that harness whose files are gone.
- **The reconciler** pushes (through the same code) instead of asking for a
  path sync, so it works local or remote. `--no-sync-documents` still skips it.
- **The mission update re-import** stays, but skips silently when the
  harness's memory root is not on this machine.
- **`scripts/clean-scratch.py`** runs on the harness machine, moves files
  there, and removes their documents through `POST /v1/documents/delete`.

## 5. Installing

### 5.1 One binary, no checkout needed

- `db/schema.sql` is embedded in memoryd (`go:embed`), so the binary runs
  from anywhere.
- `make dist` builds `memoryd` and `mcpd` for linux/amd64, linux/arm64,
  darwin/amd64 and darwin/arm64, and packs each with `scripts/`, `db/` and
  the installer into `dist/bishop-memory-<version>-<os>-<arch>.tar.gz`.

### 5.2 The server

`scripts/install.sh server` detects the OS and runs the matching installer
with:

- `--host <addr>` (default `127.0.0.1`; for the network, the tailnet or LAN
  address, or `0.0.0.0`), `--port <n>` (default 8787)
- `--data-dir <dir>` (Linux default `/var/lib/bishop-memory`; macOS default
  `~/Library/Application Support/bishop-memory`)
- `--tls-cert <file> --tls-key <file>` (optional)
- `--first-key <name>`: create the first API key and print it

Linux: a systemd system service under a dedicated user (existing script,
extended). macOS: a launchd user agent (existing script, extended); a Mac
server must stay logged in, or the agent becomes a LaunchDaemon in phase 2.

### 5.3 Each client machine

`scripts/install.sh client --url https://server:8787 --key <key>`:

- installs `mcpd` and the scripts (or uses a checkout),
- writes `~/.config/bishop-memory/client.env` with `BISHOP_MEMORY_URL` and
  `BISHOP_MEMORY_API_KEY` (mode 600), which every script and mcpd read,
- checks the connection with an authenticated call.

### 5.4 Changes in each harness (outside this repository)

The harness owns its configuration (`.claude/bishop-memory.conf`,
`.opencode/bishop-memory.conf`, `.mcp.json`, `opencode.json`). Each needs:

- `BISHOP_MEMORY_URL` pointing at the server,
- `BISHOP_MEMORY_API_KEY` (or reading `client.env`),
- `mcpd`'s env block passing both through,
- the hook's `curl` calls sending `-H "Authorization: Bearer $BISHOP_MEMORY_API_KEY"`.

These are small edits in the harness repositories; the wiki gives the exact
lines. Recorded as a decision: this repository does not edit harness
repositories.

### 5.5 Triage

The triage agents run **on the workstation that holds the harness
checkouts** (today, the Mac), against the remote memoryd with a `triage`
key. That keeps their read access to the harness files and the
`claude`/`opencode` logins where they already are. Running triage on the
server instead would need copies of every harness checkout there; recorded
as a decision, not built.

### 5.6 Moving the existing database

Stop memoryd on the Mac, copy `data/memory.db` to the server's data
directory (`sqlite3 data/memory.db ".backup /tmp/memory.db"` first, for a
consistent copy), start the server, register the harnesses again with
their client-side paths, and point the clients at the server.

### 5.7 Backups

`memoryd backup <file>` writes a consistent copy (`VACUUM INTO`), safe while
memoryd serves. The wiki's Server Install page gives a systemd timer and a
cron line keeping 14 copies; the installer does not set one up (phase 2).

## 6. Phases

1. **Built (2026-10-08):** embedded schema; API keys (file, CLI, middleware,
   start-up guard, fail-closed off loopback); built-in TLS; key support in
   mcpd, the Python scripts, `triage-run.sh` and both pages, with
   `~/.config/bishop-memory/client.env`; document push, hashes and delete
   routes; `push-memory.py`; the reconciler pushing, with a fallback to path
   sync; `clean-scratch.py` over the API; `install.sh` (server and client);
   `make dist`; `memoryd backup`; the wiki Server Install page.
   **Tested here (macOS):** unit tests for keys, the guard and push; memoryd
   with and without keys, on loopback and refused on `0.0.0.0` without one;
   push, re-push and reconcile of the kirsch tree against a keyed server;
   `install.sh client`, mcpd, `triage-run.sh --dry-run` and the cleanup dry
   run reading only `client.env`; a `make dist` archive run from outside a
   checkout. **Not yet run:** `install.sh server` for real on Linux (no Linux
   machine or container here) or on macOS (it would replace the live service);
   TLS with a real certificate.
2. **Next:** a backup timer set up by the installer; a LaunchDaemon option on macOS; a Homebrew formula and a `.deb`;
   CI building release archives on tags; the harness-side edits applied in
   each harness repository.
3. **Later:** read-only keys; lockout after repeated 401s; a global request body cap; audit of key use
   in the flight recorder; the optional PostgreSQL backend
   ([POSTGRES-PLAN.md](POSTGRES-PLAN.md)).

## 7. Decisions

- **N1. Authentication: named API keys, hashed at rest.** No accounts,
  sessions or OAuth. Recommended and chosen.
- **N2. Transport: Tailscale recommended; built-in TLS and reverse proxy
  supported; plain HTTP on a network refused only when there are no keys.**
- **N3. Files are pushed by clients**, not read by the server. Works for
  local and remote alike.
- **N4. Triage runs on the workstation**, not the server (§5.5).
- **N5. Harness repositories are edited by their owners**; the wiki gives the
  lines (§5.4).
- **N6. Loopback with no keys stays open**, so the current install does not
  change until the operator adds a key.

## 8. Risks

- **A key on disk on every client.** `client.env` is mode 600; revoke a key
  the moment a machine is lost.
- **Mixed versions.** An old reconciler against a new server still works (the
  path sync route stays); a new reconciler against an old server falls back
  to the path sync when the push route is missing.
- **Clock skew.** Timestamps are set by the server, so client clocks do not
  matter.
- **Large first push.** A first push of a big memory tree is chunked by the
  client (at most 200 files or 8 MiB per request).

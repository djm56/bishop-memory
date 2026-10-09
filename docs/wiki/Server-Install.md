# Server Install

How to run `memoryd` on another Unix machine (an Ubuntu server, any Linux box, or a Mac) and reach it across the network from the machines that run your harnesses, the triage agents and your browser. Every client sends an API key. The design behind this page is `docs/plans/NETWORK-DEPLOYMENT-PLAN.md`.

## When you need this

| Setup | What to do |
|---|---|
| One machine runs the harnesses, the triage agents and `memoryd` | Nothing. Follow the [User Guide](User-Guide). On loopback with no keys `memoryd` behaves exactly as it always has: no key, no prompt, nothing to configure |
| `memoryd` on a server, harnesses on one or more other machines | This page: install the server, create a key per client, set up each client, point each harness at the server |

```
  harness machine(s)                       server (Ubuntu, any Linux, or a Mac)
  ─────────────────                        ────────────────────────────────────
  Claude Code / OpenCode ─ mcpd ──┐
  hook ── reconciler (push files) ┼── HTTPS or private network ──▶ memoryd ── SQLite
  triage agents ── triage-run.sh ─┤        Authorization: Bearer <key>
  browser ── /triage, /missions ──┘
```

The server never reads the harness files. The machines that hold the harness checkouts push their memory trees to it by content, and the triage agents run on the workstation beside the checkouts (see [Where triage runs](#where-triage-runs)).

## 1. Choose how clients reach the server

> **One server on your own network, used by you?** Follow [Home Server Setup](Home-Server-Setup) instead: it walks through exactly that (built-in TLS with an mkcert CA, a firewall letting only your Mac in, systemd under `/opt`) step by step. This page is the full reference.

An API key sent over plain HTTP can be read by anything on the network path: another device on the Wi-Fi, a compromised router, anyone on a shared LAN. Whoever reads it has full read and write access to every mission and finding. So pick a transport that encrypts, in this order of preference.

| Option | What you do | Client URL |
|---|---|---|
| **Built-in TLS with your own CA**, recommended on your own network | Make a certificate for the server's address with [mkcert](https://github.com/FiloSottile/mkcert) and give it to the installer (`--tls-cert`, `--tls-key`). Clients trust your CA through `BISHOP_MEMORY_CA_FILE` (`install.sh client --ca`), so nothing else on them changes; browsers trust it after `mkcert -install`. Pair it with a firewall. See [Home Server Setup](Home-Server-Setup) | `https://<server address>:8787` |
| **Tailscale** (or plain WireGuard), recommended when you need access from outside your network | Install Tailscale on the server and every client. Bind `memoryd` to the server's tailnet address (`tailscale ip -4`). Nothing is exposed to the LAN or the internet, and it works from anywhere | `http://<server>.<tailnet>.ts.net:8787` or `http://100.x.y.z:8787` |
| **Built-in TLS with a public certificate** | Let's Encrypt with a DNS challenge for a LAN name; clients trust it already | `https://<name on the certificate>:8787` |
| **A reverse proxy** (Caddy, nginx) | Leave `memoryd` on `127.0.0.1` and let the proxy terminate TLS. Good when the server already runs one | `https://<proxy name>` |
| **An SSH tunnel** per client | `ssh -N -L 8787:127.0.0.1:8787 user@server` on each client. No change to `memoryd` at all; awkward for more than one machine, and the tunnel has to be up whenever a harness runs | `http://127.0.0.1:8787` |

Tailscale traffic is already encrypted, so `memoryd` logs a plain-HTTP warning at every start that you can ignore there (it cannot tell whether the network itself is encrypted).

**A reverse proxy needs a key too.** `memoryd` behind a proxy binds loopback, and on loopback with no key configured it is open. The proxy would hand that open service to the network. Create at least one key before you start the proxy, and never revoke the last one (see [API keys](#6-api-keys)). An example Caddyfile:

```
# /etc/caddy/Caddyfile
memory.example.lan {
    tls internal                    # Caddy's own CA; or omit for a public name with Let's Encrypt
    reverse_proxy 127.0.0.1:8787
}
```

## 2. Get the software

Either a release archive (no Go needed on the target) or a git checkout (needs Go).

**Release archive.** On any machine with Go and a checkout:

```bash
make dist
# dist/bishop-memory-<version>-linux-amd64.tar.gz
# dist/bishop-memory-<version>-linux-arm64.tar.gz
# dist/bishop-memory-<version>-darwin-amd64.tar.gz
# dist/bishop-memory-<version>-darwin-arm64.tar.gz
scp dist/bishop-memory-<version>-linux-amd64.tar.gz user@server:/tmp/
```

Each archive holds `bin/memoryd` and `bin/mcpd` (static, with the schema embedded, so `memoryd` runs from any directory), `scripts/`, `db/` and the README. `<version>` is `git describe --tags --always --dirty`; set `VERSION=` to override it.

On a Linux server, unpack it under `/opt`, not under `/home`: the service runs as its own user with `ProtectHome=true`, so it cannot run a binary inside a home directory. The installer refuses to run from `/home` or `/root` for that reason.

```bash
sudo mkdir -p /opt/bishop-memory
sudo tar -C /opt/bishop-memory --strip-components=1 -xzf /tmp/bishop-memory-<version>-linux-amd64.tar.gz
cd /opt/bishop-memory
```

**Git checkout.** `git clone git@github.com:djm56/bishop-memory.git` (on a Linux server, into `/opt/bishop-memory`) and install Go (the version in `go.mod`). The installers build the binaries when the source and Go are both there, and use the shipped `bin/memoryd` otherwise.

> The Linux service installer builds `memoryd` from source whenever it finds `go` on the PATH. A release archive has no source, so on a server with Go installed, install from a git checkout instead of an archive (or take `go` off the PATH for the install).

## 3. Install the server on Ubuntu or another Linux with systemd

```bash
cd /opt/bishop-memory
sudo scripts/install.sh server --host 100.101.102.103 --first-key kirsch-mac
# with built-in TLS:
sudo scripts/install.sh server --host 192.168.1.20 --first-key kirsch-mac \
     --tls-cert /path/to/memory.lan.pem --tls-key /path/to/memory.lan-key.pem
```

| Flag | Default | Meaning |
|---|---|---|
| `--host ADDR` | `127.0.0.1` | Listen address: loopback (this machine only), the tailnet or LAN address, or `0.0.0.0` (every interface). Anything but loopback needs a key |
| `--port N` | `8787` | Listen port |
| `--first-key NAME` | — | Create this API key and print it once. Required the first time you install on a non-loopback address |
| `--tls-cert FILE --tls-key FILE` | — | Serve HTTPS; give both or neither |
| `--dry-run` | — | Print what would be done and change nothing |
| `-- …` | — | Further flags passed to the platform installer |

What it does, in order:

1. Runs `scripts/install-daemon-linux.sh`: uses the shipped `bin/memoryd` (or builds it when Go is on the PATH), creates the `bishop-memory` system user and group (no shell, no home), installs `/etc/systemd/system/bishop-memory.service`, enables and starts it. At this point it listens on `127.0.0.1` only, so it is never open on the network.
2. With `--first-key`, creates the key as the `bishop-memory` user and prints it once. Copy it now; only its hash is stored.
3. If `--host` is not loopback and no key exists, stops with an error instead of going on.
   With `--first-key` naming a key that already exists, keeps that key and goes on.
4. With TLS, copies the certificate and key into `/etc/bishop-memory/tls/` (mode 0640, group `bishop-memory`).
5. Off loopback, writes the systemd drop-in that admits clients (see "The address filter" below); on loopback, removes it.
6. Writes `/etc/bishop-memory/memoryd.env` (mode 0640) with `HTTP_HOST`, `PORT`, `APP_ENV=production` and the TLS paths. Flags you leave out keep the values an earlier run wrote.
7. Restarts the service and waits up to 20 seconds for `/healthz`; on failure it prints the last 30 journal lines.

Where things live:

| Path | What |
|---|---|
| `/etc/bishop-memory/memoryd.env` | Settings: `HTTP_HOST`, `PORT`, `APP_ENV`, `TLS_CERT_FILE`, `TLS_KEY_FILE`. Edit and restart, or re-run the installer |
| `/etc/bishop-memory/tls/` | The certificate and key, when TLS is on |
| `/var/lib/bishop-memory/memory.db` | The database (plus `-wal` and `-shm` beside it) |
| `/var/lib/bishop-memory/api-keys` | The key hashes, one `<name> <sha256>` line per key, mode 0600 |
| `/etc/systemd/system/bishop-memory.service` | The unit, rendered from `scripts/bishop-memory.service` |
| `/etc/systemd/system/bishop-memory.service.d/network.conf` | The drop-in admitting clients, when `--host` is not loopback |
| `/opt/bishop-memory/bin/memoryd` | The binary the unit runs |

Day to day:

```bash
sudo systemctl status bishop-memory
sudo systemctl restart bishop-memory          # after editing memoryd.env
sudo journalctl -u bishop-memory -f           # the log, one access line per request
curl -s http://127.0.0.1:8787/healthz         # needs no key; see the note below
```

**The address filter.** The unit sets `IPAddressDeny=any` and `IPAddressAllow=127.0.0.0/8 ::1/128`, a kernel-level filter that drops every packet from any other address, whatever `HTTP_HOST` says. When `--host` is not loopback, the installer writes `/etc/systemd/system/bishop-memory.service.d/network.conf` to widen it, and to start memoryd after the network and Tailscale are up so the bind succeeds:

```ini
[Unit]
After=network-online.target tailscaled.service
Wants=network-online.target
[Service]
IPAddressAllow=100.64.0.0/10
```

By default the drop-in admits any address (`IPAddressAllow=any`). Narrow it with `--allow-from 100.64.0.0/10` for Tailscale, or `--allow-from 192.168.1.0/24` for one LAN subnet; the flag takes a comma-separated list and may be repeated. Going back to a loopback `--host` removes the drop-in.

**Firewall.** Allow the port only from the network your clients use. With `ufw`:

```bash
sudo ufw allow from 100.64.0.0/10 to any port 8787 proto tcp     # Tailscale
# or
sudo ufw allow from 192.168.1.0/24 to any port 8787 proto tcp    # one LAN subnet
sudo ufw status
```

## 4. Install the server on macOS

Run it as yourself, not with sudo:

```bash
cd /path/to/bishop-memory
scripts/install.sh server --host 100.101.102.103 --first-key kirsch-mac
```

The flags are the same. The steps differ:

1. Writes the settings to `~/Library/Application Support/bishop-memory/memoryd.env`.
2. With `--first-key`, builds `bin/memoryd` if it is missing and creates the key.
3. Runs `scripts/install-daemon.sh --env-file <that file>`, which stages the binary under `~/.local/libexec/bishop-memory`, installs the launchd user agent `com.bishop-memory.memoryd`, and (re)starts it. The agent reads the settings file through `MEMORYD_ENV_FILE`.
4. Waits for `/healthz`; on failure, see `~/Library/Logs/bishop-memory/memoryd.err.log`.

| Path | What |
|---|---|
| `~/Library/Application Support/bishop-memory/memoryd.env` | Settings |
| `<checkout>/data/memory.db` | The database |
| `<checkout>/data/api-keys` | The key hashes |
| `~/Library/LaunchAgents/com.bishop-memory.memoryd.plist` | The launchd job |
| `~/Library/Logs/bishop-memory/` | `memoryd.out.log`, `memoryd.err.log` |

A launchd user agent runs only while you are logged in. A Mac server must stay logged in (set it to log in automatically and lock the screen instead of logging out); a system-wide LaunchDaemon option is planned, not built. If the macOS firewall is on, allow `memoryd` to accept incoming connections.

If you unpacked a release archive that the browser downloaded and Gatekeeper refuses to run the binaries, clear the quarantine flag:

```bash
xattr -d com.apple.quarantine bin/*
```

## 5. Any other Unix

`scripts/install.sh server` handles Linux with systemd and macOS. Anywhere else, run `bin/memoryd` under your own supervisor with these variables (in the environment, or in a file named by `MEMORYD_ENV_FILE`):

| Variable | Default | Meaning |
|---|---|---|
| `HTTP_HOST` | `127.0.0.1` | Listen address |
| `PORT` | `8787` | Listen port |
| `DB_PATH` | `data/memory.db` | SQLite file, relative to the working directory unless absolute |
| `BISHOP_API_KEYS_FILE` | `api-keys` beside `DB_PATH` | The keys file |
| `BISHOP_API_KEY` | — | One extra key, accepted under the name `env`; for a quick single-key setup |
| `BISHOP_ALLOW_NO_AUTH` | — | `1` lets `memoryd` start on a non-loopback address with no key. Logged as a warning at every start. Do not use it on a network you do not control |
| `TLS_CERT_FILE`, `TLS_KEY_FILE` | — | Both set: serve HTTPS. One without the other: refuse to start |
| `MEMORYD_ENV_FILE` | `.env` in the working directory | Settings file read at start; variables already in the environment win |
| `APP_ENV` | `development` | `production` puts Gin in release mode |
| `LOG_LEVEL` | `info` | Advisory |
| `MEMORY_ROOT` | `testdata/memory` | Fallback root for a path sync on this machine; unused by pushing clients |

```bash
DB_PATH=/srv/bishop-memory/memory.db bin/memoryd keys add kirsch-mac
HTTP_HOST=0.0.0.0 DB_PATH=/srv/bishop-memory/memory.db APP_ENV=production bin/memoryd
```

## 6. API keys

```bash
# Linux
sudo runuser -u bishop-memory -- env DB_PATH=/var/lib/bishop-memory/memory.db /opt/bishop-memory/bin/memoryd keys add triage
sudo runuser -u bishop-memory -- env DB_PATH=/var/lib/bishop-memory/memory.db /opt/bishop-memory/bin/memoryd keys list
sudo runuser -u bishop-memory -- env DB_PATH=/var/lib/bishop-memory/memory.db /opt/bishop-memory/bin/memoryd keys revoke triage
# macOS
DB_PATH=<checkout>/data/memory.db <checkout>/bin/memoryd keys add triage
```

Always pass `DB_PATH` (or `BISHOP_API_KEYS_FILE`): without it the command works on `data/api-keys` relative to whatever directory you are in, not the service's file.

- `keys add <name>` generates a key (`bm_` followed by 32 random bytes in base64url), appends its SHA-256 to the keys file, and prints the key once. Names are letters, digits, `.`, `_` and `-`, up to 64 characters, and must be new.
- `keys list` prints the names. `keys revoke <name>` removes one.
- **One key per client machine or purpose:** `kirsch-mac`, `triage`, `browser-laptop`. A lost laptop is revoked without touching the others.
- **Hashed at rest.** The file holds only hashes; a lost key cannot be recovered, only replaced.
- **No restart.** `memoryd` re-reads the file whenever its modification time or size changes, so a new key works and a revoked one stops working on the next request.
- **Named in the log.** Every authenticated request's access-log line ends `key=<name>`.

When keys are required:

| Bind | Keys configured | Result |
|---|---|---|
| loopback | none | Open, exactly as before. Logs `no API keys configured; /v1 is open (loopback only)` |
| loopback | one or more | A key is required |
| not loopback | one or more | A key is required. Without TLS, a warning that keys cross the network in clear unless the network is encrypted |
| not loopback | none | **`memoryd` refuses to start**, unless `BISHOP_ALLOW_NO_AUTH=1` |

Revoking the last key of a service on a network address does not open it: with no key left, every `/v1` request gets 401 until you add one. On loopback, by contrast, revoking the last key returns it to the open single-machine mode, which matters behind a reverse proxy (see [section 1](#1-choose-how-clients-reach-the-server)).

What needs a key: every `/v1` route. `/healthz` (which only says ok or not), the two pages and their icons hold no data and load without one.

## 7. Set up each client machine

On every machine that runs a harness, the triage agents, or both, with a checkout or an unpacked archive:

```bash
scripts/install.sh client --url http://bishop-server.tailnet-name.ts.net:8787
# API key for http://…:8787 (empty if the server has none):   ← paste it; it is not echoed
```

It:

1. Prompts for the key (or takes `--key`, which leaves it in your shell history; prefer the prompt). With `--ca <file>` (the CA that signed a home-made server certificate, for mkcert `"$(mkcert -CAROOT)/rootCA.pem"`), copies it to `~/.config/bishop-memory/ca.pem`.
2. Writes `~/.config/bishop-memory/client.env`, mode 600:
   ```
   BISHOP_MEMORY_URL=http://bishop-server.tailnet-name.ts.net:8787
   BISHOP_MEMORY_API_KEY=bm_…
   BISHOP_MEMORY_CA_FILE=/Users/you/.config/bishop-memory/ca.pem      # only with --ca
   ```
3. Builds `bin/mcpd` when Go and the source are present; otherwise uses the shipped `bin/mcpd` from the archive.
4. Checks the key with an authenticated `GET /v1/harnesses`: 200 is success, 401 means the server refused the key, anything else that the server could not be reached.
5. Prints the harness-side settings to change next.

`mcpd`, the reconciler, `push-memory.py`, `clean-scratch.py`, every triage script and `triage-run.sh` read `client.env`. With `BISHOP_MEMORY_CA_FILE` set they trust that CA for the server's certificate (only these clients; nothing else on the machine changes what it trusts). A variable already set in the environment always wins over the file, so an explicit `BISHOP_MEMORY_URL` in a harness's `.mcp.json` or a launchd plist takes precedence. `BISHOP_MEMORY_CLIENT_ENV` points them at a different file.

## 8. Changes inside each harness

These edits are made in each **harness repository**, not in bishop-memory; this repository does not edit harness files.

**`.claude/bishop-memory.conf`** (and `.opencode/bishop-memory.conf` for an OpenCode twin):

```
BISHOP_MEMORY_MODE=central
BISHOP_MEMORY_URL=http://bishop-server.tailnet-name.ts.net:8787
BISHOP_MEMORY_HOME=/path/to/bishop-memory          # this machine's checkout or unpacked archive
BISHOP_HARNESS=kirsch
```

Then re-run `.claude/connect-bishop-memory.sh` (or `.opencode/connect-bishop-memory.sh`) so the MCP registration picks up the new URL.

**`.mcp.json` / `opencode.json`.** The command is this machine's `bin/mcpd`, and the env block carries the URL and harness name as before. No key goes here: `mcpd` reads `BISHOP_MEMORY_API_KEY` from `client.env`, so the harness repository never holds a secret.

```json
"bishop-memory": {
  "type": "stdio",
  "command": "/path/to/bishop-memory/bin/mcpd",
  "env": {
    "BISHOP_HARNESS": "kirsch",
    "BISHOP_MEMORY_URL": "http://bishop-server.tailnet-name.ts.net:8787"
  }
}
```

```json
"bishop-memory": {
  "type": "local",
  "command": ["/path/to/bishop-memory/bin/mcpd"],
  "environment": {
    "BISHOP_HARNESS": "kirschopencode",
    "BISHOP_MEMORY_URL": "http://bishop-server.tailnet-name.ts.net:8787"
  },
  "enabled": true
}
```

**The hook.** `.claude/hooks/state-continuity.sh` posts journal rows and steps with its own `curl` calls, which must send the key. The reconcile it starts reads `client.env` by itself; the hook's `curl` does not. Near the top, after the conf is read, load the key from `client.env`:

```sh
CLIENT_ENV="${BISHOP_MEMORY_CLIENT_ENV:-$HOME/.config/bishop-memory/client.env}"
if [ -z "${BISHOP_MEMORY_API_KEY:-}" ] && [ -r "$CLIENT_ENV" ]; then
  BISHOP_MEMORY_API_KEY="$(sed -n 's/^BISHOP_MEMORY_API_KEY=//p' "$CLIENT_ENV" | tail -n 1)"
fi
```

(and the same for `BISHOP_MEMORY_CA_FILE` when the server uses a home-made certificate; [Home Server Setup](Home-Server-Setup#6-point-each-harness-at-the-server) shows both), then add the header (and `${BISHOP_MEMORY_CA_FILE:+--cacert "$BISHOP_MEMORY_CA_FILE"}`) to every `curl` that calls the service:

```sh
HTTP_CODE="$(curl --silent --show-error --max-time 2 \
  -X POST \
  "$MIRROR_URL/v1/flight-recorder" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $BISHOP_MEMORY_API_KEY" \
  -d "$JSON_BODY" \
  --output /dev/null \
  --write-out '%{http_code}' \
  2>/dev/null)"
```

An empty key is harmless against a loopback service with no keys, so the same hook works in both setups. A key on a command line is visible to other local users in the process list; on a shared machine, write the header to a mode-600 file and pass `-H "@$file"` instead, as `triage-run.sh` does.

## 9. First upload of each memory tree

The server cannot read the harness files, so push each tree once:

```bash
scripts/push-memory.py --root /path/to/kirsch/.claude/memory --harness kirsch
# push-memory: kirsch: pushed 412, unchanged 0
```

It registers the harness with that root, sends every Markdown file whose content differs from the server's copy (in requests of at most 200 files), plus the agent definitions beside the root (`.claude/agents/*.md`), which become the crew list. Files over 1 MiB are skipped with a message. `--prune` also deletes the server's documents for files that no longer exist.

After that the reconciler pushes on every run, through the same code, whenever it is given `--harness` (the hook always passes it). Only changed files cross the network.

What still assumes the files are on the server, and does nothing useful against a remote one: `POST /v1/documents/sync` with a path and the `documents_sync` tool (the server reads its own disk), and `make mission-links` / `scripts/backfill-mission-links.py`, which open the database file and so run on the server. A mission update re-imports the mission's documents only when the harness's memory root exists on the server; otherwise it skips silently and the next push carries them.

## 10. The browser

Open `http://<server>:8787/missions` or `/triage`. The first `/v1` call answers 401, and the page asks for a key once. Paste one (make a `browser-<machine>` key for it); the page keeps it in the browser's local storage under `bishop.apiKey` and sends it on every call. **Forget key** in the header removes it from that browser. A wrong key prompts again.

## Where triage runs

The triage agents run **on the workstation that holds the harness checkouts**, against the remote `memoryd`, with a `triage` key in that machine's `client.env`. Two reasons:

- The processor reads the harness checkouts (`--add-dir`, taken from each harness's registered memory root). Those paths exist on the workstation, not on the server.
- The `claude` and `opencode` logins and their credentials already live there.

On that machine:

```bash
scripts/install.sh client --url http://bishop-server.tailnet-name.ts.net:8787      # the triage key
scripts/install-triage-schedule.sh --url http://bishop-server.tailnet-name.ts.net:8787
```

Re-run the schedule installer with `--url`: the launchd jobs set `BISHOP_MEMORY_URL` explicitly, and an explicit value wins over `client.env`. The key still comes from `client.env`. Running triage on the server instead would need a copy of every harness checkout there; that is a recorded decision, not built.

## 11. Moving an existing database from the Mac

1. Stop the Mac service so nothing writes after the copy, and keep it from coming back at login:
   ```bash
   launchctl bootout gui/$(id -u)/com.bishop-memory.memoryd
   launchctl disable gui/$(id -u)/com.bishop-memory.memoryd
   ```
2. Take a consistent copy (either works; `memoryd backup` refuses an existing target):
   ```bash
   DB_PATH=data/memory.db bin/memoryd backup /tmp/memory.db
   # or: sqlite3 data/memory.db ".backup /tmp/memory.db"
   ```
3. Copy it over and install it under the service user:
   ```bash
   scp /tmp/memory.db user@server:/tmp/memory.db
   # on the server
   sudo systemctl stop bishop-memory
   sudo rm -f /var/lib/bishop-memory/memory.db-wal /var/lib/bishop-memory/memory.db-shm
   sudo install -m 0600 -o bishop-memory -g bishop-memory /tmp/memory.db /var/lib/bishop-memory/memory.db
   sudo systemctl start bishop-memory
   rm /tmp/memory.db
   ```
   The schema upgrades itself at start if the server's binary is newer.
4. Point every client at the server (sections 7 and 8).
5. Run the reconciler once per harness with `--harness`, or `push-memory.py`, so the harness registrations and documents are current and filed under the paths on the client machines.

## 12. Backups

`memoryd backup <file>` writes a consistent copy with SQLite's `VACUUM INTO`, safe while the service is serving. The target must not exist.

**systemd timer (Linux),** nightly, keeping 14 days:

```sh
# /usr/local/sbin/bishop-memory-backup  (chmod 755)
#!/bin/sh
set -eu
dir=/var/lib/bishop-memory/backups
DB_PATH=/var/lib/bishop-memory/memory.db /opt/bishop-memory/bin/memoryd backup "$dir/memory-$(date +%Y%m%d-%H%M%S).db"
find "$dir" -name 'memory-*.db' -mtime +14 -delete
```

```ini
# /etc/systemd/system/bishop-memory-backup.service
[Unit]
Description=bishop-memory nightly backup

[Service]
Type=oneshot
User=bishop-memory
Group=bishop-memory
ExecStart=/usr/local/sbin/bishop-memory-backup
```

```ini
# /etc/systemd/system/bishop-memory-backup.timer
[Unit]
Description=bishop-memory nightly backup

[Timer]
OnCalendar=*-*-* 03:30
Persistent=true

[Install]
WantedBy=timers.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now bishop-memory-backup.timer
sudo systemctl start bishop-memory-backup.service      # one now, to check
ls -l /var/lib/bishop-memory/backups
```

The backups sit beside the database, so copy them off the machine as well (to your normal backup system, or `rsync` them elsewhere).

**cron** (macOS or any Unix), as the user that owns the database:

```
30 3 * * * DB_PATH=/path/to/bishop-memory/data/memory.db /path/to/bishop-memory/bin/memoryd backup /path/to/backups/memory-$(date +\%Y\%m\%d).db && find /path/to/backups -name 'memory-*.db' -mtime +14 -delete
```

To restore, stop the service, put the copy in place of `memory.db` (removing the old `-wal` and `-shm`), fix the owner, and start it.

## 13. Upgrading

1. Get the new version: unpack the new archive over the old directory (`sudo tar -C /opt/bishop-memory --strip-components=1 -xzf …`), or `git pull` in the checkout.
2. Re-run the server installer. Flags you leave out keep their earlier values:
   ```bash
   sudo scripts/install.sh server
   ```

The installer is safe to re-run. The database, the keys file and the settings in `memoryd.env` are kept, and the schema upgrades itself at start. A `--first-key` naming an existing key is skipped. On clients, rebuild `bin/mcpd` (`make build-mcpd`, or take it from the new archive) and restart the harness sessions.

## Troubleshooting

**Every call returns 401 `{"error":"unauthorized",…}`.** The client sent no key, or one the server does not know. Check `~/.config/bishop-memory/client.env` on that machine, and `keys list` on the server. A key revoked or never created there cannot work; make a new one and re-run `scripts/install.sh client`. If the URL or key is set in the environment (a harness's `.mcp.json`, a launchd plist, a shell export), that value wins over `client.env`: an explicit empty or stale `BISHOP_MEMORY_API_KEY` there hides the right one.

**The hook's journal rows stop arriving, but `mcpd` tools work.** The hook's own `curl` calls do not send the key. See [section 8](#8-changes-inside-each-harness).

**`memoryd` refuses to start: `HTTP_HOST=… is not loopback and no API key is configured`.** Create a key with `memoryd keys add <name>` against the service's keys file (the message names it), then start it. `BISHOP_ALLOW_NO_AUTH=1` runs it open instead, which you almost never want.

**`memoryd` refuses to start: `set both TLS_CERT_FILE and TLS_KEY_FILE, or neither`.** Only one is set in `memoryd.env`.

**The log says `WARNING — serving plain HTTP on …`.** Expected on Tailscale, WireGuard or an SSH tunnel. On a plain LAN, switch to built-in TLS or a proxy: the keys are readable on the wire.

**The log says `no API keys configured; /v1 is open (loopback only)`.** The normal single-machine state. Behind a reverse proxy it means the proxy is serving an open service; add a key.

**Connection refused, or a timeout.** In order: is it running (`systemctl status bishop-memory`, or the macOS log)? Is it listening on the address you think (`ss -ltnp | grep 8787` on Linux, `lsof -iTCP:8787 -sTCP:LISTEN` on macOS)? Does the `IPAddressAllow` drop-in the installer wrote include the client's network (`systemctl cat bishop-memory`) ([section 3](#3-install-the-server-on-ubuntu-or-another-linux-with-systemd))? Does the firewall allow it (`sudo ufw status`)? On Tailscale, does `tailscale ping <server>` work from the client?

**`install.sh client` says `could not reach …/v1/harnesses (HTTP 000)` with an HTTPS URL.** Usually the client does not trust the certificate, or the URL's host name is not on it. Try `curl -v https://<name>:8787/healthz` to see the TLS error. Install your CA (`mkcert -install`, or your system's trust store) on the client.

**`push-memory: this memoryd has no push route`.** The server is older than the client. Upgrade the server. An older reconciler against a newer server still works; a newer reconciler against an older server falls back to a path sync, which only finds files when the server is on the same machine.

**Search or the HUD shows no documents after moving to a server.** Nothing has pushed them yet. Run `push-memory.py` once per harness, or a reconcile with `--harness`.

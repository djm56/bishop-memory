# Home Server Setup

A step-by-step guide to one specific setup: bishop-memory on an Ubuntu server on your own network, running as a systemd service from `/opt`, used by you from one Mac. Follow it top to bottom. The general reference, with every option, is [Server Install](Server-Install).

What you get:

- memoryd on the server, started at boot by systemd, as its own `bishop-memory` user.
- HTTPS with a certificate from your own small certificate authority (mkcert), so nothing crosses your network in clear.
- An API key that the server requires on every call.
- A firewall and a systemd address filter that only let your Mac in.
- Your existing missions, findings and triage history, moved over from the Mac.
- The Mac's harnesses, triage and browser all talking to the server.

## The values used below

Replace these with yours everywhere they appear.

| Placeholder | Meaning | Example |
|---|---|---|
| `SERVER_IP` | The server's address on your network | `192.168.1.50` |
| `MAC_IP` | Your Mac's address on your network | `192.168.1.20` |
| `server` | How you `ssh` to the server | `you@192.168.1.50` |

Both addresses must stay the same. Give each a **DHCP reservation** in your router (or a static address). If the Mac's address changes later, see [Changing the Mac's address](#changing-the-macs-address).

Find them with `ip -4 addr` on the server and `ipconfig getifaddr en0` on the Mac (`en1` on some Macs).

## 1. Make the certificate (on the Mac)

mkcert creates a certificate authority that only your machines trust, and signs a certificate for the server with it.

```sh
brew install mkcert
mkcert -install        # trusts your new CA on this Mac; browsers then accept the server
mkcert -cert-file bishop-server.pem -key-file bishop-server-key.pem SERVER_IP
scp bishop-server.pem bishop-server-key.pem server:/tmp/
```

`bishop-server-key.pem` is the server's private key; once it is copied, delete the local copy (`rm bishop-server-key.pem`). The CA stays in `$(mkcert -CAROOT)`; never copy `rootCA-key.pem` anywhere.

## 2. Prepare the server

```sh
ssh server
sudo apt update && sudo apt install -y git curl
sudo snap install go --classic      # Go 1.25.5 or newer; snap puts it on sudo's PATH
go version
sudo git clone https://github.com/djm56/bishop-memory.git /opt/bishop-memory
```

It must be under `/opt`. The service cannot read anything under `/home` (its unit sets `ProtectHome=true`), and the installer refuses to run from there.

No Python is needed on the server. The scripts that use Python run on the Mac.

## 3. Install the service (on the server)

```sh
cd /opt/bishop-memory
sudo scripts/install.sh server \
  --host SERVER_IP \
  --first-key mac \
  --allow-from MAC_IP/32 \
  --tls-cert /tmp/bishop-server.pem \
  --tls-key /tmp/bishop-server-key.pem
```

The installer:

1. Builds memoryd and installs it as the `bishop-memory` systemd service. At first it listens on `127.0.0.1` only.
2. Creates the API key `mac` and **prints it once**. Copy it now: only its hash is kept.
3. Copies the certificate and key to `/etc/bishop-memory/tls/`.
4. Writes a systemd drop-in that admits your Mac and the server itself (the service otherwise accepts loopback traffic only).
5. Writes `/etc/bishop-memory/memoryd.env`, restarts the service on `https://SERVER_IP:8787`, and checks it answers.

Then remove the copies in `/tmp`, and turn the firewall on:

```sh
rm /tmp/bishop-server.pem /tmp/bishop-server-key.pem
sudo ufw allow OpenSSH                                        # first, so you keep ssh access
sudo ufw allow from MAC_IP to any port 8787 proto tcp
sudo ufw enable
sudo ufw status
```

Check it:

```sh
sudo systemctl status bishop-memory          # active (running)
sudo journalctl -u bishop-memory -n 20       # "listening on https://SERVER_IP:8787"
```

The service starts at boot. Day-to-day commands are `sudo systemctl restart bishop-memory` and `sudo journalctl -u bishop-memory -f`.

## 4. Move your data from the Mac

The Mac's database holds every mission, finding, decision and triage run. Copy it rather than rebuilding it: replaying the Markdown into an empty database loses some finding decisions.

**On the Mac**, stop using the old service first, so nothing is written after the copy:

```sh
cd ~/bishop-memory
git pull                                         # this version, for the backup command
make build build-mcpd                            # bin/memoryd and bin/mcpd
launchctl bootout gui/$(id -u)/com.bishop-memory.memoryd
mv ~/Library/LaunchAgents/com.bishop-memory.memoryd.plist ~/Library/LaunchAgents/com.bishop-memory.memoryd.plist.disabled
DB_PATH=data/memory.db bin/memoryd backup /tmp/memory.db
scp /tmp/memory.db server:/tmp/
```

Moving the plist aside stops launchd from starting the old service again at the next login. Keep `data/memory.db` on the Mac as a fallback.

**On the server**, swap in the copy. The `-wal` and `-shm` files belong to the empty database the installer made, and must go with it:

```sh
sudo systemctl stop bishop-memory
sudo rm -f /var/lib/bishop-memory/memory.db /var/lib/bishop-memory/memory.db-wal /var/lib/bishop-memory/memory.db-shm
sudo install -o bishop-memory -g bishop-memory -m 600 /tmp/memory.db /var/lib/bishop-memory/memory.db
rm /tmp/memory.db
sudo systemctl start bishop-memory
```

The API key lives in `/var/lib/bishop-memory/api-keys`, not in the database, so it survives the swap.

## 5. Connect the Mac

```sh
cd ~/bishop-memory
scripts/install.sh client --url https://SERVER_IP:8787 --ca "$(mkcert -CAROOT)/rootCA.pem"
```

Paste the `mac` key when asked; it is not echoed. This writes `~/.config/bishop-memory/client.env` (mode 600) with the URL, the key, and the CA to trust. It then checks the key against the server and should print `connected to https://SERVER_IP:8787 with the key`.

Every bishop-memory client on the Mac reads that file: mcpd, the reconciler, `push-memory.py`, `clean-scratch.py`, the triage scripts and `triage-run.sh`. Nothing else on the Mac changes what it trusts.

Point the nightly triage at the server. Its launchd job sets the URL itself, which wins over `client.env`:

```sh
scripts/install-triage-schedule.sh --url https://SERVER_IP:8787
```

Open `https://SERVER_IP:8787/missions` in the browser. It asks for the key once; paste `mac`. Your missions should be there. The Triage / Missions switch takes you to `/triage`.

## 6. Point each harness at the server

Do this in every harness on the Mac, including both halves of a harness that has a Claude Code (`.claude`) and an OpenCode (`.opencode`) side. These files belong to each harness repository.

**`bishop-memory.conf`** (`.claude/bishop-memory.conf`, and `.opencode/bishop-memory.conf` for an OpenCode twin):

```
BISHOP_MEMORY_URL=https://SERVER_IP:8787
```

Leave `BISHOP_MEMORY_MODE`, `BISHOP_MEMORY_HOME` and `BISHOP_HARNESS` as they are.

**`.mcp.json` and `opencode.json`.** These set `BISHOP_MEMORY_URL` in mcpd's environment, and that beats `client.env`. Change it to the server:

```json
"BISHOP_MEMORY_URL": "https://SERVER_IP:8787"
```

Do not put the key here. mcpd reads the key and the CA from `client.env`, so the harness repository never holds a secret. If the harness generates these files (`.claude/connect-bishop-memory.sh`), re-run it after editing the conf instead of editing them by hand.

**The hook.** `.claude/hooks/state-continuity.sh` sends journal rows with its own `curl` calls, which need the key and the CA. Near the top, after the conf is read, add:

```sh
CLIENT_ENV="${BISHOP_MEMORY_CLIENT_ENV:-$HOME/.config/bishop-memory/client.env}"
if [ -r "$CLIENT_ENV" ]; then
  [ -n "${BISHOP_MEMORY_API_KEY:-}" ] || BISHOP_MEMORY_API_KEY="$(sed -n 's/^BISHOP_MEMORY_API_KEY=//p' "$CLIENT_ENV" | tail -n 1)"
  [ -n "${BISHOP_MEMORY_CA_FILE:-}" ] || BISHOP_MEMORY_CA_FILE="$(sed -n 's/^BISHOP_MEMORY_CA_FILE=//p' "$CLIENT_ENV" | tail -n 1)"
fi
```

and add these two options to every `curl` that calls the service:

```sh
  -H "Authorization: Bearer $BISHOP_MEMORY_API_KEY" \
  ${BISHOP_MEMORY_CA_FILE:+--cacert "$BISHOP_MEMORY_CA_FILE"} \
```

The reconcile that the hook starts needs no change: it reads `client.env` itself, and pushes the harness's memory files to the server on every run.

**First upload.** Push each harness's memory tree once, so its briefs, debriefs and crew are on the server straight away:

```sh
scripts/push-memory.py --root /path/to/my-harness/.claude/memory --harness my-harness
scripts/push-memory.py --root /path/to/my-harness/.opencode/memory --harness my-harness-opencode
```

Repeat for each harness, with its memory root and its `BISHOP_HARNESS` name.

## 7. Check everything

| Check | How | Expect |
|---|---|---|
| Server up | `sudo systemctl status bishop-memory` on the server | active (running) |
| Mac reaches it | `scripts/install.sh client --url https://SERVER_IP:8787 --ca "$(mkcert -CAROOT)/rootCA.pem"` again | `connected … with the key` |
| Pages | `https://SERVER_IP:8787/missions` | your missions, no certificate warning |
| A harness | call a memory tool (for example, list missions) in a harness session | answers with your data |
| The hook | do some mission work, then look at the mission's journal on `/missions` | new rows appear |
| Triage | `make triage-classify` on the Mac | runs, or says there is nothing to do |
| Nobody else | from another device on your network, `curl -k https://SERVER_IP:8787/healthz` | times out |

The server logs every request with the key's name (`key=mac`): `sudo journalctl -u bishop-memory -f`.

## 8. Backups

memoryd writes a consistent copy while it runs. Keep 14 nights of them with a systemd timer on the server:

```sh
sudo tee /etc/systemd/system/bishop-memory-backup.service >/dev/null <<'EOF'
[Unit]
Description=Back up the bishop-memory database

[Service]
Type=oneshot
User=bishop-memory
Environment=DB_PATH=/var/lib/bishop-memory/memory.db
ExecStart=/bin/sh -c '/opt/bishop-memory/bin/memoryd backup /var/lib/bishop-memory/backups/memory-$(date +%%Y%%m%%d).db && find /var/lib/bishop-memory/backups -name "memory-*.db" -mtime +14 -delete'
EOF
sudo tee /etc/systemd/system/bishop-memory-backup.timer >/dev/null <<'EOF'
[Unit]
Description=Nightly bishop-memory backup

[Timer]
OnCalendar=*-*-* 03:30
Persistent=true

[Install]
WantedBy=timers.target
EOF
sudo install -d -o bishop-memory -g bishop-memory -m 700 /var/lib/bishop-memory/backups
sudo systemctl daemon-reload
sudo systemctl enable --now bishop-memory-backup.timer
sudo systemctl start bishop-memory-backup.service && ls -l /var/lib/bishop-memory/backups
```

For copies off the server, pull one now and then: `scp server:/var/lib/bishop-memory/backups/memory-*.db ~/Backups/` (the backups directory is readable by `bishop-memory` only; copy with `sudo` on the server first, or add yourself to the group).

## Updating

```sh
ssh server
cd /opt/bishop-memory
sudo git pull
sudo scripts/install.sh server
```

With no flags, the installer keeps every setting from the first run (address, TLS, allowed addresses) and the key. On the Mac, `git pull && make build-mcpd`, then restart your harness sessions.

## Adding another device or key

On the server:

```sh
sudo runuser -u bishop-memory -- env DB_PATH=/var/lib/bishop-memory/memory.db /opt/bishop-memory/bin/memoryd keys add laptop
sudo runuser -u bishop-memory -- env DB_PATH=/var/lib/bishop-memory/memory.db /opt/bishop-memory/bin/memoryd keys list
sudo runuser -u bishop-memory -- env DB_PATH=/var/lib/bishop-memory/memory.db /opt/bishop-memory/bin/memoryd keys revoke laptop
```

Keys take effect without a restart. A new device also needs letting in, on both the firewall (`sudo ufw allow from NEW_IP to any port 8787 proto tcp`) and the service (`sudo scripts/install.sh server --allow-from MAC_IP/32,NEW_IP/32`). It also needs the CA: copy `$(mkcert -CAROOT)/rootCA.pem` to it and pass it to `scripts/install.sh client --ca`.

## Changing the Mac's address

If the Mac's address changes, the server stops answering it. On the server:

```sh
sudo ufw delete allow from OLD_IP to any port 8787 proto tcp
sudo ufw allow from NEW_IP to any port 8787 proto tcp
cd /opt/bishop-memory && sudo scripts/install.sh server --allow-from NEW_IP/32
```

## Going back to the Mac

Everything is reversible. On the Mac:

```sh
mv ~/Library/LaunchAgents/com.bishop-memory.memoryd.plist.disabled ~/Library/LaunchAgents/com.bishop-memory.memoryd.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.bishop-memory.memoryd.plist
rm ~/.config/bishop-memory/client.env
scripts/install-triage-schedule.sh --url http://127.0.0.1:8787
```

Then set each harness's `BISHOP_MEMORY_URL` back to `http://127.0.0.1:8787`. The Mac's `data/memory.db` is as you left it; to bring the server's newer data back, copy a server backup over it first.

## When something is wrong

| Symptom | Cause and fix |
|---|---|
| `install.sh client` says `no connection, or the certificate is not trusted` | Pass `--ca "$(mkcert -CAROOT)/rootCA.pem"`. If you did, check the firewall and the drop-in on the server (`sudo ufw status`, `systemctl cat bishop-memory`). |
| `install.sh client` says `the server refused the key (401)` | The key is mistyped, or was never created on this server. Run `keys list` on the server; make a new one if needed. |
| The browser warns about the certificate | `mkcert -install` was not run on this Mac, or the URL uses a name or address the certificate does not cover. Make a new certificate for exactly what you type in the browser. |
| A harness's journal rows stop, but its tools work | The hook's `curl` calls lack the key or CA ([step 6](#6-point-each-harness-at-the-server)). |
| A harness still talks to `127.0.0.1` | Its `.mcp.json`, `opencode.json` or `bishop-memory.conf` still has the old URL. A value set there wins over `client.env`. |
| `push-memory.py` says `certificate verify failed: CA cert does not include key usage extension` | The CA was not made by mkcert. Python 3.13 and later reject a CA without the key-usage extension; use mkcert, or make the CA with `keyUsage=critical,keyCertSign`. |
| The service does not start after a reboot | `sudo journalctl -u bishop-memory -b`. A `bind: cannot assign requested address` means `SERVER_IP` is no longer the server's address: fix the DHCP reservation. |

More in [Troubleshooting](Troubleshooting) and [Server Install](Server-Install).

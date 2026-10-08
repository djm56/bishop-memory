#!/usr/bin/env bash
# scripts/install.sh — install bishop-memory on a server or set up a client.
#
#   scripts/install.sh server [--host ADDR] [--port N] [--first-key NAME]
#                             [--tls-cert FILE --tls-key FILE]
#                             [--allow-from CIDR[,CIDR…]] [--dry-run]
#                             [-- extra flags for the platform installer]
#   scripts/install.sh client --url URL [--key KEY] [--dry-run]
#
# server: installs memoryd as a service — systemd on Linux (run with sudo),
# launchd on macOS (run as yourself) — through scripts/install-daemon-linux.sh
# or scripts/install-daemon.sh, then writes its settings file, creates the
# first API key and restarts it. Settings:
#   Linux  /etc/bishop-memory/memoryd.env       database /var/lib/bishop-memory
#   macOS  ~/Library/Application Support/bishop-memory/memoryd.env
#          database <this checkout>/data/memory.db
#   --host   the address to listen on: 127.0.0.1 (default, this machine only),
#            a Tailscale or LAN address, or 0.0.0.0 (every interface).
#            Anything but loopback needs an API key: give --first-key the
#            first time, or memoryd refuses to start.
#   --tls-cert/--tls-key  serve HTTPS with this certificate and key.
#   --allow-from  Linux only: the client networks systemd lets in when --host
#            is not loopback, e.g. 100.64.0.0/10 (Tailscale) or
#            192.168.1.0/24. Default: any address (narrow it here or with a
#            firewall). The unit otherwise admits loopback traffic only.
#   Re-running keeps the settings already written for any flag left out, and
#   skips --first-key when that key exists.
#
# client: writes ~/.config/bishop-memory/client.env (mode 600) with
# BISHOP_MEMORY_URL and BISHOP_MEMORY_API_KEY, which mcpd, the reconciler,
# push-memory.py, clean-scratch.py and triage-run.sh read; builds bin/mcpd
# when Go is available; and checks the key against the server. --key is
# read from the terminal when not given, so it does not land in shell history.
#
# The plan behind this: docs/plans/NETWORK-DEPLOYMENT-PLAN.md. The guide:
# the wiki page Server-Install.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BISHOP_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
MODE="${1:-}"
[[ $# -gt 0 ]] && shift

usage() { sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-64}"; }
say() { echo "[install] $*"; }
die() { echo "install.sh: $*" >&2; exit 1; }
is_loopback() { [[ "$1" == "127.0.0.1" || "$1" == "localhost" || "$1" == "::1" || "$1" =~ ^127\. ]]; }

HOST="" PORT="" FIRST_KEY="" TLS_CERT="" TLS_KEY="" URL="" KEY="" DRY_RUN=0 ALLOW_FROM=""
EXTRA=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --host) HOST="${2:-}"; shift 2 ;;
    --port) PORT="${2:-}"; shift 2 ;;
    --first-key) FIRST_KEY="${2:-}"; shift 2 ;;
    --tls-cert) TLS_CERT="${2:-}"; shift 2 ;;
    --tls-key) TLS_KEY="${2:-}"; shift 2 ;;
    --allow-from) ALLOW_FROM="${ALLOW_FROM:+$ALLOW_FROM,}${2:-}"; shift 2 ;;
    --url) URL="${2:-}"; shift 2 ;;
    --key) KEY="${2:-}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    --) shift; EXTRA=("$@"); break ;;
    -h|--help) usage 0 ;;
    *) die "unknown flag: $1 (see --help)" ;;
  esac
done
[[ -z "$PORT" || "$PORT" =~ ^[0-9]+$ ]] || die "--port must be a number"
if [[ -n "$TLS_CERT$TLS_KEY" && ( -z "$TLS_CERT" || -z "$TLS_KEY" ) ]]; then die "give both --tls-cert and --tls-key"; fi
for f in "$TLS_CERT" "$TLS_KEY"; do [[ -z "$f" || -r "$f" ]] || die "cannot read $f"; done

run() { if [[ "$DRY_RUN" -eq 1 ]]; then echo "  would run: $*"; else "$@"; fi; }

# keep_settings FILE: fill each setting not given as a flag from the settings
# file an earlier run wrote, then the defaults. TLS stays as it was unless new
# files are given.
KEEP_TLS_CERT="" KEEP_TLS_KEY=""
keep_settings() {
  local file="$1" k v
  if [[ -r "$file" ]]; then
    while IFS='=' read -r k v; do
      case "$k" in
        HTTP_HOST) [[ -n "$HOST" ]] || HOST="$v" ;;
        PORT) [[ -n "$PORT" ]] || PORT="$v" ;;
        TLS_CERT_FILE) KEEP_TLS_CERT="$v" ;;
        TLS_KEY_FILE) KEEP_TLS_KEY="$v" ;;
      esac
    done < <(grep -E '^[A-Z_]+=' "$file")
    say "keeping settings from $file for flags not given"
  fi
  HOST="${HOST:-127.0.0.1}" PORT="${PORT:-8787}"
}

# add_first_key KEYS_CMD…: create --first-key unless that name exists.
add_first_key() {
  [[ -n "$FIRST_KEY" ]] || return 0
  if [[ "$DRY_RUN" -eq 0 ]] && "$@" keys list 2>/dev/null | grep -qx "$FIRST_KEY"; then
    say "key $FIRST_KEY already exists; keeping it"
    return 0
  fi
  say "creating API key $FIRST_KEY"
  run "$@" keys add "$FIRST_KEY"
}

# write_settings FILE: the memoryd settings file, mode 640/600.
write_settings() {
  local file="$1" scheme="http"
  [[ -n "$TLS_CERT" ]] && scheme="https"
  local body="# memoryd settings, written by scripts/install.sh on $(date -u '+%Y-%m-%d %H:%M UTC').
# Re-run the installer, or edit and restart the service, to change them.
HTTP_HOST=$HOST
PORT=$PORT
APP_ENV=production"
  if [[ -n "$TLS_CERT" ]]; then
    body+="
TLS_CERT_FILE=$TLS_CERT_DEST
TLS_KEY_FILE=$TLS_KEY_DEST"
  elif [[ -n "$KEEP_TLS_CERT" ]]; then
    scheme="https"
    body+="
TLS_CERT_FILE=$KEEP_TLS_CERT
TLS_KEY_FILE=$KEEP_TLS_KEY"
  fi
  if [[ "$DRY_RUN" -eq 1 ]]; then
    echo "  would write $file:"; printf '%s\n' "$body" | sed 's/^/    /'
  else
    mkdir -p "$(dirname "$file")"
    (umask 077; printf '%s\n' "$body" > "$file")
  fi
  SERVICE_URL="$scheme://$( [[ "$HOST" == "0.0.0.0" || "$HOST" == "::" ]] && echo 127.0.0.1 || echo "$HOST" ):$PORT"
}

# wait_healthy URL: poll /healthz for up to 20 seconds.
wait_healthy() {
  [[ "$DRY_RUN" -eq 1 ]] && return 0
  local k=() i
  [[ "$1" == https://* ]] && k=(-k)
  for i in $(seq 1 40); do
    if curl -fsS ${k[@]+"${k[@]}"} --max-time 2 "$1/healthz" >/dev/null 2>&1; then return 0; fi
    sleep 0.5
  done
  return 1
}

server_linux() {
  [[ "$DRY_RUN" -eq 1 || "$EUID" -eq 0 ]] || die "on Linux the server install needs root: sudo $0 server …"
  local etc=/etc/bishop-memory state=/var/lib/bishop-memory user=bishop-memory
  local bin="$BISHOP_ROOT/bin/memoryd"
  case "$BISHOP_ROOT" in
    /home/*|/root/*) die "the unit runs with ProtectHome=true and cannot read $BISHOP_ROOT; unpack or clone into /opt/bishop-memory and run the installer from there" ;;
  esac
  keep_settings "$etc/memoryd.env"
  TLS_CERT_DEST="$etc/tls/$(basename "${TLS_CERT:-cert.pem}")"
  TLS_KEY_DEST="$etc/tls/$(basename "${TLS_KEY:-key.pem}")"

  # 1. The service itself, on loopback until a key exists (the unit's own
  #    defaults), so it is never open on the network.
  say "installing the systemd service"
  run "$SCRIPT_DIR/install-daemon-linux.sh" ${EXTRA[@]+"${EXTRA[@]}"}

  # 2. The first API key, as the service user, in its state directory.
  add_first_key runuser -u "$user" -- env DB_PATH="$state/memory.db" "$bin"
  if ! is_loopback "$HOST" && [[ "$DRY_RUN" -eq 0 ]]; then
    if [[ -z "$(runuser -u "$user" -- env DB_PATH="$state/memory.db" "$bin" keys list | grep -v '^No keys')" ]]; then
      die "--host $HOST is not loopback and no API key exists; re-run with --first-key <name>"
    fi
  fi

  # 3. TLS files where the service can read them.
  if [[ -n "$TLS_CERT" ]]; then
    say "installing the TLS certificate and key under $etc/tls"
    run install -d -m 0750 -o root -g "$user" "$etc/tls"
    run install -m 0640 -o root -g "$user" "$TLS_CERT" "$TLS_CERT_DEST"
    run install -m 0640 -o root -g "$user" "$TLS_KEY" "$TLS_KEY_DEST"
  fi

  # 4. The unit admits loopback traffic only (IPAddressAllow). Off loopback,
  #    a drop-in admits the client networks and starts memoryd after the
  #    network (and Tailscale, when present) is up, so the bind succeeds.
  local dropin=/etc/systemd/system/bishop-memory.service.d/network.conf
  if is_loopback "$HOST"; then
    if [[ -f "$dropin" ]]; then say "removing $dropin (loopback only)"; run rm -f "$dropin"; fi
  else
    local allow="${ALLOW_FROM:-any}"
    say "writing $dropin (clients from: ${allow//,/ })"
    if [[ "$DRY_RUN" -eq 1 ]]; then
      echo "  would write IPAddressAllow=${allow//,/ } and After=network-online.target tailscaled.service"
    else
      mkdir -p "$(dirname "$dropin")"
      printf '%s\n' "# Written by scripts/install.sh: admit clients beyond loopback." \
        "[Unit]" "After=network-online.target tailscaled.service" "Wants=network-online.target" \
        "[Service]" "IPAddressAllow=${allow//,/ }" > "$dropin"
    fi
  fi
  run systemctl daemon-reload

  # 5. Settings, then a restart to apply them.
  say "writing $etc/memoryd.env"
  write_settings "$etc/memoryd.env"
  [[ "$DRY_RUN" -eq 1 ]] || { chgrp "$user" "$etc/memoryd.env"; chmod 0640 "$etc/memoryd.env"; }
  run systemctl restart bishop-memory
  wait_healthy "$SERVICE_URL" || { journalctl -u bishop-memory --no-pager -n 30 >&2 || true; die "memoryd did not come up at $SERVICE_URL"; }
  [[ "$DRY_RUN" -eq 1 ]] || say "memoryd is up at $SERVICE_URL"
  say "more keys: sudo runuser -u $user -- env DB_PATH=$state/memory.db $bin keys add <name>"
}

server_macos() {
  local support="$HOME/Library/Application Support/bishop-memory" bin="$BISHOP_ROOT/bin/memoryd"
  local dbpath="$BISHOP_ROOT/data/memory.db"
  keep_settings "$support/memoryd.env"
  TLS_CERT_DEST="$TLS_CERT" TLS_KEY_DEST="$TLS_KEY"

  # 1. Settings first: the launchd job reads them at start.
  say "writing $support/memoryd.env"
  write_settings "$support/memoryd.env"

  # 2. The first key before the job starts, so a non-loopback bind starts.
  if [[ -n "$FIRST_KEY" ]]; then
    if [[ ! -x "$bin" ]]; then
      say "building memoryd"
      run bash -c "cd '$BISHOP_ROOT' && go build -o bin/memoryd ./cmd/memoryd"
    fi
    add_first_key env DB_PATH="$dbpath" "$bin"
  fi

  # 3. The launchd job, pointed at the settings file.
  say "installing the launchd job"
  run "$SCRIPT_DIR/install-daemon.sh" --env-file "$support/memoryd.env" ${EXTRA[@]+"${EXTRA[@]}"}
  wait_healthy "$SERVICE_URL" || die "memoryd did not come up at $SERVICE_URL; see ~/Library/Logs/bishop-memory/memoryd.err.log"
  [[ "$DRY_RUN" -eq 1 ]] || say "memoryd is up at $SERVICE_URL"
  say "more keys: DB_PATH=$dbpath $bin keys add <name>"
}

client() {
  [[ -n "$URL" ]] || die "client needs --url (for example https://server.tailnet.ts.net:8787)"
  URL="${URL%/}"
  if [[ -z "$KEY" && -t 0 ]]; then
    read -r -s -p "API key for $URL (empty if the server has none): " KEY; echo
  fi
  local dir="$HOME/.config/bishop-memory" file
  file="$dir/client.env"

  say "writing $file"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    echo "  would write BISHOP_MEMORY_URL=$URL and BISHOP_MEMORY_API_KEY=<hidden>"
  else
    mkdir -p "$dir"
    (umask 077; printf 'BISHOP_MEMORY_URL=%s\nBISHOP_MEMORY_API_KEY=%s\n' "$URL" "$KEY" > "$file")
  fi

  if command -v go >/dev/null 2>&1 && [[ -f "$BISHOP_ROOT/go.mod" ]]; then
    say "building bin/mcpd"
    run bash -c "cd '$BISHOP_ROOT' && go build -o bin/mcpd ./cmd/mcpd"
  elif [[ ! -x "$BISHOP_ROOT/bin/mcpd" ]]; then
    say "WARNING: no Go toolchain and no bin/mcpd; use a release archive for this platform"
  fi

  [[ "$DRY_RUN" -eq 1 ]] && return 0
  local hdr code
  hdr="$(mktemp)"; chmod 600 "$hdr"
  [[ -n "$KEY" ]] && printf 'Authorization: Bearer %s\n' "$KEY" > "$hdr"
  code="$(curl -sS -o /dev/null -w '%{http_code}' -H "@$hdr" --max-time 5 "$URL/v1/harnesses" || true)"
  rm -f "$hdr"
  case "$code" in
    200) if [[ -n "$KEY" ]]; then say "connected to $URL with the key"; else say "connected to $URL (the server needs no key)"; fi ;;
    401) die "the server refused the key (401); check it, or create one on the server with memoryd keys add" ;;
    *) die "could not reach $URL/v1/harnesses (HTTP ${code:-none})" ;;
  esac
  cat <<EOF

[install] Next, in each harness on this machine (see the wiki page Server-Install):
  .claude/bishop-memory.conf    BISHOP_MEMORY_URL=$URL
                                BISHOP_MEMORY_HOME=$BISHOP_ROOT
  .mcp.json / opencode.json     command $BISHOP_ROOT/bin/mcpd  (mcpd reads the key from $file)
  first upload of its memory:   $SCRIPT_DIR/push-memory.py --root <harness>/.claude/memory --harness <name>
EOF
}

case "$MODE" in
  server)
    case "$(uname -s)" in
      Linux) server_linux ;;
      Darwin) server_macos ;;
      *) die "unsupported OS $(uname -s): run memoryd by hand (see the wiki page Server-Install)" ;;
    esac ;;
  client) client ;;
  -h|--help|help) usage 0 ;;
  *) usage ;;
esac

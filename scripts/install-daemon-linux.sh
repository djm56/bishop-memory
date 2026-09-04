#!/usr/bin/env bash
# scripts/install-daemon-linux.sh
#
# Install the bishop-memory daemon (memoryd) as a systemd system service
# so it starts at boot and restarts on failure. Linux counterpart to
# scripts/install-daemon.sh (macOS/launchd). Idempotent: re-runs detect
# the existing unit file, back it up once, and reload/restart.
#
# Usage:
#   scripts/install-daemon-linux.sh [--dry-run]
#
# Flags:
#   --dry-run   Print the rendered unit file, the useradd/groupadd
#               commands, and the systemctl commands that WOULD be run,
#               but do NOT touch the system in any way (no user
#               creation, no file writes, no systemctl calls). Safe to
#               run as any user, root or not.
#
# Privilege model (read this before running):
#   Creating the dedicated "bishop-memory" system account and writing
#   the unit file under /etc/systemd/system both require root. This
#   script does NOT attempt to acquire privilege itself — no internal
#   `sudo`, no re-exec, no password prompt of any kind. It detects whether
#   it is already running as root
#   (checking $EUID) and:
#     - If NOT root: performs every step that does not need root (Linux
#       + systemd + architecture detection, building the memoryd binary
#       into ./bin/), then prints the exact commands that still need
#       root — copy/paste them yourself, or re-run this whole script
#       with `sudo` — and exits 0 without having touched /etc or any
#       account database.
#     - If root: performs the full install, including the
#       root-only steps.
#   Either way, the commands that need root are always printed in full
#   before this script does anything with them, dry-run or not.
#
# Detection, not assumption:
#   - Refuses to run on a non-Linux host (see the Darwin equivalent,
#     scripts/install-daemon.sh).
#   - Verifies systemd is the running init system by checking for
#     /run/systemd/system — the detection systemd's own documentation
#     names as authoritative — rather than assuming every Linux host
#     runs systemd.
#   - Verifies the target CPU architecture via `uname -m` and maps it
#     to a Go GOARCH rather than hardcoding amd64 or arm64; refuses
#     with a clear message on anything else this project does not
#     build for (see Makefile's dist-linux-amd64 / dist-linux-arm64
#     targets).

set -euo pipefail

# --- Platform guard: Linux only -------------------------------------------
#
# Mirrors scripts/install-daemon.sh's own Darwin-only guard (Step 4
# review fix, Review C WARNING 6): refuse before touching the
# filesystem at all, not after building a binary or writing a unit file
# that a non-systemd host could never use.

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "install-daemon-linux.sh: this script installs a systemd system service." >&2
  echo "  Detected platform: $(uname -s). systemd is Linux-only." >&2
  echo "  See scripts/install-daemon.sh for the macOS/launchd equivalent." >&2
  exit 1
fi

# --- Init-system guard: systemd only ---------------------------------------
#
# /run/systemd/system existing is the detection systemd's own manual
# documents as authoritative for "is this system running under
# systemd", independent of which distro or release is installed —
# exactly the "detect rather than assume" requirement, since the
# installer has no verified access to the target host's distro or systemd
# version.

if [[ ! -d /run/systemd/system ]]; then
  echo "install-daemon-linux.sh: /run/systemd/system not found — this host" >&2
  echo "  does not appear to be running systemd as its init system." >&2
  echo "  This script only supports systemd; install/run memoryd manually" >&2
  echo "  (see README.md) on a non-systemd init." >&2
  exit 1
fi

# --- Architecture guard: detect, map, refuse anything unsupported ---------
#
# bishop-memory cross-compiles Linux binaries for amd64 and arm64 only
# (bin/memoryd-linux-amd64, bin/memoryd-linux-arm64 via the Makefile's
# dist-linux-* targets). uname -m reports the kernel's own machine
# hardware name, which we map to the matching Go GOARCH rather than
# assume one — the deployment target's architecture is unverified.

HOST_ARCH="$(uname -m)"
case "$HOST_ARCH" in
  x86_64|amd64)
    GOARCH_DETECTED="amd64"
    ;;
  aarch64|arm64)
    GOARCH_DETECTED="arm64"
    ;;
  *)
    echo "install-daemon-linux.sh: unsupported architecture: $HOST_ARCH" >&2
    echo "  bishop-memory currently builds Linux binaries for amd64 and" >&2
    echo "  arm64 only (see Makefile dist-linux-amd64 / dist-linux-arm64)." >&2
    exit 1
    ;;
esac

echo "[install-daemon-linux] detected Linux/$GOARCH_DETECTED under systemd"

# --- Paths -----------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BISHOP_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
UNIT_TEMPLATE="$SCRIPT_DIR/bishop-memory.service"
UNIT_NAME="bishop-memory.service"
UNIT_DEST="/etc/systemd/system/$UNIT_NAME"
SERVICE_USER="bishop-memory"
SERVICE_GROUP="bishop-memory"
STATE_DIR="/var/lib/bishop-memory"
BIN_DEST="$BISHOP_ROOT/bin/memoryd"
PREBUILT_BIN="$BISHOP_ROOT/bin/memoryd-linux-$GOARCH_DETECTED"
# The only other filesystem path the running service reads directly
# (cmd/memoryd/main.go's ApplySchema(db, "db/schema.sql") call, resolved
# against WorkingDirectory=$BISHOP_ROOT in the unit) — see the
# permission grant below.
SCHEMA_FILE="$BISHOP_ROOT/db/schema.sql"

if [[ ! -f "$UNIT_TEMPLATE" ]]; then
  echo "install-daemon-linux.sh: missing unit template: $UNIT_TEMPLATE" >&2
  exit 1
fi

# --- Arg parsing -------------------------------------------------------------

DRY_RUN=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    -h|--help)
      cat <<EOF
Usage: $(basename "$0") [--dry-run]

Installs bishop-memory.service as a systemd system service, running as a
dedicated non-root "bishop-memory" account. With --dry-run, prints the
rendered unit file + every privileged command without touching the
system. Creating the service account and writing to /etc both require
root; this script never elevates itself — see the header comment for
the exact privilege model.
EOF
      exit 0
      ;;
    *)
      echo "install-daemon-linux.sh: unknown flag: $1" >&2
      exit 64
      ;;
  esac
done

echo "[install-daemon-linux] bishop-memory root: $BISHOP_ROOT"
echo "[install-daemon-linux] unit destination:   $UNIT_DEST"
echo "[install-daemon-linux] state directory:    $STATE_DIR"

# --- Render the unit (substitute __BISHOP_ROOT__) ---------------------------
#
# Same placeholder-substitution approach as scripts/install-daemon.sh's
# plist rendering, including the same collision-avoidance trick for a
# BISHOP_ROOT path that happens to contain the literal placeholder text.

RENDERED_UNIT="$(mktemp -t bishop-memory-service-XXXXXX.service)"
trap 'rm -f "$RENDERED_UNIT"' EXIT

SANITISED_ROOT="${BISHOP_ROOT//__BISHOP_ROOT__/__BMR_PLACEHOLDER__}"
sed "s|__BISHOP_ROOT__|$SANITISED_ROOT|g" "$UNIT_TEMPLATE" \
  | sed "s|__BMR_PLACEHOLDER__|__BISHOP_ROOT__|g" \
  > "$RENDERED_UNIT"

# --- Compose the privileged commands (needed whether dry-run or not) -------
#
# Printed verbatim in both --dry-run and the "not root" branch below, so
# the operator always sees exactly what would run before it runs.

print_privileged_commands() {
  cat <<EOF
  # 1. Create the dedicated non-root service account (idempotent: skipped
  #    if it already exists).
  getent group "$SERVICE_GROUP" >/dev/null || groupadd --system "$SERVICE_GROUP"
  id -u "$SERVICE_USER" >/dev/null 2>&1 || useradd --system --gid "$SERVICE_GROUP" \\
    --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin \\
    --comment "bishop-memory service account" "$SERVICE_USER"

  # 2. Install the unit file (backs up any existing one first).
  [ -f "$UNIT_DEST" ] && ! [ -f "$UNIT_DEST.bak" ] && cp "$UNIT_DEST" "$UNIT_DEST.bak"
  cp "$RENDERED_UNIT" "$UNIT_DEST"
  chmod 0644 "$UNIT_DEST"

  # 3. Reload systemd's unit cache and (re)start the service.
  systemctl daemon-reload
  systemctl enable "$UNIT_NAME"
  systemctl restart "$UNIT_NAME"
EOF
}

# --- Dry run: print + exit ---------------------------------------------------

if [[ "$DRY_RUN" -eq 1 ]]; then
  echo "[install-daemon-linux] --dry-run: would write unit to $UNIT_DEST:"
  cat "$RENDERED_UNIT"
  echo "[install-daemon-linux] --dry-run: would run these commands (as root):"
  print_privileged_commands
  echo "[install-daemon-linux] --dry-run: would NOT touch the real system"
  exit 0
fi

# --- Build memoryd (no root needed) -----------------------------------------
#
# Prefers a fresh local build (matches scripts/install-daemon.sh's own
# approach and guarantees the exact source tree state, not a possibly
# stale artifact) when a Go toolchain is present; falls back to the
# Job 4 cross-compiled binary for the detected architecture otherwise —
# this host may be the one place a Go toolchain was never installed,
# which is exactly why Job 4 exists.

mkdir -p "$BISHOP_ROOT/bin"

if command -v go >/dev/null 2>&1; then
  echo "[install-daemon-linux] go toolchain found — building memoryd -> $BIN_DEST"
  (
    cd "$BISHOP_ROOT"
    go build -o "$BIN_DEST" ./cmd/memoryd
  )
elif [[ -f "$PREBUILT_BIN" ]]; then
  echo "[install-daemon-linux] no go toolchain — using prebuilt $PREBUILT_BIN"
  cp "$PREBUILT_BIN" "$BIN_DEST"
  chmod +x "$BIN_DEST"
else
  echo "install-daemon-linux.sh: no 'go' toolchain on PATH and no prebuilt" >&2
  echo "  binary at $PREBUILT_BIN. Build memoryd (see Makefile's" >&2
  echo "  dist-linux-$GOARCH_DETECTED target) and place it there, or install Go, then re-run." >&2
  exit 1
fi

# --- Root check: perform privileged steps only if actually root -------------

if [[ "$EUID" -ne 0 ]]; then
  echo "[install-daemon-linux] not running as root — the following steps"
  echo "  need root and were NOT performed. Run them yourself (they are"
  echo "  exactly what --dry-run above would show), or re-run this whole"
  echo "  script with sudo:"
  echo
  print_privileged_commands
  echo
  echo "[install-daemon-linux] non-privileged steps complete (binary built"
  echo "  at $BIN_DEST); privileged steps above are still pending."
  exit 0
fi

# --- Privileged steps (root only, from here on) ------------------------------

echo "[install-daemon-linux] running as root — performing privileged install steps"

if ! getent group "$SERVICE_GROUP" >/dev/null; then
  echo "[install-daemon-linux] creating group $SERVICE_GROUP"
  groupadd --system "$SERVICE_GROUP"
fi

if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
  echo "[install-daemon-linux] creating user $SERVICE_USER"
  useradd --system --gid "$SERVICE_GROUP" \
    --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin \
    --comment "bishop-memory service account" "$SERVICE_USER"
fi

if [[ -f "$UNIT_DEST" && ! -f "$UNIT_DEST.bak" ]]; then
  cp "$UNIT_DEST" "$UNIT_DEST.bak"
  echo "[install-daemon-linux] backed up existing unit to $UNIT_DEST.bak"
fi

cp "$RENDERED_UNIT" "$UNIT_DEST"
chmod 0644 "$UNIT_DEST"
echo "[install-daemon-linux] installed unit at $UNIT_DEST"

# --- Grant the service account read/execute access to exactly what it
# needs, and nothing else (step 7a fix; see task-20260821-02 step 7
# review CRITICAL 2) ---------------------------------------------------
#
# The previous `chmod -R o+rX "$BISHOP_ROOT"` recursively made the
# ENTIRE checkout world-readable on every run — including any
# operator-created .env file, which .gitignore documents as holding
# secrets (PORT, DB_PATH, LOG_LEVEL, future API keys). That used the
# wrong permission class ("other", widening access to every local
# account) at a scope far wider than the two files its own comment
# named, and re-widened .env every time this script re-ran even if the
# operator had since tightened it back down manually.
#
# The service (User=Group=bishop-memory, per the unit file) only ever
# needs to:
#   - execute $BIN_DEST (ExecStart)
#   - read $SCHEMA_FILE (cmd/memoryd/main.go's ApplySchema call)
# Grant exactly those two files to the service's own group, plus
# EXECUTE-only (never read) on the directory chain leading to each, so
# reaching them does not depend on assuming the checkout's ambient
# permissions ("detection, not assumption", per this script's own
# stated design elsewhere) — a directory's execute/search bit alone
# lets a process open a file it already names, without granting the
# ability to list that directory's other contents (`ls` still fails)
# or read any sibling file such as .env. Never "other", never
# recursive, and never "read" on a directory.
chgrp "$SERVICE_GROUP" "$BISHOP_ROOT" "$BISHOP_ROOT/bin" "$BISHOP_ROOT/db" "$BIN_DEST" "$SCHEMA_FILE"
chmod g+x "$BISHOP_ROOT" "$BISHOP_ROOT/bin" "$BISHOP_ROOT/db"
chmod g+rx "$BIN_DEST"
chmod g+r "$SCHEMA_FILE"
echo "[install-daemon-linux] granted $SERVICE_GROUP group traversal on $BISHOP_ROOT, $BISHOP_ROOT/bin, $BISHOP_ROOT/db"
echo "[install-daemon-linux] granted $SERVICE_GROUP group read/execute on $BIN_DEST"
echo "[install-daemon-linux] granted $SERVICE_GROUP group read on $SCHEMA_FILE"
echo "[install-daemon-linux] NOTE: this does not grant traversal above $BISHOP_ROOT —"
echo "  if $BISHOP_ROOT's own parent directories are not traversable by the"
echo "  $SERVICE_USER account (e.g. a restrictive home directory), the service"
echo "  will still fail to start; the post-start health check below will catch"
echo "  that loudly rather than silently."

systemctl daemon-reload
systemctl enable "$UNIT_NAME"
systemctl restart "$UNIT_NAME"

# --- Post-start health check (step 7a fix; closes the step 7 review
# WARNING 1) -------------------------------------------------------------
#
# `systemctl restart` returning 0 only means systemd accepted the
# request — for Type=simple (this unit), systemd does NOT wait for the
# process to prove itself healthy before returning, so a unit that
# starts and immediately exits (a hardening-directive interaction, a
# permission problem, a bind failure) would still fall through to
# "done" below with no indication anything went wrong. `systemctl
# is-active` is systemd's own documented interface (systemctl(1)) for
# querying a unit's current ActiveState, so poll it — restart is
# asynchronous, hence the short retry loop — and fail loudly, with the
# unit's own recent log lines, if it never reaches "active".
attempt=0
until systemctl is-active --quiet "$UNIT_NAME"; do
  attempt=$((attempt + 1))
  if [[ "$attempt" -ge 10 ]]; then
    echo "install-daemon-linux.sh: $UNIT_NAME did not reach the 'active'" >&2
    echo "  state within ${attempt}s of 'systemctl restart'. Recent logs:" >&2
    journalctl -u "$UNIT_NAME" --no-pager -n 30 >&2 || true
    exit 1
  fi
  sleep 1
done

echo "[install-daemon-linux] $UNIT_NAME is active"
echo "[install-daemon-linux] done — check status with:"
echo "  systemctl status $UNIT_NAME"
echo "  journalctl -u $UNIT_NAME -f"
exit 0

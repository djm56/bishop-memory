#!/usr/bin/env bash
# scripts/install-daemon.sh
#
# Install the bishop-memory daemon (memoryd) as a launchd user agent so
# the service starts at login and restarts on exit. Idempotent: re-runs
# detect the existing plist, back it up once, and reload.
#
# Usage:
#   scripts/install-daemon.sh [--dry-run] [--memory-root <path>] [--log-dir <path>]
#
# Flags:
#   --exec-dir <path>
#               Directory the daemon binary is STAGED into and run from.
#               Defaults to "$HOME/.local/libexec/bishop-memory". Keep it
#               on the INTERNAL disk. launchd's own dyld must open the
#               executable before main() runs, and on an external volume
#               that open can block indefinitely waiting on a TCC grant
#               there is no way to answer from a launchd context — the job
#               then hangs before it can log anything. Rebuilding the
#               binary re-triggers it, because a replaced file is not the
#               file that was previously approved.
#   --log-dir <path>
#               Directory for memoryd's stdout/stderr logs. Defaults to
#               "$HOME/Library/Logs/bishop-memory". Keep this on the
#               INTERNAL disk: launchd opens these files itself before
#               spawning the job, and if the path is on an external
#               volume the agent context is denied /Volumes traversal by
#               TCC, so the job dies at setup with EX_CONFIG (78) having
#               written nothing anywhere.
#   --memory-root <path>
#               Absolute path to the harness memory tree, baked into the
#               plist as MEMORY_ROOT. This is the root /v1/documents/sync
#               walks when a caller omits an explicit root. Defaults to
#               "$BISHOP_ROOT/testdata/memory" (the shipped fixture),
#               which matches the service's own built-in fallback.
#   --dry-run   Print the rendered plist + the launchctl commands that
#               WOULD be run, but do NOT touch launchctl or write the
#               plist. Useful for verification on systems where the
#               operator wants to inspect before installing.
#
# Notes:
# - Builds memoryd into "$BISHOP_ROOT/bin/memoryd" before installing
#   (so the ProgramArguments path actually exists).
# - Does NOT load the daemon; the operator runs the actual launchctl load
#   command afterwards. The script supports `--dry-run` so it can be
#   self-verified without touching the host's launchd state.

set -euo pipefail

# --- Platform guard --------------------------------------------------------
#
# Step 4 review fix (Review C, WARNING 6): this script installs a
# launchd (macOS-only) user agent. With no guard, running it on Linux
# would build the binary, create ~/Library/LaunchAgents (harmless but
# wrong-OS clutter), copy the plist into it, and only THEN fail with
# "launchctl: command not found" at the load step — after the build and
# the file write, not before. Refuse up front instead, including for
# --dry-run: dry-run's own purpose is to preview what THIS script would
# do on a machine that will actually run it, which is never true on a
# non-Darwin host.

if [[ "$(uname)" != "Darwin" ]]; then
  echo "install-daemon.sh: this script installs a launchd (macOS-only) user agent." >&2
  echo "  Detected platform: $(uname). launchd is not available here." >&2
  echo "  See scripts/install-daemon-linux.sh for the systemd equivalent on Linux." >&2
  exit 1
fi

# --- Paths ---------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BISHOP_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PLIST_TEMPLATE="$SCRIPT_DIR/com.bishop-memory.memoryd.plist"
PLIST_DEST="$HOME/Library/LaunchAgents/com.bishop-memory.memoryd.plist"

# --- Arg parsing ---------------------------------------------------------

DRY_RUN=0
MEMORY_ROOT=""
LOG_DIR=""
EXEC_DIR=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --exec-dir)
      EXEC_DIR="${2:-}"
      shift 2
      ;;
    --exec-dir=*)
      EXEC_DIR="${1#*=}"
      shift
      ;;
    --log-dir)
      LOG_DIR="${2:-}"
      shift 2
      ;;
    --log-dir=*)
      LOG_DIR="${1#*=}"
      shift
      ;;
    --memory-root)
      MEMORY_ROOT="${2:-}"
      shift 2
      ;;
    --memory-root=*)
      MEMORY_ROOT="${1#*=}"
      shift
      ;;
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    -h|--help)
      cat <<EOF
Usage: $(basename "$0") [--dry-run] [--memory-root <path>] [--log-dir <path>]

Installs com.bishop-memory.memoryd as a launchd user agent. With --dry-run
prints the rendered plist + the launchctl commands without touching the
system. --memory-root sets MEMORY_ROOT in the plist (default:
<bishop-root>/testdata/memory). --log-dir sets where memoryd's stdout and
stderr go (default: ~/Library/Logs/bishop-memory; keep it off external
volumes).
EOF
      exit 0
      ;;
    *)
      echo "install-daemon.sh: unknown flag: $1" >&2
      exit 64
      ;;
  esac
done

if [[ -z "$MEMORY_ROOT" ]]; then
  MEMORY_ROOT="$BISHOP_ROOT/testdata/memory"
  echo "[install-daemon] --memory-root not supplied; defaulting to shipped fixture"
fi

# The plist bakes MEMORY_ROOT in as an absolute path: launchd does not
# expand ~, and the daemon resolves a relative root against its
# WorkingDirectory, which would silently point at the wrong tree.
case "$MEMORY_ROOT" in
  /*) ;;
  *)
    echo "install-daemon.sh: --memory-root must be an absolute path: $MEMORY_ROOT" >&2
    exit 64
    ;;
esac

if [[ ! -d "$MEMORY_ROOT" ]]; then
  echo "install-daemon.sh: --memory-root is not a directory: $MEMORY_ROOT" >&2
  exit 66
fi

if [[ -z "$EXEC_DIR" ]]; then
  EXEC_DIR="$HOME/.local/libexec/bishop-memory"
fi

case "$EXEC_DIR" in
  /*) ;;
  *)
    echo "install-daemon.sh: --exec-dir must be an absolute path: $EXEC_DIR" >&2
    exit 64
    ;;
esac

# Refuse, rather than warn. A log path under /Volumes produces a loud
# EX_CONFIG; an EXECUTABLE under /Volumes produces a job that hangs inside
# dyld before main() with no output at all, which is far harder to
# diagnose and looks like a running service to launchctl.
case "$EXEC_DIR" in
  /Volumes/*)
    echo "install-daemon.sh: --exec-dir is under /Volumes: $EXEC_DIR" >&2
    echo "  launchd's dyld must open the executable before the program runs." >&2
    echo "  On an external volume that open can block forever waiting for a" >&2
    echo "  TCC grant that cannot be answered from a launchd context, and the" >&2
    echo "  job hangs with no diagnostics while launchctl reports it running." >&2
    echo "  Stage the binary on the internal disk instead (the default)." >&2
    exit 64
    ;;
esac

if [[ -z "$LOG_DIR" ]]; then
  LOG_DIR="$HOME/Library/Logs/bishop-memory"
fi

case "$LOG_DIR" in
  /*) ;;
  *)
    echo "install-daemon.sh: --log-dir must be an absolute path: $LOG_DIR" >&2
    exit 64
    ;;
esac

# Warn, but do not refuse: an operator whose whole home is on an external
# volume has no better option and may have granted the access explicitly.
case "$LOG_DIR" in
  /Volumes/*)
    echo "[install-daemon] WARNING: --log-dir is under /Volumes: $LOG_DIR" >&2
    echo "  launchd opens these log files before spawning the job. If the" >&2
    echo "  agent context lacks TCC access to that volume, the job will fail" >&2
    echo "  with EX_CONFIG (78) and write no diagnostics at all." >&2
    ;;
esac

echo "[install-daemon] bishop-memory root: $BISHOP_ROOT"
echo "[install-daemon] memory root:        $MEMORY_ROOT"
echo "[install-daemon] exec directory:     $EXEC_DIR"
echo "[install-daemon] log directory:      $LOG_DIR"
echo "[install-daemon] plist destination:   $PLIST_DEST"

# --- Render the plist (substitute __BISHOP_ROOT__) -----------------------

RENDERED_PLIST="$(mktemp -t bishopmemoryd-plist-XXXXXX.plist)"
trap 'rm -f "$RENDERED_PLIST"' EXIT

# Substitute __BISHOP_ROOT__ with the absolute path. Use a temporary
# placeholder to avoid re-substitution on paths containing the literal
# __BISHOP_ROOT__ (extremely unlikely, but cheap to be safe).
SANITISED_ROOT="${BISHOP_ROOT//__BISHOP_ROOT__/__BMR_PLACEHOLDER__}"
SANITISED_MEMORY_ROOT="${MEMORY_ROOT//__BISHOP_ROOT__/__BMR_PLACEHOLDER__}"
SANITISED_LOG_DIR="${LOG_DIR//__BISHOP_ROOT__/__BMR_PLACEHOLDER__}"
SANITISED_EXEC="${EXEC_DIR//__BISHOP_ROOT__/__BMR_PLACEHOLDER__}/memoryd"
sed "s|__BISHOP_ROOT__|$SANITISED_ROOT|g" "$PLIST_TEMPLATE" \
  | sed "s|__MEMORY_ROOT__|$SANITISED_MEMORY_ROOT|g" \
  | sed "s|__LOG_DIR__|$SANITISED_LOG_DIR|g" \
  | sed "s|__MEMORYD_BIN__|$SANITISED_EXEC|g" \
  | sed "s|__BMR_PLACEHOLDER__|__BISHOP_ROOT__|g" \
  > "$RENDERED_PLIST"

# --- Dry run: print + exit ----------------------------------------------

if [[ "$DRY_RUN" -eq 1 ]]; then
  echo "[install-daemon] --dry-run: would write plist to $PLIST_DEST:"
  cat "$RENDERED_PLIST"
  echo "[install-daemon] --dry-run: would run launchctl commands:"
  echo "  launchctl unload \"$PLIST_DEST\" 2>/dev/null || true"
  echo "  launchctl load -w \"$PLIST_DEST\""
  echo "[install-daemon] --dry-run: would NOT touch the real plist or launchd"
  exit 0
fi

# --- Build memoryd -------------------------------------------------------

echo "[install-daemon] building memoryd -> $BISHOP_ROOT/bin/memoryd"
mkdir -p "$BISHOP_ROOT/bin"
# `go build` needs a go.mod in the working directory. cd into the module
# root before invoking, even with an absolute source path.
(
  cd "$BISHOP_ROOT"
  go build -o "$BISHOP_ROOT/bin/memoryd" "$BISHOP_ROOT/cmd/memoryd"
)

# --- Stage the binary onto the internal disk -----------------------------
#
# The build lands in the checkout, but the checkout may sit on an external
# volume. launchd runs the staged copy instead. Copy every install so a
# rebuilt binary actually reaches the daemon — otherwise the service keeps
# silently running the previous build.

mkdir -p "$EXEC_DIR"
cp "$BISHOP_ROOT/bin/memoryd" "$EXEC_DIR/memoryd"
chmod 0755 "$EXEC_DIR/memoryd"
echo "[install-daemon] staged binary at $EXEC_DIR/memoryd"

# --- Install the plist ---------------------------------------------------

# launchd will not create this itself; a missing parent directory is
# another route to a silent EX_CONFIG.
mkdir -p "$LOG_DIR"

mkdir -p "$(dirname "$PLIST_DEST")"

if [[ -f "$PLIST_DEST" && ! -f "$PLIST_DEST.bak" ]]; then
  cp "$PLIST_DEST" "$PLIST_DEST.bak"
  echo "[install-daemon] backed up existing plist to $PLIST_DEST.bak"
fi

cp "$RENDERED_PLIST" "$PLIST_DEST"
echo "[install-daemon] installed plist at $PLIST_DEST"

# --- (Re)load the agent --------------------------------------------------

# Use the modern bootout/bootstrap pair rather than legacy load/unload.
#
# `launchctl load -w` fails with "Load failed: 5: Input/output error" when
# the job is already bootstrapped in the domain, and the legacy `list |
# grep` guard does not reliably detect that. The failure is easy to miss —
# the script carries on, the OLD process keeps running, and the freshly
# staged binary is never picked up. A silently stale daemon after a
# successful-looking install is worse than a loud failure.
DOMAIN="gui/$(id -u)"
LABEL="com.bishop-memory.memoryd"

if launchctl print "$DOMAIN/$LABEL" >/dev/null 2>&1; then
  echo "[install-daemon] booting out existing agent"
  launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
  # bootout is asynchronous; give the old process a moment to release the
  # listen socket before the replacement tries to bind it.
  sleep 1
fi

echo "[install-daemon] bootstrapping agent"
if ! launchctl bootstrap "$DOMAIN" "$PLIST_DEST"; then
  echo "install-daemon.sh: bootstrap failed for $LABEL" >&2
  echo "  Inspect with: launchctl print $DOMAIN/$LABEL" >&2
  exit 1
fi

# enable clears any prior disable; harmless when already enabled.
launchctl enable "$DOMAIN/$LABEL" 2>/dev/null || true

# Confirm the daemon actually came up rather than reporting success on a
# job that bootstrapped and then failed.
echo "[install-daemon] waiting for the service to answer"
HEALTH_URL="http://127.0.0.1:${PORT:-8787}/healthz"
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if curl --silent --max-time 2 "$HEALTH_URL" >/dev/null 2>&1; then
    echo "[install-daemon] healthy at $HEALTH_URL"
    break
  fi
  sleep 1
done
if ! curl --silent --max-time 2 "$HEALTH_URL" >/dev/null 2>&1; then
  echo "install-daemon.sh: WARNING: no response from $HEALTH_URL after 10s." >&2
  echo "  The job may still be starting. Check:" >&2
  echo "    launchctl print $DOMAIN/$LABEL | grep -E 'state|last exit'" >&2
  echo "    tail -20 $LOG_DIR/memoryd.err.log" >&2
fi

echo "[install-daemon] done"
exit 0

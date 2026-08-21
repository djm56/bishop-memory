#!/usr/bin/env bash
# scripts/install-daemon.sh
#
# Install the bishop-memory daemon (memoryd) as a launchd user agent so
# the service starts at login and restarts on exit. Idempotent: re-runs
# detect the existing plist, back it up once, and reload.
#
# Usage:
#   scripts/install-daemon.sh [--dry-run]
#
# Flags:
#   --dry-run   Print the rendered plist + the launchctl commands that
#               WOULD be run, but do NOT touch launchctl or write the
#               plist. Useful for verification on systems where the
#               operator wants to inspect before installing.
#
# Notes:
# - Builds memoryd into "$BISHOP_ROOT/bin/memoryd" before installing
#   (so the ProgramArguments path actually exists).
# - Does NOT load the daemon during Step 5; the operator runs the real
#   install post-task. The script supports `--dry-run` so it can be
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
  echo "  See Step 6 of this task for the systemd equivalent on Linux." >&2
  exit 1
fi

# --- Paths ---------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BISHOP_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PLIST_TEMPLATE="$SCRIPT_DIR/com.bishop-memory.memoryd.plist"
PLIST_DEST="$HOME/Library/LaunchAgents/com.bishop-memory.memoryd.plist"

# --- Arg parsing ---------------------------------------------------------

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

Installs com.bishop-memory.memoryd as a launchd user agent. With --dry-run
prints the rendered plist + the launchctl commands without touching the
system.
EOF
      exit 0
      ;;
    *)
      echo "install-daemon.sh: unknown flag: $1" >&2
      exit 64
      ;;
  esac
done

echo "[install-daemon] bishop-memory root: $BISHOP_ROOT"
echo "[install-daemon] plist destination:   $PLIST_DEST"

# --- Render the plist (substitute __BISHOP_ROOT__) -----------------------

RENDERED_PLIST="$(mktemp -t bishopmemoryd-plist-XXXXXX.plist)"
trap 'rm -f "$RENDERED_PLIST"' EXIT

# Substitute __BISHOP_ROOT__ with the absolute path. Use a temporary
# placeholder to avoid re-substitution on paths containing the literal
# __BISHOP_ROOT__ (extremely unlikely, but cheap to be safe).
SANITISED_ROOT="${BISHOP_ROOT//__BISHOP_ROOT__/__BMR_PLACEHOLDER__}"
sed "s|__BISHOP_ROOT__|$SANITISED_ROOT|g" "$PLIST_TEMPLATE" \
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

# --- Install the plist ---------------------------------------------------

mkdir -p "$(dirname "$PLIST_DEST")"

if [[ -f "$PLIST_DEST" && ! -f "$PLIST_DEST.bak" ]]; then
  cp "$PLIST_DEST" "$PLIST_DEST.bak"
  echo "[install-daemon] backed up existing plist to $PLIST_DEST.bak"
fi

cp "$RENDERED_PLIST" "$PLIST_DEST"
echo "[install-daemon] installed plist at $PLIST_DEST"

# --- (Re)load the agent --------------------------------------------------

# launchctl unload is best-effort: the agent may not be loaded yet on a
# fresh install, in which case the unload fails with "service not loaded"
# and we ignore that.
if launchctl list | grep -q com.bishop-memory.memoryd; then
  echo "[install-daemon] unloading existing agent"
  launchctl unload "$PLIST_DEST" || true
fi

echo "[install-daemon] loading agent"
launchctl load -w "$PLIST_DEST"

echo "[install-daemon] done"
exit 0

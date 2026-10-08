#!/usr/bin/env bash
# scripts/install-triage-schedule.sh
#
# Install (or remove) the two nightly findings-triage launchd user agents:
#
#   com.bishop-memory.triage-classify   runs scripts/triage-run.sh classify
#   com.bishop-memory.triage-process    runs scripts/triage-run.sh process
#
# Usage:
#   scripts/install-triage-schedule.sh [--classify-at HH:MM] [--process-at HH:MM]
#                                      [--url URL] [--log-dir DIR] [--dry-run]
#   scripts/install-triage-schedule.sh --uninstall
#
# Defaults: classify at 21:00, process at 21:20, URL http://127.0.0.1:8787,
# logs in ~/Library/Logs/bishop-memory. macOS only (launchd); on Linux write a
# systemd timer that runs the same two commands — see the wiki page Developer-Triage-Agents.
#
# Idempotent: re-running unloads and reloads both jobs with the new plists.
# Loading never triggers a run (RunAtLoad is false); to run now use
# `make triage-classify` / `make triage-process`.

set -euo pipefail

if [[ "$(uname)" != "Darwin" ]]; then
  echo "install-triage-schedule.sh: launchd is macOS-only. On Linux, schedule scripts/triage-run.sh with a systemd timer (see the wiki page Developer-Triage-Agents)." >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BISHOP_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TEMPLATE="$SCRIPT_DIR/com.bishop-memory.triage.plist"
AGENTS_DIR="$HOME/Library/LaunchAgents"

CLASSIFY_AT="21:00"
PROCESS_AT="21:20"
URL="http://127.0.0.1:8787"
LOG_DIR="$HOME/Library/Logs/bishop-memory"
DRY_RUN=0
UNINSTALL=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --classify-at) CLASSIFY_AT="${2:-}"; shift 2 ;;
    --classify-at=*) CLASSIFY_AT="${1#*=}"; shift ;;
    --process-at) PROCESS_AT="${2:-}"; shift 2 ;;
    --process-at=*) PROCESS_AT="${1#*=}"; shift ;;
    --url) URL="${2:-}"; shift 2 ;;
    --url=*) URL="${1#*=}"; shift ;;
    --log-dir) LOG_DIR="${2:-}"; shift 2 ;;
    --log-dir=*) LOG_DIR="${1#*=}"; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    --uninstall) UNINSTALL=1; shift ;;
    -h|--help) sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "install-triage-schedule.sh: unknown flag: $1" >&2; exit 64 ;;
  esac
done

if [[ "$UNINSTALL" -eq 1 ]]; then
  for kind in classify process; do
    plist="$AGENTS_DIR/com.bishop-memory.triage-$kind.plist"
    if [[ -f "$plist" ]]; then
      launchctl unload "$plist" 2>/dev/null || true
      rm -f "$plist"
      echo "[install-triage] removed $plist"
    fi
  done
  exit 0
fi

parse_time() {
  local value="$1" name="$2"
  if [[ ! "$value" =~ ^([01]?[0-9]|2[0-3]):([0-5][0-9])$ ]]; then
    echo "install-triage-schedule.sh: $name must be HH:MM, got $value" >&2
    exit 64
  fi
  echo "$((10#${BASH_REMATCH[1]})) $((10#${BASH_REMATCH[2]}))"
}

read -r CLASSIFY_H CLASSIFY_M <<<"$(parse_time "$CLASSIFY_AT" --classify-at)"
read -r PROCESS_H PROCESS_M <<<"$(parse_time "$PROCESS_AT" --process-at)"

case "$LOG_DIR" in
  /*) ;;
  *) echo "install-triage-schedule.sh: --log-dir must be absolute" >&2; exit 64 ;;
esac

# The PATH baked into the plist: wherever claude, opencode, go, python3 and
# curl live now. Re-run this script after installing either CLI somewhere new.
job_path="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/local/go/bin:/usr/bin:/bin:/usr/sbin:/sbin"
for tool in claude opencode go; do
  if dir="$(dirname "$(command -v "$tool" 2>/dev/null || true)")" && [[ -n "$dir" && "$dir" != "." ]]; then
    case ":$job_path:" in *":$dir:"*) ;; *) job_path="$dir:$job_path" ;; esac
  fi
done

echo "[install-triage] bishop-memory root: $BISHOP_ROOT"
echo "[install-triage] classify at:        $CLASSIFY_AT"
echo "[install-triage] process at:         $PROCESS_AT"
echo "[install-triage] service URL:        $URL"
echo "[install-triage] log directory:      $LOG_DIR"
echo "[install-triage] job PATH:           $job_path"

mkdir -p "$LOG_DIR"

render() {
  local kind="$1" hour="$2" minute="$3"
  sed -e "s|__BISHOP_ROOT__|$BISHOP_ROOT|g" \
      -e "s|__KIND__|$kind|g" \
      -e "s|__PATH__|$job_path|g" \
      -e "s|__HOME__|$HOME|g" \
      -e "s|__URL__|$URL|g" \
      -e "s|__LOG_DIR__|$LOG_DIR|g" \
      -e "s|__HOUR__|$hour|g" \
      -e "s|__MINUTE__|$minute|g" \
      "$TEMPLATE"
}

for spec in "classify $CLASSIFY_H $CLASSIFY_M" "process $PROCESS_H $PROCESS_M"; do
  read -r kind hour minute <<<"$spec"
  dest="$AGENTS_DIR/com.bishop-memory.triage-$kind.plist"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    echo "[install-triage] --dry-run: would write $dest:"
    render "$kind" "$hour" "$minute"
    echo "[install-triage] --dry-run: would run: launchctl unload \"$dest\"; launchctl load -w \"$dest\""
    continue
  fi
  mkdir -p "$AGENTS_DIR"
  if [[ -f "$dest" ]]; then
    launchctl unload "$dest" 2>/dev/null || true
  fi
  render "$kind" "$hour" "$minute" > "$dest"
  plutil -lint "$dest" >/dev/null
  launchctl load -w "$dest"
  echo "[install-triage] loaded $dest"
done

if [[ "$DRY_RUN" -eq 0 ]]; then
  echo "[install-triage] done. Check with: launchctl list | grep com.bishop-memory.triage"
fi

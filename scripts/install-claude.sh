#!/usr/bin/env bash
# scripts/install-claude.sh
#
# Register bishop-memory (mcpd) with a Claude Code installation, and
# append the claude-memory section to <project-root>/CLAUDE.md (creating
# it if missing).
#
# Two registration paths:
#
#   1. CLI path: `command -v claude` succeeds → `claude mcp add --scope user`
#      is idempotent (checked with `claude mcp get bishop-memory` first).
#   2. JSON fallback: no `claude` binary → patch
#      `<claude-home>/claude.json` (or `~/.claude.json` if that path is
#      missing) under the `mcpServers` key with a stdio entry.
#
# Idempotency:
#   - CLI path: `claude mcp get bishop-memory` exits 0 → skip.
#   - JSON path: detect existing `mcpServers.bishop-memory` → skip.
#   - CLAUDE.md patch: grep for the `memory_search` marker → skip.
#
# Usage:
#   scripts/install-claude.sh [--claude-home <path>] [--project-root <path>]
#
# Flags:
#   --claude-home   <path>  Directory containing claude.json. Default: ~/.claude.
#                            (Used only on the JSON fallback path.)
#   --project-root  <path>  Directory containing CLAUDE.md. Default: CWD.
#
# Notes:
# - Builds mcpd into "$BISHOP_ROOT/bin/mcpd" before registering.
# - JSON edits go through python3 (or jq as fallback) — NEVER sed.

set -euo pipefail

# --- Paths ---------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BISHOP_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TEMPLATES_DIR="$BISHOP_ROOT/scripts/templates"
CLAUDE_TEMPLATE="$TEMPLATES_DIR/claude-memory.md"

# --- Arg parsing ---------------------------------------------------------

CLAUDE_HOME=""
PROJECT_ROOT=""
CONFIRM_CWD=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --claude-home)
      CLAUDE_HOME="${2:-}"
      shift 2
      ;;
    --claude-home=*)
      CLAUDE_HOME="${1#*=}"
      shift
      ;;
    --project-root)
      PROJECT_ROOT="${2:-}"
      shift 2
      ;;
    --project-root=*)
      PROJECT_ROOT="${1#*=}"
      shift
      ;;
    --yes)
      CONFIRM_CWD=1
      shift
      ;;
    -h|--help)
      cat <<EOF
Usage: $(basename "$0") [--claude-home <path>] [--project-root <path>] [--yes]

Defaults:
  --claude-home   ~/.claude  (JSON-fallback path only)
  --project-root  CWD, but ONLY with --yes (see below) — there is no
                   silent CWD default.

Step 4 review fix (Review C, WARNING 7): CLAUDE.md is patched at
<project-root>/CLAUDE.md. A silent CWD default risks creating/patching a
STRAY CLAUDE.md if this script is invoked from the wrong directory
(e.g. a subproject directory nested under the real project root) — a
file Claude Code's real project-root resolution would never load, and
one a later "git init" in that subproject could sweep into its first
commit. Pass --project-root explicitly, or pass --yes to confirm using
the current directory ($(pwd)) after reviewing the printed path below.

Examples:
  $(basename "$0") --project-root /tmp/claude-sandbox --claude-home /tmp/claude-sandbox
  $(basename "$0") --yes                                          # uses ~/.claude + CWD, confirmed
EOF
      exit 0
      ;;
    *)
      echo "install-claude.sh: unknown flag: $1" >&2
      exit 64
      ;;
  esac
done

if [[ -z "$CLAUDE_HOME" ]]; then
  CLAUDE_HOME="$HOME/.claude"
  CLAUDE_HOME_OVERRIDDEN=0
else
  CLAUDE_HOME_OVERRIDDEN=1
fi

if [[ -z "$PROJECT_ROOT" ]]; then
  # Step 4 review fix (Review C, WARNING 7): no silent CWD default.
  # CLAUDE.md is patched at <project-root>/CLAUDE.md, so running this
  # from the wrong directory (e.g. inside a subproject such as
  # BishopMemory/bishop-memory rather than the real harness root)
  # creates/patches a stray CLAUDE.md that Claude Code's real
  # project-root resolution would never load. Require either an
  # explicit --project-root, or an explicit --yes acknowledging CWD.
  if [[ "$CONFIRM_CWD" -ne 1 ]]; then
    echo "install-claude.sh: --project-root was not supplied." >&2
    echo "  Resolved CWD would be used as project root: $(pwd)" >&2
    echo "  Re-run with --project-root <path> to target a specific directory," >&2
    echo "  or with --yes to confirm using the current directory above." >&2
    exit 64
  fi
  PROJECT_ROOT="$(pwd)"
  echo "[install-claude] --yes supplied — using CWD as project root: $PROJECT_ROOT"
fi

# Ensure we have python3 for JSON edits if we ever fall back to JSON mode.
# CLI mode doesn't need python3.
if ! command -v python3 >/dev/null 2>&1 && ! command -v jq >/dev/null 2>&1; then
  # Step 4 review fix (Review C, SUGGESTION 13): the original warning
  # didn't say WHY this is only a warning and not a hard failure, so a
  # user who genuinely needs the JSON fallback only discovered the
  # missing tool later, mid-run, via a bare "jq: command not found".
  # State the condition explicitly instead.
  echo "install-claude.sh: WARNING: neither python3 nor jq is on PATH." >&2
  echo "  This is not fatal IF the 'claude' CLI is also on PATH (this script" >&2
  echo "  will use 'claude mcp add' instead and never touch JSON directly)." >&2
  echo "  If 'claude' is NOT on PATH, this script will fail below with a bare" >&2
  echo "  'jq: command not found' or similar when it reaches the JSON-fallback" >&2
  echo "  registration step — install python3 or jq first to avoid that." >&2
fi

echo "[install-claude] bishop-memory root: $BISHOP_ROOT"
echo "[install-claude] claude home:        $CLAUDE_HOME"
echo "[install-claude] project root:       $PROJECT_ROOT"

# --- Build mcpd ----------------------------------------------------------

echo "[install-claude] building mcpd -> $BISHOP_ROOT/bin/mcpd"
mkdir -p "$BISHOP_ROOT/bin"
# Same module-root-cd requirement as install-opencode.sh: `go build` needs
# a go.mod in the working directory, not just on the source path.
(
  cd "$BISHOP_ROOT"
  go build -o "$BISHOP_ROOT/bin/mcpd" "$BISHOP_ROOT/cmd/mcpd"
)
MCP_BIN="$BISHOP_ROOT/bin/mcpd"

# --- Step 1: register bishop-memory MCP server ---------------------------

if command -v claude >/dev/null 2>&1; then
  # CLI path — use `claude mcp add --transport stdio --scope user ...`.
  # Idempotency: probe `claude mcp get bishop-memory` first; if it
  # succeeds (exit 0), the server is already registered.
  echo "[install-claude] 'claude' CLI detected — using CLI path"
  if claude mcp get bishop-memory >/dev/null 2>&1; then
    echo "[install-claude] bishop-memory already registered with claude CLI — skipping"
  else
    echo "[install-claude] registering bishop-memory via 'claude mcp add --scope user'"
    # Note: the CLI form is 'claude mcp add <name> [options] -- <command> [args...]'.
    # '<name>' MUST come before the option flags or the parser complains
    # about a missing 'commandOrUrl'.
    claude mcp add \
      bishop-memory \
      --transport stdio \
      --scope user \
      --env BISHOP_HARNESS=claude-code \
      --env BISHOP_MEMORY_URL=http://127.0.0.1:8787 \
      -- "$MCP_BIN"
    echo "[install-claude] claude CLI add complete"
  fi
else
  # JSON fallback path — patch <claude-home>/claude.json.
  #
  # Claude Code's user-scope MCP config lives in ~/.claude.json under the
  # top-level mcpServers key (per-project entries live under
  # projects.<path>.mcpServers). We mirror the same shape: a top-level
  # mcpServers.bishop-memory entry.
  #
  # SAFETY: when the operator passes --claude-home, we ONLY write under
  # that path — we never silently reach into the real $HOME/.claude.json.
  # The only time we touch $HOME/.claude.json is when --claude-home is
  # unset (default = ~/.claude), and even then we only fall back to the
  # legacy ~/.claude.json path if the canonical $CLAUDE_HOME/claude.json
  # does not exist (legacy install layout).
  echo "[install-claude] no 'claude' CLI — using JSON fallback path"
  mkdir -p "$CLAUDE_HOME"

  if [[ "$CLAUDE_HOME_OVERRIDDEN" -eq 1 ]]; then
    # Explicit override: write ONLY to <claude-home>/claude.json, never
    # the legacy ~/.claude.json in the real $HOME.
    CLAUDE_JSON="$CLAUDE_HOME/claude.json"
    if [[ ! -f "$CLAUDE_JSON" ]]; then
      echo "[install-claude] creating empty $CLAUDE_JSON"
      echo '{}' > "$CLAUDE_JSON"
    fi
  elif [[ -f "$CLAUDE_HOME/claude.json" ]]; then
    CLAUDE_JSON="$CLAUDE_HOME/claude.json"
  elif [[ -f "$HOME/.claude.json" ]]; then
    CLAUDE_JSON="$HOME/.claude.json"
  else
    CLAUDE_JSON="$CLAUDE_HOME/claude.json"
    echo "[install-claude] creating empty $CLAUDE_JSON"
    echo '{}' > "$CLAUDE_JSON"
  fi

  if [[ ! -f "$CLAUDE_JSON.bak" ]]; then
    cp "$CLAUDE_JSON" "$CLAUDE_JSON.bak"
    echo "[install-claude]   backed up to $CLAUDE_JSON.bak"
  fi

  echo "[install-claude] registering bishop-memory in $CLAUDE_JSON"

  if command -v python3 >/dev/null 2>&1; then
    MCP_BIN_ABS="$MCP_BIN" python3 - "$CLAUDE_JSON" <<'PY'
import json
import os
import sys

path = sys.argv[1]
with open(path, "r", encoding="utf-8") as f:
    cfg = json.load(f)

# Idempotency check.
servers = cfg.get("mcpServers") or {}
if "bishop-memory" in servers:
    print("  bishop-memory already present — no change", file=sys.stderr)
else:
    cfg["mcpServers"] = {
        **servers,
        "bishop-memory": {
            "command": os.environ["MCP_BIN_ABS"],
            "args": [],
            "env": {
                "BISHOP_HARNESS": "claude-code",
                "BISHOP_MEMORY_URL": "http://127.0.0.1:8787",
            },
        },
    }

# Step 4 review fix (Review C, WARNING 5): atomic write via a sibling
# temp file + os.replace(), matching the jq fallback below (which
# already writes to "$FILE.tmp" then `mv`s it). An in-place
# open(path, "w") can leave claude.json half-written if the process
# dies mid-write; os.replace is an atomic rename on the same
# filesystem.
tmp_path = path + ".tmp"
with open(tmp_path, "w", encoding="utf-8") as f:
    json.dump(cfg, f, indent=2)
    f.write("\n")
os.replace(tmp_path, path)
print("  patched", file=sys.stderr)
PY
  else
    jq --arg cmd "$MCP_BIN" '
      .mcpServers //= {} |
      .mcpServers["bishop-memory"] //= {
        "command": $cmd,
        "args": [],
        "env": { "BISHOP_HARNESS": "claude-code", "BISHOP_MEMORY_URL": "http://127.0.0.1:8787" }
      }
    ' "$CLAUDE_JSON" > "$CLAUDE_JSON.tmp" && mv "$CLAUDE_JSON.tmp" "$CLAUDE_JSON"
  fi
fi

# --- Step 2: append claude-memory section to CLAUDE.md -------------------

CLAUDE_MD="$PROJECT_ROOT/CLAUDE.md"
PATCH_MARKER='memory_search'
echo "[install-claude] patching $CLAUDE_MD"

mkdir -p "$PROJECT_ROOT"

if [[ -f "$CLAUDE_MD" ]] && grep -qF "$PATCH_MARKER" "$CLAUDE_MD"; then
  echo "[install-claude]   [skip] $CLAUDE_MD already has $PATCH_MARKER marker"
else
  if [[ ! -f "$CLAUDE_MD.bak" ]]; then
    if [[ -f "$CLAUDE_MD" ]]; then
      cp "$CLAUDE_MD" "$CLAUDE_MD.bak"
      echo "[install-claude]   backed up to $CLAUDE_MD.bak"
    fi
  fi

  # Create the file with a brief header if it doesn't exist yet.
  if [[ ! -f "$CLAUDE_MD" ]]; then
    cat > "$CLAUDE_MD" <<'MD'
# CLAUDE.md — project-level instructions for Claude Code

This file is the project's entry-point rules file for Claude Code. Sections
appended below are generated by `scripts/install-claude.sh`.

MD
  fi

  # Append a section header + the template content. We don't @import
  # because @import is fragile across Claude Code versions and the
  # template is small.
  {
    printf '\n## bishop-memory (appended by install-claude.sh)\n\n'
    printf '> Auto-generated by `%s`. Do not edit the body of this\n' "$SCRIPT_DIR/install-claude.sh"
    printf '> section by hand — re-run the installer to refresh.\n\n'
    cat "$CLAUDE_TEMPLATE"
    printf '\n'
  } >> "$CLAUDE_MD"
  echo "[install-claude]   [patched] $CLAUDE_MD"
fi

echo "[install-claude] done"
exit 0

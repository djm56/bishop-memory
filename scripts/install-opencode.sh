#!/usr/bin/env bash
# scripts/install-opencode.sh
#
# Register bishop-memory (mcpd) with an opencode installation: register
# the MCP server under `mcp.bishop-memory` in `<opencode-root>/opencode.json`,
# install the `memory` skill under `<opencode-root>/skills/memory/SKILL.md`,
# and append a one-line reminder to the 5 standard sub-agent AGENT.md files
# (orchestrator, junior-developer, senior-developer, code-reviewer,
# documentation-writer).
#
# All steps are idempotent: existing entries / skill / patched lines are
# detected and skipped. Each modified file is backed up to `<file>.bak`
# before the patch (first patch only; re-runs leave the original .bak alone).
#
# Usage:
#   scripts/install-opencode.sh [--opencode-root <path>]
#
# Flags:
#   --opencode-root <path>  Root containing opencode.json + skills/ + agents/.
#                            Default: $OPENCODE_CONFIG_DIR if set, else
#                            ".opencode" relative to CWD.
#
# Notes:
# - Builds mcpd into "$BISHOP_ROOT/bin/mcpd" before registering (so the
#   command path actually exists). Does NOT touch the Makefile.
# - JSON edits go through python3 (or jq as fallback) — NEVER sed.
# - Skill / AGENT.md patches go through a grep marker for idempotency.

set -euo pipefail

# --- Paths ---------------------------------------------------------------

# BISHOP_ROOT is the directory containing this script's parent. The script
# lives at <root>/scripts/install-opencode.sh, so the parent of the script
# dir is the bishop-memory repo root.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BISHOP_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TEMPLATES_DIR="$BISHOP_ROOT/scripts/templates"
SKILL_TEMPLATE="$TEMPLATES_DIR/memory-skill.md"

# --- Arg parsing ---------------------------------------------------------

OPENCODE_ROOT=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --opencode-root)
      OPENCODE_ROOT="${2:-}"
      shift 2
      ;;
    --opencode-root=*)
      OPENCODE_ROOT="${1#*=}"
      shift
      ;;
    -h|--help)
      cat <<EOF
Usage: $(basename "$0") [--opencode-root <path>]

Defaults:
  --opencode-root  \$OPENCODE_CONFIG_DIR if set, else ".opencode" relative to CWD.

Examples:
  $(basename "$0") --opencode-root /tmp/oc-sandbox/.opencode
  $(basename "$0")                              # uses \$(pwd)/.opencode
EOF
      exit 0
      ;;
    *)
      echo "install-opencode.sh: unknown flag: $1" >&2
      exit 64
      ;;
  esac
done

if [[ -z "$OPENCODE_ROOT" ]]; then
  if [[ -n "${OPENCODE_CONFIG_DIR:-}" ]]; then
    OPENCODE_ROOT="$OPENCODE_CONFIG_DIR"
  else
    OPENCODE_ROOT="$(pwd)/.opencode"
  fi
fi

# Ensure we can locate python3 for JSON edits.
if ! command -v python3 >/dev/null 2>&1; then
  if ! command -v jq >/dev/null 2>&1; then
    echo "install-opencode.sh: neither python3 nor jq available; one is required for JSON edits" >&2
    exit 1
  fi
fi

echo "[install-opencode] bishop-memory root: $BISHOP_ROOT"
echo "[install-opencode] opencode root:      $OPENCODE_ROOT"

# --- Build mcpd ----------------------------------------------------------

echo "[install-opencode] building mcpd -> $BISHOP_ROOT/bin/mcpd"
mkdir -p "$BISHOP_ROOT/bin"
# Do NOT touch the Makefile; build directly with go build. We trust the
# caller to have go on PATH (this script is intended to run in a dev
# workspace, not on a clean machine).
#
# We must cd into the module root: `go build` requires a go.mod context,
# and an absolute source path isn't enough on its own (Go would walk up
# looking for a module from the *invocation* directory).
(
  cd "$BISHOP_ROOT"
  go build -o "$BISHOP_ROOT/bin/mcpd" "$BISHOP_ROOT/cmd/mcpd"
)
MCP_BIN="$BISHOP_ROOT/bin/mcpd"

# --- Step 1: register mcp.bishop-memory in opencode.json -----------------

OPENCODE_JSON="$OPENCODE_ROOT/opencode.json"
mkdir -p "$OPENCODE_ROOT"

if [[ -f "$OPENCODE_JSON" ]] && grep -q '"bishop-memory"' "$OPENCODE_JSON"; then
  echo "[install-opencode] mcp.bishop-memory already registered in $OPENCODE_JSON — skipping"
else
  echo "[install-opencode] registering mcp.bishop-memory in $OPENCODE_JSON"
  # Backup before the first patch. If a .bak already exists from a prior
  # run, leave it — re-runs are no-ops on the JSON itself.
  if [[ -f "$OPENCODE_JSON" && ! -f "$OPENCODE_JSON.bak" ]]; then
    cp "$OPENCODE_JSON" "$OPENCODE_JSON.bak"
    echo "[install-opencode]   backed up to $OPENCODE_JSON.bak"
  fi

  # Initialize the JSON file if it doesn't exist. Start with the schema
  # hint opencode docs recommend (https://opencode.ai/docs/mcp-servers/).
  if [[ ! -f "$OPENCODE_JSON" ]]; then
    cat > "$OPENCODE_JSON" <<'JSON'
{
  "$schema": "https://opencode.ai/config.schema.json",
  "mcp": {}
}
JSON
  fi

  # JSON edit through python3 (preferred) or jq (fallback). The MCP entry
  # shape is verbatim from the opencode docs for a stdio local server:
  #   { "type": "local", "command": ["<abs bin path>"], "environment": {...}, "enabled": true }
  if command -v python3 >/dev/null 2>&1; then
    MCP_BIN_ABS="$MCP_BIN" python3 - "$OPENCODE_JSON" <<'PY'
import json
import os
import sys

path = sys.argv[1]
with open(path, "r", encoding="utf-8") as f:
    cfg = json.load(f)

mcp = cfg.setdefault("mcp", {})
# Idempotency: don't overwrite an existing entry.
if "bishop-memory" in mcp:
    print("  bishop-memory already present — no change", file=sys.stderr)
else:
    mcp["bishop-memory"] = {
        "type": "local",
        "command": [os.environ["MCP_BIN_ABS"]],
        "environment": {
            "BISHOP_MEMORY_URL": "http://127.0.0.1:8787",
            "BISHOP_HARNESS": "opencode",
        },
        "enabled": True,
    }

# Step 4 review fix (Review C, WARNING 5): write to a sibling temp file
# and os.replace() it over the target instead of truncating the
# original in place, matching the jq fallback below (which already did
# this via `... > "$FILE.tmp" && mv`). An in-place open(path, "w") can
# leave the primary opencode.json half-written if the process dies
# mid-write (disk full, OOM-kill, power loss); os.replace is an atomic
# rename on the same filesystem, so the file is never observed
# partially written.
tmp_path = path + ".tmp"
with open(tmp_path, "w", encoding="utf-8") as f:
    json.dump(cfg, f, indent=2)
    f.write("\n")
os.replace(tmp_path, path)
print("  patched", file=sys.stderr)
PY
  else
    # jq fallback — assumes the file already exists and is valid JSON.
    jq --arg cmd "$MCP_BIN" '
      .mcp //= {} |
      .mcp["bishop-memory"] //= {
        "type": "local",
        "command": [ $cmd ],
        "environment": { "BISHOP_MEMORY_URL": "http://127.0.0.1:8787", "BISHOP_HARNESS": "opencode" },
        "enabled": true
      }
    ' "$OPENCODE_JSON" > "$OPENCODE_JSON.tmp" && mv "$OPENCODE_JSON.tmp" "$OPENCODE_JSON"
  fi
fi

# --- Step 2: install the memory skill ------------------------------------

SKILL_DIR="$OPENCODE_ROOT/skills/memory"
SKILL_DEST="$SKILL_DIR/SKILL.md"
echo "[install-opencode] installing memory skill -> $SKILL_DEST"
mkdir -p "$SKILL_DIR"

# Step 4 review fix (Review C, Q7 finding): every other file this
# script patches (opencode.json, the 5 AGENT.md files) is backed up
# once before its first patch and left alone on re-runs. This unconditional
# `cp` had no such guard, so every re-run silently overwrote any
# operator edit to the installed SKILL.md with no way to recover it.
# Match the same backup-once pattern: back up only if a .bak does not
# already exist, and only when there is an existing file to preserve.
if [[ -f "$SKILL_DEST" && ! -f "$SKILL_DEST.bak" ]]; then
  cp "$SKILL_DEST" "$SKILL_DEST.bak"
  echo "[install-opencode]   backed up existing skill to $SKILL_DEST.bak"
fi
cp "$SKILL_TEMPLATE" "$SKILL_DEST"

# --- Step 3: patch the 5 AGENT.md files ----------------------------------

# The one-line reminder we append to each agent's AGENT.md if not already
# present. The grep marker is the backtick `memory_search` token — present
# in the line itself.
PATCH_LINE='Before coding, call the `memory_search` MCP tool for relevant context (see the `memory` skill).'
PATCH_MARKER='memory_search'

AGENT_NAMES=(orchestrator junior-developer senior-developer code-reviewer documentation-writer)

for AGENT_NAME in "${AGENT_NAMES[@]}"; do
  AGENT_MD="$OPENCODE_ROOT/agents/$AGENT_NAME/AGENT.md"
  if [[ ! -f "$AGENT_MD" ]]; then
    echo "[install-opencode]   [skip] $AGENT_MD does not exist (creating stub)"
    mkdir -p "$(dirname "$AGENT_MD")"
    cat > "$AGENT_MD" <<MD
---
mode: subagent
description: "$AGENT_NAME (stub created by install-opencode.sh)"
---

# $AGENT_NAME

This AGENT.md was created as a stub by install-opencode.sh because the
target sub-agent did not have an AGENT.md yet. Replace it with the real
prompt before relying on the agent in production.
MD
  fi

  if grep -qF "$PATCH_MARKER" "$AGENT_MD"; then
    echo "[install-opencode]   [skip] $AGENT_MD already has $PATCH_MARKER marker"
    continue
  fi

  # Backup before the first patch.
  if [[ ! -f "$AGENT_MD.bak" ]]; then
    cp "$AGENT_MD" "$AGENT_MD.bak"
    echo "[install-opencode]   backed up to $AGENT_MD.bak"
  fi

  # Append a blank line + the patch line. We always end the file with a
  # newline before appending.
  printf '\n%s\n' "$PATCH_LINE" >> "$AGENT_MD"
  echo "[install-opencode]   [patched] $AGENT_MD"
done

echo "[install-opencode] done"
exit 0

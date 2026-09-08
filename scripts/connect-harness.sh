#!/usr/bin/env bash
# scripts/connect-harness.sh
#
# Connect a bishop-harness to bishop-memory by applying idempotent
# configuration and patching doctrine files.
#
# Creates or updates:
#   1. .claude/bishop-memory.conf — live config with BISHOP_MEMORY_MODE,
#      BISHOP_MEMORY_URL, BISHOP_HARNESS
#   2. .mcp.json at harness root — project-scope MCP server registration
#   3. .gitignore additions — re-exclusions for conf and sync state
#   4. Two doctrine files patched with central-mode sections:
#      - .claude/skills/mission-lifecycle/SKILL.md
#      - .claude/agents/bishop.md
#
# Idempotent throughout — re-running detects existing changes and skips them.
#
# Usage:
#   scripts/connect-harness.sh --harness-root <path> --harness-name <name>
#                              [--mode <standalone|central>]
#                              [--url <url>]
#                              [--dry-run]
#
# Flags:
#   --harness-root <path>   Required. Path to the harness checkout.
#   --harness-name <name>   Required in central mode. Harness identity.
#   --mode <mode>           Default: central. One of: standalone, central.
#   --url <url>             Default: http://127.0.0.1:8787. bishop-memory URL.
#   --dry-run               Print intended changes without touching files.
#   -h, --help              Show this help message.

set -euo pipefail

# --- Paths ---------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BISHOP_MEMORY_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TEMPLATES_DIR="$BISHOP_MEMORY_ROOT/scripts/templates"

# --- Arg parsing ---------------------------------------------------------

HARNESS_ROOT=""
HARNESS_NAME=""
MODE="central"
URL="http://127.0.0.1:8787"
DRY_RUN=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --harness-root)
      HARNESS_ROOT="${2:-}"
      shift 2
      ;;
    --harness-root=*)
      HARNESS_ROOT="${1#*=}"
      shift
      ;;
    --harness-name)
      HARNESS_NAME="${2:-}"
      shift 2
      ;;
    --harness-name=*)
      HARNESS_NAME="${1#*=}"
      shift
      ;;
    --mode)
      MODE="${2:-}"
      shift 2
      ;;
    --mode=*)
      MODE="${1#*=}"
      shift
      ;;
    --url)
      URL="${2:-}"
      shift 2
      ;;
    --url=*)
      URL="${1#*=}"
      shift
      ;;
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    -h|--help)
      cat <<EOF
Usage: $(basename "$0") --harness-root <path> --harness-name <name>
                        [--mode <standalone|central>]
                        [--url <url>]
                        [--dry-run]

Connect a bishop-harness to bishop-memory.

Required flags:
  --harness-root <path>   Path to the harness checkout.
  --harness-name <name>   Harness identity (required in central mode).
                          Alphanumeric, hyphens, underscores only.

Optional flags:
  --mode <mode>           Connection mode. Default: central.
                          One of: standalone, central.
  --url <url>             bishop-memory API URL.
                          Default: http://127.0.0.1:8787.
  --dry-run               Show intended changes without modifying files.

Examples:
  # Connect with default central mode
  $(basename "$0") --harness-root /path/to/harness --harness-name myharness

  # Dry run to verify changes
  $(basename "$0") --harness-root /path/to/harness --harness-name myharness \\
    --dry-run

  # Standalone mode (no central service needed)
  $(basename "$0") --harness-root /path/to/harness --harness-name myharness \\
    --mode standalone
EOF
      exit 0
      ;;
    *)
      echo "connect-harness.sh: unknown flag: $1" >&2
      exit 64
      ;;
  esac
done

# Validation

if [[ -z "$HARNESS_ROOT" ]]; then
  echo "[connect-harness] ERROR: --harness-root is required" >&2
  exit 64
fi

if [[ ! -d "$HARNESS_ROOT" ]]; then
  echo "[connect-harness] ERROR: harness-root does not exist: $HARNESS_ROOT" >&2
  exit 1
fi

# Verify this looks like a bishop-harness
if [[ ! -f "$HARNESS_ROOT/.claude/agents/bishop.md" ]] || \
   [[ ! -f "$HARNESS_ROOT/.claude/skills/mission-lifecycle/SKILL.md" ]]; then
  echo "[connect-harness] ERROR: harness-root does not look like a bishop-harness" >&2
  echo "  Missing .claude/agents/bishop.md or .claude/skills/mission-lifecycle/SKILL.md" >&2
  exit 1
fi

if [[ "$MODE" != "standalone" && "$MODE" != "central" ]]; then
  echo "[connect-harness] ERROR: invalid mode: $MODE (must be standalone or central)" >&2
  exit 64
fi

if [[ "$MODE" == "central" && -z "$HARNESS_NAME" ]]; then
  echo "[connect-harness] ERROR: --harness-name is required in central mode" >&2
  exit 64
fi

# Validate harness name format: alphanumeric, hyphen, underscore only
if [[ ! -z "$HARNESS_NAME" ]] && [[ ! "$HARNESS_NAME" =~ ^[a-zA-Z0-9_-]+$ ]]; then
  echo "[connect-harness] ERROR: invalid harness-name: $HARNESS_NAME" >&2
  echo "  Must contain only letters, digits, hyphens, and underscores" >&2
  exit 64
fi

echo "[connect-harness] harness root:   $HARNESS_ROOT"
echo "[connect-harness] harness name:   ${HARNESS_NAME:-(not set)}"
echo "[connect-harness] mode:           $MODE"
echo "[connect-harness] URL:            $URL"
if [[ $DRY_RUN -eq 1 ]]; then
  echo "[connect-harness] DRY RUN mode enabled"
fi
echo ""

# --- Helper functions ----------------------------------------------------

backup_file() {
  local file="$1"
  if [[ -f "$file" && ! -f "$file.bak" ]]; then
    cp "$file" "$file.bak"
    echo "[connect-harness]   backed up to $file.bak"
  fi
}

write_or_print() {
  local file="$1"
  local content="$2"
  if [[ $DRY_RUN -eq 1 ]]; then
    echo "[connect-harness]   [dry-run] would write to $file"
  else
    mkdir -p "$(dirname "$file")"
    echo "$content" > "$file"
  fi
}

# --- Step 1: Create .claude/bishop-memory.conf ---------------------------

CONF_FILE="$HARNESS_ROOT/.claude/bishop-memory.conf"
echo "[connect-harness] step 1: .claude/bishop-memory.conf"

if [[ -f "$CONF_FILE" ]]; then
  # Check if BISHOP_MEMORY_HOME is already set in the conf
  if grep -qF "BISHOP_MEMORY_HOME=" "$CONF_FILE"; then
    echo "[connect-harness]   [skip] already exists with BISHOP_MEMORY_HOME set"
  else
    # File exists but needs BISHOP_MEMORY_HOME added
    if [[ $DRY_RUN -eq 1 ]]; then
      echo "[connect-harness]   [dry-run] would add BISHOP_MEMORY_HOME to $CONF_FILE"
    else
      backup_file "$CONF_FILE"
      {
        echo ""
        echo "# Path to the bishop-memory repository checkout. The harness uses this to locate"
        echo "# scripts/reconcile-memory.py for idempotent reconciliation of structured data."
        echo "BISHOP_MEMORY_HOME=$BISHOP_MEMORY_ROOT"
      } >> "$CONF_FILE"
      echo "[connect-harness]   [patched] added BISHOP_MEMORY_HOME to $CONF_FILE"
    fi
  fi
else
  if [[ $DRY_RUN -eq 0 ]]; then
    backup_file "$CONF_FILE"
  fi

  # Read the template and substitute HARNESS_NAME and BISHOP_MEMORY_HOME
  CONF_CONTENT=$(cat "$TEMPLATES_DIR/bishop-memory.conf.example")
  CONF_CONTENT="${CONF_CONTENT//HARNESS_NAME_PLACEHOLDER/$HARNESS_NAME}"
  CONF_CONTENT="${CONF_CONTENT//BISHOP_MEMORY_HOME_PLACEHOLDER/$BISHOP_MEMORY_ROOT}"

  # Adjust MODE and URL if needed
  if [[ "$MODE" == "standalone" ]]; then
    CONF_CONTENT="${CONF_CONTENT//BISHOP_MEMORY_MODE=central/BISHOP_MEMORY_MODE=standalone}"
  fi
  if [[ "$URL" != "http://127.0.0.1:8787" ]]; then
    CONF_CONTENT="${CONF_CONTENT//BISHOP_MEMORY_URL=http:\/\/127.0.0.1:8787/BISHOP_MEMORY_URL=$URL}"
  fi

  write_or_print "$CONF_FILE" "$CONF_CONTENT"
  echo "[connect-harness]   [patched] created $CONF_FILE"
fi

# --- Step 2: Create/update .mcp.json ------------------------------------

MCP_JSON="$HARNESS_ROOT/.mcp.json"
echo "[connect-harness] step 2: .mcp.json"

MCPD_BIN="$BISHOP_MEMORY_ROOT/bin/mcpd"

if [[ ! -f "$MCP_JSON" ]]; then
  backup_file "$MCP_JSON"

  if [[ $DRY_RUN -eq 1 ]]; then
    echo "[connect-harness]   [dry-run] would create $MCP_JSON with bishop-memory server"
  else
    mkdir -p "$(dirname "$MCP_JSON")"
    python3 - "$MCP_JSON" <<PYTHON_EOF
import json
import os

path = "$MCP_JSON"
config = {
    "mcpServers": {
        "bishop-memory": {
            "type": "stdio",
            "command": "$MCPD_BIN",
            "args": [],
            "env": {
                "BISHOP_HARNESS": "$HARNESS_NAME",
                "BISHOP_MEMORY_URL": "$URL"
            }
        }
    }
}

with open(path, "w", encoding="utf-8") as f:
    json.dump(config, f, indent=2)
    f.write("\n")
PYTHON_EOF
    echo "[connect-harness]   [patched] created $MCP_JSON"
  fi
else
  # Merge into existing .mcp.json
  if python3 - "$MCP_JSON" <<PYTHON_EOF 2>/dev/null | grep -q "already present"
import json
import os

path = "$MCP_JSON"
with open(path, "r", encoding="utf-8") as f:
    config = json.load(f)

servers = config.get("mcpServers") or {}
if "bishop-memory" in servers:
    print("already present")
    exit(0)
print("not present")
PYTHON_EOF
  then
    echo "[connect-harness]   [skip] bishop-memory server already registered in $MCP_JSON"
  else
    backup_file "$MCP_JSON"

    if [[ $DRY_RUN -eq 1 ]]; then
      echo "[connect-harness]   [dry-run] would merge bishop-memory server into $MCP_JSON"
    else
      python3 - "$MCP_JSON" <<PYTHON_EOF
import json
import os

path = "$MCP_JSON"
with open(path, "r", encoding="utf-8") as f:
    config = json.load(f)

servers = config.get("mcpServers") or {}
servers["bishop-memory"] = {
    "type": "stdio",
    "command": "$MCPD_BIN",
    "args": [],
    "env": {
        "BISHOP_HARNESS": "$HARNESS_NAME",
        "BISHOP_MEMORY_URL": "$URL"
    }
}
config["mcpServers"] = servers

tmp_path = path + ".tmp"
with open(tmp_path, "w", encoding="utf-8") as f:
    json.dump(config, f, indent=2)
    f.write("\n")
os.replace(tmp_path, path)
PYTHON_EOF
      echo "[connect-harness]   [patched] merged into $MCP_JSON"
    fi
  fi
fi

# --- Step 3: Update .gitignore -------------------------------------------

GITIGNORE="$HARNESS_ROOT/.gitignore"
echo "[connect-harness] step 3: .gitignore"

GITIGNORE_ADDITIONS=".claude/bishop-memory.conf
.claude/memory/state/.bishop-memory-cursor
.claude/memory/state/.bishop-memory-lock"

# Check if all rules are present
if [[ -f "$GITIGNORE" ]]; then
  ALL_PRESENT=1
  while IFS= read -r rule; do
    if ! grep -qF "$rule" "$GITIGNORE"; then
      ALL_PRESENT=0
      break
    fi
  done <<< "$GITIGNORE_ADDITIONS"

  if [[ $ALL_PRESENT -eq 1 ]]; then
    echo "[connect-harness]   [skip] .gitignore already has required rules"
  else
    backup_file "$GITIGNORE"

    if [[ $DRY_RUN -eq 1 ]]; then
      echo "[connect-harness]   [dry-run] would add rules to $GITIGNORE"
    else
      {
        echo ""
        echo "# Per-environment config and sync state — each harness carries its own identity"
        echo "# and sync position. Committing these would cause every clone to collide."
        echo ".claude/bishop-memory.conf"
        echo "# Defensive rules: the following are also ignored by .claude/memory/ (line 30 below),"
        echo "# but are listed explicitly here so that narrowing the .claude/memory/ rule later"
        echo "# does not silently start committing cursor and lock state."
        echo ".claude/memory/state/.bishop-memory-cursor"
        echo ".claude/memory/state/.bishop-memory-lock"
      } >> "$GITIGNORE"
      echo "[connect-harness]   [patched] added rules to $GITIGNORE"
    fi
  fi
else
  echo "[connect-harness]   [skip] .gitignore does not exist (will be created by harness)"
fi

# --- Step 4: Patch .claude/skills/mission-lifecycle/SKILL.md -----------

SKILL_MD="$HARNESS_ROOT/.claude/skills/mission-lifecycle/SKILL.md"
echo "[connect-harness] step 4: .claude/skills/mission-lifecycle/SKILL.md"

SKILL_MARKER="### Central Mode — When bishop-memory Owns The ID"

if grep -qF "$SKILL_MARKER" "$SKILL_MD"; then
  echo "[connect-harness]   [skip] already has central-mode section"
else
  backup_file "$SKILL_MD"

  if [[ $DRY_RUN -eq 1 ]]; then
    echo "[connect-harness]   [dry-run] would patch $SKILL_MD"
  else
    # Read the template
    SKILL_PATCH=$(cat "$TEMPLATES_DIR/skill-md-central-mode.txt")

    # Find the line after "### Working Out The Next One" section ends
    # and insert the central-mode section there.
    # The section to patch in goes after "### Working Out The Next One"
    # and before "---" separator

    python3 - "$SKILL_MD" <<PYTHON_EOF
import re

path = "$SKILL_MD"
with open(path, "r", encoding="utf-8") as f:
    content = f.read()

# Find the insertion point: after the "Working Out The Next One" section
# This section ends with "The ID belongs to Bishop, not to the agent creating..."
# and is followed by a "---" separator.

insertion_point = content.find("### Working Out The Next One")
if insertion_point == -1:
    print("ERROR: Could not find insertion point")
    exit(1)

# Find the end of this subsection (the next line that starts with "###" or "---")
search_start = insertion_point + len("### Working Out The Next One")
next_marker = content.find("\n### ", search_start)
if next_marker == -1:
    next_marker = content.find("\n---", search_start)
if next_marker == -1:
    print("ERROR: Could not find end of section")
    exit(1)

# Insert the patch before the next marker
patch = open("$TEMPLATES_DIR/skill-md-central-mode.txt", "r").read()
new_content = content[:next_marker] + "\n\n" + patch + "\n" + content[next_marker:]

tmp_path = path + ".tmp"
with open(tmp_path, "w", encoding="utf-8") as f:
    f.write(new_content)
import os
os.replace(tmp_path, path)
PYTHON_EOF
    echo "[connect-harness]   [patched] added central-mode section"
  fi
fi

# --- Step 5: Patch .claude/agents/bishop.md ----------------------------

BISHOP_MD="$HARNESS_ROOT/.claude/agents/bishop.md"
echo "[connect-harness] step 5: .claude/agents/bishop.md"

BISHOP_MARKER="The method depends on \`.claude/bishop-memory.conf\`"

if grep -qF "$BISHOP_MARKER" "$BISHOP_MD"; then
  echo "[connect-harness]   [skip] already has central-mode section"
else
  backup_file "$BISHOP_MD"

  if [[ $DRY_RUN -eq 1 ]]; then
    echo "[connect-harness]   [dry-run] would patch $BISHOP_MD"
  else
    # Find "## Standing Up A New Mission" and replace the opening paragraph
    python3 - "$BISHOP_MD" <<PYTHON_EOF
import re

path = "$BISHOP_MD"
with open(path, "r", encoding="utf-8") as f:
    content = f.read()

# Find the section
section_start = content.find("## Standing Up A New Mission")
if section_start == -1:
    print("ERROR: Could not find Standing Up A New Mission section")
    exit(1)

# Find the end of the section title line
section_content_start = content.find("\n", section_start) + 1

# Find the next "## " section or "---" to know where this section ends
next_section = content.find("\n## ", section_content_start)
if next_section == -1:
    print("ERROR: Could not find next section")
    exit(1)

# The current opening is: "Before step 1 runs on a genuinely new mission..."
# We need to replace it with the new text that reads from bishop-memory.conf

old_opening_start = section_content_start
old_opening_end = content.find("\n\nThen hand", section_content_start)
if old_opening_end == -1:
    print("ERROR: Could not find end of opening paragraph")
    exit(1)

# Read the new opening
with open("$TEMPLATES_DIR/bishop-md-mission-setup.txt", "r") as f:
    new_opening = f.read()

# Replace
new_content = content[:old_opening_start] + new_opening + content[old_opening_end:]

tmp_path = path + ".tmp"
with open(tmp_path, "w", encoding="utf-8") as f:
    f.write(new_content)
import os
os.replace(tmp_path, path)
PYTHON_EOF
    echo "[connect-harness]   [patched] updated mission setup section"
  fi
fi

# --- Final checks and summary -------------------------------------------

echo ""
echo "[connect-harness] summary"
if [[ $DRY_RUN -eq 1 ]]; then
  echo "[connect-harness] DRY RUN: no files were modified"
else
  echo "[connect-harness] connection changes applied"
fi

echo ""
echo "[connect-harness] NEXT STEPS:"
echo "  1. Restart Claude Code to load the new project-scope MCP server"
echo "  2. When prompted, approve the 'bishop-memory' server in Claude Code"
echo "     (project-scope servers require approval)"

if [[ "$MODE" == "central" ]]; then
  echo ""
  echo "[connect-harness] central-mode warning:"
  echo "  This harness is now in central mode and requires bishop-memory to be running."
  if ! curl -s "$URL/healthz" >/dev/null 2>&1; then
    echo "  WARNING: bishop-memory service is not responding at $URL"
    echo "  Ensure the service is running before starting a mission."
  else
    echo "  bishop-memory service is reachable at $URL"
  fi
fi

echo "[connect-harness] done"
exit 0

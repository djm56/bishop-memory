#!/usr/bin/env bash
# scripts/triage-run.sh — the single entry point for the findings-triage agents.
#
# Usage:
#   scripts/triage-run.sh classify [--reclassify SLUG] [--model M] [--dry-run]
#   scripts/triage-run.sh process  [--category SLUG] [--limit N] [--model M] [--dry-run]
#
# Called by launchd (scripts/install-triage-schedule.sh), by `make
# triage-classify` / `make triage-process`, and by hand. It:
#
#   1. checks bishop-memory answers on /healthz (exit 2 if not — never run an
#      agent against nothing);
#   2. decides whether there is work: classify exits 0 when no finding is
#      unclassified; process asks GET /v1/triage/next-category and exits 0
#      when nothing is waiting;
#   3. runs Claude Code headless from the bishop-memory checkout, so the
#      project-scope agents in .claude/agents/ and the skill in
#      .claude/skills/ are discovered, with ONLY the bishop-triage MCP server
#      (mcpd in its triage profile) and the tools the agent definition names;
#   4. appends one line per run to $TRIAGE_LOG_DIR/triage.log and keeps the
#      full JSON result beside it.
#
# Environment (all optional):
#   BISHOP_MEMORY_URL         default http://127.0.0.1:8787
#   TRIAGE_ITEMS_PER_RUN      findings the processor may recommend on per run (default 30)
#   TRIAGE_CATEGORIES_PER_RUN categories per process invocation (default 1)
#   TRIAGE_CLASSIFY_MODEL     default haiku
#   TRIAGE_PROCESS_MODEL      default sonnet
#   TRIAGE_MAX_TURNS          default 60 (classify) / 120 (process)
#   TRIAGE_MAX_BUDGET_USD     default 2 (classify) / 5 (process)
#   TRIAGE_LOG_DIR            default ~/Library/Logs/bishop-memory
#   TRIAGE_ADD_DIRS           colon-separated extra directories to open read-only for the
#                             processor; by default every registered harness checkout
#                             (derived from /v1/harnesses memory_root, two levels up).
#   CLAUDE_BIN                default: `claude` on PATH
#
# Credentials: a launchd job has no Claude login and no API key in its
# environment, so the headless CLI stops at "Not logged in". The runner
# sources $TRIAGE_ENV_FILE (default: <bishop-root>/.env, gitignored, the same
# file the service reads) before anything else. Put
#   ANTHROPIC_API_KEY=sk-ant-...
# in it, mode 0600. Any TRIAGE_* or BISHOP_MEMORY_URL value in the file is a
# default; a variable already set in the environment wins.
#
# Exit codes: 0 ran or nothing to do; 1 the agent run failed; 2 a precondition
# failed (service down, mcpd missing, claude missing, no credentials); 64
# usage error.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BISHOP_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# --- Credentials and defaults from .env --------------------------------------
ENV_FILE="${TRIAGE_ENV_FILE:-$BISHOP_ROOT/.env}"
if [[ -f "$ENV_FILE" && -r "$ENV_FILE" ]]; then
  perms="$(stat -f '%Lp' "$ENV_FILE" 2>/dev/null || stat -c '%a' "$ENV_FILE" 2>/dev/null || echo '')"
  if [[ -n "$perms" && "$perms" != "600" && "$perms" != "400" ]]; then
    echo "triage-run.sh: WARNING: $ENV_FILE is mode $perms; it holds credentials — chmod 600 it" >&2
  fi
  # Environment wins over the file: snapshot what is already set, source, restore.
  _pre_env="$(mktemp -t triage-env-XXXXXX)"
  export -p > "$_pre_env"
  set -a
  # shellcheck disable=SC1090
  . "$ENV_FILE"
  set +a
  # shellcheck disable=SC1090
  . "$_pre_env"
  rm -f "$_pre_env"
fi

URL="${BISHOP_MEMORY_URL:-http://127.0.0.1:8787}"
ITEMS="${TRIAGE_ITEMS_PER_RUN:-30}"
CATEGORIES_PER_RUN="${TRIAGE_CATEGORIES_PER_RUN:-1}"
LOG_DIR="${TRIAGE_LOG_DIR:-$HOME/Library/Logs/bishop-memory}"
CLAUDE_BIN="${CLAUDE_BIN:-claude}"
MCPD_BIN="$BISHOP_ROOT/bin/mcpd"

KIND="${1:-}"
shift || true
CATEGORY=""
RECLASSIFY=""
MODEL=""
LIMIT=""
DRY_RUN=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --category) CATEGORY="${2:-}"; shift 2 ;;
    --category=*) CATEGORY="${1#*=}"; shift ;;
    --reclassify) RECLASSIFY="${2:-}"; shift 2 ;;
    --reclassify=*) RECLASSIFY="${1#*=}"; shift ;;
    --model) MODEL="${2:-}"; shift 2 ;;
    --model=*) MODEL="${1#*=}"; shift ;;
    --limit) LIMIT="${2:-}"; shift 2 ;;
    --limit=*) LIMIT="${1#*=}"; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "triage-run.sh: unknown flag: $1" >&2; exit 64 ;;
  esac
done

case "$KIND" in
  classify|process) ;;
  *) echo "triage-run.sh: first argument must be classify or process" >&2; exit 64 ;;
esac
[[ -n "$LIMIT" ]] && ITEMS="$LIMIT"

mkdir -p "$LOG_DIR"
LOG_FILE="$LOG_DIR/triage.log"
STAMP="$(date -u '+%Y-%m-%dT%H%M%SZ')"
RESULT_FILE="$LOG_DIR/triage-$KIND-$STAMP.json"

log() { printf '%s %s\n' "$(date -u '+%Y-%m-%d %H:%M:%S UTC')" "$*" | tee -a "$LOG_FILE" >&2; }

# --- Preconditions ----------------------------------------------------------

if ! curl -fsS --max-time 5 "$URL/healthz" >/dev/null 2>&1; then
  log "[$KIND] bishop-memory at $URL is not answering; nothing run"
  exit 2
fi
if ! command -v "$CLAUDE_BIN" >/dev/null 2>&1; then
  log "[$KIND] claude CLI not found on PATH ($PATH); nothing run"
  exit 2
fi
if [[ -z "${ANTHROPIC_API_KEY:-}" && -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
  # No key in the environment or in $ENV_FILE. A logged-in CLI may still work
  # from a terminal, but a scheduled job will stop at "Not logged in" — say so
  # now rather than after a wasted run.
  log "[$KIND] WARNING: no ANTHROPIC_API_KEY (or CLAUDE_CODE_OAUTH_TOKEN) in the environment or $ENV_FILE; a scheduled run will fail with 'Not logged in'"
fi
if [[ ! -x "$MCPD_BIN" ]]; then
  log "[$KIND] $MCPD_BIN missing; building it"
  (cd "$BISHOP_ROOT" && go build -o "$MCPD_BIN" ./cmd/mcpd) || { log "[$KIND] mcpd build failed"; exit 2; }
fi

# --- Is there work? ----------------------------------------------------------

json_field() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get(sys.argv[1], ""))' "$1"; }

if [[ "$KIND" == "classify" ]]; then
  if [[ -z "$RECLASSIFY" ]]; then
    WAITING="$(curl -fsS "$URL/v1/findings?unclassified=1&limit=1" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["findings"]))')"
    if [[ "$WAITING" == "0" ]]; then
      log "[classify] every finding is classified; nothing to do"
      exit 0
    fi
  fi
  MODEL="${MODEL:-${TRIAGE_CLASSIFY_MODEL:-haiku}}"
  AGENT="findings-classifier"
  MAX_TURNS="${TRIAGE_MAX_TURNS:-60}"
  BUDGET="${TRIAGE_MAX_BUDGET_USD:-2}"
  ALLOWED="mcp__bishop-triage__*,Read"
  if [[ -n "$RECLASSIFY" ]]; then
    PROMPT="Re-classify every finding currently in category '$RECLASSIFY' (fetch them with finding_list category=$RECLASSIFY, in batches), following the Classifier section of .claude/skills/findings-triage/SKILL.md. Overwrite each classification. Model id: $MODEL."
  else
    PROMPT="Classify every unclassified finding, following the Classifier section of .claude/skills/findings-triage/SKILL.md. Model id: $MODEL."
  fi
else
  MODEL="${MODEL:-${TRIAGE_PROCESS_MODEL:-sonnet}}"
  AGENT="findings-processor"
  MAX_TURNS="${TRIAGE_MAX_TURNS:-120}"
  BUDGET="${TRIAGE_MAX_BUDGET_USD:-5}"
  ALLOWED="mcp__bishop-triage__*,Read,Glob,Grep"
fi

# --- MCP config: mcpd in its triage profile, nothing else -------------------

MCP_JSON="$(python3 - "$MCPD_BIN" "$URL" <<'PY'
import json, sys
print(json.dumps({"mcpServers": {"bishop-triage": {
    "type": "stdio", "command": sys.argv[1], "args": [],
    "env": {"MCPD_PROFILE": "triage", "BISHOP_MEMORY_URL": sys.argv[2], "BISHOP_HARNESS": "triage"}}}}))
PY
)"

# --- Directories the processor may read (harness checkouts) ----------------

# macOS ships bash 3.2 (no mapfile, no associative arrays), so the directory
# list is built with a plain while-read loop over newline-separated output.
ADD_DIR_ARGS=()
if [[ "$KIND" == "process" ]]; then
  if [[ -n "${TRIAGE_ADD_DIRS:-}" ]]; then
    dir_list="$(printf '%s' "$TRIAGE_ADD_DIRS" | tr ':' '\n')"
  else
    dir_list="$(curl -fsS "$URL/v1/harnesses" | python3 -c '
import json, os, sys
for h in json.load(sys.stdin)["harnesses"]:
    root = h["memory_root"].rstrip("/")
    # <checkout>/.claude/memory -> <checkout>
    checkout = os.path.dirname(os.path.dirname(root))
    if os.path.isdir(checkout):
        print(checkout)')"
  fi
  while IFS= read -r d; do
    [[ -n "$d" && -d "$d" ]] && ADD_DIR_ARGS+=(--add-dir "$d")
  done <<<"$dir_list"
fi

run_agent() {
  local prompt="$1"
  local cmd=("$CLAUDE_BIN" -p "$prompt"
    --agent "$AGENT"
    --model "$MODEL"
    --mcp-config "$MCP_JSON" --strict-mcp-config
    --allowedTools "$ALLOWED"
    --permission-mode dontAsk
    --max-turns "$MAX_TURNS"
    --max-budget-usd "$BUDGET"
    --output-format json)
  if [[ ${#ADD_DIR_ARGS[@]} -gt 0 ]]; then
    cmd+=("${ADD_DIR_ARGS[@]}")
  fi
  if [[ "$DRY_RUN" -eq 1 ]]; then
    log "[$KIND] DRY RUN — would run from $BISHOP_ROOT:"
    printf '  %q' "${cmd[@]}" >&2; printf '\n' >&2
    return 0
  fi
  log "[$KIND] starting agent=$AGENT model=$MODEL"
  local rc=0
  (cd "$BISHOP_ROOT" && "${cmd[@]}") >"$RESULT_FILE" 2>>"$LOG_FILE" || rc=$?
  if [[ $rc -ne 0 ]]; then
    log "[$KIND] agent exited $rc; see $RESULT_FILE and $LOG_FILE"
    return 1
  fi
  local summary
  summary="$(python3 - "$RESULT_FILE" <<'PY'
import json, sys
try:
    d = json.load(open(sys.argv[1]))
except Exception as exc:
    print(f"result not parseable: {exc}")
    sys.exit(0)
text = (d.get("result") or "").strip().replace("\n", " | ")
flag = "ERROR " if d.get("is_error") else ""
print(f"{flag}turns={d.get('num_turns')} cost_usd={d.get('total_cost_usd')} stop={d.get('stop_reason')} :: {text[:400]}")
if d.get("is_error"):
    sys.exit(3)
PY
)" || { log "[$KIND] agent reported an error: $summary"; return 1; }
  log "[$KIND] done $summary"
  return 0
}

if [[ "$KIND" == "classify" ]]; then
  run_agent "$PROMPT"
  exit $?
fi

# --- process: one or more categories by rotation ---------------------------

ran=0
for ((i = 0; i < CATEGORIES_PER_RUN; i++)); do
  if [[ -n "$CATEGORY" ]]; then
    slug="$CATEGORY"
    CATEGORY=""   # an explicit category runs once
  else
    next="$(curl -sS "$URL/v1/triage/next-category")"
    slug="$(printf '%s' "$next" | json_field category)"
    if [[ -z "$slug" ]]; then
      if [[ $ran -eq 0 ]]; then log "[process] no category has findings waiting; nothing to do"; fi
      break
    fi
  fi
  PROMPT="Process category '$slug' with an item cap of $ITEMS findings, following the Processor section of .claude/skills/findings-triage/SKILL.md. Call triage_category_findings with category=$slug and limit=$ITEMS. Model id: $MODEL."
  run_agent "$PROMPT" || exit 1
  ran=$((ran + 1))
done
exit 0

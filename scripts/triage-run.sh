#!/usr/bin/env bash
# scripts/triage-run.sh — the single entry point for the findings-triage agents.
#
# Usage:
#   scripts/triage-run.sh classify [--reclassify SLUG] [--engine E] [--model M] [--dry-run]
#   scripts/triage-run.sh process  [--category SLUG] [--limit N] [--engine E] [--model M] [--dry-run]
#   scripts/triage-run.sh grade    [--limit N] [--mission ID[,ID...]] [--regrade] [--engine E] [--model M] [--dry-run]
#
# Called by launchd (scripts/install-triage-schedule.sh), by `make
# triage-classify` / `make triage-process` / `make triage-grade`, and by hand. It:
#
#   1. checks bishop-memory answers on /healthz (exit 2 if not — never run an
#      agent against nothing);
#   2. decides whether there is work: classify exits 0 when no finding is
#      unclassified; process asks GET /v1/triage/next-category and exits 0
#      when nothing is waiting; grade asks GET /v1/mission-grades/waiting and
#      exits 0 when every finished mission has a verdict;
#   3. runs the agent headless from the bishop-memory checkout, with ONLY the
#      bishop-triage MCP server (mcpd in its triage profile) and the tools the
#      agent definition names, on one of two engines:
#        claude    Claude Code (`claude -p`); agents in .claude/agents/
#        opencode  OpenCode (`opencode run`); agents in .opencode/agents/,
#                  using the providers and keys in your own OpenCode config
#      Both engines follow the same doctrine: .claude/skills/findings-triage/SKILL.md
#      for classify and process, .claude/skills/mission-grading/SKILL.md for grade;
#   4. appends one line per run to $TRIAGE_LOG_DIR/triage.log and keeps the
#      full result beside it (.json for claude, .jsonl events for opencode).
#
# Environment (all optional):
#   BISHOP_MEMORY_URL         default http://127.0.0.1:8787
#   BISHOP_MEMORY_CA_FILE     CA to trust for an https URL with a home-made
#                             certificate (also read from client.env)
#   BISHOP_MEMORY_API_KEY     sent to the service when set (also read from
#                             ~/.config/bishop-memory/client.env)
#   TRIAGE_ENGINE             claude (default) or opencode, for both kinds
#   TRIAGE_CLASSIFY_ENGINE    engine for classify only; wins over TRIAGE_ENGINE
#   TRIAGE_PROCESS_ENGINE     engine for process only; wins over TRIAGE_ENGINE
#   TRIAGE_GRADE_ENGINE       engine for grade only; wins over TRIAGE_ENGINE
#   TRIAGE_GRADE_LIMIT        finished missions graded per run, 1-10 (default 10); --limit wins
#   TRIAGE_ITEMS_PER_RUN      findings the processor may recommend on per run (default 30)
#   TRIAGE_CATEGORIES_PER_RUN categories per process invocation (default 1)
#   TRIAGE_CLASSIFY_MODEL     default haiku (claude) / opencode-go/glm-5.3-flash (opencode)
#   TRIAGE_PROCESS_MODEL      default sonnet (claude) / opencode-go/glm-5.2 (opencode)
#   TRIAGE_GRADE_MODEL        default sonnet (claude) / opencode-go/glm-5.2 (opencode)
#   TRIAGE_MAX_TURNS          default 60 (classify) / 120 (process) / 20 (grade); OpenCode calls them steps
#   TRIAGE_MAX_BUDGET_USD     default 2 (classify) / 5 (process) / 2 (grade); claude only
#   TRIAGE_TIMEOUT_MIN        stop a run still going after this many minutes (default 45, 20 for grade; 0 = never)
#   TRIAGE_LOG_DIR            default ~/Library/Logs/bishop-memory
#   TRIAGE_ADD_DIRS           colon-separated extra directories to open read-only for the
#                             processor; by default every registered harness checkout
#                             (derived from /v1/harnesses memory_root, two levels up).
#                             The grader works from the database only and gets none.
#
# grade flags: --mission names the missions to grade (each must be finished);
# --regrade first deletes their existing verdicts (DELETE /v1/mission-grades/ID),
# which is the only way a graded mission is graded again. --regrade needs
# --mission.
#   CLAUDE_BIN                default: `claude` on PATH
#   OPENCODE_BIN              default: `opencode` on PATH
#
# Credentials: a launchd job has no Claude login and no API key in its
# environment, so the headless Claude CLI stops at "Not logged in". The runner
# sources $TRIAGE_ENV_FILE (default: <bishop-root>/.env, gitignored, the same
# file the service reads) before anything else. For the claude engine put
#   ANTHROPIC_API_KEY=sk-ant-...      (API billing), or
#   CLAUDE_CODE_OAUTH_TOKEN=...       (your Claude subscription; `claude setup-token`)
# in it, mode 0600. The opencode engine needs nothing there: OpenCode reads its
# own config (~/.config/opencode/opencode.json, ~/.local/share/opencode/auth.json),
# which a launchd job running as you can read. Any TRIAGE_* or
# BISHOP_MEMORY_URL value in the file is a default; a variable already set in
# the environment wins.
#
# Exit codes: 0 ran or nothing to do; 1 the agent run failed or timed out; 2 a
# precondition failed (service down, mcpd missing, CLI missing, agent not
# resolvable, model id wrong for the engine); 64 usage error.

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

# --- Client settings (server URL and API key) --------------------------------
# ~/.config/bishop-memory/client.env, written by `scripts/install.sh client`,
# fills BISHOP_MEMORY_URL and BISHOP_MEMORY_API_KEY when neither the
# environment nor .env set them. mcpd reads the same file for the agents.
CLIENT_ENV="${BISHOP_MEMORY_CLIENT_ENV:-$HOME/.config/bishop-memory/client.env}"
if [[ -f "$CLIENT_ENV" && -r "$CLIENT_ENV" ]]; then
  _pre_env="$(mktemp -t triage-env-XXXXXX)"
  export -p > "$_pre_env"
  set -a
  # shellcheck disable=SC1090
  . "$CLIENT_ENV"
  set +a
  # shellcheck disable=SC1090
  . "$_pre_env"
  rm -f "$_pre_env"
fi

URL="${BISHOP_MEMORY_URL:-http://127.0.0.1:8787}"

# The API key goes to curl through a private header file, never on its command
# line, where any local user could read it in the process list.
AUTH_HEADER_FILE=""
if [[ -n "${BISHOP_MEMORY_API_KEY:-}" ]]; then
  AUTH_HEADER_FILE="$(mktemp -t triage-auth-XXXXXX)"
  chmod 600 "$AUTH_HEADER_FILE"
  printf 'Authorization: Bearer %s\n' "$BISHOP_MEMORY_API_KEY" > "$AUTH_HEADER_FILE"
  trap 'rm -f "$AUTH_HEADER_FILE"' EXIT
fi
# api_curl: curl with the API key header when there is one, trusting
# BISHOP_MEMORY_CA_FILE (a home-made server certificate's CA) when set.
CURL_OPTS=()
[[ -n "$AUTH_HEADER_FILE" ]] && CURL_OPTS+=(-H "@$AUTH_HEADER_FILE")
[[ -n "${BISHOP_MEMORY_CA_FILE:-}" ]] && CURL_OPTS+=(--cacert "${BISHOP_MEMORY_CA_FILE/#\~/$HOME}")
api_curl() {
  curl ${CURL_OPTS[@]+"${CURL_OPTS[@]}"} "$@"
}

ITEMS="${TRIAGE_ITEMS_PER_RUN:-30}"
CATEGORIES_PER_RUN="${TRIAGE_CATEGORIES_PER_RUN:-1}"
TIMEOUT_MIN="${TRIAGE_TIMEOUT_MIN:-45}"
LOG_DIR="${TRIAGE_LOG_DIR:-$HOME/Library/Logs/bishop-memory}"
CLAUDE_BIN="${CLAUDE_BIN:-claude}"
OPENCODE_BIN="${OPENCODE_BIN:-opencode}"
MCPD_BIN="$BISHOP_ROOT/bin/mcpd"

KIND="${1:-}"
shift || true
CATEGORY=""
RECLASSIFY=""
MODEL=""
LIMIT=""
ENGINE_FLAG=""
MISSIONS=""
REGRADE=0
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
    --engine) ENGINE_FLAG="${2:-}"; shift 2 ;;
    --engine=*) ENGINE_FLAG="${1#*=}"; shift ;;
    --mission) MISSIONS="${2:-}"; shift 2 ;;
    --mission=*) MISSIONS="${1#*=}"; shift ;;
    --regrade) REGRADE=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) sed -n '2,71p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "triage-run.sh: unknown flag: $1" >&2; exit 64 ;;
  esac
done

case "$KIND" in
  classify) KIND_ENGINE="${TRIAGE_CLASSIFY_ENGINE:-}" ;;
  process) KIND_ENGINE="${TRIAGE_PROCESS_ENGINE:-}" ;;
  grade) KIND_ENGINE="${TRIAGE_GRADE_ENGINE:-}" ;;
  *) echo "triage-run.sh: first argument must be classify, process or grade" >&2; exit 64 ;;
esac
if [[ "$KIND" != "grade" && ( -n "$MISSIONS" || "$REGRADE" -eq 1 ) ]]; then
  echo "triage-run.sh: --mission and --regrade apply to grade only" >&2; exit 64
fi
if [[ "$REGRADE" -eq 1 && -z "$MISSIONS" ]]; then
  echo "triage-run.sh: --regrade needs --mission ID[,ID...]; it never regrades everything" >&2; exit 64
fi
ENGINE="${ENGINE_FLAG:-${KIND_ENGINE:-${TRIAGE_ENGINE:-claude}}}"
case "$ENGINE" in
  claude|opencode) ;;
  *) echo "triage-run.sh: engine must be claude or opencode, got '$ENGINE'" >&2; exit 64 ;;
esac
if [[ ! "$TIMEOUT_MIN" =~ ^[0-9]+$ ]]; then
  echo "triage-run.sh: TRIAGE_TIMEOUT_MIN must be a whole number of minutes, got '$TIMEOUT_MIN'" >&2
  exit 64
fi
[[ -n "$LIMIT" ]] && ITEMS="$LIMIT"

mkdir -p "$LOG_DIR"
LOG_FILE="$LOG_DIR/triage.log"
STAMP="$(date -u '+%Y-%m-%dT%H%M%SZ')"
if [[ "$ENGINE" == "opencode" ]]; then
  RESULT_FILE="$LOG_DIR/triage-$KIND-$STAMP.jsonl"
else
  RESULT_FILE="$LOG_DIR/triage-$KIND-$STAMP.json"
fi

log() { printf '%s %s\n' "$(date -u '+%Y-%m-%d %H:%M:%S UTC')" "$*" | tee -a "$LOG_FILE" >&2; }

# --- Preconditions ----------------------------------------------------------

if ! api_curl -fsS --max-time 5 "$URL/healthz" >/dev/null 2>&1; then
  log "[$KIND] bishop-memory at $URL is not answering; nothing run"
  exit 2
fi
if [[ "$ENGINE" == "claude" ]]; then
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
elif ! command -v "$OPENCODE_BIN" >/dev/null 2>&1; then
  log "[$KIND] opencode CLI not found on PATH ($PATH); nothing run"
  exit 2
fi
if [[ ! -x "$MCPD_BIN" ]]; then
  log "[$KIND] $MCPD_BIN missing; building it"
  (cd "$BISHOP_ROOT" && go build -o "$MCPD_BIN" ./cmd/mcpd) || { log "[$KIND] mcpd build failed"; exit 2; }
fi

# --- Is there work? ----------------------------------------------------------

json_field() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get(sys.argv[1], ""))' "$1"; }

if [[ "$KIND" == "classify" ]]; then
  if [[ -z "$RECLASSIFY" ]]; then
    WAITING="$(api_curl -fsS "$URL/v1/findings?unclassified=1&limit=1" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["findings"]))')"
    if [[ "$WAITING" == "0" ]]; then
      log "[classify] every finding is classified; nothing to do"
      exit 0
    fi
  fi
  if [[ "$ENGINE" == "opencode" ]]; then default_model="opencode-go/glm-5.3-flash"; else default_model="haiku"; fi
  MODEL="${MODEL:-${TRIAGE_CLASSIFY_MODEL:-$default_model}}"
  MODEL_VAR="TRIAGE_CLASSIFY_MODEL"
  AGENT="findings-classifier"
  MAX_TURNS="${TRIAGE_MAX_TURNS:-60}"
  BUDGET="${TRIAGE_MAX_BUDGET_USD:-2}"
  ALLOWED="mcp__bishop-triage__*,Read"
  if [[ -n "$RECLASSIFY" ]]; then
    PROMPT="Re-classify every finding currently in category '$RECLASSIFY' (fetch them with finding_list category=$RECLASSIFY, in batches), following the Classifier section of .claude/skills/findings-triage/SKILL.md. Overwrite each classification. Model id: $MODEL."
  else
    PROMPT="Classify every unclassified finding, following the Classifier section of .claude/skills/findings-triage/SKILL.md. Model id: $MODEL."
  fi
elif [[ "$KIND" == "grade" ]]; then
  GRADE_LIMIT="${LIMIT:-${TRIAGE_GRADE_LIMIT:-10}}"
  if [[ ! "$GRADE_LIMIT" =~ ^[0-9]+$ || "$GRADE_LIMIT" -lt 1 || "$GRADE_LIMIT" -gt 10 ]]; then
    log "[grade] the grade limit must be 1-10, got '$GRADE_LIMIT' (from --limit or TRIAGE_GRADE_LIMIT); nothing run"
    exit 2
  fi
  MISSION_JSON=""
  if [[ -n "$MISSIONS" ]]; then
    MISSION_JSON="$(printf '%s' "$MISSIONS" | python3 -c '
import json, sys
ids = [m.strip() for m in sys.stdin.read().split(",") if m.strip()]
if not 1 <= len(ids) <= 10:
    sys.exit("name 1-10 missions")
print(json.dumps(ids))')" || { log "[grade] --mission must name 1-10 mission ids; nothing run"; exit 64; }
    # --regrade: the operator reopening missions, here and nowhere else.
    if [[ "$REGRADE" -eq 1 ]]; then
      for m in $(printf '%s' "$MISSION_JSON" | python3 -c 'import json,sys; print(" ".join(json.load(sys.stdin)))'); do
        if [[ "$DRY_RUN" -eq 1 ]]; then
          log "[grade] DRY RUN — would delete the verdict on $m"
        else
          api_curl -fsS -X DELETE "$URL/v1/mission-grades/$m" >/dev/null || { log "[grade] could not reopen $m for regrading; nothing run"; exit 2; }
          log "[grade] reopened $m for regrading"
        fi
      done
    fi
  else
    WAITING="$(api_curl -fsS "$URL/v1/mission-grades/waiting" | json_field waiting)"
    if [[ "$WAITING" == "0" ]]; then
      log "[grade] every finished mission has a verdict; nothing to do"
      exit 0
    fi
  fi
  if [[ "$ENGINE" == "opencode" ]]; then default_model="opencode-go/glm-5.2"; else default_model="sonnet"; fi
  MODEL="${MODEL:-${TRIAGE_GRADE_MODEL:-$default_model}}"
  MODEL_VAR="TRIAGE_GRADE_MODEL"
  AGENT="mission-grader"
  MAX_TURNS="${TRIAGE_MAX_TURNS:-20}"
  BUDGET="${TRIAGE_MAX_BUDGET_USD:-2}"
  # A grade run is one short pass; it gets a tighter default timeout.
  if [[ -z "${TRIAGE_TIMEOUT_MIN:-}" ]]; then TIMEOUT_MIN=20; fi
  ALLOWED="mcp__bishop-triage__triage_run_start,mcp__bishop-triage__triage_run_finish,mcp__bishop-triage__grade_claim,mcp__bishop-triage__grade_write,Read"
  if [[ -n "$MISSION_JSON" ]]; then
    PROMPT="Grade these finished missions, following .claude/skills/mission-grading/SKILL.md: call grade_claim once with mission_ids=$MISSION_JSON. Model id: $MODEL."
  else
    PROMPT="Grade finished missions, following .claude/skills/mission-grading/SKILL.md: call grade_claim once with limit=$GRADE_LIMIT. Model id: $MODEL."
  fi
else
  if [[ "$ENGINE" == "opencode" ]]; then default_model="opencode-go/glm-5.2"; else default_model="sonnet"; fi
  MODEL="${MODEL:-${TRIAGE_PROCESS_MODEL:-$default_model}}"
  MODEL_VAR="TRIAGE_PROCESS_MODEL"
  AGENT="findings-processor"
  MAX_TURNS="${TRIAGE_MAX_TURNS:-120}"
  BUDGET="${TRIAGE_MAX_BUDGET_USD:-5}"
  ALLOWED="mcp__bishop-triage__*,Read,Glob,Grep"
fi

# A model left over from the other engine (sonnet under opencode, or
# opencode-go/... under claude) would fail only after the agent started.
if [[ "$ENGINE" == "opencode" && "$MODEL" != */* ]]; then
  log "[$KIND] the opencode engine needs a provider/model id such as opencode-go/glm-5.2, got '$MODEL' (from --model or $MODEL_VAR); nothing run"
  exit 2
fi
if [[ "$ENGINE" == "claude" && "$MODEL" == */* ]]; then
  log "[$KIND] '$MODEL' is a provider/model id for opencode, but the engine is claude (from --model or $MODEL_VAR); nothing run"
  exit 2
fi

# --- Directories the processor may read (harness checkouts) ----------------

# macOS ships bash 3.2 (no mapfile, no associative arrays), so the directory
# list is built with a plain while-read loop over newline-separated output.
ADD_DIRS=""
if [[ "$KIND" == "process" ]]; then
  if [[ -n "${TRIAGE_ADD_DIRS:-}" ]]; then
    dir_list="$(printf '%s' "$TRIAGE_ADD_DIRS" | tr ':' '\n')"
  else
    dir_list="$(api_curl -fsS "$URL/v1/harnesses" | python3 -c '
import json, os, sys
for h in json.load(sys.stdin)["harnesses"]:
    root = h["memory_root"].rstrip("/")
    # <checkout>/.claude/memory -> <checkout>
    checkout = os.path.dirname(os.path.dirname(root))
    if os.path.isdir(checkout):
        print(checkout)')"
  fi
  while IFS= read -r d; do
    if [[ -n "$d" && -d "$d" ]]; then
      ADD_DIRS="$ADD_DIRS$d"$'\n'
    fi
  done <<<"$dir_list"
fi

# --- Engine configuration: mcpd in its triage profile, nothing else ---------

ADD_DIR_ARGS=()
OC_CONFIG=""
if [[ "$ENGINE" == "claude" ]]; then
  MCP_JSON="$(python3 - "$MCPD_BIN" "$URL" <<'PY'
import json, sys
print(json.dumps({"mcpServers": {"bishop-triage": {
    "type": "stdio", "command": sys.argv[1], "args": [],
    "env": {"MCPD_PROFILE": "triage", "BISHOP_MEMORY_URL": sys.argv[2], "BISHOP_HARNESS": "triage"}}}}))
PY
)"
  while IFS= read -r d; do
    if [[ -n "$d" ]]; then ADD_DIR_ARGS+=(--add-dir "$d"); fi
  done <<<"$ADD_DIRS"
else
  # Layered over the user's own OpenCode config for this run only: the
  # bishop-triage server, the step cap, read access to the harness checkouts
  # (OpenCode has no --add-dir; outside the project it needs an
  # external_directory allow), and nothing shared, snapshotted, formatted or
  # auto-updated. small_model is pinned to the run's model so no side call
  # (titles, compaction) goes to another provider.
  OC_CONFIG="$(python3 - "$MCPD_BIN" "$URL" "$AGENT" "$MAX_TURNS" "$MODEL" "$ADD_DIRS" <<'PY'
import json, sys
mcpd, url, agent, steps, model, dirs = sys.argv[1:7]
agent_cfg = {"steps": int(steps)}
external = {}
for d in filter(None, dirs.split("\n")):
    external[d.rstrip("/")] = "allow"
    external[d.rstrip("/") + "/*"] = "allow"
if external:
    agent_cfg["permission"] = {"external_directory": external}
print(json.dumps({
    "share": "disabled", "autoupdate": False, "snapshot": False,
    "lsp": False, "formatter": False, "small_model": model,
    "mcp": {"bishop-triage": {
        "type": "local", "command": [mcpd], "enabled": True,
        "environment": {"MCPD_PROFILE": "triage", "BISHOP_MEMORY_URL": url, "BISHOP_HARNESS": "triage"}}},
    "agent": {agent: agent_cfg},
}))
PY
)"
  # OpenCode does not fail on an unknown --agent: it warns and falls back to
  # its default agent, which can edit files and run shell commands. Refuse to
  # run unless the agent resolves to our definition with neither.
  agent_json="$(cd "$BISHOP_ROOT" && OPENCODE_CONFIG_CONTENT="$OC_CONFIG" "$OPENCODE_BIN" debug agent "$AGENT" --pure </dev/null 2>/dev/null)" || {
    log "[$KIND] OpenCode cannot resolve agent $AGENT (.opencode/agents/$AGENT.md); nothing run"
    exit 2
  }
  if ! printf '%s' "$agent_json" | python3 -c '
import json, sys
d = json.load(sys.stdin)
tools = d.get("tools") or {}
ok = d.get("name") == sys.argv[1] and not d.get("native") and not any(tools.get(t) for t in ("edit", "write", "bash", "task"))
sys.exit(0 if ok else 1)' "$AGENT"; then
    log "[$KIND] OpenCode resolved $AGENT to something other than .opencode/agents/$AGENT.md, or it can edit files or run commands; nothing run"
    exit 2
  fi
fi

run_agent() {
  local prompt="$1" title="$2"
  local cmd=()
  if [[ "$TIMEOUT_MIN" != "0" ]] && command -v perl >/dev/null 2>&1; then
    # An alarm survives exec, so the agent process itself gets SIGALRM at the
    # limit. A hung run would otherwise hold the launchd job, and with it
    # every later night's run, indefinitely.
    cmd=(perl -e 'alarm shift; exec @ARGV or die "exec: $!\n"' "$((10#$TIMEOUT_MIN * 60))")
  fi
  if [[ "$ENGINE" == "claude" ]]; then
    cmd+=("$CLAUDE_BIN" -p "$prompt"
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
  else
    # --pure leaves the user's OpenCode plugins out of an unattended run.
    cmd+=(env "OPENCODE_CONFIG_CONTENT=$OC_CONFIG" "$OPENCODE_BIN" run "$prompt"
      --pure
      --agent "$AGENT"
      --model "$MODEL"
      --title "$title"
      --format json)
  fi
  if [[ "$DRY_RUN" -eq 1 ]]; then
    log "[$KIND] DRY RUN — would run from $BISHOP_ROOT (engine=$ENGINE):"
    printf '  %q' "${cmd[@]}" >&2; printf '\n' >&2
    return 0
  fi
  log "[$KIND] starting engine=$ENGINE agent=$AGENT model=$MODEL"
  local rc=0
  # stdin from /dev/null: `opencode run` appends piped stdin to the prompt and
  # waits for end of input, so an open stdin hangs it before the first request.
  (cd "$BISHOP_ROOT" && "${cmd[@]}") </dev/null >"$RESULT_FILE" 2>>"$LOG_FILE" || rc=$?
  if [[ $rc -eq 142 ]]; then
    log "[$KIND] agent stopped after TRIAGE_TIMEOUT_MIN=$TIMEOUT_MIN minutes; see $RESULT_FILE"
    return 1
  fi
  if [[ $rc -ne 0 ]]; then
    log "[$KIND] agent exited $rc; see $RESULT_FILE and $LOG_FILE"
    return 1
  fi
  local summary
  summary="$(python3 - "$RESULT_FILE" "$ENGINE" <<'PY'
import json, sys
path, engine = sys.argv[1], sys.argv[2]

if engine == "claude":
    try:
        d = json.load(open(path))
    except Exception as exc:
        print(f"result not parseable: {exc}")
        sys.exit(0)
    text = (d.get("result") or "").strip().replace("\n", " | ")
    flag = "ERROR " if d.get("is_error") else ""
    print(f"{flag}turns={d.get('num_turns')} cost_usd={d.get('total_cost_usd')} stop={d.get('stop_reason')} :: {text[:400]}")
    sys.exit(3 if d.get("is_error") else 0)

# opencode: one JSON event per line. A step ends with step_finish (its cost
# and reason); the closing words are the text parts of the last message from
# the agent; a provider or session failure arrives as an error event. An
# agent that gives up closes its triage run with status failed and still
# exits cleanly, so that call is read too. (No apostrophes in this heredoc:
# bash 3.2 misparses them inside a substitution.)
steps, cost, reason, last_msg = 0, 0.0, None, None
texts, errors, tool_errors, run_failed = [], [], 0, False
for line in open(path):
    try:
        e = json.loads(line)
    except ValueError:
        continue
    kind, part = e.get("type"), e.get("part") or {}
    state = part.get("state") or {}
    if kind == "step_finish":
        steps += 1
        cost += part.get("cost") or 0
        reason = part.get("reason")
    elif kind == "text":
        if part.get("messageID") != last_msg:
            texts, last_msg = [], part.get("messageID")
        texts.append(part.get("text") or "")
    elif kind == "tool_use":
        if state.get("status") == "error":
            tool_errors += 1
        elif str(part.get("tool", "")).endswith("triage_run_finish") and (state.get("input") or {}).get("status") == "failed":
            run_failed = True
    elif kind == "error":
        err = e.get("error") or {}
        errors.append((err.get("data") or {}).get("message") or err.get("name") or "error")
text = " ".join(texts).strip().replace("\n", " | ")
failed = bool(errors) or steps == 0 or run_failed
flag = ("ERROR run closed as failed " if run_failed else "ERROR ") if failed else ""
detail = f" errors={'; '.join(errors)[:200]}" if errors else ""
print(f"{flag}steps={steps} tool_errors={tool_errors} cost_usd={round(cost, 4)} stop={reason}{detail} :: {text[:400]}")
sys.exit(3 if failed else 0)
PY
)" || { log "[$KIND] agent reported an error: $summary"; return 1; }
  log "[$KIND] done $summary"
  return 0
}

if [[ "$KIND" == "classify" ]]; then
  run_agent "$PROMPT" "bishop-memory triage classify"
  exit $?
fi

# --- grade: one pass over at most GRADE_LIMIT finished missions -------------

if [[ "$KIND" == "grade" ]]; then
  run_agent "$PROMPT" "bishop-memory mission grade"
  exit $?
fi

# --- process: one or more categories by rotation ---------------------------

# The claude engine names the harness checkouts through --add-dir; OpenCode
# has no equivalent, so its prompt names them.
DIRS_NOTE=""
if [[ "$ENGINE" == "opencode" && -n "$ADD_DIRS" ]]; then
  DIRS_NOTE=" The harness checkouts you may read: $(printf '%s' "$ADD_DIRS" | tr '\n' ' ' | sed 's/ *$//')."
fi

ran=0
for ((i = 0; i < CATEGORIES_PER_RUN; i++)); do
  if [[ -n "$CATEGORY" ]]; then
    slug="$CATEGORY"
    CATEGORY=""   # an explicit category runs once
  else
    next="$(api_curl -sS "$URL/v1/triage/next-category")"
    slug="$(printf '%s' "$next" | json_field category)"
    if [[ -z "$slug" ]]; then
      if [[ $ran -eq 0 ]]; then log "[process] no category has findings waiting; nothing to do"; fi
      break
    fi
  fi
  PROMPT="Process category '$slug' with an item cap of $ITEMS findings, following the Processor section of .claude/skills/findings-triage/SKILL.md. Call triage_category_findings with category=$slug and limit=$ITEMS.$DIRS_NOTE Model id: $MODEL."
  run_agent "$PROMPT" "bishop-memory triage process $slug" || exit 1
  ran=$((ran + 1))
done
exit 0

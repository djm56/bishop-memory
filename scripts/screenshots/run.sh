#!/usr/bin/env bash
# scripts/screenshots/run.sh — regenerate the documentation screenshots.
#
# Usage: scripts/screenshots/run.sh [--out DIR] [--port N] [--keep]
#        make screenshots
#
# 1. Builds memoryd into a temporary directory and starts it on a free port
#    (default 8790) against a FRESH database in that directory. Your real
#    service and database are never touched.
# 2. Loads the taxonomy and the made-up demo findings in
#    scripts/screenshots/demo.json, through the HTTP API.
# 3. Captures each tab of /triage in light and dark with headless Chrome
#    (scripts/screenshots/capture.js) into docs/wiki/images/, which
#    `make wiki-publish` uploads with the wiki pages.
# 4. Stops the service and removes the temporary directory (--keep leaves
#    the service running and prints its URL, to look at the demo page).
#
# Needs: go, python3, node (18+) with npm, and Chrome or Chromium (set
# CHROME_PATH if it is not in a standard place). The first run installs
# playwright-core into scripts/screenshots/node_modules (gitignored); it
# does not download a browser. If oxipng or optipng is installed the images
# are compressed losslessly afterwards (brew install oxipng).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BISHOP_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
OUT="$BISHOP_ROOT/docs/wiki/images"
PORT=8790
KEEP=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --out) OUT="${2:-}"; shift 2 ;;
    --port) PORT="${2:-}"; shift 2 ;;
    --keep) KEEP=1; shift ;;
    -h|--help) sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "screenshots: unknown flag: $1" >&2; exit 64 ;;
  esac
done

for tool in go python3 node npm; do
  command -v "$tool" >/dev/null 2>&1 || { echo "screenshots: $tool not found on PATH" >&2; exit 2; }
done
if lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "screenshots: port $PORT is in use; pass --port" >&2
  exit 2
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/bishop-screenshots.XXXXXX")"
PID=""
cleanup() {
  if [[ -n "$PID" && "$KEEP" -eq 0 ]]; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
    if lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
      echo "screenshots: WARNING: something is still listening on port $PORT after stopping the demo service" >&2
    fi
  fi
  if [[ "$KEEP" -eq 0 ]]; then rm -rf "$WORK"; fi
}
trap cleanup EXIT

echo "[screenshots] building memoryd"
(cd "$BISHOP_ROOT" && go build -o "$WORK/memoryd" ./cmd/memoryd)

# memoryd reads db/schema.sql relative to its working directory, so it runs
# from the checkout; DB_PATH points at the throwaway database.
echo "[screenshots] starting a throwaway service on 127.0.0.1:$PORT"
# exec replaces the subshell with memoryd, so $! is memoryd's own PID and the
# cleanup trap stops the server itself. Without exec, $! is the subshell's PID
# and killing it leaves memoryd running, orphaned, on the port.
(cd "$BISHOP_ROOT" && HTTP_HOST=127.0.0.1 PORT="$PORT" DB_PATH="$WORK/demo.db" APP_ENV=production \
  exec "$WORK/memoryd" >"$WORK/memoryd.log" 2>&1) &
PID=$!
URL="http://127.0.0.1:$PORT"
for _ in $(seq 1 40); do
  curl -fsS "$URL/healthz" >/dev/null 2>&1 && break
  sleep 0.25
done
curl -fsS "$URL/healthz" >/dev/null || { echo "screenshots: the service did not start; log:" >&2; cat "$WORK/memoryd.log" >&2; exit 1; }

echo "[screenshots] loading the taxonomy and the demo data"
"$BISHOP_ROOT/scripts/triage-seed-categories.py" --url "$URL" >/dev/null
"$SCRIPT_DIR/seed-demo.py" --url "$URL"

if [[ ! -d "$SCRIPT_DIR/node_modules/playwright-core" ]]; then
  echo "[screenshots] installing playwright-core (first run only)"
  (cd "$SCRIPT_DIR" && npm install --silent --no-audit --no-fund)
fi

mkdir -p "$OUT"
(cd "$BISHOP_ROOT" && node "$SCRIPT_DIR/capture.js" "$URL" "$OUT")

# Lossless compression when a tool is installed; the images are committed, so
# smaller files keep the repository and the wiki lighter. Pixels are unchanged.
if command -v oxipng >/dev/null 2>&1; then
  echo "[screenshots] compressing with oxipng"
  oxipng --quiet -o 3 --strip safe "$OUT"/*.png
elif command -v optipng >/dev/null 2>&1; then
  echo "[screenshots] compressing with optipng"
  optipng -quiet -o2 -strip all "$OUT"/*.png
fi

if [[ "$KEEP" -eq 1 ]]; then
  echo "[screenshots] --keep: the demo service is still running at $URL/triage (pid $PID, data in $WORK)"
  trap - EXIT
fi

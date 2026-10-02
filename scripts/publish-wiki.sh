#!/usr/bin/env bash
# scripts/publish-wiki.sh — push docs/wiki/*.md to the GitHub wiki.
#
# A GitHub wiki is its own git repository (<repo>.wiki.git). GitHub creates
# it the first time a page is made in the web UI, and there is no API to do
# that, so the ONE manual step is: open https://github.com/<owner>/<repo>/wiki
# and click "Create the first page" (any content). After that this script
# owns the wiki: it clones the wiki repo, replaces every page with the
# contents of docs/wiki/, commits, and pushes.
#
# Usage:
#   scripts/publish-wiki.sh [--dry-run] [--remote git@github.com:owner/repo.wiki.git]
#
# The remote defaults to the origin remote of this checkout with ".wiki"
# inserted before ".git". Pages are versioned here in docs/wiki/ so a wiki
# edit made on github.com is overwritten by the next publish — edit the
# files in docs/wiki/ and publish, never the wiki directly.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BISHOP_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
WIKI_SRC="$BISHOP_ROOT/docs/wiki"

DRY_RUN=0
REMOTE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY_RUN=1; shift ;;
    --remote) REMOTE="${2:-}"; shift 2 ;;
    --remote=*) REMOTE="${1#*=}"; shift ;;
    -h|--help) sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "publish-wiki.sh: unknown flag: $1" >&2; exit 64 ;;
  esac
done

if [[ -z "$REMOTE" ]]; then
  origin="$(cd "$BISHOP_ROOT" && git remote get-url origin)"
  REMOTE="${origin%.git}.wiki.git"
fi

if [[ ! -d "$WIKI_SRC" ]] || ! ls "$WIKI_SRC"/*.md >/dev/null 2>&1; then
  echo "publish-wiki.sh: no pages in $WIKI_SRC" >&2
  exit 66
fi

if ! git ls-remote "$REMOTE" >/dev/null 2>&1; then
  echo "publish-wiki.sh: wiki repository $REMOTE does not exist yet." >&2
  echo "  Open the repository's Wiki tab on github.com and create the first page (any content)," >&2
  echo "  then re-run this script. GitHub only creates the wiki git repository from the web UI." >&2
  exit 2
fi

WORK="$(mktemp -d -t bishop-wiki-XXXXXX)"
trap 'rm -rf "$WORK"' EXIT
git clone --quiet "$REMOTE" "$WORK/wiki"

# Replace every page: remove old .md files, copy the source set.
find "$WORK/wiki" -maxdepth 1 -name '*.md' -delete
cp "$WIKI_SRC"/*.md "$WORK/wiki/"

# Images (docs/wiki/images/, written by `make screenshots`) are replaced the
# same way, so a screenshot removed from the source disappears from the wiki.
rm -rf "$WORK/wiki/images"
if [[ -d "$WIKI_SRC/images" ]]; then
  cp -R "$WIKI_SRC/images" "$WORK/wiki/images"
fi

cd "$WORK/wiki"
git add -A
if git diff --cached --quiet; then
  echo "[publish-wiki] wiki already matches docs/wiki/; nothing to push"
  exit 0
fi
echo "[publish-wiki] changes:"
git diff --cached --stat | tail -20
if [[ "$DRY_RUN" -eq 1 ]]; then
  echo "[publish-wiki] --dry-run: not committing or pushing"
  exit 0
fi
git -c user.name="$(cd "$BISHOP_ROOT" && git config user.name)" \
    -c user.email="$(cd "$BISHOP_ROOT" && git config user.email)" \
    commit --quiet -m "Publish wiki from docs/wiki ($(cd "$BISHOP_ROOT" && git rev-parse --short HEAD))"
git push --quiet origin HEAD
echo "[publish-wiki] pushed to $REMOTE"

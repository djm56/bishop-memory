#!/usr/bin/env bash
# scripts/triage-deploy.sh
#
# Create or update the deploy worktree that the scheduled triage jobs run from,
# and rebuild bin/mcpd in it. Run it through the Makefile:
#
#   make triage-deploy                        # worktree at $HOME/bishop-memory-deploy
#   make triage-deploy DEPLOY_DIR=/abs/path   # worktree at /abs/path
#
# or directly:
#
#   scripts/triage-deploy.sh [--dir DIR]
#
# DIR must be an absolute path. Without --dir it is $DEPLOY_DIR when that is set,
# and $HOME/bishop-memory-deploy otherwise.
#
# The worktree is pinned to origin/main and only ever moved forward:
#
#   1. fetch origin main into the repository that holds this script;
#   2. if DIR does not exist, create it as a detached worktree at origin/main;
#      if DIR is a linked worktree of that repository, fast-forward it to origin/main;
#   3. remove bin/mcpd in DIR and rebuild it there with `make build-mcpd`.
#
# The worktree is detached because git will not check out main in a second worktree
# while the development checkout holds it. The script never moves DIR onto a branch.
#
# A worktree is refused, with a message and HEAD left where it is, when:
#   - DIR is not a linked worktree of this repository: a plain directory, a worktree
#     of another repository, or this repository's main checkout;
#   - DIR is on a branch rather than detached;
#   - DIR has a modified, staged or untracked file (ignored files, such as bin/, do not count);
#   - DIR has a commit that origin/main does not have (DIR is ahead of origin/main, or diverged).
#
# Exit status:
#   0   DIR is at origin/main and bin/mcpd was built from it
#   1   refused; the message names the path and the reason
#   2   git or make is not on PATH, or a git step could not run or finish
#   3   the build failed; bin/mcpd was removed, and DIR may already be at origin/main
#   64  usage
#
# Known gap: a DIR inside this checkout is not refused. Git accepts it, and the
# development checkout then shows the deploy worktree as an untracked directory.

set -euo pipefail

usage() {
  sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
}

DIR="${DEPLOY_DIR:-$HOME/bishop-memory-deploy}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir) DIR="${2:-}"; shift 2 ;;
    --dir=*) DIR="${1#*=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "triage-deploy.sh: unknown argument: $1" >&2; exit 64 ;;
  esac
done

case "$DIR" in
  /*) ;;
  *) echo "triage-deploy.sh: the deploy path must be an absolute path, got: '$DIR'" >&2; exit 64 ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

for tool in git make; do
  if ! command -v "$tool" >/dev/null; then
    echo "triage-deploy.sh: $tool is not on PATH ($PATH); nothing was changed" >&2
    exit 2
  fi
done

if ! REPO="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>&1)"; then
  echo "triage-deploy.sh: $SCRIPT_DIR is not in a git checkout: $REPO" >&2
  exit 2
fi

refuse() {
  echo "triage-deploy.sh: refused: $*" >&2
  exit 1
}

# update_existing brings a DIR that already exists to origin/main, or refuses it.
update_existing() {
  if [[ ! -d "$DIR" ]]; then
    refuse "$DIR exists and is not a directory"
  fi

  local real_dir top dir_git common repo_common branch changes head rc
  real_dir="$(cd "$DIR" && pwd -P)"

  # A plain directory, or one inside another checkout, has a different top-level or no repository at all.
  if ! top="$(git -C "$DIR" rev-parse --show-toplevel 2>&1)"; then
    refuse "$DIR is not a git worktree ($top)"
  fi
  if [[ "$top" != "$real_dir" ]]; then
    refuse "$DIR is inside another checkout ($top), not a worktree of its own"
  fi

  # In the main checkout the git dir and the common dir are the same directory; in a linked worktree they differ.
  dir_git="$(cd "$DIR" && cd "$(git rev-parse --git-dir)" && pwd -P)"
  common="$(cd "$DIR" && cd "$(git rev-parse --git-common-dir)" && pwd -P)"
  repo_common="$(cd "$REPO" && cd "$(git rev-parse --git-common-dir)" && pwd -P)"
  if [[ "$dir_git" == "$common" ]]; then
    refuse "$DIR is the main checkout of a repository; the deploy path must be a linked worktree, and the main checkout is not touched"
  fi
  if [[ "$common" != "$repo_common" ]]; then
    refuse "$DIR is a worktree of another repository ($common), not of $REPO"
  fi

  if branch="$(git -C "$DIR" symbolic-ref -q HEAD)"; then
    refuse "$DIR is on branch ${branch#refs/heads/}; the deploy worktree must stay detached at origin/main"
  fi

  # Porcelain status lists every modified, staged and untracked path, one per line. Empty output means clean.
  if ! changes="$(git -C "$DIR" status --porcelain --untracked-files=all 2>&1)"; then
    echo "triage-deploy.sh: git status failed in $DIR; nothing was changed: $changes" >&2
    exit 2
  fi
  if [[ -n "$changes" ]]; then
    echo "triage-deploy.sh: refused: $DIR has changes; commit, stash or remove them first. Changed paths:" >&2
    printf '%s\n' "$changes" | sed 's/^/    /' >&2
    exit 1
  fi

  if ! head="$(git -C "$DIR" rev-parse --verify --quiet 'HEAD^{commit}')"; then
    echo "triage-deploy.sh: could not read HEAD in $DIR; nothing was changed" >&2
    exit 2
  fi
  if [[ "$head" == "$NEW" ]]; then
    echo "[triage-deploy] $DIR is already at origin/main ($(git -C "$DIR" rev-parse --short HEAD))"
    return 0
  fi

  # Exit 0 means HEAD is an ancestor of origin/main, so a fast-forward is possible. Exit 1 means it is not.
  rc=0
  git -C "$DIR" merge-base --is-ancestor "$head" "$NEW" || rc=$?
  if [[ $rc -eq 1 ]]; then
    refuse "$DIR has a commit that origin/main does not have (HEAD ${head:0:7} is not an ancestor of origin/main), so it is ahead of or diverged from origin/main; it was not changed"
  elif [[ $rc -ne 0 ]]; then
    echo "triage-deploy.sh: git could not compare HEAD with origin/main in $DIR (exit $rc); nothing was changed" >&2
    exit 2
  fi

  if ! git -C "$DIR" merge --quiet --ff-only "$NEW"; then
    echo "triage-deploy.sh: fast-forward of $DIR to origin/main failed; nothing was changed" >&2
    exit 2
  fi
  echo "[triage-deploy] fast-forwarded $DIR from ${head:0:7} to origin/main ($(git -C "$DIR" rev-parse --short HEAD))"
}

echo "[triage-deploy] fetching origin main into $REPO"
if ! git -C "$REPO" fetch --quiet origin '+refs/heads/main:refs/remotes/origin/main'; then
  echo "triage-deploy.sh: fetch of origin main failed; $DIR was not changed" >&2
  exit 2
fi
if ! NEW="$(git -C "$REPO" rev-parse --verify --quiet 'refs/remotes/origin/main^{commit}')"; then
  echo "triage-deploy.sh: origin/main does not name a commit after the fetch; $DIR was not changed" >&2
  exit 2
fi

if [[ ! -e "$DIR" && ! -L "$DIR" ]]; then
  mkdir -p "$(dirname "$DIR")"
  if ! git -C "$REPO" worktree add --quiet --detach "$DIR" "$NEW"; then
    echo "triage-deploy.sh: git could not create the worktree at $DIR" >&2
    exit 2
  fi
  echo "[triage-deploy] created $DIR at origin/main ($(git -C "$DIR" rev-parse --short HEAD))"
else
  update_existing
fi

# bin/mcpd is removed before the build, so a failed build leaves no older binary for the jobs to run.
# The new binary is a new file, not an overwrite of the old one.
echo "[triage-deploy] building bin/mcpd in $DIR"
rm -f "$DIR/bin/mcpd"
if ! make -C "$DIR" build-mcpd; then
  echo "triage-deploy.sh: build failed in $DIR; bin/mcpd was removed so no earlier binary runs; the worktree is at $(git -C "$DIR" rev-parse --short HEAD)" >&2
  exit 3
fi
echo "[triage-deploy] $DIR is at $(git -C "$DIR" rev-parse --short HEAD) with bin/mcpd rebuilt"
exit 0

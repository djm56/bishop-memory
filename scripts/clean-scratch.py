#!/usr/bin/env python3
"""Clear old scratch files from every harness's memory workspace.

A harness keeps scratch notes, backups and calibration logs under
<memory root>/workspace/. They stay searchable in bishop-memory until they are
cleared. Run this on the machine that holds the harness checkouts: it moves
every workspace file older than --days (by modification time) into the Trash
(~/.Trash on macOS, ~/.local/share/Trash/files elsewhere), under
bishop-scratch-<timestamp>/<harness>/, and deletes their documents through the
API (POST /v1/documents/delete), so it works against a remote memoryd too.
Harnesses and their roots come from GET /v1/harnesses; only roots that exist
on this machine are cleaned. workspace/README.md and hidden files
(.gitkeep and the like) are always kept.

Dry run by default: lists what would go. --apply moves and deletes.

URL and API key come from --url / BISHOP_MEMORY_URL and BISHOP_MEMORY_API_KEY,
or ~/.config/bishop-memory/client.env.

Usage:
  scripts/clean-scratch.py [--days 30] [--harness kirsch] [--url URL] [--apply]
"""

import argparse
import datetime as dt
import importlib.util
import os
import shutil
import sys
import time
import urllib.parse

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("reconcile_memory", os.path.join(HERE, "reconcile-memory.py"))
reconcile = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(reconcile)

KEEP = {"README.md"}


def scratch_files(workspace, cutoff):
    """Yield (path, size) for every file under workspace older than cutoff."""
    for dirpath, dirnames, filenames in os.walk(workspace):
        dirnames[:] = [d for d in dirnames if not os.path.islink(os.path.join(dirpath, d))]
        for name in filenames:
            path = os.path.join(dirpath, name)
            if name.startswith(".") or (dirpath == workspace and name in KEEP):
                continue
            try:
                st = os.lstat(path)
            except FileNotFoundError:
                continue
            if st.st_mtime < cutoff:
                yield path, st.st_size


def prune_empty_dirs(workspace):
    """Remove directories under workspace left empty, deepest first."""
    for dirpath, _, _ in sorted(os.walk(workspace), key=lambda w: -w[0].count(os.sep)):
        if dirpath != workspace and not os.listdir(dirpath):
            os.rmdir(dirpath)


def human(n):
    for unit in ("B", "KB", "MB", "GB"):
        if n < 1024 or unit == "GB":
            return f"{n:.0f} {unit}" if unit == "B" else f"{n:.1f} {unit}"
        n /= 1024


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--days", type=int, default=30, help="clear files older than this many days (default 30)")
    parser.add_argument("--harness", help="only this harness (default: every registered harness)")
    parser.add_argument("--url", default=os.environ.get("BISHOP_MEMORY_URL", "http://127.0.0.1:8787"))
    parser.add_argument("--apply", action="store_true", help="move and delete (default: dry run)")
    args = parser.parse_args()
    if args.days < 1:
        parser.error("--days must be at least 1")

    client = reconcile.Client(args.url)
    _, body = client.get("/v1/harnesses")
    rows = sorted(((h["name"], h["memory_root"]) for h in body["harnesses"]),
                  key=lambda r: r[0])
    if args.harness:
        rows = [r for r in rows if r[0] == args.harness]
        if not rows:
            sys.exit(f"no registered harness named {args.harness!r}")

    cutoff = time.time() - args.days * 86400
    trash_root = "~/.Trash" if sys.platform == "darwin" else "~/.local/share/Trash/files"
    trash = os.path.expanduser(f"{trash_root}/bishop-scratch-{dt.datetime.now():%Y%m%d-%H%M%S}")
    seen = set()
    total_files = total_bytes = total_docs = 0

    print(("Clearing" if args.apply else "Dry run (nothing changed; --apply to clear):") +
          f" workspace files older than {args.days} days")
    for name, root in rows:
        workspace = os.path.join(os.path.realpath(root), "workspace")
        if workspace in seen or not os.path.isdir(workspace):
            continue
        seen.add(workspace)

        files = sorted(scratch_files(workspace, cutoff))
        size = sum(s for _, s in files)
        _, hashes = client.get("/v1/documents/hashes?harness=" + urllib.parse.quote(name))
        indexed = set(p.split(":")[0] for p in hashes["hashes"])
        docs = sum(1 for path, _ in files if path in indexed)
        print(f"  {name}: {len(files)} files, {human(size)}, {docs} search documents  ({workspace})")
        for path, _ in files[:5]:
            print(f"      {os.path.relpath(path, workspace)}")
        if len(files) > 5:
            print(f"      … and {len(files) - 5} more")

        if args.apply and files:
            for path, _ in files:
                dest = os.path.join(trash, name, os.path.relpath(path, workspace))
                os.makedirs(os.path.dirname(dest), exist_ok=True)
                shutil.move(path, dest)
            paths = [p for p, _ in files]
            for i in range(0, len(paths), 1000):
                if not client.write("POST", "/v1/documents/delete", {"paths": paths[i:i + 1000]}, "delete documents"):
                    sys.exit(f"clean-scratch: the files are in {trash}, but deleting their documents failed; "
                             f"run scripts/push-memory.py --root {root} --harness {name} --prune to finish")
            prune_empty_dirs(workspace)

        total_files += len(files)
        total_bytes += size
        total_docs += docs

    print(f"Total: {total_files} files, {human(total_bytes)}, {total_docs} search documents")
    if args.apply and total_files:
        print(f"Moved to {trash}")


if __name__ == "__main__":
    main()

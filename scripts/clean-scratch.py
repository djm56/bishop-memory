#!/usr/bin/env python3
"""Clear old scratch files from every harness's memory workspace.

A harness keeps scratch notes, backups and calibration logs under
<memory root>/workspace/. They stay searchable in bishop-memory until they are
cleared. This moves every workspace file older than --days (by modification
time) into the macOS Trash, under bishop-scratch-<timestamp>/<harness>/, and
deletes its documents and search rows. workspace/README.md and hidden files
(.gitkeep and the like) are always kept.

Dry run by default: lists what would go. --apply moves and deletes.

Usage:
  scripts/clean-scratch.py [--days 30] [--harness kirsch] [--db data/memory.db] [--apply]
"""

import argparse
import datetime as dt
import os
import shutil
import sqlite3
import sys
import time

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
    parser.add_argument("--db", default="data/memory.db")
    parser.add_argument("--apply", action="store_true", help="move and delete (default: dry run)")
    args = parser.parse_args()
    if args.days < 1:
        parser.error("--days must be at least 1")

    db = sqlite3.connect(args.db, timeout=30)
    db.execute("PRAGMA busy_timeout = 30000")
    rows = db.execute("SELECT name, memory_root FROM harnesses ORDER BY last_seen_at DESC").fetchall()
    if args.harness:
        rows = [r for r in rows if r[0] == args.harness]
        if not rows:
            sys.exit(f"no registered harness named {args.harness!r}")

    cutoff = time.time() - args.days * 86400
    trash = os.path.expanduser(f"~/.Trash/bishop-scratch-{dt.datetime.now():%Y%m%d-%H%M%S}")
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
        docs = 0
        for path, _ in files:
            docs += db.execute(
                "SELECT COUNT(*) FROM documents WHERE source_path = ? OR source_path LIKE ? || ':%'",
                (path, path),
            ).fetchone()[0]
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
                ids = [r[0] for r in db.execute(
                    "SELECT id FROM documents WHERE source_path = ? OR source_path LIKE ? || ':%'", (path, path))]
                for doc_id in ids:
                    db.execute("DELETE FROM documents_fts WHERE rowid = ?", (doc_id,))
                    db.execute("DELETE FROM documents WHERE id = ?", (doc_id,))
            db.commit()
            prune_empty_dirs(workspace)

        total_files += len(files)
        total_bytes += size
        total_docs += docs

    print(f"Total: {total_files} files, {human(total_bytes)}, {total_docs} search documents")
    if args.apply and total_files:
        print(f"Moved to {trash}")


if __name__ == "__main__":
    main()

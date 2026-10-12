#!/usr/bin/env python3
"""Rename a harness everywhere bishop-memory records it.

A harness's name is BISHOP_HARNESS in its bishop-memory.conf and in its MCP
registration (.mcp.json or opencode.json). Changing it there starts new rows
under the new name; this script moves the rows already written under the old
one, so the harness keeps one history:

  harnesses            the registry row is renamed (memory_root kept); if the
                       new name is already registered, the old row is dropped
  missions, findings,
  directive_proposals,
  documents            the harness column
  flight_recorder,
  mission_steps,
  service_records      an agent recorded as "OLD:agent" becomes "NEW:agent"

Journal notes that mention the old name in their text are left as written:
the journal is an append-only record of what was said at the time.

Safe to run again. A harness session started before its settings changed
keeps writing under the old name until it restarts; run this once more after
that to move what it wrote in between.

Dry run by default: it reports what it would change. --apply writes, after
copying the database to <db stem>.bak-<timestamp>.db with SQLite's backup API
(safe while memoryd is running), all in one transaction. It opens the database
file directly, so run it on the machine that holds it.

Usage:
  scripts/rename-harness.py --rename OLD=NEW [--rename OLD=NEW ...] [--db data/memory.db] [--apply]
"""
import argparse
import datetime as dt
import re
import sqlite3
import sys

NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
HARNESS_COLUMNS = ("missions", "findings", "directive_proposals", "documents")
AGENT_COLUMNS = ("flight_recorder", "mission_steps", "service_records")


def has_column(db, table, column):
    return any(row[1] == column for row in db.execute(f"PRAGMA table_info({table})"))


def rename(db, old, new):
    """Move every record of OLD to NEW. Returns report lines."""
    out = []
    for table in HARNESS_COLUMNS:
        if not has_column(db, table, "harness"):
            continue
        n = db.execute(f"UPDATE {table} SET harness = ? WHERE harness = ?", (new, old)).rowcount
        if n:
            out.append(f"{table}: {n} rows")
    prefix = old + ":"
    for table in AGENT_COLUMNS:
        n = db.execute(
            f"UPDATE {table} SET agent = ? || substr(agent, ?) WHERE substr(agent, 1, ?) = ?",
            (new + ":", len(prefix) + 1, len(prefix), prefix)).rowcount
        if n:
            out.append(f"{table}.agent: {n} rows")
    old_row = db.execute("SELECT memory_root FROM harnesses WHERE name = ?", (old,)).fetchone()
    new_row = db.execute("SELECT memory_root FROM harnesses WHERE name = ?", (new,)).fetchone()
    if old_row and new_row:
        db.execute("DELETE FROM harnesses WHERE name = ?", (old,))
        out.append(f"harnesses: {new} already registered ({new_row[0]}); dropped {old}")
    elif old_row:
        db.execute("UPDATE harnesses SET name = ? WHERE name = ?", (new, old))
        out.append(f"harnesses: renamed ({old_row[0]})")
    return out


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--db", default="data/memory.db")
    parser.add_argument("--rename", action="append", default=[], metavar="OLD=NEW", required=True)
    parser.add_argument("--apply", action="store_true", help="write the changes (default: dry run)")
    args = parser.parse_args()

    pairs = []
    for pair in args.rename:
        old, sep, new = pair.partition("=")
        if not sep or not NAME.match(old) or not NAME.match(new) or old == new:
            parser.error(f"--rename wants OLD=NEW, two different harness names, got {pair!r}")
        pairs.append((old, new))
    olds, news = {o for o, _ in pairs}, {n for _, n in pairs}
    if len(olds) != len(pairs) or len(news) != len(pairs) or olds & news:
        parser.error("each name may appear once, and no name may be both renamed and a rename target")

    db = sqlite3.connect(args.db, timeout=30)
    db.execute("PRAGMA busy_timeout = 30000")
    # A run is stopped by its timeout (45 minutes at most), so one still marked
    # running after two hours died without closing and writes nothing more.
    running = db.execute("""SELECT COUNT(*) FROM triage_runs WHERE status = 'running'
                             AND started_at > datetime('now', '-2 hours')""").fetchone()[0]
    if running and args.apply:
        sys.exit(f"{running} triage run(s) in progress; re-run when they finish")

    if args.apply:
        # Keep the .db suffix so .gitignore's *.db rule covers the backup.
        stem = args.db[:-3] if args.db.endswith(".db") else args.db
        backup = f"{stem}.bak-{dt.datetime.now():%Y%m%d-%H%M%S}.db"
        with sqlite3.connect(backup) as dst:
            db.backup(dst)
        print(f"backup: {backup}")

    report = []
    try:
        db.execute("BEGIN IMMEDIATE")
        for old, new in pairs:
            lines = rename(db, old, new)
            report.append(f"{old} -> {new}: " + ("; ".join(lines) if lines else "nothing recorded under the old name"))
        if args.apply:
            db.commit()
        else:
            db.rollback()
    except Exception:
        db.rollback()
        raise

    print("applied:" if args.apply else "dry run (nothing written; --apply to write):")
    for line in report:
        print(f"  {line}")


if __name__ == "__main__":
    main()

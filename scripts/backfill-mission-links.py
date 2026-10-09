#!/usr/bin/env python3
"""One-off backfill for the mission HUD (docs/plans/MISSION-HUD-PLAN.md §2.4).

Fills the mission links the database has never recorded:

  1. harness names  — move rows from a stray harness name to the canonical one
                      (--rename-harness OLD=NEW) and drop the stray row.
  2. orphan docs    — delete documents (and their FTS rows) that lie under no
                      registered harness memory root.
  3. step timing    — mission_steps.started_at / ended_at / summary, from the
                      flight_recorder's mission.step and step-sync events.
  4. finding links  — findings.mission_id, from debrief "[date] — target"
                      references, then from when the finding was recorded.

Dry run by default: every part reports what it would change. --apply writes,
after copying the database to <db stem>.bak-<timestamp>.db with SQLite's backup API
(safe while memoryd is running). Only NULL columns are ever filled.

Run after deploying the matching memoryd and POST /v1/documents/sync, so the
debriefs are in the documents table with their mission_id.

Usage:
  scripts/backfill-mission-links.py [--db data/memory.db]
      [--rename-harness kirsch-opencode=kirschopencode] [--apply]
"""

import argparse
import datetime as dt
import re
import sqlite3
import sys

# How long after a mission closes a finding still counts as its own: the
# learning pass and the reconciler run at close, just after the status change.
CLOSE_GRACE = "+2 hours"

STEP_STATUS = re.compile(r"^Mission step \S+ (?:created|updated) \(status: ([a-z-]+)\)$")
FINDING_REF = re.compile(r"\[(\d{4}-\d{2}-\d{2})\]\s*[—–-]+\s*([^`;\n]+)")


def rename_harnesses(db, renames):
    """Part 1: move every row from OLD to NEW and drop OLD's harnesses row."""
    changes = []
    for old, new in renames:
        for table in ("missions", "findings", "directive_proposals", "documents"):
            if not any(col[1] == "harness" for col in db.execute(f"PRAGMA table_info({table})")):
                continue
            n = db.execute(f"SELECT COUNT(*) FROM {table} WHERE harness = ?", (old,)).fetchone()[0]
            if n:
                changes.append(f"{table}: {n} rows {old} -> {new}")
                db.execute(f"UPDATE {table} SET harness = ? WHERE harness = ?", (new, old))
        if db.execute("SELECT 1 FROM harnesses WHERE name = ?", (old,)).fetchone():
            changes.append(f"harnesses: delete {old}")
            db.execute("DELETE FROM harnesses WHERE name = ?", (old,))
    return changes


def delete_orphan_documents(db):
    """Part 2: documents under no registered harness memory root."""
    rows = db.execute(
        """SELECT id, kind FROM documents d
            WHERE NOT EXISTS (SELECT 1 FROM harnesses h
                               WHERE d.source_path LIKE rtrim(h.memory_root, '/') || '/%')"""
    ).fetchall()
    kinds = {}
    for doc_id, kind in rows:
        kinds[kind] = kinds.get(kind, 0) + 1
        db.execute("DELETE FROM documents_fts WHERE rowid = ?", (doc_id,))
        db.execute("DELETE FROM documents WHERE id = ?", (doc_id,))
    return [f"delete {len(rows)} documents: " + ", ".join(f"{k}={v}" for k, v in sorted(kinds.items()))] if rows else []


def backfill_steps(db):
    """Part 3: step timing from mission.step events, summary from step-sync.

    Only events recorded before the mission closed count, so a PROGRESS.md
    replayed after the fact does not lend its replay time to the steps.
    started_at is the first in-progress event; ended_at the last done or
    failed event, matching memoryd's live stamping.
    """
    events = db.execute(
        """SELECT f.mission_id, f.step, f.note, f.created_at
             FROM flight_recorder f JOIN missions m ON m.id = f.mission_id
            WHERE f.event = 'mission.step' AND f.step IS NOT NULL
              AND (m.closed_at IS NULL OR f.created_at <= m.closed_at)
            ORDER BY f.id"""
    ).fetchall()
    started, ended = {}, {}
    for mission_id, step, note, created_at in events:
        match = STEP_STATUS.match(note or "")
        if not match:
            continue
        key = (mission_id, step)
        status = match.group(1)
        if status == "in-progress":
            started.setdefault(key, created_at)
        elif status in ("done", "failed"):
            ended[key] = created_at

    summaries = {}
    for mission_id, step, note in db.execute(
        """SELECT mission_id, step, note FROM flight_recorder
            WHERE event = 'step-sync' AND mission_id IS NOT NULL AND step IS NOT NULL
            ORDER BY id"""
    ):
        summaries[(mission_id, step)] = note

    counts = {"started_at": 0, "ended_at": 0, "summary": 0}
    for column, values in (("started_at", started), ("ended_at", ended), ("summary", summaries)):
        for (mission_id, step), value in values.items():
            cur = db.execute(
                f"UPDATE mission_steps SET {column} = ? WHERE mission_id = ? AND step = ? AND {column} IS NULL",
                (value, mission_id, step),
            )
            counts[column] += cur.rowcount
    return [f"mission_steps.{c}: fill {n}" for c, n in counts.items()]


def link_findings(db, verbose):
    """Part 4: findings.mission_id, from debrief references, then timing."""
    unlinked = {
        row[0]: row
        for row in db.execute(
            """SELECT id, harness, finding_date, target, created_at FROM findings
                WHERE mission_id IS NULL"""
        )
    }
    links = {}

    # 4a. "[YYYY-MM-DD] — target" references inside each debrief's
    # "Findings and Patterns Linked" section. A reference names a finding when
    # exactly one unlinked finding has that date and a target that starts with
    # the reference text (or the reverse — debriefs abbreviate both ways).
    by_ref = 0
    for mission_id, body in db.execute(
        "SELECT mission_id, body FROM documents WHERE kind = 'debrief' AND mission_id IS NOT NULL"
    ):
        section = re.search(r"^## Findings and Patterns Linked\s*$(.*?)(?=^## |\Z)", body, re.M | re.S)
        if not section:
            continue
        for date, ref in FINDING_REF.findall(section.group(1)):
            ref = ref.strip().strip("`").strip()
            if not ref:
                continue
            matches = [
                fid for fid, (_, _, fdate, target, _) in unlinked.items()
                if fid not in links and fdate == date and target
                and (target.startswith(ref) or ref.startswith(target))
            ]
            if len(matches) == 1:
                links[matches[0]] = mission_id
                by_ref += 1

    # 4b. The one mission of the finding's harness that was running when the
    # finding was recorded (opened_at .. closed_at + CLOSE_GRACE).
    by_time, ambiguous, none = 0, [], 0
    for fid, (_, harness, _, _, created_at) in unlinked.items():
        if fid in links or not harness:
            continue
        candidates = [
            r[0] for r in db.execute(
                f"""SELECT id FROM missions
                     WHERE harness = ? AND opened_at <= ?
                       AND (closed_at IS NULL OR ? <= datetime(closed_at, '{CLOSE_GRACE}'))""",
                (harness, created_at, created_at),
            )
        ]
        if len(candidates) == 1:
            links[fid] = candidates[0]
            by_time += 1
        elif candidates:
            ambiguous.append((fid, candidates))
        else:
            none += 1

    for fid, mission_id in links.items():
        db.execute("UPDATE findings SET mission_id = ? WHERE id = ? AND mission_id IS NULL", (mission_id, fid))

    out = [
        f"findings.mission_id: {len(unlinked)} unlinked; link {by_ref} by debrief reference, "
        f"{by_time} by timing; {len(ambiguous)} ambiguous, {none} with no candidate mission"
    ]
    if verbose:
        out += [f"  ambiguous finding {fid}: {', '.join(c)}" for fid, c in ambiguous]
    return out


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--db", default="data/memory.db")
    parser.add_argument("--rename-harness", action="append", default=[], metavar="OLD=NEW")
    parser.add_argument("--apply", action="store_true", help="write the changes (default: dry run)")
    parser.add_argument("--verbose", action="store_true", help="list ambiguous findings")
    args = parser.parse_args()

    renames = []
    for pair in args.rename_harness:
        old, sep, new = pair.partition("=")
        if not sep or not old or not new:
            parser.error(f"--rename-harness wants OLD=NEW, got {pair!r}")
        renames.append((old, new))

    db = sqlite3.connect(args.db, timeout=30)
    db.execute("PRAGMA busy_timeout = 30000")

    running = db.execute("SELECT COUNT(*) FROM triage_runs WHERE status = 'running'").fetchone()[0]
    if running and args.apply:
        sys.exit(f"{running} triage run(s) in progress; re-run when they finish")

    if args.apply:
        # Keep the .db suffix so .gitignore's *.db rule covers the backup.
        stem = args.db[:-3] if args.db.endswith(".db") else args.db
        backup = f"{stem}.bak-{dt.datetime.now():%Y%m%d-%H%M%S}.db"
        with sqlite3.connect(backup) as dst:
            db.backup(dst)
        print(f"backup: {backup}")

    try:
        db.execute("BEGIN IMMEDIATE")
        report = []
        report += rename_harnesses(db, renames)
        report += delete_orphan_documents(db)
        report += backfill_steps(db)
        report += link_findings(db, args.verbose)
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

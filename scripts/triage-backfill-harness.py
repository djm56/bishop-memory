#!/usr/bin/env python3
"""
scripts/triage-backfill-harness.py — set findings.harness on rows mirrored
before the column existed.

For every harness registered in the service (or supplied with --register),
parse that harness's FINDINGS.md into natural keys. Every finding in the
service whose harness is NULL and whose natural key matches exactly one
harness gets that harness through PUT /v1/findings/:id/harness. Keys that
match no harness, or more than one, are reported and left alone.

Usage:
  scripts/triage-backfill-harness.py [--url URL] [--register NAME=/abs/path/to/.claude/memory ...] [--dry-run]

--register also upserts the harness through PUT /v1/harnesses/:name, which is
what the reconciler does on every run; use it the first time, before any
harness has reconciled against the new service build.

Exit codes: 0 success, 1 a write failed, 2 a registered path is not a memory tree.
"""
import argparse
import os
import sys
import urllib.error

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import triage_common as tc  # noqa: E402


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--url", default=os.environ.get("BISHOP_MEMORY_URL", "http://127.0.0.1:8787"))
    parser.add_argument("--register", action="append", default=[], metavar="NAME=/abs/path")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    for spec in args.register:
        if "=" not in spec:
            tc.die(f"--register expects NAME=/abs/path, got {spec!r}", 2)
        name, root = spec.split("=", 1)
        root = os.path.abspath(root)
        if not os.path.isfile(os.path.join(root, "findings", "FINDINGS.md")):
            tc.die(f"{root} has no findings/FINDINGS.md — is it a .claude/memory tree?", 2)
        if args.dry_run:
            print(f"[backfill] would register {name} -> {root}")
        else:
            tc.api("PUT", f"{args.url}/v1/harnesses/{name}", {"memory_root": root})
            print(f"[backfill] registered {name} -> {root}")

    harnesses = tc.load_harnesses(args.url)
    if args.dry_run:
        for spec in args.register:
            name, root = spec.split("=", 1)
            harnesses[name] = os.path.abspath(root)
    if not harnesses:
        tc.die("[backfill] no harnesses registered; pass --register NAME=/abs/path or run the reconciler first", 2)

    key_to_harness = {}
    for name, root in harnesses.items():
        path = os.path.join(root, "findings", "FINDINGS.md")
        if not os.path.isfile(path):
            print(f"[backfill] {name}: {path} missing, skipped", file=sys.stderr)
            continue
        count = 0
        for entry in tc.entries(path):
            date, target = tc.split_heading_date(tc.entry_heading(entry))
            suggestion = tc.entry_field(entry, "Suggestion")
            if not suggestion:
                continue
            key_to_harness.setdefault(tc.natural_key(date, target, suggestion), set()).add(name)
            count += 1
        print(f"[backfill] {name}: {count} entries in FINDINGS.md")

    _, body = tc.api("GET", f"{args.url}/v1/findings")
    updated = unmatched = ambiguous = already = failed = 0
    for row in body.get("findings", []):
        if row.get("harness"):
            already += 1
            continue
        owners = key_to_harness.get(tc.key_of_api_row(row), set())
        if len(owners) == 0:
            unmatched += 1
            print(f"[backfill] #{row['id']} matches no harness: [{row.get('finding_date')}] {row.get('target')}")
            continue
        if len(owners) > 1:
            ambiguous += 1
            print(f"[backfill] #{row['id']} matches several harnesses {sorted(owners)}; left NULL")
            continue
        owner = next(iter(owners))
        if args.dry_run:
            print(f"[backfill] would set #{row['id']} -> {owner}")
            updated += 1
            continue
        try:
            tc.api("PUT", f"{args.url}/v1/findings/{row['id']}/harness", {"harness": owner})
            updated += 1
        except urllib.error.HTTPError as exc:
            failed += 1
            print(f"[backfill] #{row['id']}: HTTP {exc.code}", file=sys.stderr)

    print(f"[backfill] updated={updated} already_set={already} unmatched={unmatched} ambiguous={ambiguous} failed={failed}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""
scripts/triage-seed-categories.py — load db/finding-categories.json into the
finding_categories table through PUT /v1/finding-categories/:slug.

Idempotent: every run upserts every category in the file. With
--deactivate-missing, categories present in the service but absent from the
file are marked inactive (never deleted — classifications reference them).

Usage:
  scripts/triage-seed-categories.py [--url http://127.0.0.1:8787] [--file db/finding-categories.json] [--deactivate-missing] [--dry-run]

Exit codes: 0 success, 1 one or more upserts failed, 2 the file could not be read.
"""
import argparse
import json
import os
import sys
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT_FILE = os.path.join(os.path.dirname(HERE), "db", "finding-categories.json")


def request(method, url, payload=None):
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(url, data=data, method=method,
                                 headers={"Content-Type": "application/json", "Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as resp:
        body = resp.read()
        return resp.status, (json.loads(body) if body else {})


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--url", default=os.environ.get("BISHOP_MEMORY_URL", "http://127.0.0.1:8787"))
    parser.add_argument("--file", default=DEFAULT_FILE)
    parser.add_argument("--deactivate-missing", action="store_true")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    try:
        with open(args.file, encoding="utf-8") as fh:
            doc = json.load(fh)
    except (OSError, ValueError) as exc:
        print(f"[seed] cannot read {args.file}: {exc}", file=sys.stderr)
        return 2

    categories = doc.get("categories", [])
    failed = 0
    seen = set()
    for index, cat in enumerate(categories):
        slug = cat["slug"]
        seen.add(slug)
        payload = {
            "name": cat["name"],
            "description": cat["description"],
            "examples": cat.get("examples", ""),
            "sort_order": cat.get("sort_order", index + 1),
            "active": cat.get("active", True),
        }
        if args.dry_run:
            print(f"[seed] would upsert {slug}")
            continue
        try:
            status, _ = request("PUT", f"{args.url}/v1/finding-categories/{slug}", payload)
            print(f"[seed] {slug}: {status}")
        except urllib.error.HTTPError as exc:
            failed += 1
            print(f"[seed] {slug}: HTTP {exc.code} {exc.read().decode(errors='replace')[:200]}", file=sys.stderr)
        except urllib.error.URLError as exc:
            print(f"[seed] {args.url} unreachable: {exc.reason}", file=sys.stderr)
            return 1

    if args.deactivate_missing and not args.dry_run:
        status, body = request("GET", f"{args.url}/v1/finding-categories?include_inactive=1")
        for cat in body.get("categories", []):
            if cat["slug"] in seen or not cat["active"]:
                continue
            payload = {"name": cat["name"], "description": cat["description"],
                       "examples": cat.get("examples") or "", "sort_order": cat["sort_order"], "active": False}
            try:
                request("PUT", f"{args.url}/v1/finding-categories/{cat['slug']}", payload)
                print(f"[seed] {cat['slug']}: deactivated (absent from file)")
            except urllib.error.HTTPError as exc:
                failed += 1
                print(f"[seed] {cat['slug']}: deactivate failed HTTP {exc.code}", file=sys.stderr)

    print(f"[seed] {len(categories)} categories in file, {failed} failed")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())

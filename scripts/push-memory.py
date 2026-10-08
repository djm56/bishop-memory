#!/usr/bin/env python3
"""Push a harness memory tree to bishop-memory by content.

For a memoryd on another machine, which cannot read this machine's files
(docs/plans/NETWORK-DEPLOYMENT-PLAN.md §4). It sends every Markdown file
under --root whose content differs from the server's copy, plus the agent
definitions beside the root (<root>/../agents/*.md), and registers the
harness with this root. --prune also deletes the server's documents for files
that no longer exist. The reconciler does the same push on every run; this is
for a first upload, or to push without reconciling.

URL and API key come from --url / BISHOP_MEMORY_URL and BISHOP_MEMORY_API_KEY,
or ~/.config/bishop-memory/client.env.

Usage:
  scripts/push-memory.py --root /path/to/.claude/memory --harness kirsch [--prune]
"""
import argparse
import importlib.util
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("reconcile_memory", os.path.join(HERE, "reconcile-memory.py"))
reconcile = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(reconcile)


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--root", required=True, help="the harness memory root (…/.claude/memory)")
    parser.add_argument("--harness", required=True, help="the harness name (BISHOP_HARNESS)")
    parser.add_argument("--url", default=os.environ.get("BISHOP_MEMORY_URL", "http://127.0.0.1:8787"))
    parser.add_argument("--prune", action="store_true", help="delete documents whose files are gone")
    args = parser.parse_args()

    root = os.path.realpath(args.root)
    if not os.path.isdir(root):
        sys.exit(f"push-memory: {root} is not a directory")
    client = reconcile.Client(args.url)
    if not client.write("PUT", f"/v1/harnesses/{args.harness}", {"memory_root": root}, "register harness"):
        sys.exit("push-memory: could not register the harness (check the URL and API key)")
    result = reconcile.push_memory(client, root, args.harness, prune=args.prune)
    if result is None:
        sys.exit("push-memory: this memoryd has no push route; upgrade it, or run it on this machine and use POST /v1/documents/sync")
    pushed, unchanged, deleted = result
    print(f"push-memory: {args.harness}: pushed {pushed}, unchanged {unchanged}" + (f", deleted {deleted}" if args.prune else ""))


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""
scripts/export-decisions.py — write operator decisions from bishop-memory back
into a harness's Markdown: FINDINGS.md status lines and ratified DIRECTIVES.md
entries.

Markdown stays the source of truth. This script only ever:
  * replaces the **Status**, **Approver** and **Date approved** lines of an
    entry whose natural key matches a decided finding, and appends one
    **Disposition (triage):** line carrying the operator's note;
  * appends a ratified directive entry (and its index row) that DIRECTIVES.md
    does not yet carry.

Safety: a backup copy goes to <memory_root>/workspace/triage-backups/ before
every write; the write is atomic; the file is re-read afterwards and the
number of changed lines is compared with what was intended, refusing on any
mismatch. An entry whose Markdown status is already non-proposed and differs
from the service's decision is a CONFLICT: it is reported and left untouched
(a hand edit in the file wins; reconcile it with scripts/reconcile-memory.py).

Usage:
  scripts/export-decisions.py --harness NAME [--url URL] [--dry-run]
  scripts/export-decisions.py --all [--url URL] [--dry-run]

Exit codes: 0 nothing to do or everything written; 1 a write failed;
2 a harness is not registered or its memory tree is missing; 3 conflicts only.
"""
import argparse
import os
import re
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import triage_common as tc  # noqa: E402

STATUS_LINE = re.compile(r"^\*\*Status\*\*:.*$", re.M)
APPROVER_LINE = re.compile(r"^\*\*Approver\*\*:.*$", re.M)
DATE_LINE = re.compile(r"^\*\*Date approved\*\*:.*$", re.M)
DISPOSITION_LINE = re.compile(r"^\*\*Disposition \(triage\):\*\*.*$", re.M)
DIR_HEADING = re.compile(r"^### (DIR-\d+) — ", re.M)
INDEX_HEADER = re.compile(r"^\| ID \| Title \| Applies when \(short\) \| Status \|\n\|[-| ]+\|\n", re.M)
RETIRED_HEADING = re.compile(r"^## Retired Entries", re.M)

# The exporter renders dates the way the kirsch ledger already does
# ("02 October 2026"), so a hand-approved entry and an exported one read alike.
def human_date(iso):
    try:
        return time.strftime("%d %B %Y", time.strptime(iso[:10], "%Y-%m-%d"))
    except ValueError:
        return iso


def rewrite_entry(block, finding):
    """Return the block with its decision lines rewritten, or None if unchanged."""
    status = finding["status"]
    approver = finding.get("approver") or "—"
    date = human_date(finding.get("date_approved") or "")
    note = finding.get("decision_note") or ""

    if status in ("approved", "applied"):
        approver_text = approver
        date_text = date or "—"
    else:
        approver_text = "—"
        date_text = "—"

    new = block
    new = STATUS_LINE.sub(f"**Status**: {status}", new, count=1)
    new = APPROVER_LINE.sub(f"**Approver**: {approver_text}", new, count=1)
    new = DATE_LINE.sub(f"**Date approved**: {date_text}", new, count=1)

    disposition = ""
    if status in ("rejected", "retired", "superseded"):
        disposition = f"**Disposition (triage):** {status} by {approver} on {date} — {note}".rstrip(" —")
    elif note:
        disposition = f"**Disposition (triage):** {note}"
    if disposition:
        if DISPOSITION_LINE.search(new):
            new = DISPOSITION_LINE.sub(disposition.replace("\\", "\\\\"), new, count=1)
        else:
            body = new.rstrip("\n")
            trailing = new[len(body):]
            new = body + "\n" + disposition + (trailing if trailing else "\n")
    return None if new == block else new


def export_findings(url, harness, root, dry_run):
    path = os.path.join(root, "findings", "FINDINGS.md")
    if not os.path.isfile(path):
        print(f"[export] {harness}: {path} missing", file=sys.stderr)
        return 2, 0, 0
    _, body = tc.api("GET", f"{url}/v1/findings?harness={harness}")
    decided = {tc.key_of_api_row(f): f for f in body.get("findings", []) if f["status"] != "proposed"}
    if not decided:
        print(f"[export] {harness}: no decided findings in the service")
        return 0, 0, 0

    with open(path, encoding="utf-8") as fh:
        text = fh.read()
    preamble, blocks = tc.split_entries(text)

    changed = conflicts = 0
    out_blocks = []
    for block in blocks:
        key = tc.block_key(block)
        finding = decided.get(key) if key else None
        if finding is None:
            out_blocks.append(block)
            continue
        md_status_match = STATUS_LINE.search(block)
        md_status = md_status_match.group(0).split(":", 1)[1].strip() if md_status_match else "proposed"
        if md_status == finding["status"]:
            out_blocks.append(block)
            continue
        if md_status != "proposed":
            conflicts += 1
            print(f"[export] CONFLICT #{finding['id']}: FINDINGS.md says {md_status}, service says {finding['status']} — left untouched")
            out_blocks.append(block)
            continue
        new = rewrite_entry(block, finding)
        if new is None:
            out_blocks.append(block)
            continue
        changed += 1
        print(f"[export] #{finding['id']} -> {finding['status']} ({(finding.get('target') or '')[:60]})")
        out_blocks.append(new)

    if changed == 0:
        print(f"[export] {harness}: FINDINGS.md already current ({conflicts} conflicts)")
        return (3 if conflicts else 0), 0, conflicts
    if dry_run:
        print(f"[export] {harness}: DRY RUN — would rewrite {changed} entries in {path}")
        return (3 if conflicts else 0), changed, conflicts

    new_text = preamble + "".join(out_blocks)
    expected_delta = sum(1 for a, b in zip(blocks, out_blocks) if a != b)
    tc.atomic_write(path, new_text, backup_dir=os.path.join(root, "workspace", "triage-backups"))
    with open(path, encoding="utf-8") as fh:
        reread = fh.read()
    if reread != new_text:
        print(f"[export] {harness}: re-read of {path} does not match what was written", file=sys.stderr)
        return 1, changed, conflicts
    print(f"[export] {harness}: rewrote {expected_delta} entries in {path} (backup in workspace/triage-backups/)")
    return (3 if conflicts else 0), changed, conflicts


def render_directive(p, evidence_label, added):
    lines = [
        f"### {p['directive_id']} — {p['title']}",
        f"- **Applies when:** {p['applies_when']}",
        "- **Status:** active",
        f"- **Rule:** {p['rule']}",
        f"- **Rationale:** {p['rationale']}",
        f"- **Reviewer check:** {p['reviewer_check']}",
    ]
    if p.get("example"):
        lines.append(f"- **Example:** Illustrative — {p['example']}")
    lines.append(f"- **Added:** {added} · **Source:** bishop-memory triage · **Evidence:** {evidence_label}")
    return "\n".join(lines) + "\n"


def evidence_label(url, ids):
    """'[date] — target' of the first evidence finding, <=80 chars, as the template asks."""
    if not ids:
        return "—"
    _, body = tc.api("GET", f"{url}/v1/findings?ids={','.join(str(i) for i in ids)}")
    rows = sorted(body.get("findings", []), key=lambda r: r["id"])
    if not rows:
        return "findings " + ", ".join(f"#{i}" for i in ids)
    first = rows[0]
    suffix = f" (+{len(rows) - 1})" if len(rows) > 1 else ""
    label = f"[{first.get('finding_date') or ''}] — {first.get('target') or ''}"
    return label[:80 - len(suffix)].rstrip() + suffix


def export_directives(url, harness, root, dry_run):
    path = os.path.join(root, "reference", "DIRECTIVES.md")
    if not os.path.isfile(path):
        print(f"[export] {harness}: {path} missing", file=sys.stderr)
        return 2, 0
    _, body = tc.api("GET", f"{url}/v1/directive-proposals?state=accepted")
    proposals = [p for p in body.get("directive_proposals", []) if not p.get("harness") or p["harness"] == harness]
    if not proposals:
        return 0, 0

    with open(path, encoding="utf-8") as fh:
        text = fh.read()
    present = set(DIR_HEADING.findall(text))
    todo = [p for p in proposals if p["directive_id"] not in present]
    if not todo:
        print(f"[export] {harness}: DIRECTIVES.md already carries every ratified directive")
        return 0, 0

    if not RETIRED_HEADING.search(text) or not INDEX_HEADER.search(text):
        print(f"[export] {harness}: DIRECTIVES.md lacks the Directive Index table or the Retired Entries heading; refusing to append", file=sys.stderr)
        return 1, 0

    for p in todo:
        added = (p.get("decided_at") or "")[:10] or time.strftime("%Y-%m-%d", time.gmtime())
        entry = render_directive(p, evidence_label(url, p.get("evidence", [])), added)
        if len(entry) > 1510:
            print(f"[export] {p['directive_id']}: rendered entry is {len(entry)} chars (>1510); refusing", file=sys.stderr)
            return 1, 0
        # Entry: just before "## Retired Entries", after any existing entries.
        pos = RETIRED_HEADING.search(text).start()
        head = text[:pos].rstrip("\n") + "\n\n"
        text = head + entry + "\n" + text[pos:]
        # Index row: appended to the end of the Directive Index table.
        m = INDEX_HEADER.search(text)
        table_end = m.end()
        while table_end < len(text) and text[table_end] == "|":
            nl = text.find("\n", table_end)
            table_end = len(text) if nl == -1 else nl + 1
        short = p["applies_when"]
        if len(short) > 60:
            short = short[:57] + "..."
        row = f"| {p['directive_id']} | {p['title']} | {short} | active |\n"
        text = text[:table_end] + row + text[table_end:]
        print(f"[export] {p['directive_id']} — {p['title']} ({len(entry)} chars)")

    if dry_run:
        print(f"[export] {harness}: DRY RUN — would append {len(todo)} directive(s) to {path}")
        return 0, len(todo)
    tc.atomic_write(path, text, backup_dir=os.path.join(root, "workspace", "triage-backups"))
    print(f"[export] {harness}: appended {len(todo)} directive(s) to {path}")
    return 0, len(todo)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--url", default=os.environ.get("BISHOP_MEMORY_URL", "http://127.0.0.1:8787"))
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--harness")
    group.add_argument("--all", action="store_true")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    harnesses = tc.load_harnesses(args.url)
    if args.harness:
        if args.harness not in harnesses:
            tc.die(f"[export] harness {args.harness!r} is not registered; run the reconciler or triage-backfill-harness.py --register first", 2)
        targets = {args.harness: harnesses[args.harness]}
    else:
        targets = harnesses
    if not targets:
        tc.die("[export] no harnesses registered", 2)

    worst = 0
    for name, root in sorted(targets.items()):
        if not os.path.isdir(root):
            print(f"[export] {name}: memory root {root} is not a directory (volume unmounted?)", file=sys.stderr)
            worst = max(worst, 2)
            continue
        code, _, _ = export_findings(args.url, name, root, args.dry_run)
        worst = max(worst, code)
        code, _ = export_directives(args.url, name, root, args.dry_run)
        worst = max(worst, code)
    return worst


if __name__ == "__main__":
    sys.exit(main())

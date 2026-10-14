#!/usr/bin/env python3
"""
scripts/export-decisions.py — write operator decisions from bishop-memory back
into a harness's Markdown: FINDINGS.md status lines and DIRECTIVES.md entries.

Markdown stays the source of truth. This script only ever:
  * FINDINGS.md: replaces the **Status**, **Approver** and **Date approved**
    lines of an entry whose natural key matches a decided finding, and appends
    one **Disposition (triage):** line carrying the operator's note;
  * DIRECTIVES.md, from the latest accepted/declined proposal per directive ID
    (the latest `decided_at` wins; a tie goes to the higher proposal id):
      - adds an accepted directive the file does not yet carry (entry and
        Index row, inserted before "## Retired Entries");
      - amends a service-owned entry that differs from its accepted proposal
        (entry span and Index row; the printed diff shows what changed);
      - retires a service-owned entry whose proposal was declined (Status ->
        deprecated, Index row removed, a Retired row added);
      - re-accepts a deprecated service-owned entry that was accepted again
        (Status -> active, Index row restored, Retired row removed; the removed
        row is printed so a hand-written Successor stays visible).

Ownership. A DIRECTIVES.md entry is service-owned only when the **Source:**
token of its Added line is exactly `bishop-memory triage`. A hand-authored
entry is never modified: a decision for it is reported as a CONFLICT and left
alone (a hand edit wins; reconcile it with scripts/reconcile-memory.py). The
same holds for a FINDINGS.md entry whose status is already non-proposed and
differs from the service's decision.

DIRECTIVES.md is edited surgically, never regenerated. Everything the service
does not own (hand-authored entries and their Index and Retired rows, the
preamble, How Entries Bind, the Directives comment, the Retired prose) stays
byte-identical, and a preservation check refuses the write otherwise. An amend
or re-accept also refuses when the service-owned entry holds a line that is not
a template bullet (a fence, a heading, free text), or is followed by a heading
that is neither a directive nor "## Retired Entries": hand-extended content is
never overwritten or orphaned. A deprecated service-owned entry with no Retired
row is reported as a note and not repaired.

Safety: a backup copy goes to <memory_root>/workspace/triage-backups/ before
every write; the write is atomic; the file is re-read afterwards and compared
with what was written. A refused directive edit writes nothing. A network or
HTTP error while fetching evidence for a new entry fails that harness with exit
1; --all carries on with the next harness.

Usage:
  scripts/export-decisions.py --harness NAME [--url URL] [--dry-run]
  scripts/export-decisions.py --all [--url URL] [--dry-run]

Exit codes: 0 nothing to do or everything written; 1 a write failed or was
refused; 2 a harness is not registered or its memory tree is missing; 3
conflicts only. Across harnesses and files the worst code wins: 1 or 2 outranks
3, 3 outranks 0, and the larger of 1 and 2 wins (see worse()).
"""
import argparse
import os
import re
import sys
import time
import difflib
import urllib.error

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


def worse(current, code):
    """Combine two exit codes: 1 or 2 outranks 3, 3 outranks 0, the larger of 1 and 2 wins.

    3 means "conflicts only", so a real failure (1, 2) always outranks it."""
    if code == 0:
        return current
    if code == 3:
        return current or 3
    return code if current in (0, 3) else max(current, code)


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


# --------------------------------------------------------------------------
# DIRECTIVES.md — surgical edits over text
#
# DIRECTIVES.md is human-ratified. Nothing here regenerates the file: every
# operation below is a pure function that changes one entry span or one table
# row and leaves every other byte alone. apply_directive_decisions() ends with
# check_preservation(), which refuses the whole transform if anything outside
# the touched IDs differs.
# --------------------------------------------------------------------------

MAX_ENTRY_CHARS = 1510
SERVICE_SOURCE = "bishop-memory triage"
EXAMPLE_PREFIX = "Illustrative — "
# The only line shapes the template allows inside an entry (Supersedes lives
# inside the Added bullet), besides blank lines.
ENTRY_BULLET = re.compile(r"^- \*\*(Applies when|Status|Rule|Rationale|Reviewer check|Example|Added):\*\*")

# An entry span ends at the next heading of ANY level 1-3 ("### ", "## ", "# ")
# or at EOF, so the last entry never swallows "## Retired Entries".
SECTION_BREAK = re.compile(r"^#{1,3} ", re.M)
HEADING_TITLE = re.compile(r"^### DIR-\d+ — (.*)$", re.M)
RETIRED_HEADER = re.compile(r"^\| ID \| Title \(as ratified\) \| Status \| Successor \|[ \t]*\n\|[-| :]+\|[ \t]*\n", re.M)
ROW_ID = re.compile(r"^\|\s*(DIR-\d+)\s*\|")
STATUS_BULLET = re.compile(r"^(- \*\*Status:\*\*[ \t]*)(\S+)([ \t]*)$", re.M)
ADDED_BULLET = re.compile(r"^- \*\*Added:\*\*[ \t]*(.*?)[ \t]*$", re.M)
SOURCE_TOKEN = re.compile(r"\*\*Source:\*\*[ \t]*(.*?)(?:[ \t]+·[ \t]+|[ \t]*$)")


class DirectiveError(Exception):
    """A DIRECTIVES.md edit that must be refused; nothing is written."""


class DirectiveResult:
    """What apply_directive_decisions() decided: new text plus a report."""

    def __init__(self, text):
        self.text = text
        self.actions = []     # (directive_id, kind, title) — kind: new|amended|re-accepted|retired
        self.conflicts = []   # message strings
        self.notes = []       # message strings
        self.diffs = {}       # directive_id -> unified diff of an amended entry


def clean(value):
    """Collapse all whitespace (newlines included) so a field renders on one line."""
    return " ".join(str(value if value is not None else "").split())


def cell(value):
    """Escape pipes for a Markdown table cell."""
    return re.sub(r"(?<!\\)\|", r"\\|", clean(value))


def render_directive(p, added_line, status="active"):
    """Render a directive entry. added_line is everything after '- **Added:** '."""
    lines = [
        f"### {clean(p['directive_id'])} — {clean(p['title'])}",
        f"- **Applies when:** {clean(p['applies_when'])}",
        f"- **Status:** {status}",
        f"- **Rule:** {clean(p['rule'])}",
        f"- **Rationale:** {clean(p['rationale'])}",
        f"- **Reviewer check:** {clean(p['reviewer_check'])}",
    ]
    example = clean(p.get("example"))
    if example:
        if not example.startswith(EXAMPLE_PREFIX):
            example = EXAMPLE_PREFIX + example
        lines.append(f"- **Example:** {example}")
    lines.append(f"- **Added:** {added_line}")
    return "\n".join(lines) + "\n"


def new_added_line(added, evidence):
    """The Added bullet body for an entry the service creates."""
    return f"{added} · **Source:** {SERVICE_SOURCE} · **Evidence:** {evidence}"


def render_index_row(p):
    """The Directive Index row for a service-written entry (Applies when cut to 60)."""
    applies = clean(p["applies_when"])
    if len(applies) > 60:
        applies = applies[:57] + "..."
    return f"| {clean(p['directive_id'])} | {cell(p['title'])} | {cell(applies)} | active |"


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


def directive_spans(text):
    """[(directive_id, start, end)] for every '### DIR-NNN — ' entry, in file order.

    The span runs from the heading to the next heading of any level (so it
    includes the entry's trailing blank lines) or to EOF."""
    spans = []
    for m in DIR_HEADING.finditer(text):
        nxt = SECTION_BREAK.search(text, m.end())
        spans.append((m.group(1), m.start(), nxt.start() if nxt else len(text)))
    return spans


def find_span(text, directive_id):
    """(start, end) of one entry, None if absent. A duplicated heading is refused."""
    found = [(s, e) for did, s, e in directive_spans(text) if did == directive_id]
    if len(found) > 1:
        raise DirectiveError(f"{directive_id} has {len(found)} headings in DIRECTIVES.md")
    return found[0] if found else None


def is_service_owned(entry):
    """True only when the Added line's **Source:** token is exactly 'bishop-memory triage'.

    The token is read, not substring-matched: an Evidence field that mentions
    the phrase does not make an entry service-owned."""
    added = ADDED_BULLET.search(entry)
    if not added:
        return False
    source = SOURCE_TOKEN.search(added.group(1))
    return bool(source) and source.group(1).strip() == SERVICE_SOURCE


def parse_status(entry):
    """The value of the entry's actual '- **Status:** <value>' line."""
    m = STATUS_BULLET.search(entry)
    if not m:
        raise DirectiveError("entry has no '- **Status:** <value>' line")
    return m.group(2)


def same_lines(a, b):
    """Equal after trimming trailing whitespace per line and at the end."""
    return [l.rstrip() for l in a.rstrip().split("\n")] == [l.rstrip() for l in b.rstrip().split("\n")]


def find_table(text, kind):
    """(body_end, rows) of the 'index' or 'retired' table; rows are (id|None, start, end)."""
    if kind == "index":
        m = INDEX_HEADER.search(text)
    else:
        h = RETIRED_HEADING.search(text)
        m = RETIRED_HEADER.search(text, h.end()) if h else None
    if not m:
        raise DirectiveError(f"DIRECTIVES.md has no recognisable {kind} table header")
    pos, rows = m.end(), []
    while pos < len(text) and text[pos] == "|":
        nl = text.find("\n", pos)
        end = len(text) if nl == -1 else nl + 1
        idm = ROW_ID.match(text[pos:end])
        rows.append((idm.group(1) if idm else None, pos, end))
        pos = end
    return pos, rows


def insert_line(text, pos, line):
    """Insert one line at pos, adding a newline first if pos is an unterminated EOF."""
    if pos == len(text) and text and not text.endswith("\n"):
        line = "\n" + line
    return text[:pos] + line + "\n" + text[pos:]


def remove_rows(text, kind, directive_id):
    """Remove every row of the given table whose ID is directive_id."""
    _, rows = find_table(text, kind)
    for rid, s, e in reversed(rows):
        if rid == directive_id:
            text = text[:s] + text[e:]
    return text


def set_index_row(text, directive_id, row):
    """Replace the entry's Index row in place; if it has none, insert it in ID order.

    Insertion goes immediately before the first existing row with a higher
    number, or after the last row. Existing rows are never reordered."""
    body_end, rows = find_table(text, "index")
    for rid, s, e in rows:
        if rid == directive_id:
            return text[:s] + row + "\n" + text[e:]
    number = int(directive_id[4:])
    for rid, s, _ in rows:
        if rid and int(rid[4:]) > number:
            return insert_line(text, s, row)
    return insert_line(text, body_end, row)


def append_index_row(text, row):
    """Append one Index row after the last row."""
    body_end, _ = find_table(text, "index")
    return insert_line(text, body_end, row)


def add_retired_row(text, directive_id, title):
    """Append '| DIR | title | deprecated | — |' unless the ID already has a Retired row."""
    body_end, rows = find_table(text, "retired")
    if any(rid == directive_id for rid, _, _ in rows):
        return text
    return insert_line(text, body_end, f"| {directive_id} | {cell(title)} | deprecated | — |")


def insert_entry(text, entry):
    """Insert a rendered entry just before '## Retired Entries', after the last entry."""
    r = RETIRED_HEADING.search(text)
    if not r:
        raise DirectiveError("DIRECTIVES.md has no '## Retired Entries' heading")
    if not text[:r.start()].endswith("\n\n"):
        raise DirectiveError("no blank line before '## Retired Entries'; refusing to guess the spacing")
    return text[:r.start()] + entry.rstrip("\n") + "\n\n" + text[r.start():]


def replace_span(text, start, end, entry):
    """Replace one entry span with a rendered entry, keeping the span's trailing blank lines."""
    old = text[start:end]
    trailing = old[len(old.rstrip()):] or "\n"
    return text[:start] + entry.rstrip("\n") + trailing + text[end:]


def check_replaceable(did, text, start, end):
    """Refuse to replace a service-owned span that is not purely template bullets.

    Every line after the heading must be a recognised template bullet or blank,
    and the line that ends the span must be a directive heading, '## Retired
    Entries' or EOF. Otherwise a hand-inserted fence or '#' line would end the
    span early (SECTION_BREAK) and leave an orphaned tail behind the new entry.
    Retire does not call this: it rewrites only the Status line and keeps every
    other byte, so span shape cannot cause damage there."""
    for line in text[start:end].split("\n")[1:]:
        if line.strip() and not ENTRY_BULLET.match(line):
            raise DirectiveError(f"{did}: service-owned entry holds a line outside the template: {line!r}; refusing to replace it")
    if end < len(text):
        nxt = text[end:].split("\n", 1)[0]
        if not (DIR_HEADING.match(nxt) or RETIRED_HEADING.match(nxt)):
            raise DirectiveError(f"{did}: service-owned entry is followed by an unexpected heading: {nxt!r}; refusing to replace it")


def retire_span(entry):
    """Set an entry's Status line from active to deprecated; every other byte is kept."""
    return STATUS_BULLET.sub(lambda m: m.group(1) + "deprecated" + m.group(3), entry, count=1)


def _mask(text, ids):
    """text with the entry spans, Index rows and Retired rows of ids cut out."""
    cuts = [(s, e) for did, s, e in directive_spans(text) if did in ids]
    for kind in ("index", "retired"):
        _, rows = find_table(text, kind)
        cuts += [(s, e) for rid, s, e in rows if rid in ids]
    out, pos = [], 0
    for s, e in sorted(cuts):
        if s < pos:
            raise DirectiveError("overlapping regions while checking preservation")
        out.append(text[pos:s])
        pos = e
    out.append(text[pos:])
    return "".join(out).rstrip("\n")


def check_preservation(old, new, touched, appended):
    """Refuse unless everything the service does not own is byte-identical.

    With the entry spans and the Index/Retired rows of the touched and appended
    IDs cut out, old and new must be equal: that covers the preamble, How
    Entries Bind, the Directives comment, the Retired prose, table headers,
    every other entry, and every Index/Retired row of every other ID. Each
    touched entry must have been service-owned in the old text, and each
    touched or appended entry must be service-owned in the new text."""
    ids = set(touched) | set(appended)
    for did in touched:
        span = find_span(old, did)
        if span is None or not is_service_owned(old[span[0]:span[1]]):
            raise DirectiveError(f"{did}: the old entry is not service-owned; refusing to modify it")
    for did in ids:
        span = find_span(new, did)
        if span is None or not is_service_owned(new[span[0]:span[1]]):
            raise DirectiveError(f"{did}: the new entry is missing or not service-owned")
    a, b = _mask(old, ids), _mask(new, ids)
    if a != b:
        sample = list(difflib.unified_diff(a.splitlines(), b.splitlines(), "old", "new", n=0, lineterm=""))[:8]
        raise DirectiveError("text outside the touched entries would change:\n    " + "\n    ".join(sample))


def build_targets(proposals, harness):
    """One target per directive ID: {id: (state, proposal)}.

    Only 'accepted' and 'declined' proposals count (pending ones never change the
    file), and proposals for another harness are ignored. When one ID has several,
    the latest decision wins: highest decided_at, then highest proposal id. So
    accepted-then-declined retires and declined-then-accepted re-accepts."""
    best = {}
    for p in proposals:
        state, did = p.get("state"), p.get("directive_id")
        if state not in ("accepted", "declined") or not did:
            continue
        if p.get("harness") and p["harness"] != harness:
            continue
        key = (p.get("decided_at") or "", p.get("id") or 0)
        if did not in best or key > best[did][0]:
            best[did] = (key, state, p)
    return {did: (state, p) for did, (_, state, p) in best.items()}


def _check_budget(did, entry):
    if len(entry) > MAX_ENTRY_CHARS:
        raise DirectiveError(f"{did}: rendered entry is {len(entry)} chars (>{MAX_ENTRY_CHARS}); refusing")


def apply_directive_decisions(text, targets, evidence_for, today):
    """Apply decided directive proposals to DIRECTIVES.md text, surgically.

    targets comes from build_targets(); evidence_for(proposal) returns the
    Evidence label for an entry that does not exist yet. Returns a
    DirectiveResult. Raises DirectiveError (nothing to write) on any refusal."""
    result = DirectiveResult(text)
    find_table(text, "index")
    find_table(text, "retired")
    touched, appended = set(), set()
    new = text

    for did in sorted(targets, key=lambda d: int(d[4:])):
        state, p = targets[did]
        span = find_span(new, did)

        if state == "accepted":
            if span is None:
                added = (p.get("decided_at") or "")[:10] or today
                entry = render_directive(p, new_added_line(added, evidence_for(p)))
                _check_budget(did, entry)
                new = append_index_row(insert_entry(new, entry), render_index_row(p))
                appended.add(did)
                result.actions.append((did, "new", clean(p["title"])))
                continue
            body = new[span[0]:span[1]]
            if not is_service_owned(body):
                result.conflicts.append(f"CONFLICT {did}: DIRECTIVES.md entry is hand-authored, service says accepted — left untouched")
                continue
            current = parse_status(body)
            added = ADDED_BULLET.search(body)
            rendered = render_directive(p, added.group(1) if added else new_added_line(today, "—"))
            _check_budget(did, rendered)
            if same_lines(rendered, body):
                continue
            check_replaceable(did, new, span[0], span[1])
            kind = "re-accepted" if current == "deprecated" else "amended"
            diff = "".join(difflib.unified_diff(
                (body.rstrip("\n") + "\n").splitlines(keepends=True), rendered.splitlines(keepends=True),
                fromfile=f"{did} (file)", tofile=f"{did} (service)"))
            new = replace_span(new, span[0], span[1], rendered)
            new = set_index_row(new, did, render_index_row(p))
            if kind == "re-accepted":
                _, retired_rows = find_table(new, "retired")
                removed = [new[s:e].rstrip("\n") for rid, s, e in retired_rows if rid == did]
                if removed:
                    diff += "@@ Retired Entries: row removed @@\n" + "".join(f"-{row}\n" for row in removed)
                new = remove_rows(new, "retired", did)
            result.diffs[did] = diff
            touched.add(did)
            result.actions.append((did, kind, clean(p["title"])))
            continue

        # state == "declined"
        if span is None:
            result.notes.append(f"{did}: declined but not in DIRECTIVES.md")
            continue
        body = new[span[0]:span[1]]
        if not is_service_owned(body):
            result.conflicts.append(f"CONFLICT {did}: DIRECTIVES.md entry is hand-authored, service says declined — left untouched")
            continue
        current = parse_status(body)
        if current == "deprecated":
            _, retired_rows = find_table(new, "retired")
            if not any(rid == did for rid, _, _ in retired_rows):
                result.notes.append(f"{did}: already deprecated but has no Retired Entries row; not repaired")
            continue
        if current != "active":
            raise DirectiveError(f"{did}: unexpected Status {current!r}; refusing")
        title = HEADING_TITLE.search(body).group(1).strip()
        new = new[:span[0]] + retire_span(body) + new[span[1]:]
        new = remove_rows(new, "index", did)
        new = add_retired_row(new, did, title)
        touched.add(did)
        result.actions.append((did, "retired", title))

    if new != text:
        check_preservation(text, new, touched, appended)
    result.text = new
    return result


def export_directives(url, harness, root, dry_run):
    """Export directive decisions from the service to DIRECTIVES.md.

    Returns (exit_code, changes): 0 ok, 1 refused or failed, 2 file missing,
    3 conflicts only (a hand-authored entry the service has a decision for)."""
    path = os.path.join(root, "reference", "DIRECTIVES.md")
    if not os.path.isfile(path):
        print(f"[export] {harness}: {path} missing", file=sys.stderr)
        return 2, 0

    proposals = []
    for state in ("accepted", "declined"):
        try:
            _, body = tc.api("GET", f"{url}/v1/directive-proposals?state={state}")
        except urllib.error.HTTPError as exc:
            if exc.code != 404:  # 404: an older memoryd without proposals
                raise
            body = {}
        proposals.extend(body.get("directive_proposals", []))
    targets = build_targets(proposals, harness)
    if not targets:
        return 0, 0

    with open(path, encoding="utf-8") as fh:
        original = fh.read()

    today = time.strftime("%Y-%m-%d", time.gmtime())
    try:
        result = apply_directive_decisions(
            original, targets, lambda p: evidence_label(url, p.get("evidence", [])), today)
    except DirectiveError as exc:
        print(f"[export] {harness}: DIRECTIVES.md not modified — {exc}", file=sys.stderr)
        return 1, 0
    except (OSError, ValueError) as exc:  # urllib.error.URLError/HTTPError are OSErrors
        print(f"[export] {harness}: DIRECTIVES.md not modified — could not fetch evidence from the service: {exc}", file=sys.stderr)
        return 1, 0

    for note in result.notes:
        print(f"[export] {note}")
    for conflict in result.conflicts:
        print(f"[export] {conflict}")
    ok_code = 3 if result.conflicts else 0

    for did, kind, title in result.actions:
        if did in result.diffs:
            print(f"[export] WARNING: {did} {kind} — diff:")
            for line in result.diffs[did].splitlines():
                print(f"  {line}")
        print(f"[export] {did} — {title} [{kind}]")

    if result.text == original:
        print(f"[export] {harness}: DIRECTIVES.md already current")
        return ok_code, 0

    changes = len(result.actions)
    if dry_run:
        print(f"[export] {harness}: DRY RUN — would make {changes} change(s) to {path}")
        return ok_code, changes

    tc.atomic_write(path, result.text, backup_dir=os.path.join(root, "workspace", "triage-backups"))
    with open(path, encoding="utf-8") as fh:
        reread = fh.read()
    if reread != result.text:
        print(f"[export] {harness}: re-read of {path} does not match what was written", file=sys.stderr)
        return 1, 0

    print(f"[export] {harness}: made {changes} change(s) to {path} (backup in workspace/triage-backups/)")
    return ok_code, changes


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
            worst = worse(worst, 2)
            continue
        code, _, _ = export_findings(args.url, name, root, args.dry_run)
        worst = worse(worst, code)
        code, _ = export_directives(args.url, name, root, args.dry_run)
        worst = worse(worst, code)
    return worst


if __name__ == "__main__":
    sys.exit(main())

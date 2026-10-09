"""
scripts/triage_common.py — helpers shared by the triage scripts.

Loads scripts/reconcile-memory.py as a module (its file name has a hyphen, so
it cannot be imported by name) and re-exports the Markdown parsing functions
the exporter and the harness backfill need, so every script computes a
finding's natural key — (finding_date, target, suggestion) — with exactly the
code the reconciler uses to create the row in the first place.
"""
import importlib.util
import json
import os
import re
import sys
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))

_spec = importlib.util.spec_from_file_location("reconcile_memory", os.path.join(HERE, "reconcile-memory.py"))
reconcile = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(reconcile)

entries = reconcile.entries
entry_heading = reconcile.entry_heading
entry_field = reconcile.entry_field
split_heading_date = reconcile.split_heading_date
truncate = reconcile.truncate

ENTRY_HEADING = re.compile(r"^### ", re.M)


def api(method, url, payload=None, timeout=30):
    """One JSON request. Returns (status, body-dict). Raises urllib errors."""
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(url, data=data, method=method,
                                 headers={"Content-Type": "application/json", "Accept": "application/json",
                                          **reconcile.api_headers()})
    with urllib.request.urlopen(req, timeout=timeout, context=reconcile.ssl_context()) as resp:
        body = resp.read()
        return resp.status, (json.loads(body) if body else {})


def natural_key(finding_date, target, suggestion):
    """The reconciler's dedupe key, with the same truncation it applies."""
    return (truncate(finding_date or "", 64), truncate(target or "", 256), truncate(suggestion or "", 2000))


def key_of_api_row(row):
    return (row.get("finding_date") or "", row.get("target") or "", row.get("suggestion") or "")


def split_entries(text):
    """
    Split a findings-style Markdown file into (preamble, [block, ...]) where
    each block starts at a '### ' heading and runs to the next one. Joining
    preamble + blocks reproduces the file byte for byte.
    """
    positions = [m.start() for m in ENTRY_HEADING.finditer(text)]
    if not positions:
        return text, []
    preamble = text[:positions[0]]
    blocks = []
    for i, start in enumerate(positions):
        end = positions[i + 1] if i + 1 < len(positions) else len(text)
        blocks.append(text[start:end])
    return preamble, blocks


def block_key(block):
    """Natural key of one Markdown entry block, via the reconciler's parsers."""
    entry = block.rstrip("\n")
    if entry.startswith("### "):
        entry = entry[4:]  # reconcile's entries() strips the heading marker too
    date, target = split_heading_date(entry_heading(entry))
    suggestion = entry_field(entry, "Suggestion")
    if not suggestion:
        return None
    return natural_key(date, target, suggestion)


def atomic_write(path, text, backup_dir=None):
    """Write text to path via a temp file + rename; keep a timestamped backup."""
    import shutil
    import tempfile
    import time

    if backup_dir:
        os.makedirs(backup_dir, exist_ok=True)
        stamp = time.strftime("%Y%m%d-%H%M%S", time.gmtime())
        shutil.copy2(path, os.path.join(backup_dir, f"{os.path.basename(path)}.bak-{stamp}"))
    directory = os.path.dirname(path)
    fd, tmp = tempfile.mkstemp(prefix=".triage-", dir=directory)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            fh.write(text)
        os.replace(tmp, path)
    except Exception:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def load_harnesses(url):
    status, body = api("GET", f"{url}/v1/harnesses")
    return {h["name"]: h["memory_root"] for h in body.get("harnesses", [])}


def die(message, code=1):
    print(message, file=sys.stderr)
    sys.exit(code)

#!/usr/bin/env python3
"""
scripts/reconcile-memory.py

Idempotent reconciliation of bishop-memory's structured tables from an existing
harness memory tree. Parses canonical Markdown formats and syncs them through
the HTTP API, using natural-key dedupe to avoid duplicates.

This script subsumes the old backfill behavior (first run against an empty
database) and adds idempotent reconciliation (subsequent runs safely top up
missing rows and sync drifted fields).

  state/MISSION-ARCHIVE.md          -> missions          (create + PATCH if status/outcome drifts)
  state/CURRENT-MISSION.md          -> missions          (status override for live mission)
  missions/<id>/PROGRESS.md         -> mission_steps     (create or update as needed)
  state/FLIGHT-RECORDER.md          -> flight_recorder   (opt-in: --include-journal, per-mission dedupe)
  findings/FINDINGS.md              -> findings          (create only; findings are append-only)
  findings/PATTERNS.md              -> patterns          (create only; patterns are append-only)
  findings/service-records/<a>.md   -> service_records   (create only; records are append-only)

CURRENT-MISSION.md is a bullet list (`- Field: value`), not a table like its
three sibling state files (MISSION-ARCHIVE.md, PROGRESS.md, FLIGHT-RECORDER.md
all parse via table_rows()). See parse_missions() below for why it gets its
own line-by-line parse instead of going through table_rows().

Four things to know:

1. WRITES GO DIRECT TO HTTP, NOT THROUGH mcpd. Preserves original agent names.

2. THE API APPENDS ITS OWN EVENTS. createMission, updateMission, and
   createMissionStep each write a `mission.created` / `mission.updated` /
   `mission.step` row into flight_recorder as part of their transaction.
   Those audit rows are how this script's own work gets tracked.

3. THE JOURNAL IS OPT-IN. flight_recorder cannot be fully enumerated
   (hard cap of 100 rows per request, no offset). Global dedupe is
   impossible, so journal reconciliation is opt-in via --include-journal
   and per-mission only (requires <100 rows per mission).

4. CURRENT-MISSION.md HAS NO SUMMARY FIELD. Its six fields are Mission ID,
   Status, Owner, Next Action, Last Updated, Blockers — see
   .claude/templates/state/STATE-FILE-TEMPLATE.md in the harness. Only
   Mission ID and Status are read here; the mission's title always comes
   from BRIEF.md's `## Goal` via brief_goal().

Idempotency: Run this twice in a row and the second pass must change nothing
(all counts created/updated must be 0, exit code 0).

Usage:
  scripts/reconcile-memory.py --root <memory-root> [--url http://127.0.0.1:8787]
  scripts/reconcile-memory.py --root <memory-root> --dry-run
  scripts/reconcile-memory.py --root <memory-root> --include-journal
  scripts/reconcile-memory.py --root <memory-root> --skip-steps
  scripts/reconcile-memory.py --root <memory-root> --no-sync-documents
"""

import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

# The harness writes an em dash as the "no value" placeholder in table cells
# and entry headings. Treat it as empty rather than as literal content.
EM_DASH = "—"
PLACEHOLDERS = {"", "-", EM_DASH, "none", "None", "n/a", "N/A"}


def normalize_empty(value):
    """
    Normalize empty-like values for comparison, matching the behavior of clean()
    for the domain clean() accepts.

    Treats SQL NULL (arrives as Python None), empty string, and all placeholder
    forms recognized by clean() as equal-empty, using the same normalization
    (stripping whitespace and unescaping pipes) as clean() does for parsed values.
    This ensures the parsed and database sides of the comparison agree exactly.
    This prevents the reconciler from PATCHing the same rows on every hook fire
    forever — multiple representations of "nothing", but only the actual live
    next_action should change. A NULL from legacy rows that never got a next_action,
    and a parsed "" (template's placeholder normalized by clean()), must compare
    equal and require no write.

    The two functions deliberately diverge on one case: given a non-string value
    (such as a list or dict from malformed JSON), clean() would raise AttributeError
    by calling .strip() unconditionally, whereas normalize_empty() returns the value
    unchanged via its isinstance guard. This allows malformed responses to participate
    in comparison rather than aborting — unequal types fail the comparison, which is
    correct, rather than crashing the reconciler.
    """
    if value is None:
        return ""
    # Guard against non-string values (e.g., malformed JSON response with a list or dict)
    # to prevent TypeError on membership test. If a value is not a string or None,
    # it should not be silently treated as empty — return it as-is so malformed
    # data remains visible in comparisons rather than raising during the check.
    if not isinstance(value, str):
        return value
    value = value.strip()
    # Unescape the pipes the journal format requires inside Note cells.
    value = value.replace("\\|", "|")
    if value in PLACEHOLDERS:
        return ""
    return value


# --------------------------------------------------------------------------
# HTTP
# --------------------------------------------------------------------------

class Client:
    def __init__(self, base_url, dry_run=False):
        self.base = base_url.rstrip("/")
        self.dry_run = dry_run
        self.sent = 0

    def _request(self, method, path, payload=None):
        url = self.base + path
        data = None
        headers = {}
        if payload is not None:
            data = json.dumps(payload).encode("utf-8")
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        with urllib.request.urlopen(req, timeout=30) as resp:
            body = resp.read().decode("utf-8")
            return resp.status, json.loads(body) if body.strip() else {}

    def get(self, path):
        return self._request("GET", path)

    def write(self, method, path, payload, label):
        """POST/PATCH with dry-run support. Returns True on success."""
        if self.dry_run:
            print(f"  [dry-run] {method} {path}  {label}")
            self.sent += 1
            return True
        try:
            status, _ = self._request(method, path, payload)
        except urllib.error.HTTPError as exc:
            detail = exc.read().decode("utf-8", "replace")[:300]
            print(f"  !! {method} {path} -> HTTP {exc.code}: {detail}", file=sys.stderr)
            print(f"     payload: {json.dumps(payload)[:300]}", file=sys.stderr)
            return False
        except urllib.error.URLError as exc:
            print(f"  !! {method} {path} -> {exc.reason}", file=sys.stderr)
            return False
        if status >= 300:
            print(f"  !! {method} {path} -> HTTP {status}", file=sys.stderr)
            return False
        self.sent += 1
        return True


# --------------------------------------------------------------------------
# Parsing helpers (unchanged from backfill-memory.py)
# --------------------------------------------------------------------------

def clean(value):
    """Normalise a cell/field value; placeholders collapse to ''."""
    if value is None:
        return ""
    value = value.strip()
    # Unescape the pipes the journal format requires inside Note cells.
    value = value.replace("\\|", "|")
    return "" if value in PLACEHOLDERS else value


def split_row(line):
    """
    Split one Markdown table row into cells.

    Splits on unescaped pipes only: the FLIGHT-RECORDER format mandates that
    a literal pipe inside a Note is written `\\|`, and splitting naively on
    every `|` would tear those notes into extra columns and shift the whole
    row.
    """
    line = line.strip()
    if line.startswith("|"):
        line = line[1:]
    if line.endswith("|"):
        line = line[:-1]
    return [c for c in re.split(r"(?<!\\)\|", line)]


def table_rows(text, expected_cols):
    """Yield data rows from the first Markdown table with expected_cols columns."""
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped.startswith("|"):
            continue
        # Skip the separator row (|---|---|).
        if re.fullmatch(r"\|[\s|:-]+\|", stripped):
            continue
        cells = split_row(stripped)
        if len(cells) != expected_cols:
            continue
        # Skip the header row by detecting known header labels.
        first = cells[0].strip().lower()
        if first in {"step", "timestamp", "mission id"}:
            continue
        yield [c.strip() for c in cells]


def bullet_fields(text):
    """
    Yield (field, value) pairs from a `- Field: value` bullet list.

    CURRENT-MISSION.md is the one canonical state file that is a bullet
    list rather than a Markdown table (its three siblings — MISSION-
    ARCHIVE.md, PROGRESS.md, FLIGHT-RECORDER.md — all go through
    table_rows()). Its six fields are written one per line as
    `- Field Name: value`, per
    .claude/templates/state/STATE-FILE-TEMPLATE.md in the harness. Field
    names are matched case-insensitively against the template's exact
    wording; no tolerance is added for a variant spelling or a table shape
    nobody writes — the template is authoritative and this is the only
    shape that occurs.
    """
    for line in text.splitlines():
        match = re.match(r"^-\s*([A-Za-z][A-Za-z ]*):\s*(.*)$", line.strip())
        if not match:
            continue
        yield match.group(1).strip().lower(), clean(match.group(2))


def entries(path):
    """Split an append-only Markdown ledger into its `### ` entries."""
    if not os.path.isfile(path):
        return []
    text = open(path, encoding="utf-8").read()
    parts = re.split(r"^### ", text, flags=re.M)[1:]
    return [p.rstrip() for p in parts if p.strip()]


def entry_heading(entry):
    """First line of an entry, without the `### `."""
    return entry.splitlines()[0].strip()


def entry_field(entry, name):
    """
    Pull `**Name**: value` out of an entry.

    Values run until the next `**Field**:` or the end of the entry, so
    multi-line prose is captured whole.
    """
    match = re.search(
        r"\*\*" + re.escape(name) + r"\*\*:\s*(.*?)(?=\n\*\*[A-Z][A-Za-z ]*\*\*:|\Z)",
        entry,
        re.S,
    )
    return clean(match.group(1)) if match else ""


def split_heading_date(heading):
    """
    Split `2026-09-02 — rest` or `[2026-09-04] — rest` into (date, rest).

    Both spellings occur in the service-record files, so accept either. A
    heading with no leading date returns ('', heading).
    """
    match = re.match(r"^\[?(\d{4}-\d{2}-\d{2})\]?\s*" + EM_DASH + r"\s*(.*)$", heading)
    if match:
        return match.group(1), match.group(2).strip()
    return "", heading


def truncate(value, limit):
    return value if len(value) <= limit else value[: limit - 1].rstrip() + "…"


# --------------------------------------------------------------------------
# Source parsers (unchanged from backfill-memory.py)
# --------------------------------------------------------------------------

def parse_missions(root):
    """state/MISSION-ARCHIVE.md + state/CURRENT-MISSION.md -> mission dicts."""
    missions = {}

    # First, load archive missions (closed, authoritative on outcome)
    path = os.path.join(root, "state", "MISSION-ARCHIVE.md")
    if os.path.isfile(path):
        text = open(path, encoding="utf-8").read()
        for cells in table_rows(text, 4):
            mission_id, completed, outcome, summary = (clean(c) for c in cells)
            if not mission_id.startswith("mission-"):
                continue
            missions[mission_id] = {
                "id": mission_id,
                "title": brief_goal(root, mission_id) or summary,
                "status": "complete",
                "outcome": outcome if outcome in {"done", "failed"} else "",
                "summary": summary,
                "next_action": "",  # Archived missions have no next action
            }

    # Then, load the live mission (if any), which overrides status if present.
    #
    # CURRENT-MISSION.md is a bullet list, not a table (see bullet_fields()
    # and the module docstring) — table_rows() never matches a single line
    # of it. Read via table_rows() this loop always found mission_id == ""
    # for every harness that ever wrote a mission, so the `if mission_id:`
    # gate below never fired, the live mission never entered `missions`,
    # and reconcile_steps() never even requested its PROGRESS.md. Fixed by
    # parsing the actual bullet shape instead.
    path = os.path.join(root, "state", "CURRENT-MISSION.md")
    if os.path.isfile(path):
        text = open(path, encoding="utf-8").read()
        mission_id = ""
        status = ""
        next_action = ""
        for field, value in bullet_fields(text):
            if field == "mission id":
                mission_id = value
            elif field == "status":
                status = value if value in {"not-started", "in-progress", "blocked", "complete"} else ""
            elif field == "next action":
                # bullet_fields() already called clean() on the value, which normalizes
                # the template's `none` placeholder to ""; treat it as empty.
                next_action = value
        # clean() (called inside bullet_fields) already collapsed a literal
        # "none" — the template's explicit placeholder for an unset Mission
        # ID — to "". So a file carrying `- Mission ID: none` yields
        # mission_id == "" here, and the `if mission_id:` gate below
        # correctly treats that as "no live mission" rather than creating
        # one literally named "none".

        if mission_id:
            # Upsert: archive wins on both status and outcome (archive is the closing record).
            # CURRENT-MISSION.md supplies status only for a mission that is NOT in the archive.
            if mission_id in missions:
                # Mission is in archive; keep its canonical status, outcome, and next_action ("").
                # Archive has precedence — a closed mission has no next action, even if
                # CURRENT-MISSION.md still has a stale value (the mission has moved on).
                pass
            else:
                # Mission is live (not archived); use CURRENT-MISSION.md status and next_action.
                # CURRENT-MISSION.md has no Summary field (see module
                # docstring, note 4) — title comes from brief_goal() alone,
                # with no Markdown fallback. "summary" is kept as "" only
                # for dict-shape parity with the archive branch above;
                # nothing downstream reads it.
                missions[mission_id] = {
                    "id": mission_id,
                    "title": brief_goal(root, mission_id),
                    "status": status or "not-started",
                    "outcome": "",  # No outcome until archived
                    "summary": "",
                    "next_action": next_action,
                }

    return list(missions.values())


def brief_goal(root, mission_id):
    """First paragraph under `## Goal` in a mission's BRIEF.md."""
    path = os.path.join(root, "missions", mission_id, "BRIEF.md")
    if not os.path.isfile(path):
        return ""
    text = open(path, encoding="utf-8").read()
    match = re.search(r"^##\s+Goal\s*\n+(.+?)(?=\n\s*\n|\n##\s|\Z)", text, re.S | re.M)
    if not match:
        return ""
    return " ".join(match.group(1).split())


def parse_steps(root, mission_id):
    """missions/<id>/PROGRESS.md -> step dicts."""
    path = os.path.join(root, "missions", mission_id, "PROGRESS.md")
    if not os.path.isfile(path):
        return []
    text = open(path, encoding="utf-8").read()
    steps = []
    for cells in table_rows(text, 5):
        step, phase, agent, status, notes = (clean(c) for c in cells)
        if not step:
            continue
        steps.append({
            "step": truncate(step, 16),
            "phase": truncate(phase, 64),
            "agent": truncate(agent, 128),
            "status": status if status in {"pending", "in-progress", "done", "failed"} else "",
            "notes": truncate(notes, 2000),
        })
    return steps


def parse_journal(root):
    """state/FLIGHT-RECORDER.md -> event dicts."""
    path = os.path.join(root, "state", "FLIGHT-RECORDER.md")
    if not os.path.isfile(path):
        return []
    text = open(path, encoding="utf-8").read()
    events = []
    for cells in table_rows(text, 6):
        ts, mission_id, step, agent, event, note = (clean(c) for c in cells)
        if not event or not note:
            # event and note are both `required` server-side.
            continue
        events.append({
            "occurred_at": truncate(ts, 64),
            "mission_id": mission_id,
            "step": truncate(step, 16),
            "agent": truncate(agent, 64),
            "event": truncate(event, 64),
            "note": truncate(note, 2000),
        })
    return events


def parse_findings(root):
    """findings/FINDINGS.md -> finding dicts."""
    out = []
    for entry in entries(os.path.join(root, "findings", "FINDINGS.md")):
        date, target = split_heading_date(entry_heading(entry))
        suggestion = entry_field(entry, "Suggestion")
        if not suggestion:
            continue  # `suggestion` is the one required field.
        out.append({
            "finding_date": truncate(date, 64),
            "target": truncate(target, 256),
            "suggestion": truncate(suggestion, 2000),
            "rationale": truncate(entry_field(entry, "Rationale"), 2000),
        })
    return out


def parse_patterns(root):
    """findings/PATTERNS.md -> pattern dicts."""
    out = []
    for entry in entries(os.path.join(root, "findings", "PATTERNS.md")):
        name = entry_heading(entry)
        if not name:
            continue
        # `**Discovered**: 2026-09-02, mission-20260902-01`
        discovered = entry_field(entry, "Discovered")
        at, mission = "", ""
        if discovered:
            bits = [b.strip() for b in discovered.split(",", 1)]
            at = bits[0]
            mission = bits[1] if len(bits) > 1 else ""
        out.append({
            "name": truncate(name, 256),
            "context": truncate(entry_field(entry, "Context"), 2000),
            "solution": truncate(entry_field(entry, "Solution"), 2000),
            "example": truncate(entry_field(entry, "Example"), 2000),
            "discovered_at": truncate(at, 64),
            "discovered_mission": truncate(mission, 128),
        })
    return out


def parse_service_records(root):
    """findings/service-records/<agent>.md -> service record dicts."""
    directory = os.path.join(root, "findings", "service-records")
    if not os.path.isdir(directory):
        return []
    out = []
    for name in sorted(os.listdir(directory)):
        if not name.endswith(".md"):
            continue
        agent = name[:-3]
        for entry in entries(os.path.join(directory, name)):
            date, title = split_heading_date(entry_heading(entry))
            source = entry_field(entry, "Source")
            out.append({
                "agent": truncate(agent, 128),
                "record_date": truncate(date, 64),
                "title": truncate(title, 256),
                "note": truncate(entry_field(entry, "Note"), 2000),
                "adjustment": truncate(entry_field(entry, "Adjustment"), 2000),
                "source": source if source in {"self-reported", "bishop-observed"} else "",
            })
    return out


# --------------------------------------------------------------------------
# Dedupe and reconcile
# --------------------------------------------------------------------------

def fetch_failure_note(fetch_failed, skipped=None):
    """
    Build the summary-line suffix reported for a fetch failure, naming an
    actual count rather than only a boolean "(could not fetch existing)".

    Two shapes of fetch exist in this script:
      - Per-item (steps, journal events): each mission gets its own GET, so
        some missions can succeed while others fail. Pass the entity's own
        `skipped_fetch_failed` counter as `skipped` so the line states
        exactly how many parsed items a partial failure left unexamined —
        the gap that made counts silently fail to sum in the original
        [steps] line.
      - All-or-nothing (missions, findings, patterns, service records): one
        shared GET; if it fails, every parsed item is skipped, which is
        already the "parsed" figure on the same line. Call with
        `skipped=None` and this reports the failed-request count instead —
        there's nothing narrower to report.
    """
    if fetch_failed <= 0:
        return ""
    if skipped is not None:
        return f" ({skipped} skipped — fetch failed for {fetch_failed} mission(s))"
    return f" (fetch failed — {fetch_failed} request(s) could not confirm existing state)"


def prune(payload):
    """Drop empty-string fields so `omitempty` validation sees them as absent."""
    return {k: v for k, v in payload.items() if v not in ("", None)}


def reconcile_missions(client, parsed_missions):
    """Create missing missions and PATCH any with drifted status/outcome."""
    status_counts = {"parsed": len(parsed_missions), "created": 0, "updated": 0, "failed": 0, "skipped": 0, "fetch_failed": 0}

    # Fetch existing missions
    try:
        _, body = client.get("/v1/missions")
        existing = {m["id"]: m for m in body.get("missions", [])}
    except Exception as exc:
        print(f"  !! GET /v1/missions: could not fetch existing (network error, service down, or timeout)", file=sys.stderr)
        print(f"     {exc}", file=sys.stderr)
        status_counts["fetch_failed"] = 1
        return status_counts

    for mission in parsed_missions:
        if mission["id"] in existing:
            # Mission exists: check if status, outcome, or next_action differs
            db_mission = existing[mission["id"]]

            # Derive expected values
            expected_status = mission["status"]
            expected_outcome = mission["outcome"]
            expected_next_action = mission.get("next_action", "")

            # Check for drift, using normalized comparison for next_action.
            # Normalize both sides: SQL NULL (Python None), "", and the
            # template's "none" placeholder all represent "nothing", so they
            # must compare equal. Without this, a legacy NULL and a parsed ""
            # differ forever, and the reconciler PATCHes the same rows on
            # every hook fire.
            #
            # AUDIT-VOLUME NOTE: Once next_action tracks the live mission (no longer empty),
            # every state-sync that changes CURRENT-MISSION.md's Next Action line produces
            # one genuine mission.updated audit row from the API. The API writes an audit row
            # on every call regardless, but this reconciler's comparison — checking whether a
            # value differs before calling client.write — produces one row per genuine state
            # change. The same technique applies to mission steps through steps_equal(), so
            # the principle is consistent: an append-only audit journal paired with
            # comparison-based deduplication on the client side.
            db_status = db_mission.get("status", "")
            db_outcome = db_mission.get("outcome", "") or ""
            db_next_action = normalize_empty(db_mission.get("next_action"))

            if db_status != expected_status or db_outcome != expected_outcome or \
               normalize_empty(expected_next_action) != db_next_action:
                update_payload = {}
                if db_status != expected_status:
                    update_payload["status"] = expected_status
                if db_outcome != expected_outcome:
                    update_payload["outcome"] = expected_outcome
                if normalize_empty(expected_next_action) != db_next_action:
                    update_payload["next_action"] = expected_next_action

                escaped_id = urllib.parse.quote(mission['id'], safe='')
                if not client.write("PATCH", f"/v1/missions/{escaped_id}", update_payload,
                                    f"{mission['id']} (status={expected_status}, outcome={expected_outcome}, next_action={expected_next_action})"):
                    status_counts["failed"] += 1
                else:
                    status_counts["updated"] += 1
            else:
                status_counts["skipped"] += 1
        else:
            # Mission doesn't exist: create it
            payload = prune({
                "id": mission["id"],
                "title": truncate(mission.get("title", ""), 500),
                "status": mission["status"],
            })
            if not client.write("POST", "/v1/missions", payload, mission["id"]):
                status_counts["failed"] += 1
                continue

            # If there's an outcome or next_action, PATCH them
            # (neither is settable on create; outcome has always been PATCHed, and now next_action too)
            patch_payload = {}
            if mission["outcome"]:
                patch_payload["outcome"] = mission["outcome"]
            if mission.get("next_action"):
                patch_payload["next_action"] = mission["next_action"]

            if patch_payload:
                escaped_id = urllib.parse.quote(mission['id'], safe='')
                patch_label = ", ".join(f"{k}={v}" for k, v in patch_payload.items())
                if not client.write("PATCH", f"/v1/missions/{escaped_id}", patch_payload,
                                    f"{mission['id']} {patch_label}"):
                    status_counts["failed"] += 1

            status_counts["created"] += 1

    return status_counts


def reconcile_steps(client, parsed_missions, root, skip_steps=False):
    """Create or update mission steps as needed, with comparison-based dedup."""
    # "fetch_failed" counts affected MISSIONS (one GET per mission, so a
    # mission either fetches or doesn't); "skipped_fetch_failed" counts the
    # STEPS that fetch failure left untouched, so that
    # parsed == created + updated + unchanged + failed + skipped_fetch_failed
    # always holds. Without the second counter, a mission-level fetch
    # failure quietly removes its steps from every bucket after "parsed",
    # and the summary line looks like a clean, fully-examined run even
    # though some steps were never even compared.
    status_counts = {"parsed": 0, "created": 0, "updated": 0, "unchanged": 0, "failed": 0,
                      "fetch_failed": 0, "skipped_fetch_failed": 0}

    # Parse steps eagerly for all missions (to report true parsed count even during outages)
    all_parsed_steps = {}
    for mission in parsed_missions:
        steps = parse_steps(root, mission["id"])
        all_parsed_steps[mission["id"]] = steps
        status_counts["parsed"] += len(steps)

    # If skipping, we're done — report parsed count only, no fetches or writes
    if skip_steps:
        return status_counts

    # Fetch all existing steps (keyed by (mission_id, step))
    # Track which missions had successful fetches so we can skip writes for failed ones
    existing_steps = {}
    missions_fetch_failed = set()
    for mission in parsed_missions:
        try:
            escaped_id = urllib.parse.quote(mission['id'], safe='')
            _, body = client.get(f"/v1/missions/{escaped_id}/steps")
            for step in body.get("steps", []):
                key = (mission["id"], step["step"])
                existing_steps[key] = step
        except urllib.error.HTTPError as exc:
            if exc.code != 404:
                print(f"  !! GET /v1/missions/{mission['id']}/steps: HTTP {exc.code}; skipping writes for this mission", file=sys.stderr)
                missions_fetch_failed.add(mission["id"])
                status_counts["fetch_failed"] += 1
            # 404 is fine (mission may not exist yet or have no steps)
        except Exception as exc:
            print(f"  !! GET /v1/missions/{mission['id']}/steps: could not fetch existing; skipping writes for this mission", file=sys.stderr)
            print(f"     {exc}", file=sys.stderr)
            missions_fetch_failed.add(mission["id"])
            status_counts["fetch_failed"] += 1

    # Helper: compare parsed step payload against stored step, handling normalization.
    # The three key normalisation traps:
    #   1. Truncation: parse_steps already truncates (step to 16, phase to 64, agent to 128, notes to 2000).
    #      Stored values are whatever was posted, already truncated. Compare post-truncation payload against stored.
    #   2. Pruning: prune() drops empty-string and None fields so omitempty validation works. A field the payload
    #      omits is a field the reconciler has no opinion about; COALESCE preserves whatever is stored. Only
    #      compare keys present in the pruned payload — omitted fields are not differences.
    #   3. None normalization: stored row comes back as JSON with NULL -> None. Normalise None to "" before
    #      comparing, or a stored NULL against a payload that omits the field will read as different forever.
    def steps_equal(parsed_payload, stored_step):
        """Compare parsed (pruned) payload against stored step. Return True if they're the same."""
        payload = prune(parsed_payload)
        # Only keys in the payload matter; omitted fields are not differences.
        for key in payload:
            stored_value = stored_step.get(key)
            # Normalise stored NULL to "" for comparison
            if stored_value is None:
                stored_value = ""
            payload_value = payload[key]
            if stored_value != payload_value:
                return False
        return True

    # Create or update steps, skipping writes for missions whose existing-steps fetch failed
    for mission in parsed_missions:
        steps = all_parsed_steps[mission["id"]]
        # Skip writes for missions whose existing-steps fetch failed. Count
        # the steps themselves here (not just the mission) so the summary
        # line can report exactly how many parsed steps were never examined.
        if mission["id"] in missions_fetch_failed:
            status_counts["skipped_fetch_failed"] += len(steps)
            continue

        for step in steps:
            key = (mission["id"], step["step"])
            payload = prune(step)

            if key in existing_steps:
                # Step exists: check if it differs
                if steps_equal(step, existing_steps[key]):
                    status_counts["unchanged"] += 1
                else:
                    # Step differs: upsert it
                    escaped_id = urllib.parse.quote(mission['id'], safe='')
                    if not client.write("POST", f"/v1/missions/{escaped_id}/steps",
                                        payload, f"{mission['id']} step {step['step']} (update)"):
                        status_counts["failed"] += 1
                    else:
                        status_counts["updated"] += 1
            else:
                # Step doesn't exist: create it
                escaped_id = urllib.parse.quote(mission['id'], safe='')
                if not client.write("POST", f"/v1/missions/{escaped_id}/steps",
                                    payload, f"{mission['id']} step {step['step']}"):
                    status_counts["failed"] += 1
                else:
                    status_counts["created"] += 1

    return status_counts


def reconcile_journal(client, parsed_missions, root):
    """Reconcile flight recorder events (per-mission, opt-in)."""
    # Same per-mission fetch shape as reconcile_steps(): "fetch_failed" counts
    # affected missions, "skipped_fetch_failed" counts the events those
    # missions' rows left untouched, so a reader can tell how many parsed
    # events were genuinely skipped rather than inferring it from stderr.
    # Note: "parsed" here also excludes events for missions this run never
    # heard of at all (see the `not any(...)` filter below) — that's a
    # separate, deliberate scope filter, not a fetch failure, and is not
    # counted here.
    status_counts = {"parsed": 0, "created": 0, "failed": 0, "skipped": 0, "warned": 0,
                      "fetch_failed": 0, "skipped_fetch_failed": 0}

    # Fetch all existing journal rows (per-mission, with cap check)
    existing_journal = {}
    missions_journal_fetch_failed = set()
    for mission in parsed_missions:
        try:
            _, body = client.get(f"/v1/flight-recorder?mission_id={urllib.parse.quote_plus(mission['id'])}&limit=100")
            rows = body.get("flight_recorder", [])
            if len(rows) >= 100:
                print(f"  [WARN] {mission['id']}: journal at 100-row cap; cannot verify completeness; skipping writes for this mission", file=sys.stderr)
                missions_journal_fetch_failed.add(mission["id"])
                status_counts["fetch_failed"] += 1
                continue
            for row in rows:
                # Natural key: (mission_id, occurred_at, step, event, note)
                key = (row.get("mission_id", ""), row.get("occurred_at", ""), row.get("step", ""), row.get("event", ""), row.get("note", ""))
                existing_journal[key] = row
        except Exception as exc:
            print(f"  !! GET /v1/flight-recorder?mission_id={mission['id']}: could not fetch existing; skipping writes for this mission", file=sys.stderr)
            print(f"     {exc}", file=sys.stderr)
            missions_journal_fetch_failed.add(mission["id"])
            status_counts["fetch_failed"] += 1

    # Parse and create missing events
    events = parse_journal(root)
    status_counts["parsed"] = len(events)
    for event in events:
        # Only process events for missions we know about and whose fetch succeeded
        if not any(m["id"] == event["mission_id"] for m in parsed_missions):
            continue
        if event["mission_id"] in missions_journal_fetch_failed:
            status_counts["skipped_fetch_failed"] += 1
            continue

        key = (event["mission_id"], event["occurred_at"], event["step"], event["event"], event["note"])
        if key in existing_journal:
            status_counts["skipped"] += 1
        else:
            if not client.write("POST", "/v1/flight-recorder", prune(event),
                                f"{event['occurred_at']} {event['event']}"):
                status_counts["failed"] += 1
            else:
                status_counts["created"] += 1

    return status_counts


def reconcile_findings(client, root):
    """Create missing findings."""
    status_counts = {"parsed": 0, "created": 0, "failed": 0, "skipped": 0, "fetch_failed": 0}

    # Fetch existing findings (keyed by natural key)
    existing = {}
    try:
        _, body = client.get("/v1/findings")
        for f in body.get("findings", []):
            key = (f.get("finding_date", ""), f.get("target", ""), f.get("suggestion", ""))
            existing[key] = f
    except Exception as exc:
        print(f"  !! GET /v1/findings: could not fetch existing (network error, service down, or timeout)", file=sys.stderr)
        print(f"     {exc}", file=sys.stderr)
        status_counts["fetch_failed"] = 1
        return status_counts

    # Parse and create missing findings
    findings = parse_findings(root)
    status_counts["parsed"] = len(findings)
    for finding in findings:
        key = (finding["finding_date"], finding["target"], finding["suggestion"])
        if key in existing:
            status_counts["skipped"] += 1
        else:
            if not client.write("POST", "/v1/findings", prune(finding),
                                f"{finding['finding_date']} {finding['target']}"):
                status_counts["failed"] += 1
            else:
                status_counts["created"] += 1

    return status_counts


def reconcile_patterns(client, root):
    """Create missing patterns."""
    status_counts = {"parsed": 0, "created": 0, "failed": 0, "skipped": 0, "fetch_failed": 0}

    # Fetch existing patterns (keyed by name)
    existing = {}
    try:
        _, body = client.get("/v1/patterns")
        for p in body.get("patterns", []):
            existing[p.get("name", "")] = p
    except Exception as exc:
        print(f"  !! GET /v1/patterns: could not fetch existing (network error, service down, or timeout)", file=sys.stderr)
        print(f"     {exc}", file=sys.stderr)
        status_counts["fetch_failed"] = 1
        return status_counts

    # Parse and create missing patterns
    patterns = parse_patterns(root)
    status_counts["parsed"] = len(patterns)
    for pattern in patterns:
        if pattern["name"] in existing:
            status_counts["skipped"] += 1
        else:
            if not client.write("POST", "/v1/patterns", prune(pattern), pattern["name"]):
                status_counts["failed"] += 1
            else:
                status_counts["created"] += 1

    return status_counts


def reconcile_records(client, root):
    """Create missing service records."""
    status_counts = {"parsed": 0, "created": 0, "failed": 0, "skipped": 0, "fetch_failed": 0}

    # Fetch existing records (keyed by natural key)
    existing = {}
    try:
        _, body = client.get("/v1/service-records")
        for r in body.get("service_records", []):
            key = (r.get("agent", ""), r.get("record_date", ""), r.get("title", ""))
            existing[key] = r
    except Exception as exc:
        print(f"  !! GET /v1/service-records: could not fetch existing (network error, service down, or timeout)", file=sys.stderr)
        print(f"     {exc}", file=sys.stderr)
        status_counts["fetch_failed"] = 1
        return status_counts

    # Parse and create missing records
    records = parse_service_records(root)
    status_counts["parsed"] = len(records)
    for record in records:
        key = (record["agent"], record["record_date"], record["title"])
        if key in existing:
            status_counts["skipped"] += 1
        else:
            if not client.write("POST", "/v1/service-records", prune(record),
                                f"{record['agent']} {record['record_date']}"):
                status_counts["failed"] += 1
            else:
                status_counts["created"] += 1

    return status_counts


# --------------------------------------------------------------------------
# Main
# --------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(
        description="Idempotent reconciliation of bishop-memory from a harness memory tree.")
    parser.add_argument("--root", required=True, help="Path to the .claude/memory tree.")
    parser.add_argument("--url", default=os.environ.get("BISHOP_MEMORY_URL", "http://127.0.0.1:8787"),
                        help="Base URL of the bishop-memory HTTP API.")
    parser.add_argument("--dry-run", action="store_true",
                        help="Parse and report what would be written; send nothing.")
    parser.add_argument("--include-journal", action="store_true",
                        help="Reconcile flight-recorder (journal) as well. Default: off (hook keeps it current).")
    parser.add_argument("--skip-steps", action="store_true",
                        help="Skip mission step reconciliation. Use this to exclude step syncing from the reconciliation run.")
    parser.add_argument("--no-sync-documents", action="store_true",
                        help="Skip POST /v1/documents/sync. Default: sync documents first.")
    args = parser.parse_args()

    root = os.path.abspath(os.path.expanduser(args.root))
    if not os.path.isdir(root):
        print(f"error: --root is not a directory: {root}", file=sys.stderr)
        sys.exit(66)

    client = Client(args.url, dry_run=args.dry_run)

    print(f"[reconcile] memory root: {root}")
    print(f"[reconcile] api:         {client.base}")
    if args.dry_run:
        print("[reconcile] DRY RUN — nothing will be written")
    if args.include_journal:
        print("[reconcile] including journal reconciliation")
    if args.skip_steps:
        print("[reconcile] skipping mission step reconciliation")
    if args.no_sync_documents:
        print("[reconcile] skipping document sync")
    print()

    total_failed_operations = 0  # Track actual count of failed operations
    total_fetch_failures = 0     # Track count of fetch failures
    total_entities = 0

    # --- Sync documents first (if not disabled) ----
    if not args.no_sync_documents:
        if not client.write("POST", "/v1/documents/sync", {}, "trigger index sync"):
            total_failed_operations += 1
        print("[documents] sync triggered")
    print()

    # --- Missions ---------
    missions = parse_missions(root)
    counts = reconcile_missions(client, missions)
    fetch_status = fetch_failure_note(counts.get('fetch_failed', 0))
    print(f"[missions] {counts['parsed']} parsed, {counts['created']} created, " +
          f"{counts['updated']} updated, {counts['skipped']} unchanged, {counts['failed']} failed{fetch_status}")
    total_failed_operations += counts['failed']
    total_fetch_failures += counts.get('fetch_failed', 0)
    total_entities += counts['created'] + counts['updated']

    # --- Mission steps ----
    counts = reconcile_steps(client, missions, root, skip_steps=args.skip_steps)
    if args.skip_steps:
        print(f"[steps] {counts['parsed']} parsed (skipped)")
    else:
        # skipped_fetch_failed is what makes this line's counts sum to
        # "parsed" even when some missions' step-fetch failed and others'
        # didn't — see fetch_failure_note() and reconcile_steps().
        fetch_status = fetch_failure_note(counts.get('fetch_failed', 0), counts.get('skipped_fetch_failed', 0))
        print(f"[steps] {counts['parsed']} parsed, {counts['created']} created, {counts['updated']} updated, " +
              f"{counts['unchanged']} unchanged, {counts['failed']} failed{fetch_status}")
        total_failed_operations += counts['failed']
        total_fetch_failures += counts.get('fetch_failed', 0)
        total_entities += counts['created'] + counts['updated']

    # --- Flight recorder (journal) -- opt-in ----
    if args.include_journal:
        counts = reconcile_journal(client, missions, root)
        fetch_status = fetch_failure_note(counts.get('fetch_failed', 0), counts.get('skipped_fetch_failed', 0))
        print(f"[flight-recorder] {counts['parsed']} parsed, {counts['created']} created, " +
              f"{counts['skipped']} unchanged, {counts['failed']} failed{fetch_status}", end="")
        if counts['warned'] > 0:
            print(f", {counts['warned']} warned (at cap)")
        else:
            print()
        total_failed_operations += counts['failed']
        total_fetch_failures += counts.get('fetch_failed', 0)
        total_entities += counts['created']
    else:
        print("[flight-recorder] (skipped — use --include-journal to reconcile)")

    # --- Findings --------
    print()
    counts = reconcile_findings(client, root)
    fetch_status = fetch_failure_note(counts.get('fetch_failed', 0))
    print(f"[findings] {counts['parsed']} parsed, {counts['created']} created, " +
          f"{counts['skipped']} unchanged, {counts['failed']} failed{fetch_status}")
    total_failed_operations += counts['failed']
    total_fetch_failures += counts.get('fetch_failed', 0)
    total_entities += counts['created']

    # --- Patterns --------
    counts = reconcile_patterns(client, root)
    fetch_status = fetch_failure_note(counts.get('fetch_failed', 0))
    print(f"[patterns] {counts['parsed']} parsed, {counts['created']} created, " +
          f"{counts['skipped']} unchanged, {counts['failed']} failed{fetch_status}")
    total_failed_operations += counts['failed']
    total_fetch_failures += counts.get('fetch_failed', 0)
    total_entities += counts['created']

    # --- Service records -
    counts = reconcile_records(client, root)
    fetch_status = fetch_failure_note(counts.get('fetch_failed', 0))
    print(f"[service-records] {counts['parsed']} parsed, {counts['created']} created, " +
          f"{counts['skipped']} unchanged, {counts['failed']} failed{fetch_status}")
    total_failed_operations += counts['failed']
    total_fetch_failures += counts.get('fetch_failed', 0)
    total_entities += counts['created']

    # --- Summary --------
    print()
    verb = "would send" if args.dry_run else "sent"
    error_detail = ""
    if total_failed_operations > 0 or total_fetch_failures > 0:
        error_parts = []
        if total_failed_operations > 0:
            error_parts.append(f"{total_failed_operations} operation failed")
        if total_fetch_failures > 0:
            error_parts.append(f"{total_fetch_failures} fetch failed")
        error_detail = f", {', '.join(error_parts)}"
    print(f"[reconcile] {verb} {client.sent} requests, {total_entities} entities created/updated{error_detail}")
    if total_failed_operations > 0 or total_fetch_failures > 0:
        sys.exit(1)
    sys.exit(0)


if __name__ == "__main__":
    main()

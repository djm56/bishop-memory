#!/usr/bin/env python3
"""
Unit tests for scripts/reconcile-memory.py. Most cases run on a tiny temp fixture
or on the shipped fixture (testdata/memory) with a mock client. The main() cases
run the reconciler as a subprocess, pointed at a fake HTTP service on 127.0.0.1.
Nothing touches a real memory root or the live service.

  - parse_steps(): the step table in missions/<id>/PROGRESS.md, file present
    and file absent. Each case has its own assertion message.
  - The missions folder lookup through BISHOP_LAYOUT. The declared folder name
    is read; a name the layout does not declare is not read.
  - reconcile_findings(): an empty Approver falls back to APPROVER_DEFAULT.
  - parse_findings() on the shipped fixture: the **Mission** link each fixture
    finding receives. The comment above each assertion says what it pins, and
    cites the plan section where the case follows one.
  - parse_steps() on the shipped fixture: the status each step row keeps.
  - reconcile_steps() on the shipped fixture: an unknown status is warned and
    counted on the reconcile path, and under skip_steps nothing is warned.
  - reconcile_findings() on the shipped fixture: a Mission candidate that names
    no known mission is warned and left out of the create payload, and is not
    warned when its entry already exists at the service.
  - reconcile_journal(): a mission at the 100-row journal cap is counted as
    skipped (journal cap), apart from a fetch failure; below the cap it is not;
    a capped mission with no local events is warned and skips nothing.
  - reconcile_journal() buckets: every parsed event lands in exactly one summed
    bucket, so the buckets sum to parsed in each way an event can land, and an
    event for a mission this run does not know is out_of_scope.
  - reconcile_journal() detail lines: one line per mission with journal events,
    naming that mission's counts; a mission with no events has none.
  - reconcile_steps() warnings: an unknown step status is warned only after the
    mission's existing-steps fetch succeeds; a fetch failure is not warned.
  - main() exit status and summary for the journal cap: cap skips alone exit 3
    and name the cap; a failure with a cap skip exits 1; no cap skips exit 0; a
    capped mission with no local events exits 0. Out-of-scope events alone exit 0
    and are named in the flight-recorder summary.
  - fetch_journal() and reconcile_journal() against a paging server: every page
    is read and no cap arises, an exact page is not capped, a failing page is a
    fetch failure with no partial pass, a page that reports more with no rows
    is a fetch failure. Against a server that does not page, the cap holds.

The step-table tests write into the folder and file names BISHOP_LAYOUT
declares, so they test the parser and not the name. The lookup tests use the
literal names, so a change to the declared value fails them.
"""
import http.server
import importlib.util
import io
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
import urllib.parse
from contextlib import redirect_stderr, redirect_stdout
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
RECONCILER = os.path.join(HERE, "reconcile-memory.py")
sys.path.insert(0, HERE)
_spec = importlib.util.spec_from_file_location("reconcile_memory", RECONCILER)
reconcile = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(reconcile)

MISSION_ID = "mission-20260904-01"

STEP_TABLE = """# Progress — mission-20260904-01

| Step | Phase | Agent | Status | Notes |
|------|-------|-------|--------|-------|
| 1 | — | @hicks | done | Restructured fixture tree |
| 2 | — | @apone | pending | Review |
"""

FINDINGS_HEAD = "# Findings\n\n<!-- Append new entries below this line -->\n\n"
FINDING_NO_APPROVER = """### 2026-10-01 — Alpha target
**Suggestion**: Alpha suggestion
**Rationale**: Alpha rationale
**Status**: approved
**Date approved**: 2026-10-02

"""
FINDING_WITH_APPROVER = """### 2026-10-01 — Beta target
**Suggestion**: Beta suggestion
**Rationale**: Beta rationale
**Status**: approved
**Approver**: Donovan
**Date approved**: 2026-10-02

"""


class MockClient:
    """Mock client. GET /v1/findings returns the rows given; writes are recorded."""

    def __init__(self, findings=()):
        self.findings = list(findings)
        self.dry_run = False
        self.written = []

    def get(self, path):
        if path == "/v1/findings":
            return 200, {"findings": self.findings}
        return 200, {}

    def write(self, method, path, payload, label):
        self.written.append({"method": method, "path": path, "payload": payload, "label": label})
        return True


class _TempRoot(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = self.tmp.name

    def write_progress(self, folder, text=STEP_TABLE):
        """Write a PROGRESS.md under <root>/<folder>/<MISSION_ID>/ and return its path."""
        mission_dir = os.path.join(self.root, folder, MISSION_ID)
        os.makedirs(mission_dir, exist_ok=True)
        path = os.path.join(mission_dir, reconcile.BISHOP_LAYOUT.progress)
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(text)
        return path


class StepTableTests(_TempRoot):
    """parse_steps() on the declared layout: the file-present and file-absent branches."""

    def test_present_progress_yields_steps(self):
        self.write_progress(reconcile.BISHOP_LAYOUT.missions_dir)
        steps = reconcile.parse_steps(self.root, MISSION_ID)
        self.assertEqual([s["step"] for s in steps], ["1", "2"],
                         msg="file present yields steps: expected steps 1 and 2 from PROGRESS.md")
        self.assertEqual((steps[0]["agent"], steps[0]["status"]), ("@hicks", "done"),
                         msg="file present yields steps: step 1 agent or status not parsed")

    def test_absent_progress_yields_none(self):
        os.makedirs(os.path.join(self.root, reconcile.BISHOP_LAYOUT.missions_dir, MISSION_ID))
        steps = reconcile.parse_steps(self.root, MISSION_ID)
        self.assertEqual(steps, [], msg="absent file yields none: parse_steps returned steps with no PROGRESS.md")


class MissionsFolderLookupTests(_TempRoot):
    """parse_steps() reads the mission folder by the name BISHOP_LAYOUT declares, and no other."""

    def test_declared_missions_folder_is_read(self):
        # Literal name on purpose: this pins the declared value, not the constant.
        self.write_progress("missions")
        steps = reconcile.parse_steps(self.root, MISSION_ID)
        self.assertEqual(len(steps), 2,
                         msg="declared missions folder is read: expected 2 steps under missions/")

    def test_undeclared_folder_name_is_not_read(self):
        # Literal name on purpose: "notes" is not declared under any layout value this suite pins.
        self.write_progress("notes")
        steps = reconcile.parse_steps(self.root, MISSION_ID)
        self.assertEqual(steps, [],
                         msg="undeclared folder notes is not read: parse_steps read notes/ as the mission folder")


class ApproverDefaultTests(_TempRoot):
    """reconcile_findings() records APPROVER_DEFAULT when the Markdown names no Approver."""

    def _decision_approvers(self):
        """Run reconcile_findings() over one finding with no Approver and one with Approver Donovan; return approver by route."""
        findings_dir = os.path.join(self.root, reconcile.BISHOP_LAYOUT.findings_dir)
        os.makedirs(findings_dir)
        with open(os.path.join(findings_dir, reconcile.BISHOP_LAYOUT.findings_file), "w", encoding="utf-8") as fh:
            fh.write(FINDINGS_HEAD + FINDING_NO_APPROVER + FINDING_WITH_APPROVER)

        # Both rows already exist at the service as proposed, so each reaches the decision route.
        existing = [
            {"id": "f-alpha", "finding_date": "2026-10-01", "target": "Alpha target",
             "suggestion": "Alpha suggestion", "status": "proposed"},
            {"id": "f-beta", "finding_date": "2026-10-01", "target": "Beta target",
             "suggestion": "Beta suggestion", "status": "proposed"},
        ]
        client = MockClient(findings=existing)
        with redirect_stderr(io.StringIO()):
            counts = reconcile.reconcile_findings(client, self.root, known_missions=())
        self.assertEqual(counts["decided"], 2, msg=f"expected two decisions, got counts {counts}")
        return {w["path"]: w["payload"]["approver"] for w in client.written}

    def test_empty_approver_falls_back_to_default(self):
        approvers = self._decision_approvers()
        self.assertEqual(approvers.get("/v1/findings/f-alpha/decision"), "FINDINGS.md",
                         msg="empty Approver: decision did not fall back to APPROVER_DEFAULT")

    def test_explicit_approver_is_carried(self):
        approvers = self._decision_approvers()
        self.assertEqual(approvers.get("/v1/findings/f-beta/decision"), "Donovan",
                         msg="explicit Approver: decision did not carry the Markdown value")

    def test_default_constant_is_findings_file_name(self):
        self.assertEqual(reconcile.APPROVER_DEFAULT, "FINDINGS.md",
                         msg="APPROVER_DEFAULT value changed from FINDINGS.md")


FIXTURE_ROOT = os.path.join(HERE, "..", "testdata", "memory")
FIXTURE_MISSION = "mission-20260904-01"


def _fixture_known_missions():
    """The mission ids the reconciler knows from the shipped fixture: archive rows and live mission."""
    return {m["id"] for m in reconcile.parse_missions(FIXTURE_ROOT)}


def _fixture_finding(target):
    """Return the parse_findings() dict whose heading target equals target, from the shipped fixture."""
    matches = [f for f in reconcile.parse_findings(FIXTURE_ROOT, _fixture_known_missions()) if f["target"] == target]
    if len(matches) != 1:
        raise AssertionError(f"fixture finding {target!r}: expected one entry, found {len(matches)}")
    return matches[0]


class FindingMissionLinkTests(unittest.TestCase):
    """
    parse_findings() on the shipped fixture: the mission_id each **Mission** field yields.

    Calls parse_findings() directly, with the known ids the fixture's archive and
    folders give. It does not reach reconcile_findings(), so the link is read
    from the parsed dict, the value the create payload carries.
    The fixture entries are in testdata/memory/findings/FINDINGS.md; the ids they
    name are the archive rows in testdata/memory/state/MISSION-ARCHIVE.md.
    """

    def test_plain_id_links_to_its_mission(self):
        # Control: a plain two-digit id resolves to itself. A failure here means
        # parse_findings() no longer reads the Mission field.
        self.assertEqual(_fixture_finding("Plain mission id link")["mission_id"], "mission-20261009-10",
                         msg="plain id: Mission field mission-20261009-10 not linked to itself")

    def test_three_digit_counter_links_to_itself(self):
        # The extraction rule reads the whole counter, so mission-20261009-100 yields
        # mission-20261009-100 and never mission-20261009-10
        # (docs/plans/HARNESS-PORTABILITY-PLAN.md, section 7, Phase 2).
        self.assertEqual(_fixture_finding("Three-digit counter link")["mission_id"], "mission-20261009-100",
                         msg="three-digit counter: -100 not linked to itself")

    def test_retry_suffix_links_to_nothing(self):
        # The candidate is mission-20261009-02-retry, which matches no known id, so
        # the value yields no link (docs/plans/HARNESS-PORTABILITY-PLAN.md, section 7,
        # Phase 2). The original mission -02 is known and is not linked.
        self.assertEqual(_fixture_finding("Retry suffix link")["mission_id"], "",
                         msg="retry suffix: -02-retry linked to a known id instead of nothing")

    def test_harness_qualified_id_links_to_itself(self):
        # The candidate is mission-20260907-anom-01, an archived id, so the value
        # yields that id (docs/plans/HARNESS-PORTABILITY-PLAN.md, section 7, Phase 2).
        self.assertEqual(_fixture_finding("Harness-qualified id link")["mission_id"], "mission-20260907-anom-01",
                         msg="harness-qualified id: anom-01 not linked to itself")

    def test_unarchived_plain_id_stays_unlinked(self):
        # The candidate mission-20261009-77 names no archived or live mission, so the
        # value yields no link (docs/plans/HARNESS-PORTABILITY-PLAN.md, section 7, Phase 2).
        self.assertEqual(_fixture_finding("Unarchived plain id link")["mission_id"], "",
                         msg="unarchived plain id: candidate naming no known mission was linked")

    def test_trailing_text_links_to_id_before_text(self):
        # Control: the candidate ends at the space, so the value yields mission-20261009-02
        # (docs/plans/HARNESS-PORTABILITY-PLAN.md, section 7, Phase 2).
        self.assertEqual(_fixture_finding("Trailing text link")["mission_id"], "mission-20261009-02",
                         msg="trailing text: id before the space not linked")


class FindingWarningTests(unittest.TestCase):
    """reconcile_findings() on the shipped fixture: candidates naming no known mission are warned and unlinked."""

    def _run(self):
        """Run reconcile_findings() with no existing findings; return (counts, stderr text, payloads by target)."""
        client = MockClient(findings=[])
        stderr = io.StringIO()
        with redirect_stderr(stderr):
            counts = reconcile.reconcile_findings(client, FIXTURE_ROOT, known_missions=_fixture_known_missions())
        payloads = {w["payload"]["target"]: w["payload"] for w in client.written if w["path"] == "/v1/findings"}
        return counts, stderr.getvalue(), payloads

    def test_unknown_candidates_are_warned(self):
        # Fixture entries whose candidate matches no known mission are each counted
        # and printed as a WARN line (docs/plans/HARNESS-PORTABILITY-PLAN.md, section 7,
        # Phase 2). The entries are in testdata/memory/findings/FINDINGS.md.
        counts, stderr, _ = self._run()
        self.assertEqual(counts["warned"], 2,
                         msg=f"unknown candidates: expected 2 warned findings, got counts {counts}")
        self.assertIn("[WARN] 2026-10-09 Retry suffix link: Mission mission-20261009-02-retry names no known mission",
                      stderr, msg="retry suffix: no WARN line names the value")
        self.assertIn("[WARN] 2026-10-09 Unarchived plain id link: Mission mission-20261009-77 names no known mission",
                      stderr, msg="unarchived plain id: no WARN line names the value")

    def test_known_candidate_is_linked_not_warned(self):
        # Positive control: a candidate that matches a known id is linked and
        # produces no WARN line, so the warning is not printed for every entry.
        _, stderr, payloads = self._run()
        self.assertEqual(payloads["Harness-qualified id link"].get("mission_id"), "mission-20260907-anom-01",
                         msg="harness-qualified id: known id not carried in the create payload")
        self.assertNotIn("Harness-qualified id link", stderr,
                         msg="harness-qualified id: a known id produced a WARN line")

    def test_unknown_candidate_is_not_in_payload(self):
        # The create payload carries no mission_id for an entry whose candidate is unknown.
        _, _, payloads = self._run()
        self.assertNotIn("mission_id", payloads["Retry suffix link"],
                         msg="retry suffix: create payload carries a mission_id for an unknown candidate")
        self.assertNotIn("mission_id", payloads["Unarchived plain id link"],
                         msg="unarchived plain id: create payload carries a mission_id for an unknown candidate")

    def test_existing_unknown_candidate_is_not_warned(self):
        # Both unknown-candidate entries already exist at the service, so neither is
        # sent again. Their WARN lines and the "warned" count are for entries this run
        # creates, so neither is warned. The existing rows reuse the keys of a first run.
        _, _, first = self._run()
        existing = [{"id": f"f-{t}", "status": "proposed", "finding_date": first[t]["finding_date"],
                     "target": t, "suggestion": first[t]["suggestion"]}
                    for t in ("Retry suffix link", "Unarchived plain id link")]
        client = MockClient(findings=existing)
        stderr = io.StringIO()
        with redirect_stderr(stderr):
            counts = reconcile.reconcile_findings(client, FIXTURE_ROOT, known_missions=_fixture_known_missions())
        # The WARN line is asserted first, so a failure quotes the entry it names.
        self.assertNotIn("names no known mission", stderr.getvalue(),
                         msg="existing unknown candidates: a WARN line was printed for an entry already at the service")
        self.assertEqual(counts["warned"], 0,
                         msg=f"existing unknown candidates: entries already at the service were counted as warned: {counts}")


class StepStatusTests(unittest.TestCase):
    """
    parse_steps() on the shipped fixture: the status each PROGRESS.md row keeps.

    Calls parse_steps() directly on testdata/memory. The fixture's mission
    folder is the declared one, so this reads the same file the golden reads.
    """

    def _steps(self):
        return {s["step"]: s for s in reconcile.parse_steps(FIXTURE_ROOT, FIXTURE_MISSION)}

    def test_known_step_status_is_kept(self):
        # Control: a status inside the step set is carried through unchanged.
        self.assertEqual(self._steps()["1"]["status"], "done",
                         msg="known status: step 1 status 'done' not carried")

    def test_unknown_step_status_is_kept_raw_and_not_sent(self):
        # Step 3 carries "blocked", which is outside the step set. The row is kept,
        # its status is "" so the payload omits it, and the value as written is kept
        # in _status_raw for the warning (docs/plans/HARNESS-PORTABILITY-PLAN.md,
        # section 7, Phase 2).
        steps = self._steps()
        self.assertIn("3", steps, msg="unknown status: step 3 row was dropped")
        self.assertEqual(steps["3"]["status"], "",
                         msg="unknown status: step 3 status is not blank, so an invalid value would be sent")
        self.assertEqual(steps["3"]["_status_raw"], "blocked",
                         msg="unknown status: the value as written was not preserved for the warning")


class StepWarningTests(unittest.TestCase):
    """reconcile_steps() on the shipped fixture: an unknown status is warned, counted and not sent."""

    def _run(self):
        """Run reconcile_steps() with no existing steps; return (counts, stderr text, payloads by label)."""
        client = MockClient()
        stderr = io.StringIO()
        with redirect_stderr(stderr):
            counts = reconcile.reconcile_steps(client, [{"id": FIXTURE_MISSION}], FIXTURE_ROOT)
        payloads = {w["label"]: w["payload"] for w in client.written if w["method"] == "POST"}
        return counts, stderr.getvalue(), payloads

    def test_unknown_status_is_warned_and_counted(self):
        # The fixture's status outside the step set (step 3) is counted as a warning.
        counts, stderr, _ = self._run()
        self.assertEqual(counts["warned"], 1,
                         msg=f"unknown status: expected 1 warned step, got counts {counts}")
        self.assertIn("[WARN] mission-20260904-01 step 3: status 'blocked' is not a step status",
                      stderr, msg="unknown status: no WARN line names step 3 and the value 'blocked'")

    def test_unknown_status_is_not_in_payload(self):
        # Step 3's payload has no status key. Step 1's known status is still sent.
        _, _, payloads = self._run()
        self.assertNotIn("status", payloads["mission-20260904-01 step 3"],
                         msg="unknown status: step 3 payload carries a status")
        self.assertEqual(payloads["mission-20260904-01 step 1"].get("status"), "done",
                         msg="unknown status: known status of step 1 not sent")

    def test_skip_steps_warns_and_counts_nothing(self):
        # Under skip_steps no step is sent, so the unknown status is not warned: no
        # WARN line, no count, no request. The parsed count still reports the three rows read.
        client = MockClient()
        stderr = io.StringIO()
        with redirect_stderr(stderr):
            counts = reconcile.reconcile_steps(client, [{"id": FIXTURE_MISSION}], FIXTURE_ROOT, skip_steps=True)
        self.assertEqual(counts["warned"], 0,
                         msg=f"skip steps: a step was counted as warned under --skip-steps: {counts}")
        self.assertNotIn("[WARN]", stderr.getvalue(),
                         msg="skip steps: a WARN line was printed under --skip-steps")
        self.assertEqual(counts["parsed"], 3, msg=f"skip steps: parsed count is not the three rows read: {counts}")
        self.assertEqual(client.written, [], msg="skip steps: a write was sent under --skip-steps")


class JournalClient(MockClient):
    """Mock client whose flight-recorder GET returns a fixed number of rows for each mission."""

    def __init__(self, rows_by_mission=None, fail=False):
        super().__init__()
        self.rows_by_mission = rows_by_mission or {}
        self.fail = fail

    def get(self, path):
        if path.startswith("/v1/flight-recorder"):
            if self.fail:
                raise OSError("simulated network failure")
            mission = urllib.parse.parse_qs(urllib.parse.urlsplit(path).query)["mission_id"][0]
            count = self.rows_by_mission.get(mission, 0)
            rows = [{"mission_id": mission, "occurred_at": f"row {i}", "step": "", "event": "e", "note": "n"}
                    for i in range(count)]
            return 200, {"flight_recorder": rows}
        return super().get(path)


class JournalCapTests(unittest.TestCase):
    """reconcile_journal() on the shipped fixture: a mission at the 100-row cap is skipped and counted apart."""

    def _run(self, client):
        """Run reconcile_journal() for the fixture's one mission; return (counts, stderr text)."""
        stderr = io.StringIO()
        with redirect_stderr(stderr):
            counts = reconcile.reconcile_journal(client, [{"id": FIXTURE_MISSION}], FIXTURE_ROOT)
        return counts, stderr.getvalue()

    def test_mission_at_cap_is_skipped_as_journal_cap(self):
        # The fixture's events for the mission are in testdata/memory/state/FLIGHT-RECORDER.md.
        # At 100 rows the mission is
        # capped: its events are counted as skipped (journal cap), its mission is
        # counted once as warned, and neither counts as a fetch failure or a failure.
        counts, stderr = self._run(JournalClient({FIXTURE_MISSION: 100}))
        self.assertEqual(counts["skipped_journal_cap"], 3,
                         msg=f"journal cap: expected 3 events skipped at the cap, got counts {counts}")
        self.assertEqual(counts["warned"], 1, msg=f"journal cap: expected 1 warned mission, got counts {counts}")
        self.assertEqual(counts["fetch_failed"], 0, msg=f"journal cap: cap counted as a fetch failure: {counts}")
        self.assertEqual(counts["failed"], 0, msg=f"journal cap: cap counted as a failed write: {counts}")
        self.assertEqual(counts["created"], 0, msg=f"journal cap: an event was written past the cap: {counts}")
        self.assertIn("[WARN] mission-20260904-01: journal at 100-row cap", stderr,
                      msg="journal cap: no WARN line names the mission")

    def test_mission_below_cap_is_reconciled(self):
        # Positive control: at 99 rows the mission is not capped, so its
        # events are written and none is counted as skipped for the cap.
        counts, stderr = self._run(JournalClient({FIXTURE_MISSION: 99}))
        self.assertEqual(counts["skipped_journal_cap"], 0,
                         msg=f"below cap: 99 rows counted as the journal cap: {counts}")
        self.assertEqual(counts["created"], 3, msg=f"below cap: expected 3 events written, got counts {counts}")
        self.assertEqual(counts["warned"], 0, msg=f"below cap: a warning was counted: {counts}")
        self.assertNotIn("[WARN]", stderr, msg="below cap: a WARN line was printed")

    def test_fetch_failure_is_not_journal_cap(self):
        # A failed GET is a fetch failure: its events are skipped_fetch_failed, and
        # skipped_journal_cap stays at zero.
        counts, _ = self._run(JournalClient(fail=True))
        self.assertEqual(counts["fetch_failed"], 1, msg=f"fetch failure: expected 1 fetch failure: {counts}")
        self.assertEqual(counts["skipped_fetch_failed"], 3,
                         msg=f"fetch failure: expected 3 events skipped for the fetch failure: {counts}")
        self.assertEqual(counts["skipped_journal_cap"], 0,
                         msg=f"fetch failure: the failed GET was counted as the journal cap: {counts}")

    def test_capped_mission_without_local_events_is_warned_not_skipped(self):
        # mission-20261009-02 has no events in the fixture's journal. At the cap it is
        # warned once, and no event is skipped for the cap, so it does not set the exit status.
        counts, stderr = self._run_for("mission-20261009-02", JournalClient({"mission-20261009-02": 100}))
        self.assertEqual(counts["warned"], 1,
                         msg=f"capped without local events: expected 1 warned mission, got counts {counts}")
        self.assertEqual(counts["skipped_journal_cap"], 0,
                         msg=f"capped without local events: a journal cap skip was counted with no events: {counts}")
        self.assertEqual(counts["fetch_failed"], 0,
                         msg=f"capped without local events: the cap was counted as a fetch failure: {counts}")
        self.assertIn("[WARN] mission-20261009-02: journal at 100-row cap", stderr,
                      msg="capped without local events: no WARN line names the mission")

    def _run_for(self, mission_id, client):
        """Run reconcile_journal() for one mission of the fixture; return (counts, stderr text)."""
        stderr = io.StringIO()
        with redirect_stderr(stderr):
            counts = reconcile.reconcile_journal(client, [{"id": mission_id}], FIXTURE_ROOT)
        return counts, stderr.getvalue()


class _ExitFakeHandler(http.server.BaseHTTPRequestHandler):
    """
    Answers each GET with an empty body, except a flight-recorder GET, which gets the rows the server names.
    When the server's paged attribute is set, a flight-recorder GET answers as a paging server does:
    the page at offset, with has_more. Otherwise it answers as an older server does, with every row and no has_more.
    """

    def do_GET(self):
        url = urllib.parse.urlsplit(self.path)
        if url.path.startswith("/v1/missions/") and url.path.endswith("/steps"):
            # A server's failing_steps names the missions whose existing-steps GET answers 500.
            mission = urllib.parse.unquote(url.path[len("/v1/missions/"):-len("/steps")])
            if mission in getattr(self.server, "failing_steps", ()):
                self.send_error(500, "simulated steps failure")
                return
        if url.path == "/v1/flight-recorder":
            query = urllib.parse.parse_qs(url.query)
            mission = query["mission_id"][0]
            # A server's failing_journal names the missions whose flight-recorder GET answers 500.
            if mission in getattr(self.server, "failing_journal", ()):
                self.send_error(500, "simulated journal failure")
                return
            rows = [{"mission_id": mission, "occurred_at": f"row {i}", "step": "", "event": "e", "note": "n"}
                    for i in range(self.server.capped.get(mission, 0))]
            if getattr(self.server, "paged", False):
                offset = int(query.get("offset", ["0"])[0])
                page = rows[offset:offset + min(int(query["limit"][0]), 100)]
                body = {"flight_recorder": page, "has_more": offset + len(page) < len(rows)}
            else:
                body = {"flight_recorder": rows}
        else:
            body = {}
        data = json.dumps(body).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *args):
        # Keep the test output clean.
        pass


def _run_main(capped, paged=False, failing_steps=(), root=None, failing_journal=()):
    """
    Run the reconciler as a subprocess, dry-run with --include-journal, against a fake
    service. capped maps a mission id to the number of journal rows the fake returns for it.
    paged makes the fake page its journal GETs. failing_steps names the missions whose
    existing-steps GET answers 500. failing_journal names the missions whose flight-recorder
    GET answers 500. root is the memory tree (default: the shipped fixture).
    Return the CompletedProcess.
    """
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), _ExitFakeHandler)
    server.capped = capped
    server.paged = paged
    server.failing_steps = set(failing_steps)
    server.failing_journal = set(failing_journal)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        # Minimal environment, as in test_reconcile_golden.py: no client.env on this machine can change the run.
        env = {
            "PATH": os.environ.get("PATH", ""),
            "BISHOP_MEMORY_CLIENT_ENV": os.path.join(tempfile.gettempdir(), "bishop-memory-no-such-client.env"),
        }
        cmd = [sys.executable, "-I", RECONCILER, "--root", root or FIXTURE_ROOT,
               "--url", f"http://127.0.0.1:{server.server_address[1]}",
               "--harness", "fixture", "--dry-run", "--include-journal"]
        return subprocess.run(cmd, env=env, capture_output=True, timeout=120)
    finally:
        server.shutdown()
        server.server_close()


def _summary_line(proc):
    """The last line of the reconciler's stdout: its [reconcile] summary line."""
    return proc.stdout.decode("utf-8").rstrip("\n").splitlines()[-1]


class JournalCapExitTests(unittest.TestCase):
    """main() exit status and summary for the journal cap, run as a subprocess against a fake service."""

    def test_cap_skips_only_exits_3_and_names_cap(self):
        # The fixture's FIXTURE_MISSION has three journal events. At 100 rows the cap skips
        # all three, and nothing else in the run is a problem: the run exits 3 and the
        # summary names the cap.
        proc = _run_main({FIXTURE_MISSION: 100})
        summary = _summary_line(proc)
        err = proc.stderr.decode("utf-8", "replace")
        self.assertEqual(proc.returncode, 3,
                         msg=f"journal cap only: exit code is {proc.returncode}, expected 3; stderr: {err}")
        self.assertIn("3 skipped (journal cap)", summary,
                      msg="journal cap only: the summary does not name 3 skipped (journal cap)")
        self.assertNotIn("fetch failed", summary,
                         msg="journal cap only: the cap is reported as a fetch failure")

    def test_no_cap_skips_exits_0(self):
        # Positive control: no mission at the cap, so no skip and no problem; the run exits 0.
        proc = _run_main({})
        self.assertEqual(proc.returncode, 0,
                         msg=f"no cap skips: exit code is {proc.returncode}, expected 0; stderr: {proc.stderr.decode('utf-8', 'replace')}")
        self.assertNotIn("journal cap", _summary_line(proc), msg="no cap skips: the summary names the journal cap")

    def test_capped_mission_without_local_events_exits_0(self):
        # mission-20261009-02 has no local events, so the cap skips none and the run exits 0.
        proc = _run_main({"mission-20261009-02": 100})
        err = proc.stderr.decode("utf-8", "replace")
        self.assertEqual(proc.returncode, 0,
                         msg=f"capped without local events: exit code is {proc.returncode}, expected 0; stderr: {err}")
        self.assertIn("[WARN] mission-20261009-02: journal at 100-row cap", err,
                      msg="capped without local events: no WARN line names the mission")
        self.assertNotIn("skipped (journal cap)", _summary_line(proc),
                         msg="capped without local events: the summary names a journal cap skip")

    def test_failure_alone_exits_1(self):
        # The fixture's FIXTURE_MISSION has an existing-steps GET that answers 500: a fetch
        # failure, and no cap. The run exits 1 and the summary names the fetch failure.
        proc = _run_main({}, failing_steps={FIXTURE_MISSION})
        summary = _summary_line(proc)
        err = proc.stderr.decode("utf-8", "replace")
        self.assertEqual(proc.returncode, 1,
                         msg=f"failure alone: exit code is {proc.returncode}, expected 1; stderr: {err}")
        self.assertIn("fetch failed", summary, msg="failure alone: the summary names no fetch failure")
        self.assertNotIn("journal cap", summary, msg="failure alone: the summary names a journal cap skip")

    def test_failure_with_cap_skip_exits_1(self):
        # Precedence: the same fetch failure as above, and the cap skips the mission's
        # three events. A failure outranks a cap skip, so the run exits 1, and the summary
        # names both the fetch failure and the cap skip.
        proc = _run_main({FIXTURE_MISSION: 100}, failing_steps={FIXTURE_MISSION})
        summary = _summary_line(proc)
        err = proc.stderr.decode("utf-8", "replace")
        self.assertEqual(proc.returncode, 1,
                         msg=f"failure with cap: exit code is {proc.returncode}, expected 1; stderr: {err}")
        self.assertIn("fetch failed", summary, msg="failure with cap: the summary names no fetch failure")
        self.assertIn("3 skipped (journal cap)", summary, msg="failure with cap: the summary does not name the cap skip")


class OutOfScopeRunTests(unittest.TestCase):
    """main() with journal events for a mission this run does not know: named as out of scope, and not a failure."""

    def test_out_of_scope_events_are_named_and_exit_0(self):
        # Positive control: the fixture's events all belong to a mission the fixture holds,
        # so its flight-recorder line names no out-of-scope event.
        fixture_line = _flight_recorder_line(_run_main({}))
        self.assertNotIn("out of scope", fixture_line,
                         msg=f"fixture: the flight-recorder line names out-of-scope events: {fixture_line}")
        # A tree with only the journal: no mission is parsed, so all three events are out of scope.
        with tempfile.TemporaryDirectory() as root:
            state_dir = os.path.join(root, reconcile.BISHOP_LAYOUT.state_dir)
            os.makedirs(state_dir)
            shutil.copy(os.path.join(FIXTURE_ROOT, reconcile.BISHOP_LAYOUT.state_dir, reconcile.BISHOP_LAYOUT.flight_recorder),
                        state_dir)
            proc = _run_main({}, root=root)
        err = proc.stderr.decode("utf-8", "replace")
        line = _flight_recorder_line(proc)
        self.assertEqual(proc.returncode, 0,
                         msg=f"out of scope only: exit code is {proc.returncode}, expected 0; stderr: {err}")
        self.assertIn("3 parsed", line, msg=f"out of scope only: the line does not read 3 parsed: {line}")
        self.assertIn("3 out of scope", line, msg=f"out of scope only: the line does not name 3 out of scope: {line}")


class _RefusingWrites(JournalClient):
    """A journal client whose writes are refused, as a service answering 500 would refuse them."""

    def write(self, method, path, payload, label):
        return False


class _StepsFetchFailClient(MockClient):
    """A mock client whose existing-steps GET raises, as a network failure would."""

    def get(self, path):
        if path.endswith("/steps"):
            raise OSError("simulated network failure")
        return super().get(path)


def _journal_output(client, missions):
    """Run reconcile_journal() against the fixture journal; return (counts, stdout text, stderr text)."""
    out, err = io.StringIO(), io.StringIO()
    with redirect_stdout(out), redirect_stderr(err):
        counts = reconcile.reconcile_journal(client, missions, FIXTURE_ROOT)
    return counts, out.getvalue(), err.getvalue()


def _flight_recorder_line(proc):
    """The [flight-recorder] summary line of the reconciler's stdout, or '' when there is none."""
    lines = [line for line in proc.stdout.decode("utf-8").splitlines() if line.startswith("[flight-recorder] ")]
    return lines[0] if lines else ""


class JournalBucketTests(unittest.TestCase):
    """reconcile_journal(): the buckets sum to parsed, and each event lands in the bucket its case names."""

    def _cases(self):
        """(case name, bucket the three events must land in, client, parsed missions)."""
        served = [_event_row(event) for event in FIXTURE_EVENTS]
        known = [{"id": FIXTURE_MISSION}]
        return [
            ("created", "created", JournalClient({FIXTURE_MISSION: 99}), known),
            ("unchanged", "skipped", PagedJournalClient(served, paged=False), known),
            ("failed", "failed", _RefusingWrites({FIXTURE_MISSION: 99}), known),
            ("journal cap", "skipped_journal_cap", JournalClient({FIXTURE_MISSION: 100}), known),
            ("fetch failed", "skipped_fetch_failed", JournalClient(fail=True), known),
            ("out of scope", "out_of_scope", JournalClient({FIXTURE_MISSION: 99}), []),
        ]

    def test_buckets_sum_to_parsed_in_every_case(self):
        # Each case is a subTest, so a failure names its case. The positive control is the
        # bucket check: the three fixture events must land in the bucket the case names.
        for name, bucket, client, missions in self._cases():
            with self.subTest(case=name):
                counts, _, _ = _journal_output(client, missions)
                self.assertEqual(counts["parsed"], 3, msg=f"{name}: fixture did not yield three events: {counts}")
                summed = sum(counts[b] for b in reconcile.JOURNAL_SUMMED_BUCKETS)
                self.assertEqual(summed, counts["parsed"],
                                 msg=f"{name}: buckets sum to {summed}, parsed is {counts['parsed']}: {counts}")
                self.assertEqual(counts[bucket], 3, msg=f"{name}: expected 3 events in {bucket}: {counts}")

    def test_every_event_count_is_summed(self):
        # A count added to reconcile_journal() but left out of JOURNAL_SUMMED_BUCKETS would
        # let the sum read as complete while missing it. warned and fetch_failed count
        # missions, not events, so they are not in the sum.
        counts, _, _ = _journal_output(JournalClient({FIXTURE_MISSION: 99}), [{"id": FIXTURE_MISSION}])
        self.assertEqual(set(counts) - {"parsed", "warned", "fetch_failed"}, set(reconcile.JOURNAL_SUMMED_BUCKETS),
                         msg=f"a counted bucket is not summed into parsed: {sorted(counts)}")


class JournalDetailLineTests(unittest.TestCase):
    """reconcile_journal(): one detail line per mission with journal events, naming that mission's counts."""

    def test_mission_with_events_has_a_detail_line(self):
        _, out, _ = _journal_output(JournalClient({FIXTURE_MISSION: 99}), [{"id": FIXTURE_MISSION}])
        self.assertIn("  [flight-recorder] mission-20260904-01: 3 parsed, 3 created, 0 unchanged, 0 failed",
                      out.splitlines(), msg=f"created: no detail line names the mission's counts: {out}")

    def test_journal_cap_is_named_on_the_detail_line(self):
        _, out, _ = _journal_output(JournalClient({FIXTURE_MISSION: 100}), [{"id": FIXTURE_MISSION}])
        self.assertIn("  [flight-recorder] mission-20260904-01: 3 parsed, 0 created, 0 unchanged, 0 failed, "
                      "3 skipped (journal cap)", out.splitlines(),
                      msg=f"journal cap: detail line does not name the cap skip: {out}")

    def test_fetch_failure_is_named_on_the_detail_line(self):
        _, out, _ = _journal_output(JournalClient(fail=True), [{"id": FIXTURE_MISSION}])
        self.assertIn("  [flight-recorder] mission-20260904-01: 3 parsed, 0 created, 0 unchanged, 0 failed, "
                      "3 skipped (fetch failed)", out.splitlines(),
                      msg=f"fetch failure: detail line does not name the fetch skip: {out}")

    def test_mission_without_events_has_no_detail_line(self):
        # Positive control in the same test: the mission with events does have its line,
        # and mission-20261009-02, which has no journal events, does not.
        _, out, _ = _journal_output(JournalClient({FIXTURE_MISSION: 99}), [{"id": FIXTURE_MISSION}])
        self.assertTrue(any(line.startswith("  [flight-recorder] mission-20260904-01:") for line in out.splitlines()),
                        msg=f"positive control: the mission with events has no detail line: {out}")
        _, out, _ = _journal_output(JournalClient({"mission-20261009-02": 100}), [{"id": "mission-20261009-02"}])
        self.assertFalse(any(line.startswith("  [flight-recorder] mission-20261009-02:") for line in out.splitlines()),
                         msg=f"a mission with no journal events has a detail line: {out}")


MIXED_KNOWN = ["mission-20261101-01", "mission-20261101-02", "mission-20261101-03"]
MIXED_UNKNOWN = "mission-20261101-04"
# (mission, event, note) rows of the mixed journal, in file order: three for the first
# known mission, two for the second, one for the third, two for a mission the run does not know.
MIXED_EVENTS = [
    (MIXED_KNOWN[0], "step-sync", "held"),
    (MIXED_KNOWN[0], "step-sync", "new"),
    (MIXED_KNOWN[0], "step-sync", "refused"),
    (MIXED_KNOWN[1], "step-sync", "capped one"),
    (MIXED_KNOWN[1], "step-sync", "capped two"),
    (MIXED_KNOWN[2], "step-sync", "fetch fails"),
    (MIXED_UNKNOWN, "step-sync", "unknown one"),
    (MIXED_UNKNOWN, "step-sync", "unknown two"),
]


def _write_mixed_tree(root, with_archive=True):
    """
    Write a memory tree into root whose journal holds MIXED_EVENTS, one row per event with a
    distinct timestamp. With_archive also writes a MISSION-ARCHIVE.md naming MIXED_KNOWN, so
    those three are the missions the run knows and MIXED_UNKNOWN is not.
    """
    state_dir = os.path.join(root, reconcile.BISHOP_LAYOUT.state_dir)
    os.makedirs(state_dir)
    rows = "".join(f"| 2026-11-01 10:{i:02d} UTC | {m} | {i} | @bishop | {e} | {n} |\n"
                   for i, (m, e, n) in enumerate(MIXED_EVENTS))
    with open(os.path.join(state_dir, reconcile.BISHOP_LAYOUT.flight_recorder), "w", encoding="utf-8") as handle:
        handle.write("# Flight Recorder\n\n| Timestamp | Mission ID | Step | Agent | Event | Note |\n"
                     "|---|---|---|---|---|---|\n" + rows)
    if with_archive:
        rows = "".join(f"| {m} | 2026-11-01 11:00 UTC | done | Mixed-run fixture. |\n" for m in MIXED_KNOWN)
        with open(os.path.join(state_dir, reconcile.BISHOP_LAYOUT.mission_archive), "w", encoding="utf-8") as handle:
            handle.write("# Mission Archive\n\n| Mission ID | Completed | Outcome | Summary |\n"
                         "|---|---|---|---|\n" + rows)


class _MixedJournalClient(MockClient):
    """
    A service for the mixed journal. The first known mission already holds its "held" event; the
    second answers a full page (the cap); the third's flight-recorder GET raises. A write whose
    note is "refused" fails. Every write attempted is kept in written.
    """

    def get(self, path):
        if path.startswith("/v1/flight-recorder"):
            mission = urllib.parse.parse_qs(urllib.parse.urlsplit(path).query)["mission_id"][0]
            if mission == MIXED_KNOWN[0]:
                return 200, {"flight_recorder": [{"mission_id": mission, "occurred_at": "2026-11-01 10:00 UTC",
                                                  "step": "0", "event": "step-sync", "note": "held"}]}
            if mission == MIXED_KNOWN[1]:
                return 200, {"flight_recorder": [{"mission_id": mission, "occurred_at": f"filler {i}", "step": "",
                                                  "event": "e", "note": "n"} for i in range(reconcile.JOURNAL_PAGE_SIZE)]}
            raise OSError("simulated network failure")
        return super().get(path)

    def write(self, method, path, payload, label):
        super().write(method, path, payload, label)
        return payload["note"] != "refused"


class JournalMixedRunTests(unittest.TestCase):
    """reconcile_journal() over a journal whose events split across every bucket and an out-of-scope mission."""

    def test_events_split_across_buckets_and_sum_to_parsed(self):
        # Control (DIR-026): the single-bucket cases in JournalBucketTests pass when a tally
        # sends an event to the wrong bucket in a mixed run. This test fails then: with the
        # capped branch's outcome set to "skipped_fetch_failed" the sum still reads 8 but the
        # bucket values and detail lines differ; with an outcome left uncounted the sum reads 7.
        # The writes are the positive control for the absent ones: two events are attempted,
        # and none for the held, capped, fetch-failed or out-of-scope events.
        with tempfile.TemporaryDirectory() as root:
            _write_mixed_tree(root)
            client = _MixedJournalClient()
            missions = [{"id": m} for m in MIXED_KNOWN]
            out, err = io.StringIO(), io.StringIO()
            with redirect_stdout(out), redirect_stderr(err):
                counts = reconcile.reconcile_journal(client, missions, root)
        self.assertEqual(counts["parsed"], 8, msg=f"mixed run: expected 8 parsed events: {counts}")
        summed = sum(counts[b] for b in reconcile.JOURNAL_SUMMED_BUCKETS)
        self.assertEqual(summed, counts["parsed"], msg=f"mixed run: buckets sum to {summed}, parsed is {counts['parsed']}: {counts}")
        self.assertEqual({b: counts[b] for b in reconcile.JOURNAL_SUMMED_BUCKETS},
                         {"created": 1, "skipped": 1, "failed": 1, "skipped_fetch_failed": 1,
                          "skipped_journal_cap": 2, "out_of_scope": 2},
                         msg=f"mixed run: events are in the wrong buckets: {counts}")
        self.assertEqual([w["payload"]["note"] for w in client.written], ["new", "refused"],
                         msg=f"mixed run: wrong events were written: {client.written}")
        self.assertEqual(out.getvalue().splitlines(), [
            f"  [flight-recorder] {MIXED_KNOWN[0]}: 3 parsed, 1 created, 1 unchanged, 1 failed",
            f"  [flight-recorder] {MIXED_KNOWN[1]}: 2 parsed, 0 created, 0 unchanged, 0 failed, 2 skipped (journal cap)",
            f"  [flight-recorder] {MIXED_KNOWN[2]}: 1 parsed, 0 created, 0 unchanged, 0 failed, 1 skipped (fetch failed)",
        ], msg=f"mixed run: per-mission lines are wrong, or one names the out-of-scope mission: {out.getvalue()}")


def _summary_counts(line):
    """
    The counts a [flight-recorder] summary line prints, by name. A term the line does not
    print is 0. "skipped" is the count the fetch note names, as in "(1 skipped — fetch failed ...)".
    """
    def count(pattern):
        match = re.search(pattern, line)
        return int(match.group(1)) if match else 0
    return {
        "parsed": count(r"\] (\d+) parsed"), "created": count(r"(\d+) created"),
        "unchanged": count(r"(\d+) unchanged"), "failed": count(r"(\d+) failed"),
        "cap": count(r"(\d+) skipped \(journal cap\)"), "out_of_scope": count(r"(\d+) out of scope"),
        "fetch_skipped": count(r"\((\d+) skipped \u2014 fetch failed"),
    }


class JournalSummaryLineTests(unittest.TestCase):
    """main(): the printed [flight-recorder] line's counts, with the fetch note's skipped count, sum to parsed."""

    def test_summary_line_counts_sum_to_parsed(self):
        # Control (DIR-026): with main() passing no skipped count to fetch_failure_note, the
        # fetch note reads "(fetch failed — 1 request(s) ...)", fetch_skipped is 0 and the
        # counts sum to 7, not 8. With the scope note dropped they sum to 6. The line is a
        # subprocess's stdout (main(), not reconcile_journal() called directly). The run
        # splits events across created, the cap, a journal fetch failure and out of scope.
        # Blind spot: both runs are dry runs and a dry run never fails a write, so "failed"
        # is 0 here; JournalMixedRunTests covers the failed bucket.
        with tempfile.TemporaryDirectory() as root:
            _write_mixed_tree(root)
            capped = _run_main({MIXED_KNOWN[1]: 100}, root=root, failing_journal={MIXED_KNOWN[2]})
            # The control runs inside the with block, while root exists, so main() reads the journal.
            control = _run_main({}, root=root)
        line = _flight_recorder_line(capped)
        self.assertNotEqual(line, "", msg=f"summary: no [flight-recorder] line: {capped.stdout.decode('utf-8', 'replace')}")
        counts = _summary_counts(line)
        self.assertEqual(counts, {"parsed": 8, "created": 3, "unchanged": 0, "failed": 0, "cap": 2,
                                  "out_of_scope": 2, "fetch_skipped": 1},
                         msg=f"summary: the line's counts are wrong: {line}")
        total = sum(v for k, v in counts.items() if k != "parsed")
        self.assertEqual(total, counts["parsed"], msg=f"summary: counts sum to {total}, parsed is {counts['parsed']}: {line}")
        # Positive control for the fetch note: the same tree with no journal fetch failure has no
        # fetch note, and its counts still sum to parsed. An empty line (a missing root, a run
        # that exits before printing) fails the non-empty assertion, so it cannot read as zeros.
        control_line = _flight_recorder_line(control)
        self.assertNotEqual(control_line, "", msg=f"summary control: no [flight-recorder] line: "
                            f"{control.stdout.decode('utf-8', 'replace')} {control.stderr.decode('utf-8', 'replace')}")
        clean = _summary_counts(control_line)
        self.assertEqual(clean, {"parsed": 8, "created": 6, "unchanged": 0, "failed": 0, "cap": 0,
                                 "out_of_scope": 2, "fetch_skipped": 0},
                         msg=f"summary control: the clean line's counts are wrong: {control_line}")
        # The regex reads "(0 skipped — fetch failed ...)" as fetch_skipped 0, so the control
        # also asserts the clean line carries no fetch note at all.
        self.assertNotIn("fetch failed", control_line,
                         msg=f"summary control: a fetch note on a line with no fetch failure: {control_line}")
        self.assertEqual(sum(v for k, v in clean.items() if k != "parsed"), clean["parsed"],
                         msg=f"summary control: counts do not sum to parsed: {clean}")


class StepFetchWarningTests(unittest.TestCase):
    """reconcile_steps(): an unknown status is warned only once the mission's existing-steps fetch has succeeded."""

    def _run(self, client):
        """Run reconcile_steps() for the fixture's mission; return (counts, stderr text)."""
        stderr = io.StringIO()
        with redirect_stderr(stderr):
            counts = reconcile.reconcile_steps(client, [{"id": FIXTURE_MISSION}], FIXTURE_ROOT)
        return counts, stderr.getvalue()

    def test_fetch_failure_is_not_warned_and_success_is(self):
        # Absent case: the fetch fails, so the fetch failure is counted, and step 3 gets no
        # WARN line and no warned count. Positive control, same method: with a fetch that
        # succeeds, the same step 3 is warned.
        counts, stderr = self._run(_StepsFetchFailClient())
        self.assertEqual(counts["fetch_failed"], 1, msg=f"fetch failure: expected 1 fetch failure: {counts}")
        self.assertEqual(counts["skipped_fetch_failed"], 3,
                         msg=f"fetch failure: expected 3 steps skipped for the fetch failure: {counts}")
        self.assertEqual(counts["warned"], 0, msg=f"fetch failure: a step was warned after the fetch failed: {counts}")
        self.assertNotIn("[WARN]", stderr, msg="fetch failure: a WARN line was printed for a step that was not sent")
        counts, stderr = self._run(MockClient())
        self.assertEqual(counts["warned"], 1, msg=f"successful fetch: expected 1 warned step, got counts {counts}")
        self.assertIn("[WARN] mission-20260904-01 step 3: status 'blocked' is not a step status", stderr,
                      msg="successful fetch: no WARN line names step 3")


def _filler_row(i):
    """A journal row for FIXTURE_MISSION that matches no parsed event."""
    return {"mission_id": FIXTURE_MISSION, "occurred_at": f"filler {i}", "step": "", "event": "e", "note": "n"}


def _event_row(event):
    """The service's copy of a parsed event: the five fields of the natural key, exactly as parsed."""
    return {k: event[k] for k in ("mission_id", "occurred_at", "step", "event", "note")}


def _journal_rows(total, events_at):
    """
    total journal rows for FIXTURE_MISSION. events_at maps a row index to a parsed event
    the service holds at that index; every other index is a filler row.
    """
    rows = [_filler_row(i) for i in range(total)]
    for index, event in events_at.items():
        rows[index] = _event_row(event)
    return rows


class PagedJournalClient(MockClient):
    """
    Mock service for the flight-recorder GET. paged=True answers as a paging server does:
    rows[offset:offset+limit] with limit capped at JOURNAL_PAGE_SIZE, and has_more. paged=False
    answers as an older server does: the first rows up to limit, offset ignored, no has_more.
    fail_offset names an offset whose GET raises instead of answering. Every journal GET path
    is kept in requests.
    """

    def __init__(self, rows, paged=True, fail_offset=None):
        super().__init__()
        self.rows = rows
        self.paged = paged
        self.fail_offset = fail_offset
        self.requests = []

    def get(self, path):
        if not path.startswith("/v1/flight-recorder"):
            return super().get(path)
        self.requests.append(path)
        query = urllib.parse.parse_qs(urllib.parse.urlsplit(path).query)
        limit = min(int(query["limit"][0]), reconcile.JOURNAL_PAGE_SIZE)
        if not self.paged:
            return 200, {"flight_recorder": self.rows[:limit]}
        offset = int(query["offset"][0])
        if offset == self.fail_offset:
            raise OSError(f"simulated network failure at offset {offset}")
        page = self.rows[offset:offset + limit]
        return 200, {"flight_recorder": page, "has_more": offset + len(page) < len(self.rows)}


class _EmptyPageClient(PagedJournalClient):
    """A paging server whose first GETs report has_more on an empty page. Stops after three GETs."""

    def get(self, path):
        if not path.startswith("/v1/flight-recorder"):
            return super().get(path)
        self.requests.append(path)
        return 200, {"flight_recorder": [], "has_more": len(self.requests) <= 3}


FIXTURE_EVENTS = [event for event in reconcile.parse_journal(FIXTURE_ROOT) if event["mission_id"] == FIXTURE_MISSION]


def _journal_run(client):
    """Run reconcile_journal() for FIXTURE_MISSION against client; return (counts, stderr text)."""
    stderr = io.StringIO()
    with redirect_stderr(stderr):
        counts = reconcile.reconcile_journal(client, [{"id": FIXTURE_MISSION}], FIXTURE_ROOT)
    return counts, stderr.getvalue()


class PagedJournalTests(unittest.TestCase):
    """reconcile_journal() against a paging server, and against a server that does not page."""

    @classmethod
    def setUpClass(cls):
        # Positive control: the fixture has the three events every case below looks for.
        if len(FIXTURE_EVENTS) != 3:
            raise AssertionError(f"fixture precondition: expected 3 events for {FIXTURE_MISSION}, got {len(FIXTURE_EVENTS)}")

    def test_paged_server_reads_every_page_and_is_not_capped(self):
        # 250 rows over the page size. The events sit at indexes 150, 151 and 240, past
        # the first page. All three are found only if the later pages are read.
        rows = _journal_rows(250, {150: FIXTURE_EVENTS[0], 151: FIXTURE_EVENTS[1], 240: FIXTURE_EVENTS[2]})
        client = PagedJournalClient(rows)
        counts, stderr = _journal_run(client)
        self.assertEqual(counts["skipped"], 3,
                         msg=f"paged 250 rows: events on later pages not all found as unchanged: {counts}")
        self.assertEqual(counts["created"], 0,
                         msg=f"paged 250 rows: an event on a later page was written as missing: {counts}")
        self.assertEqual(counts["skipped_journal_cap"], 0,
                         msg=f"paged 250 rows: a paging server was counted as the journal cap: {counts}")
        self.assertEqual(counts["warned"], 0, msg=f"paged 250 rows: a warning was counted: {counts}")
        self.assertNotIn("[WARN]", stderr, msg="paged 250 rows: a WARN line was printed")
        self.assertEqual([r.split("offset=")[1] for r in client.requests], ["0", "100", "200"],
                         msg=f"paged 250 rows: pages requested were {client.requests}")

    def test_paged_server_exact_page_is_not_capped(self):
        # Exactly one full page, nothing after it. has_more is false, so the page is
        # complete: the three events are found and nothing is capped.
        rows = _journal_rows(100, {0: FIXTURE_EVENTS[0], 1: FIXTURE_EVENTS[1], 2: FIXTURE_EVENTS[2]})
        client = PagedJournalClient(rows)
        counts, stderr = _journal_run(client)
        self.assertEqual(counts["warned"], 0,
                         msg=f"paged exact page: a full page with has_more false was warned as the cap: {counts}")
        self.assertEqual(counts["skipped"], 3, msg=f"paged exact page: expected 3 events found as unchanged: {counts}")
        self.assertEqual(counts["skipped_journal_cap"], 0,
                         msg=f"paged exact page: a full page was counted as the journal cap: {counts}")
        self.assertEqual(len(client.requests), 1,
                         msg=f"paged exact page: a request past the end was sent: {client.requests}")

    def test_paged_server_exact_two_pages_stops_at_has_more(self):
        # Two full pages with has_more false on the second. The reader stops there: no
        # third GET is sent, and the events at the start, middle and end are all found.
        rows = _journal_rows(200, {0: FIXTURE_EVENTS[0], 100: FIXTURE_EVENTS[1], 199: FIXTURE_EVENTS[2]})
        client = PagedJournalClient(rows)
        counts, _ = _journal_run(client)
        self.assertEqual(counts["skipped"], 3, msg=f"paged two pages: expected 3 events found as unchanged: {counts}")
        self.assertEqual(counts["skipped_journal_cap"], 0,
                         msg=f"paged two pages: a full second page was counted as the journal cap: {counts}")
        self.assertEqual([r.split("offset=")[1] for r in client.requests], ["0", "100"],
                         msg=f"paged two pages: pages requested were {client.requests}")

    def test_failing_page_is_fetch_failure_not_partial_pass(self):
        # Page one answers and holds the three events; the GET at offset 100 fails. The
        # mission must be a fetch failure with its events skipped for it, not a pass on page one.
        rows = _journal_rows(250, {0: FIXTURE_EVENTS[0], 1: FIXTURE_EVENTS[1], 2: FIXTURE_EVENTS[2]})
        client = PagedJournalClient(rows, fail_offset=100)
        counts, stderr = _journal_run(client)
        self.assertEqual(counts["fetch_failed"], 1,
                         msg=f"failing page: expected 1 fetch failure for the mission: {counts}")
        self.assertEqual(counts["skipped_fetch_failed"], 3,
                         msg=f"failing page: expected 3 events skipped for the fetch failure: {counts}")
        self.assertEqual(counts["skipped"], 0,
                         msg=f"failing page: events were passed on page one alone: {counts}")
        self.assertEqual(counts["created"], 0, msg=f"failing page: an event was written: {counts}")
        self.assertEqual(counts["skipped_journal_cap"], 0,
                         msg=f"failing page: the failed page was counted as the journal cap: {counts}")
        self.assertIn("could not fetch existing", stderr, msg="failing page: no fetch failure line was printed")
        self.assertIn("simulated network failure at offset 100", stderr,
                      msg="failing page: the failure text names the page that failed")

    def test_has_more_on_empty_page_is_fetch_failure(self):
        # has_more with no rows would never advance the offset. The reader must stop with a
        # fetch failure after the first such page, not send another GET.
        client = _EmptyPageClient([])
        counts, stderr = _journal_run(client)
        self.assertEqual(counts["fetch_failed"], 1,
                         msg=f"empty page with has_more: expected 1 fetch failure: {counts}")
        self.assertEqual(len(client.requests), 1,
                         msg=f"empty page with has_more: {len(client.requests)} GETs sent, expected 1: {client.requests}")
        self.assertIn("has_more is set on an empty page at offset 0", stderr,
                      msg="empty page with has_more: the failure text does not name the page")

    def test_server_that_does_not_page_keeps_the_cap(self):
        # An older server ignores offset and answers one page with no has_more. At
        # 250 rows it sends only its first page: the mission is capped, one GET is sent.
        rows = _journal_rows(250, {0: FIXTURE_EVENTS[0], 1: FIXTURE_EVENTS[1], 2: FIXTURE_EVENTS[2]})
        client = PagedJournalClient(rows, paged=False)
        counts, stderr = _journal_run(client)
        self.assertEqual(counts["warned"], 1, msg=f"old server at 250 rows: expected 1 warned mission: {counts}")
        self.assertEqual(counts["skipped_journal_cap"], 3,
                         msg=f"old server at 250 rows: expected 3 events skipped at the cap: {counts}")
        self.assertEqual(len(client.requests), 1,
                         msg=f"old server at 250 rows: {len(client.requests)} GETs sent, expected 1: {client.requests}")
        self.assertIn("[WARN] mission-20260904-01: journal at 100-row cap", stderr,
                      msg="old server at 250 rows: no WARN line names the mission")

    def test_row_repeated_across_a_page_boundary_is_not_created_again(self):
        # The service holds event 0 at index 99, the last row of page one, and again at
        # index 100, the first row of page two: a row that moved down one place between
        # the two GETs. The repeat is the same natural key, so the event is found once
        # and nothing is written for it.
        events_at = {5: FIXTURE_EVENTS[1], 99: FIXTURE_EVENTS[0], 100: FIXTURE_EVENTS[0], 101: FIXTURE_EVENTS[2]}
        rows = _journal_rows(102, events_at)
        if not (99 < reconcile.JOURNAL_PAGE_SIZE <= 100 and rows[99] == rows[100]):
            raise AssertionError("test precondition: the repeated row must straddle the page boundary")
        client = PagedJournalClient(rows)
        counts, _ = _journal_run(client)
        self.assertEqual(counts["created"], 0,
                         msg=f"repeat across pages: an event the service holds twice was written again: {counts}")
        self.assertEqual(counts["skipped"], 3,
                         msg=f"repeat across pages: expected 3 events found as unchanged: {counts}")
        self.assertEqual([r.split("offset=")[1] for r in client.requests], ["0", "100"],
                         msg=f"repeat across pages: pages requested were {client.requests}")

    def test_row_differing_only_in_note_is_not_the_event(self):
        # The opposite edge of the repeat: the service holds a row that matches event 2 in
        # every natural-key field but its note. That is another event, so event 2 is
        # written, and the two events with an exact row are not.
        near = dict(_event_row(FIXTURE_EVENTS[2]), note=FIXTURE_EVENTS[2]["note"] + " (edited)")
        rows = _journal_rows(10, {0: FIXTURE_EVENTS[0], 1: FIXTURE_EVENTS[1]})
        rows[2] = near
        counts, _ = _journal_run(PagedJournalClient(rows))
        self.assertEqual(counts["created"], 1,
                         msg=f"note differs: expected 1 event written for the row that differs in note: {counts}")
        self.assertEqual(counts["skipped"], 2,
                         msg=f"note differs: expected the 2 exact rows found as unchanged: {counts}")

    def test_read_of_exactly_the_page_ceiling_is_complete(self):
        # With the ceiling at three pages, 300 rows are exactly three pages and the
        # third reports has_more false: the read completes and finds every event.
        rows = _journal_rows(300, {0: FIXTURE_EVENTS[0], 150: FIXTURE_EVENTS[1], 299: FIXTURE_EVENTS[2]})
        client = PagedJournalClient(rows)
        with mock.patch.object(reconcile, "JOURNAL_MAX_PAGES", 3):
            counts, _ = _journal_run(client)
        self.assertEqual(counts["fetch_failed"], 0,
                         msg=f"ceiling exactly met: a read that ends at the ceiling was a fetch failure: {counts}")
        self.assertEqual(counts["skipped"], 3,
                         msg=f"ceiling exactly met: expected 3 events found as unchanged: {counts}")
        self.assertEqual([r.split("offset=")[1] for r in client.requests], ["0", "100", "200"],
                         msg=f"ceiling exactly met: pages requested were {client.requests}")

    def test_read_past_the_page_ceiling_is_fetch_failure(self):
        # With the ceiling at three pages, 301 rows still report has_more after the third
        # page. The read stops there with a fetch failure and sends no fourth GET.
        rows = _journal_rows(301, {0: FIXTURE_EVENTS[0], 1: FIXTURE_EVENTS[1], 2: FIXTURE_EVENTS[2]})
        client = PagedJournalClient(rows)
        with mock.patch.object(reconcile, "JOURNAL_MAX_PAGES", 3):
            counts, stderr = _journal_run(client)
        self.assertEqual(counts["fetch_failed"], 1,
                         msg=f"ceiling exceeded: expected 1 fetch failure for the mission: {counts}")
        self.assertEqual(counts["skipped_fetch_failed"], 3,
                         msg=f"ceiling exceeded: expected 3 events skipped for the fetch failure: {counts}")
        self.assertEqual(len(client.requests), 3,
                         msg=f"ceiling exceeded: {len(client.requests)} GETs sent, expected 3: {client.requests}")
        self.assertIn("has_more is still set after 3 pages", stderr,
                      msg="ceiling exceeded: the failure text does not name the ceiling")


class PagedJournalExitTests(unittest.TestCase):
    """main() against a paging fake service over HTTP, so the real Client and the query string are exercised."""

    def test_paged_service_over_http_exits_0_without_cap(self):
        # 250 filler rows across three pages and no events held: every parsed event is
        # created in dry-run, nothing is capped, and the run exits 0.
        proc = _run_main({FIXTURE_MISSION: 250}, paged=True)
        err = proc.stderr.decode("utf-8", "replace")
        self.assertEqual(proc.returncode, 0,
                         msg=f"paged over HTTP: exit code is {proc.returncode}, expected 0; stderr: {err}")
        self.assertNotIn("journal cap", _summary_line(proc),
                         msg="paged over HTTP: the summary names the journal cap")
        self.assertNotIn("journal at", err, msg="paged over HTTP: a journal cap WARN line was printed")


if __name__ == "__main__":
    unittest.main()

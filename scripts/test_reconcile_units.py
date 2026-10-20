#!/usr/bin/env python3
"""
Unit tests for three reads in reconcile-memory.py, each on a tiny temp fixture
with a mock client, so nothing touches a real memory root or a running service.

  - parse_steps(): the step table in missions/<id>/PROGRESS.md, file present
    and file absent. Each case has its own assertion message.
  - The missions folder lookup through BISHOP_LAYOUT. The declared folder name
    is read; a name the layout does not declare is not read.
  - reconcile_findings(): an empty Approver falls back to APPROVER_DEFAULT.
  - parse_findings() and parse_steps() on the shipped fixture (testdata/memory):
    the **Mission** link each fixture finding receives, and the status of the
    fixture's step row with a value outside the step set. These pin current
    behaviour. Each case whose name ends in current_behaviour asserts what the
    reconciler does today, not what it should do; the step that fixes the case
    flips its assertion.

The step-table tests write into the folder and file names BISHOP_LAYOUT
declares, so they test the parser and not the name. The lookup tests use the
literal names, so a change to the declared value fails them.
"""
import importlib.util
import io
import os
import sys
import tempfile
import unittest
from contextlib import redirect_stderr

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
_spec = importlib.util.spec_from_file_location("reconcile_memory", os.path.join(HERE, "reconcile-memory.py"))
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
            counts = reconcile.reconcile_findings(client, self.root)
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


def _fixture_finding(target):
    """Return the parse_findings() dict whose heading target equals target, from the shipped fixture."""
    matches = [f for f in reconcile.parse_findings(FIXTURE_ROOT) if f["target"] == target]
    if len(matches) != 1:
        raise AssertionError(f"fixture finding {target!r}: expected one entry, found {len(matches)}")
    return matches[0]


class FindingMissionLinkTests(unittest.TestCase):
    """
    parse_findings() on the shipped fixture: the mission_id each **Mission** field yields.

    Calls parse_findings() directly. It does not reach reconcile_findings(), so
    the link is read from the parsed dict, the value the create payload carries.
    The expected ids are the fixture's archived missions: mission-20261009-02,
    mission-20261009-10, mission-20261009-100 and mission-20260907-anom-01.
    """

    def test_plain_id_links_to_its_mission(self):
        # Control: a plain two-digit id resolves to itself. A failure here means
        # parse_findings() no longer reads the Mission field.
        self.assertEqual(_fixture_finding("Plain mission id link")["mission_id"], "mission-20261009-10",
                         msg="plain id: Mission field mission-20261009-10 not linked to itself")

    def test_three_digit_counter_current_behaviour_links_to_ten(self):
        # Current behaviour: MISSION_ID is unanchored and stops at two digits, so
        # mission-20261009-100 is linked to mission-20261009-10. The correct link is
        # mission-20261009-100. The fix step flips this assertion.
        self.assertEqual(_fixture_finding("Three-digit counter link")["mission_id"], "mission-20261009-10",
                         msg="three-digit counter: current behaviour is a link to -10, not -100")

    def test_retry_suffix_current_behaviour_links_to_original(self):
        # Current behaviour: mission-20261009-02-retry is linked to mission-20261009-02.
        # The fix step makes the candidate match no known id, so this entry is unlinked.
        self.assertEqual(_fixture_finding("Retry suffix link")["mission_id"], "mission-20261009-02",
                         msg="retry suffix: current behaviour is a link to the original -02")

    def test_harness_qualified_id_current_behaviour_is_unlinked(self):
        # Current behaviour: mission-20260907-anom-01 yields no link, because the
        # pattern needs two digits after the date and "anom" is not digits. The fix
        # step links it to mission-20260907-anom-01.
        self.assertEqual(_fixture_finding("Harness-qualified id link")["mission_id"], "",
                         msg="harness-qualified id: current behaviour is no link")

    def test_trailing_text_links_to_id_before_text(self):
        # Control: the id ends at the space, so the link is mission-20261009-02 under
        # both the current pattern and the extraction rule.
        self.assertEqual(_fixture_finding("Trailing text link")["mission_id"], "mission-20261009-02",
                         msg="trailing text: id before the space not linked")


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

    def test_unknown_step_status_current_behaviour_is_blanked(self):
        # Current behaviour: step 3 carries "blocked", which is outside the step set,
        # so the row is kept and its status is replaced with "". The payload then
        # omits status. The fix step keeps the value and counts it as a warning.
        steps = self._steps()
        self.assertIn("3", steps, msg="unknown status: step 3 row was dropped, not kept with a blank status")
        self.assertEqual(steps["3"]["status"], "",
                         msg="unknown status: current behaviour is a blank status for step 3")


if __name__ == "__main__":
    unittest.main()

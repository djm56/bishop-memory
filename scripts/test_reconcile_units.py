#!/usr/bin/env python3
"""
Unit tests for three reads in reconcile-memory.py, each on a tiny temp fixture
with a mock client, so nothing touches a real memory root or a running service.

  - parse_steps(): the step table in missions/<id>/PROGRESS.md, file present
    and file absent. Each case has its own assertion message.
  - The missions folder lookup through BISHOP_LAYOUT. The declared folder name
    is read; a name the layout does not declare is not read.
  - reconcile_findings(): an empty Approver falls back to APPROVER_DEFAULT.

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


if __name__ == "__main__":
    unittest.main()

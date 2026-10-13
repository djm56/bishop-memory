#!/usr/bin/env python3
"""
Tests for reconcile_directives() in reconcile-memory.py.

Each test writes a DIRECTIVES.md into a temp memory root and drives the
function with a mock client, so nothing touches a real memory root or a
running service.
"""
import importlib.util
import io
import os
import sys
import tempfile
import unittest
import urllib.error
from contextlib import redirect_stderr

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
_spec = importlib.util.spec_from_file_location("reconcile_memory", os.path.join(HERE, "reconcile-memory.py"))
reconcile = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(reconcile)

SERVICE_ENTRY = """### DIR-001 — Owned Rule
- **Rule:** rule
- **Rationale:** why
- **Added:** 2026-10-13 · **Source:** bishop-memory triage · **Evidence:** [2026-10-13] — x

"""
HAND_ENTRY = """### DIR-002 — Hand-Authored Rule
- **Rule:** Hand-written rule
- **Rationale:** Hand-written rationale
- **Added:** 2026-09-01 · **Source:** mission-20260901-01 · **Evidence:** [2026-09-01] — y

"""
DIRECTIVES_HEAD = "# Project Directives\n\n## Directives\n\n"


def row(directive_id, title, rule, rationale, ratified_at):
    return {"directive_id": directive_id, "title": title, "rule": rule,
            "rationale": rationale, "ratified_at": ratified_at}


class MockClient:
    """Mock HTTP client. proposals_error, if set, is raised by the proposals GET."""

    def __init__(self, directives=(), proposals=(), proposals_error=None):
        self.directives = list(directives)
        self.proposals = list(proposals)
        self.proposals_error = proposals_error
        self.dry_run = False
        self.written = []

    def get(self, path):
        if path == "/v1/directives":
            return 200, {"directives": self.directives}
        if path == "/v1/directive-proposals":
            if self.proposals_error is not None:
                raise self.proposals_error
            return 200, {"directive_proposals": self.proposals}
        return 200, {}

    def write(self, method, path, payload, label):
        self.written.append({"method": method, "path": path, "payload": payload, "label": label})
        return True


def http_error(code):
    err = urllib.error.HTTPError("http://svc/v1/directive-proposals", code, "x", {}, io.BytesIO(b""))
    return err


class ReconcileDirectives(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        os.makedirs(os.path.join(self.tmp.name, "reference"))

    def reconcile(self, body, client):
        with open(os.path.join(self.tmp.name, "reference", "DIRECTIVES.md"), "w", encoding="utf-8") as fh:
            fh.write(DIRECTIVES_HEAD + body)
        with redirect_stderr(io.StringIO()) as err:
            result = reconcile.reconcile_directives(client, self.tmp.name)
        return result, err.getvalue()

    def test_service_owned_directive_is_skipped(self):
        client = MockClient(
            directives=[row("DIR-001", "Owned Rule", "rule", "why", "2026-10-13")],
            proposals=[{"directive_id": "DIR-001", "state": "accepted"}])
        result, _ = self.reconcile(SERVICE_ENTRY, client)
        self.assertEqual((result["owned"], result["created"], result["updated"], result["skipped"]), (1, 0, 0, 0))
        self.assertEqual(client.written, [])

    def test_owned_directive_is_skipped_even_when_the_service_row_differs(self):
        client = MockClient(
            directives=[row("DIR-001", "Owned Rule", "an older rule", "why", "2026-10-13")],
            proposals=[{"directive_id": "DIR-001", "state": "declined"}])
        result, _ = self.reconcile(SERVICE_ENTRY, client)
        self.assertEqual(result["owned"], 1)
        self.assertEqual(client.written, [])

    def test_hand_authored_directive_is_still_put(self):
        client = MockClient(directives=[row("DIR-002", "Hand-Authored Rule", "Old rule", "Old rationale", "2026-09-01")])
        result, _ = self.reconcile(HAND_ENTRY, client)
        self.assertEqual((result["updated"], result["owned"]), (1, 0))
        self.assertEqual([(w["method"], w["path"]) for w in client.written], [("PUT", "/v1/directives/DIR-002")])

    def test_owned_and_hand_authored_counted_separately(self):
        client = MockClient(proposals=[{"directive_id": "DIR-001", "state": "accepted"}])
        result, _ = self.reconcile(SERVICE_ENTRY + HAND_ENTRY, client)
        self.assertEqual((result["parsed"], result["owned"], result["created"]), (2, 1, 1))
        self.assertEqual([w["path"] for w in client.written], ["/v1/directives/DIR-002"])

    def test_unchanged_hand_authored_directive_is_skipped_not_put(self):
        client = MockClient(directives=[row("DIR-002", "Hand-Authored Rule", "Hand-written rule", "Hand-written rationale", "2026-09-01")])
        result, _ = self.reconcile(HAND_ENTRY, client)
        self.assertEqual((result["skipped"], result["updated"], result["created"]), (1, 0, 0))
        self.assertEqual(client.written, [])

    def test_proposals_404_means_no_proposals_and_reconciliation_proceeds(self):
        client = MockClient(proposals_error=http_error(404))
        result, err = self.reconcile(SERVICE_ENTRY + HAND_ENTRY, client)
        self.assertEqual(result["fetch_failed"], 0)
        self.assertEqual((result["owned"], result["created"]), (0, 2))
        self.assertEqual(len(client.written), 2)
        self.assertNotIn("directive-proposals", err)

    def test_proposals_other_http_error_skips_all_puts(self):
        for code in (500, 403):
            client = MockClient(proposals_error=http_error(code))
            result, err = self.reconcile(SERVICE_ENTRY + HAND_ENTRY, client)
            self.assertEqual(result["fetch_failed"], 1)
            self.assertEqual(client.written, [])
            self.assertIn("Skipping all directive reconciliation", err)

    def test_proposals_network_error_skips_all_puts(self):
        client = MockClient(proposals_error=urllib.error.URLError("connection refused"))
        result, _ = self.reconcile(SERVICE_ENTRY + HAND_ENTRY, client)
        self.assertEqual(result["fetch_failed"], 1)
        self.assertEqual(client.written, [])

    def test_directives_fetch_failure_skips_everything(self):
        class Broken(MockClient):
            def get(self, path):
                if path == "/v1/directives":
                    raise urllib.error.URLError("down")
                return super().get(path)

        client = Broken()
        result, _ = self.reconcile(HAND_ENTRY, client)
        self.assertEqual(result["fetch_failed"], 1)
        self.assertEqual(client.written, [])


if __name__ == "__main__":
    unittest.main()

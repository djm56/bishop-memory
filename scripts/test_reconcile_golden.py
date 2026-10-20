#!/usr/bin/env python3
"""
Golden test for scripts/reconcile-memory.py --dry-run.

ReconcileDryRunGolden.test_dry_run_matches_golden runs the reconciler as a
subprocess (python -I, the real script, not its functions) against the shipped
fixture (testdata/memory),
with --harness fixture --dry-run --include-journal, pointed at a fake HTTP
service this test starts on 127.0.0.1 on an ephemeral port. The live memoryd
at 127.0.0.1:8787 is never contacted.

The fake answers every GET with fixed JSON, so the dry-run's comparisons are
deterministic. It records every request it receives. The golden holds the exit
code, stdout, stderr, and that request list, after normalising the fake's URL
and the fixture root path.

Only the URL and paths are normalised. The fixture's timestamps come from its
own Markdown, not the clock, so they are not normalised. A volatile value would
make the golden fail on the next run, which is how it would show.

ReconcileDryRunGolden.test_dry_run_sends_no_writes is the positive control for
the golden: the fake must have received GETs, and no POST, PUT, PATCH or DELETE.

Updating the golden after an intended change:

    UPDATE_GOLDEN=1 python3 scripts/test_reconcile_golden.py

Known reconciler behaviour recorded here as-is, not fixed by this test:
  - MISSION_ID is unanchored (scripts/reconcile-memory.py, MISSION_ID regex).
  - parse_steps() drops an unknown step status silently.
The fixture exercises neither. FINDINGS.md has no **Mission** field, and every
PROGRESS.md status is a known value.
"""
import difflib
import http.server
import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest
import urllib.parse

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)
RECONCILER = os.path.join(HERE, "reconcile-memory.py")
FIXTURE_ROOT = os.path.join(REPO, "testdata", "memory")
GOLDEN = os.path.join(REPO, "testdata", "golden", "reconcile-dry-run.golden")
HARNESS = "fixture"
LIVE_PORT = 8787  # the live memoryd; the fake must never bind it

# Fixed answers for the fake service. Shaped as the reconciler reads them.
STEPS_PATH = "/v1/missions/mission-20260904-01/steps"
ROUTES = {
    "/v1/missions": {"missions": [
        # Exists with a stale status and no outcome, so the dry-run reports a PATCH.
        {"id": "mission-20260904-01", "status": "in-progress", "outcome": "", "next_action": ""},
    ]},
    STEPS_PATH: {"steps": [
        # Exists with a different status from PROGRESS.md, so the dry-run reports an update.
        {"step": "1", "phase": "", "agent": "@hicks", "status": "pending", "notes": ""},
    ]},
    "/v1/flight-recorder": {"flight_recorder": []},
    "/v1/findings": {"findings": []},
    "/v1/directives": {"directives": []},
    "/v1/directive-proposals": {"directive_proposals": []},
    "/v1/patterns": {"patterns": []},
    "/v1/service-records": {"service_records": []},
}


class _FakeHandler(http.server.BaseHTTPRequestHandler):
    """Answers from ROUTES and records (method, path-with-query) on the server."""

    def _handle(self, method):
        self.server.received.append((method, self.path))
        if method != "GET":
            # The dry-run must not reach here. A write is recorded and refused.
            return self._send(405, {"error": "writes are not expected in this test"})
        route = urllib.parse.urlsplit(self.path).path
        if route in ROUTES:
            return self._send(200, ROUTES[route])
        return self._send(404, {"error": "not found"})

    def _send(self, status, payload):
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        self._handle("GET")

    def do_POST(self):
        self._handle("POST")

    def do_PUT(self):
        self._handle("PUT")

    def do_PATCH(self):
        self._handle("PATCH")

    def do_DELETE(self):
        self._handle("DELETE")

    def log_message(self, *args):
        # Keep the test output clean; requests are recorded instead.
        pass


class _Fake(http.server.ThreadingHTTPServer):
    """A fake service that carries the list of requests it has received."""

    def __init__(self):
        super().__init__(("127.0.0.1", 0), _FakeHandler)
        self.received = []


def run_dry_run(base_url):
    """Run the reconciler against base_url and return the CompletedProcess.

    The child gets a minimal environment. BISHOP_MEMORY_CLIENT_ENV points at a
    path that does not exist, so no client.env on this machine can change the
    run. -I already ignores PYTHON* variables.
    """
    env = {
        "PATH": os.environ.get("PATH", ""),
        "BISHOP_MEMORY_CLIENT_ENV": os.path.join(tempfile.gettempdir(), "bishop-memory-no-such-client.env"),
    }
    cmd = [
        sys.executable, "-I", RECONCILER,
        "--root", FIXTURE_ROOT,
        "--url", base_url,
        "--harness", HARNESS,
        "--dry-run",
        "--include-journal",
    ]
    return subprocess.run(cmd, env=env, capture_output=True, timeout=120)


def normalise(text, base_url):
    """Replace the fake's URL and the fixture root with fixed tokens."""
    text = text.replace(base_url, "<URL>")
    for root in {os.path.realpath(FIXTURE_ROOT), os.path.abspath(FIXTURE_ROOT)}:
        text = text.replace(root, "<ROOT>")
    return text


class ReconcileDryRunGolden(unittest.TestCase):
    """Runs the reconciler dry-run once against the fake and keeps the result."""

    @classmethod
    def setUpClass(cls):
        cls.server = _Fake()
        cls.port = cls.server.server_address[1]
        if cls.port == LIVE_PORT:
            raise RuntimeError("fake service bound the live memoryd port")
        cls.base_url = f"http://127.0.0.1:{cls.port}"
        thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        thread.start()
        cls.thread = thread
        try:
            cls.proc = run_dry_run(cls.base_url)
        finally:
            cls.server.shutdown()
            cls.server.server_close()
        cls.received = list(cls.server.received)

    def test_dry_run_matches_golden(self):
        """Compares the normalised exit code, stdout, stderr and request list to the golden."""
        stdout = self.proc.stdout.decode("utf-8")
        stderr = self.proc.stderr.decode("utf-8")
        lines = [
            "# reconcile dry-run golden — generated by test_reconcile_golden.py",
            "# regenerate: UPDATE_GOLDEN=1 python3 scripts/test_reconcile_golden.py",
            "--- exit ---",
            str(self.proc.returncode),
            "--- stdout ---",
            normalise(stdout, self.base_url).rstrip("\n"),
            "--- stderr ---",
            normalise(stderr, self.base_url).rstrip("\n") or "(empty)",
            "--- requests received by fake ---",
        ]
        lines += [f"{method} {path}" for method, path in self.received] or ["(none)"]
        got = "\n".join(lines) + "\n"

        if os.environ.get("UPDATE_GOLDEN"):
            os.makedirs(os.path.dirname(GOLDEN), exist_ok=True)
            with open(GOLDEN, "w", encoding="utf-8") as fh:
                fh.write(got)
            print(f"UPDATE_GOLDEN set: wrote {GOLDEN}", file=sys.stderr)
            return

        with open(GOLDEN, encoding="utf-8") as fh:
            want = fh.read()
        if got != want:
            diff = "\n".join(difflib.unified_diff(
                want.splitlines(), got.splitlines(),
                fromfile="golden", tofile="got", lineterm=""))
            self.fail(f"reconcile dry-run output differs from {GOLDEN}:\n{diff}")

    def test_dry_run_sends_no_writes(self):
        """Positive control: the fake received GETs, and no write reached it."""
        self.assertTrue(self.received, "fake received no requests; the reconciler never reached it")
        writes = [(m, p) for m, p in self.received if m != "GET"]
        self.assertEqual(writes, [], "dry-run sent write requests")
        self.assertEqual(self.proc.returncode, 0, self.proc.stderr.decode("utf-8", "replace"))


if __name__ == "__main__":
    unittest.main()

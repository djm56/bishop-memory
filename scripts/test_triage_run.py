"""
Tests for scripts/triage-run.sh: the OpenCode agent guard, and the run that
follows it.

Each test copies the script and our three agent definitions into a temporary
root, so the script resolves its agents from that copy. It runs against a stub
OPENCODE_BIN, which answers from files the test writes, and against a local
stub of the bishop-memory service. Nothing here calls the real OpenCode, a
model, or the live service.

The permission rule lists in the fixtures are what OpenCode v2.0.32 reported
for our three agents through `debug agents`, copied from that output. The
description and body the guard compares are read from the copied definition,
which is the same text v2 reported for the same file.

Each test name says the path it exercises. Each docstring names the control:
the change to the guard that makes the test fail.
"""

import http.server
import json
import os
import re
import shutil
import subprocess
import tempfile
import threading
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)
SCRIPT_SRC = os.path.join(HERE, "triage-run.sh")
AGENTS_SRC = os.path.join(REPO, ".opencode", "agents")

KIND_AGENT = {
    "classify": "findings-classifier",
    "process": "findings-processor",
    "grade": "mission-grader",
}

# Captured from `opencode debug agents` on OpenCode v2.0.32, with no config
# content. Each list is the agent's own rules after the built-in defaults.
GRADER_RULES = [
    {"action": "*", "resource": "*", "effect": "allow"},
    {"action": "read", "resource": "*.env", "effect": "ask"},
    {"action": "read", "resource": "*.env.*", "effect": "ask"},
    {"action": "read", "resource": "*.env.example", "effect": "allow"},
    {"action": "*", "resource": "*", "effect": "deny"},
    {"action": "read", "resource": "*", "effect": "allow"},
    {"action": "read", "resource": "*.env", "effect": "deny"},
    {"action": "read", "resource": "*.env.*", "effect": "deny"},
    {"action": "bishop-triage_triage_run_start", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_triage_run_finish", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_grade_claim", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_grade_write", "resource": "*", "effect": "allow"},
    {"action": "browser", "resource": "*", "effect": "deny"},
]

CLASSIFIER_RULES = [
    {"action": "*", "resource": "*", "effect": "allow"},
    {"action": "read", "resource": "*.env", "effect": "ask"},
    {"action": "read", "resource": "*.env.*", "effect": "ask"},
    {"action": "read", "resource": "*.env.example", "effect": "allow"},
    {"action": "*", "resource": "*", "effect": "deny"},
    {"action": "read", "resource": "*", "effect": "allow"},
    {"action": "read", "resource": "*.env", "effect": "deny"},
    {"action": "read", "resource": "*.env.*", "effect": "deny"},
    {"action": "bishop-triage_triage_categories", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_triage_next_unclassified", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_triage_classify", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_triage_run_start", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_triage_run_finish", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_finding_list", "resource": "*", "effect": "allow"},
    {"action": "browser", "resource": "*", "effect": "deny"},
]

PROCESSOR_RULES = [
    {"action": "*", "resource": "*", "effect": "allow"},
    {"action": "read", "resource": "*.env", "effect": "ask"},
    {"action": "read", "resource": "*.env.*", "effect": "ask"},
    {"action": "read", "resource": "*.env.example", "effect": "allow"},
    {"action": "*", "resource": "*", "effect": "deny"},
    {"action": "read", "resource": "*", "effect": "allow"},
    {"action": "read", "resource": "*.env", "effect": "deny"},
    {"action": "read", "resource": "*.env.*", "effect": "deny"},
    {"action": "glob", "resource": "*", "effect": "allow"},
    {"action": "grep", "resource": "*", "effect": "allow"},
    {"action": "list", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_triage_categories", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_triage_category_findings", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_triage_recent_decisions", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_triage_group_create", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_triage_recommend", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_directive_propose", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_triage_run_start", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_triage_run_finish", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_finding_list", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_pattern_list", "resource": "*", "effect": "allow"},
    {"action": "bishop-triage_memory_search", "resource": "*", "effect": "allow"},
    {"action": "browser", "resource": "*", "effect": "deny"},
]

RULES = {
    "mission-grader": GRADER_RULES,
    "findings-classifier": CLASSIFIER_RULES,
    "findings-processor": PROCESSOR_RULES,
}

# One step_finish, so the run summary counts a step and exits 0.
STEP_OK = (
    '{"type":"text","part":{"messageID":"m1","text":"done"}}\n'
    '{"type":"step_finish","part":{"cost":0.01,"reason":"stop"}}\n'
)

# Stands in for OPENCODE_BIN. It answers `debug` from STUB_DEBUG_OUT and
# exits STUB_DEBUG_RC; it records each `run` call's arguments and config, then
# answers from STUB_RUN_OUT and exits STUB_RUN_RC. Any other call is refused.
STUB_SH = """#!/bin/sh
case "$1" in
  debug)
    cat "$STUB_DEBUG_OUT"
    exit "${STUB_DEBUG_RC:-0}" ;;
  run)
    printf '%s\\n' "$@" > "$STUB_RUN_ARGS"
    printf '%s' "${OPENCODE_CONFIG_CONTENT:-}" > "$STUB_RUN_CONFIG"
    cat "$STUB_RUN_OUT"
    exit "${STUB_RUN_RC:-0}" ;;
  *)
    echo "stub: unexpected call: $*" >&2
    exit 99 ;;
esac
"""


class FakeMemory(http.server.BaseHTTPRequestHandler):
    """Answers the few bishop-memory routes the runner reads before the guard."""

    ROUTES = {
        "/healthz": {"ok": True},
        "/v1/findings": {"findings": [{"id": "f1"}]},
        "/v1/mission-grades/waiting": {"waiting": 1},
        "/v1/triage/next-category": {"category": "demo"},
        "/v1/harnesses": {"harnesses": []},
    }

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        body = json.dumps(self.ROUTES.get(path, {})).encode()
        self.send_response(200 if path in self.ROUTES else 404)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        pass  # keep test output clean


class TriageRunCase(unittest.TestCase):
    """Builds the temporary root, the stubs and the fake service for each test."""

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="triage-run-test-")
        self.root = os.path.join(self.tmp, "root")
        os.makedirs(os.path.join(self.root, "scripts"))
        os.makedirs(os.path.join(self.root, "bin"))
        os.makedirs(os.path.join(self.root, ".opencode", "agents"))
        self.script = os.path.join(self.root, "scripts", "triage-run.sh")
        shutil.copy(SCRIPT_SRC, self.script)
        os.chmod(self.script, 0o755)
        # The runner builds nothing when bin/mcpd exists; a no-op stands in.
        mcpd = os.path.join(self.root, "bin", "mcpd")
        with open(mcpd, "w") as fh:
            fh.write("#!/bin/sh\nexit 0\n")
        os.chmod(mcpd, 0o755)
        for name in os.listdir(AGENTS_SRC):
            shutil.copy(os.path.join(AGENTS_SRC, name), os.path.join(self.root, ".opencode", "agents", name))

        self.stub = os.path.join(self.tmp, "opencode-stub")
        with open(self.stub, "w") as fh:
            fh.write(STUB_SH)
        os.chmod(self.stub, 0o755)

        self.log_dir = os.path.join(self.tmp, "logs")
        self.debug_out = os.path.join(self.tmp, "debug.out")
        self.run_args = os.path.join(self.tmp, "run.args")
        self.run_config = os.path.join(self.tmp, "run.config")
        self.run_out = os.path.join(self.tmp, "run.out")

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), FakeMemory)
        self.url = "http://127.0.0.1:%d" % self.server.server_address[1]
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        shutil.rmtree(self.tmp, ignore_errors=True)

    def definition(self, agent):
        """The description and body of the copied definition, as the guard reads them."""
        with open(os.path.join(self.root, ".opencode", "agents", agent + ".md"), encoding="utf8") as fh:
            lines = fh.read().split("\n")
        end = lines.index("---", 1)
        desc = next(m.group(1) for m in (re.match(r'^description:\s*"(.*)"\s*$', l) for l in lines[1:end]) if m)
        return desc, "\n".join(lines[end + 1:]).strip()

    def agent_entry(self, name, rules, system=None, description=None, source=None):
        """One agent as `debug agents` lists it, built from our definition unless overridden.

        The listed name is `name`. The text comes from the copied definition
        `source`, which defaults to `name`.
        """
        desc, body = self.definition(source or name)
        return {
            "id": name,
            "name": name,
            "description": desc if description is None else description,
            "system": body if system is None else system,
            "mode": "primary",
            "hidden": False,
            "permissions": rules,
        }

    def run_script(self, kind, listing, *, debug_rc=0, run_out=STEP_OK, run_rc=0):
        """Run the copied script for one kind. The listing is JSON, or raw text when a string."""
        text = listing if isinstance(listing, str) else json.dumps(listing)
        with open(self.debug_out, "w") as fh:
            fh.write(text)
        with open(self.run_out, "w") as fh:
            fh.write(run_out)
        env = {
            "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
            "HOME": self.tmp,
            "TRIAGE_LOG_DIR": self.log_dir,
            "TRIAGE_ENV_FILE": os.path.join(self.tmp, "absent.env"),
            "BISHOP_MEMORY_CLIENT_ENV": os.path.join(self.tmp, "absent-client.env"),
            "BISHOP_MEMORY_URL": self.url,
            "OPENCODE_BIN": self.stub,
            "STUB_DEBUG_OUT": self.debug_out,
            "STUB_DEBUG_RC": str(debug_rc),
            "STUB_RUN_ARGS": self.run_args,
            "STUB_RUN_CONFIG": self.run_config,
            "STUB_RUN_OUT": self.run_out,
            "STUB_RUN_RC": str(run_rc),
        }
        return subprocess.run(
            ["/bin/bash", self.script, kind, "--engine", "opencode"],
            env=env,
            stdin=subprocess.DEVNULL,
            capture_output=True,
            text=True,
            timeout=120,
        )

    def log(self):
        """The runner's log file for this test."""
        path = os.path.join(self.log_dir, "triage.log")
        if not os.path.exists(path):
            return ""
        with open(path, encoding="utf8") as fh:
            return fh.read()

    def ran(self):
        """True when the run stub was called, which is the one thing a refused guard must prevent."""
        return os.path.exists(self.run_args)

    def args(self):
        """The argument lines the run stub received."""
        with open(self.run_args, encoding="utf8") as fh:
            return fh.read().split("\n")

    def assertRefused(self, proc, reason):
        """The guard refused: exit 2, nothing run, and the guard's stated reason on stderr."""
        self.assertEqual(proc.returncode, 2, proc.stderr + self.log())
        self.assertIn("nothing run", self.log())
        self.assertIn(reason, proc.stderr)
        self.assertFalse(self.ran(), "the run stub was called after a refusal")


class GuardAccepts(TriageRunCase):
    def test_guard_accepts_each_triage_agent_definition_and_runs_it(self):
        """Path: the guard passes our own definition and the run starts.

        Control: a guard that refuses every listing fails here (exit 2, no run
        call). Each kind is run once, with its own agent, from a fresh state.
        """
        for kind, agent in KIND_AGENT.items():
            with self.subTest(agent=agent):
                if os.path.exists(self.run_args):
                    os.remove(self.run_args)
                proc = self.run_script(kind, [self.agent_entry(agent, RULES[agent])])
                self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
                self.assertTrue(self.ran())
                args = self.args()
                self.assertEqual(args[args.index("--agent") + 1], agent)

    def test_run_is_started_standalone_without_pure_and_with_the_layered_config(self):
        """Path: the run invocation under v2 (no --pure, --standalone, layered config).

        Control: putting `--pure` back into the run command fails the first
        assertion. Dropping `--standalone` fails the second. Dropping the
        layered config fails the third, because the MCP server is missing.
        """
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        args = self.args()
        self.assertNotIn("--pure", args)
        self.assertIn("--standalone", args)
        with open(self.run_config, encoding="utf8") as fh:
            self.assertIn("bishop-triage", fh.read())


class GuardRefuses(TriageRunCase):
    def test_guard_refuses_an_unknown_agent_name(self):
        """Path: the named agent is absent from the listing.

        Control: a guard that treats an empty match as a pass fails here, and
        the run stub is then called.
        """
        proc = self.run_script("grade", [self.agent_entry("Build", GRADER_RULES, source="mission-grader")])
        self.assertRefused(proc, "0 agents named mission-grader")

    def test_guard_refuses_two_entries_with_the_same_name(self):
        """Path: two agents share the name, so the guard cannot tell which one runs.

        Control: a guard that takes the first match and ignores the count
        passes this listing and fails the test.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES)
        proc = self.run_script("grade", [entry, dict(entry)])
        self.assertRefused(proc, "2 agents named mission-grader")

    def test_guard_refuses_a_definition_that_is_not_ours(self):
        """Path: the agent of our name has a different body from our file.

        Control: removing the body comparison from the identity check passes
        this listing, and the test fails.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES, system="# Some other agent\n\nDo things.\n")
        proc = self.run_script("grade", [entry])
        self.assertRefused(proc, "is not the definition in")

    def test_guard_refuses_an_edit_allowed_after_the_deny(self):
        """Path: a later rule allows edit everywhere, overriding the deny.

        Control: a check that accepts any covering deny, rather than the last
        covering rule, passes this listing, and the test fails.
        """
        rules = GRADER_RULES + [{"action": "edit", "resource": "*", "effect": "allow"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "is not denied edit")

    def test_guard_refuses_an_edit_allowed_for_some_paths(self):
        """Path: a later rule allows edit only under src/, which is still an edit permission.

        Control: a check that looks only at rules covering every resource
        passes this listing, and the test fails.
        """
        rules = GRADER_RULES + [{"action": "edit", "resource": "src/*", "effect": "allow"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "is not denied edit")

    def test_guard_refuses_a_shell_allowed_after_the_deny(self):
        """Path: a later rule allows bash, the shell action in v2.

        Control: removing bash from the guarded actions passes this listing,
        and the test fails.
        """
        rules = GRADER_RULES + [{"action": "bash", "resource": "*", "effect": "allow"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "is not denied bash")

    def test_guard_refuses_when_opencode_debug_fails(self):
        """Path: `opencode debug agents` exits non-zero.

        Control: removing the failure branch around the listing command means the
        log names a different refusal, and the "cannot resolve" assertion fails.
        """
        proc = self.run_script("grade", [], debug_rc=1)
        self.assertEqual(proc.returncode, 2, proc.stderr + self.log())
        self.assertIn("OpenCode cannot resolve agent mission-grader", self.log())
        self.assertFalse(self.ran())

    def test_guard_refuses_an_unparseable_agent_listing(self):
        """Path: the listing command exits 0 but prints something that is not JSON.

        Control: a guard that swallows the parse error and returns a pass
        fails this test, because the run stub is then called.
        """
        proc = self.run_script("grade", "not json {")
        self.assertRefused(proc, "guard check failed")


class GuardPositiveControl(TriageRunCase):
    def test_guard_accepts_an_edit_allow_that_a_later_deny_overrides(self):
        """Path: an edit allow that comes before the final deny is overridden, so the agent is still denied.

        This is the positive control for the edit refusals above. A guard that
        refuses every edit allow, wherever it sits, fails this test.
        """
        rules = [{"action": "edit", "resource": "*", "effect": "allow"}] + GRADER_RULES
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())


ERROR_EVENT = (
    '{"type":"error","error":{"type":"unknown",'
    '"message":"Agent not found: \\"mission-grader\\""}}\n'
)


class RunReportsErrors(TriageRunCase):
    def test_run_error_event_message_reaches_the_log(self):
        """Path: the run exits 0 but its stream holds a v2 error event; the summary reads error.message.

        Control: reading the message from error.name, as v1 did, drops the text
        and the log assertion fails. A run that exits 0 with no step is also
        failed, so the exit code is 1 either way.
        """
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)],
                               run_out=ERROR_EVENT, run_rc=0)
        self.assertEqual(proc.returncode, 1, proc.stderr + self.log())
        self.assertIn("Agent not found", self.log())

    def test_run_nonzero_exit_points_at_the_result_file(self):
        """Path: OpenCode v2 exits 1 on an error event, so the summary is not read.

        The log names the result file that holds the error, and exit is 1.
        Control: dropping the result-file pointer from the non-zero exit line
        fails the "agent exited 1; see" assertion.
        """
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)],
                               run_out=ERROR_EVENT, run_rc=1)
        self.assertEqual(proc.returncode, 1, proc.stderr + self.log())
        self.assertIn("agent exited 1; see", self.log())


if __name__ == "__main__":
    unittest.main()

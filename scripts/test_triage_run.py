"""
Tests for scripts/triage-run.sh: the OpenCode agent guard, and the run that
follows it.

Each test copies the script and our agent definitions into a temporary root,
so the script resolves its agents from that copy. It runs against a stub
OPENCODE_BIN, which answers from files the test writes, and against a local
stub of the bishop-memory service. Nothing here calls the real OpenCode, a
model, or the live service.

The permission rule lists in the fixtures are what OpenCode v2.0.32 reported
for our agents through `debug agents`, copied from that output. The
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
import signal
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

# The grader's description as its definition writes it. The guard is tested
# against this literal, so a change to the definition shows up here.
LITERAL_GRADER_DESCRIPTION = (
    "Scheduled grader for finished bishop-memory missions. Claims up to ten finished "
    "missions, grades each A to F from the grading packet the service assembles, and "
    "writes a short summary and forward-looking suggestions through the bishop-triage "
    "MCP tools. Database only, one pass per run; writes no files."
)

# One step_finish, so the run summary counts a step and exits 0.
STEP_OK = (
    '{"type":"text","part":{"messageID":"m1","text":"done"}}\n'
    '{"type":"step_finish","part":{"cost":0.01,"reason":"stop"}}\n'
)

# Stands in for OPENCODE_BIN. For `debug` it counts the call in STUB_DEBUG_COUNT.
# STUB_DEBUG_HANG makes it sleep until the guard's alarm kills it. The first
# STUB_DEBUG_COLD calls print STUB_DEBUG_COLD_OUT and exit 0. Otherwise it writes
# STUB_DEBUG_ERR to stderr, prints STUB_DEBUG_OUT and exits STUB_DEBUG_RC. For
# `run` it records the arguments and config, then answers from STUB_RUN_OUT and
# exits STUB_RUN_RC. Any other call is refused.
STUB_SH = """#!/bin/sh
case "$1" in
  debug)
    n=$(( $(cat "$STUB_DEBUG_COUNT" 2>/dev/null || echo 0) + 1 ))
    echo "$n" > "$STUB_DEBUG_COUNT"
    if [ -n "${STUB_DEBUG_HANG:-}" ]; then exec sleep 600; fi
    if [ "$n" -le "${STUB_DEBUG_COLD:-0}" ]; then printf '%s' "${STUB_DEBUG_COLD_OUT:-}"; exit 0; fi
    if [ -n "${STUB_DEBUG_ERR:-}" ]; then printf '%s\\n' "$STUB_DEBUG_ERR" >&2; fi
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

# The tools the script and its guard use, all but perl. The no-perl test builds
# a PATH from these alone.
TOOLS_WITHOUT_PERL = (
    "python3", "cat", "mktemp", "date", "tr", "head", "sed", "awk", "sleep", "rm",
    "mkdir", "stat", "chmod", "grep", "wc", "cut", "dirname", "basename", "curl", "tee",
    "uname", "sort",
)


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
        self.debug_count = os.path.join(self.tmp, "debug.count")
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

    def definition_path(self, agent):
        """The path of the copied definition for one agent."""
        return os.path.join(self.root, ".opencode", "agents", agent + ".md")

    def definition(self, agent):
        """The description and body of the copied definition, as the guard reads them."""
        with open(self.definition_path(agent), encoding="utf8") as fh:
            lines = fh.read().split("\n")
        end = lines.index("---", 1)
        desc = next(m.group(1) for m in (re.match(r'^description:\s*"(.*)"\s*$', l) for l in lines[1:end]) if m)
        return desc, "\n".join(lines[end + 1:]).strip()

    def agent_entry(self, name, rules, system=None, description=None, source=None, mode=None):
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
            "mode": "primary" if mode is None else mode,
            "hidden": False,
            "permissions": rules,
        }

    def base_env(self, path=None):
        """The environment every run of the script gets: absent credentials, the fake service, the stubs."""
        return {
            "PATH": path if path is not None else os.environ.get("PATH", "/usr/bin:/bin"),
            "HOME": self.tmp,
            "TRIAGE_LOG_DIR": self.log_dir,
            "TRIAGE_ENV_FILE": os.path.join(self.tmp, "absent.env"),
            "BISHOP_MEMORY_CLIENT_ENV": os.path.join(self.tmp, "absent-client.env"),
            "BISHOP_MEMORY_URL": self.url,
            "OPENCODE_BIN": self.stub,
            "STUB_DEBUG_OUT": self.debug_out,
            "STUB_DEBUG_COUNT": self.debug_count,
            "STUB_RUN_ARGS": self.run_args,
            "STUB_RUN_CONFIG": self.run_config,
            "STUB_RUN_OUT": self.run_out,
        }

    def run_script(self, kind, listing, *, debug_rc=0, debug_err=None, run_out=STEP_OK, run_rc=0,
                   cold=0, cold_out="", hang=False, guard_timeout=30, path=None):
        """Run the copied script for one kind. The listing is JSON, or raw text when a string.

        `cold` makes the first that many `debug` calls print `cold_out`. `hang`
        makes every `debug` call sleep until the guard's alarm stops it.
        """
        text = listing if isinstance(listing, str) else json.dumps(listing)
        with open(self.debug_out, "w") as fh:
            fh.write(text)
        with open(self.run_out, "w") as fh:
            fh.write(run_out)
        env = self.base_env(path)
        env.update({
            "STUB_DEBUG_RC": str(debug_rc),
            "STUB_DEBUG_ERR": debug_err or "",
            "STUB_DEBUG_COLD": str(cold),
            "STUB_DEBUG_COLD_OUT": cold_out,
            "STUB_DEBUG_HANG": "1" if hang else "",
            "STUB_RUN_RC": str(run_rc),
            "TRIAGE_GUARD_TIMEOUT_SEC": str(guard_timeout),
        })
        # The script runs in its own session, so a hung run can be killed with
        # every process it started. Past the limit the test fails with a message.
        proc = subprocess.Popen(
            ["/bin/bash", self.script, kind, "--engine", "opencode"],
            env=env,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            start_new_session=True,
        )
        try:
            out, err = proc.communicate(timeout=30)
        except subprocess.TimeoutExpired:
            os.killpg(proc.pid, signal.SIGKILL)
            proc.communicate()
            self.fail("triage-run.sh did not finish within 30 seconds; the run was killed")
        return subprocess.CompletedProcess(proc.args, proc.returncode, out, err)

    def run_help(self, *args):
        """Run the copied script with the given arguments and return the completed process."""
        return subprocess.run(["/bin/bash", self.script, *args], env=self.base_env(),
                              stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=30)

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

    def debug_calls(self):
        """How many times the stub was asked for `debug agents`."""
        if not os.path.exists(self.debug_count):
            return 0
        with open(self.debug_count, encoding="utf8") as fh:
            return int(fh.read().strip() or 0)

    def args(self):
        """The argument lines the run stub received."""
        with open(self.run_args, encoding="utf8") as fh:
            return fh.read().split("\n")

    def assertRefused(self, proc, reason):
        """The guard refused: exit 2, nothing run, and the guard's stated reason on stderr and in the log."""
        self.assertEqual(proc.returncode, 2, proc.stderr + self.log())
        self.assertIn("nothing run", self.log())
        self.assertIn(reason, proc.stderr)
        self.assertIn(reason, self.log())
        self.assertFalse(self.ran(), "the run stub was called after a refusal")

    def assertCannotCheck(self, proc, reason):
        """The guard could not check: exit 4, nothing run, the reason in the log, and no refusal wording."""
        self.assertEqual(proc.returncode, 4, proc.stderr + self.log())
        self.assertIn("could not check agent", self.log())
        self.assertIn("nothing run", self.log())
        self.assertIn(reason, self.log())
        self.assertNotIn("cannot resolve", self.log())
        self.assertFalse(self.ran(), "the run stub was called after the guard could not check")


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

    def test_guard_accepts_a_description_equal_to_the_literal_in_the_definition(self):
        """Path: the grader's description is compared as text, against a literal fixture.

        The test first pins the literal to the copied definition, so a changed
        definition fails here rather than in a silent mismatch. Control: a guard
        that compares the description after stripping its last word refuses this
        listing, and the test fails.
        """
        self.assertEqual(self.definition("mission-grader")[0], LITERAL_GRADER_DESCRIPTION)
        entry = self.agent_entry("mission-grader", GRADER_RULES, description=LITERAL_GRADER_DESCRIPTION)
        proc = self.run_script("grade", [entry])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_accepts_an_entry_matched_by_name_when_its_id_differs(self):
        """Path: the one entry is matched by its name, and its id is something else.

        Control: a guard that matches on id alone counts no entry here, refuses
        with "0 agents named", and the test fails.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES)
        entry["id"] = "some-other-id"
        proc = self.run_script("grade", [entry])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

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

    def test_guard_accepts_an_external_directory_allow_after_the_final_deny(self):
        """Path: the layered config's external_directory allow is named by the listing after the deny.

        Control: taking external_directory out of the allowed actions refuses
        this listing, and the test fails.
        """
        rules = GRADER_RULES + [{"action": "external_directory", "resource": "/some/dir/*", "effect": "allow"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_accepts_a_deny_on_a_narrower_resource_after_the_final_deny(self):
        """Path: a deny for one resource after the final deny denies, and the guard accepts it.

        Control: a guard that skips only denies on the resource `*` refuses
        this listing, and the test fails.
        """
        rules = GRADER_RULES + [{"action": "edit", "resource": "src/*", "effect": "deny"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_accepts_a_deny_on_every_resource_after_the_final_deny(self):
        """Path: a deny for every resource after the final deny is accepted.

        Control: a guard that skips only denies on resources other than `*`
        refuses the browser deny already in the grader's rules, and the test fails.
        """
        rules = GRADER_RULES + [{"action": "edit", "resource": "*", "effect": "deny"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())


class GuardRefuses(TriageRunCase):
    def test_guard_refuses_an_unknown_agent_name(self):
        """Path: the named agent is absent from a non-empty listing.

        Control: a guard that treats an empty match as a pass calls the run
        stub, so the exit code and the refusal text fail here.
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

    def test_guard_refuses_a_shadowing_entry_whose_id_is_the_agent_name(self):
        """Path: a second entry has our name as its id, so the name is held twice.

        Control: a guard that matches on name alone counts one entry, accepts
        the listing, and the test fails.
        """
        ours = self.agent_entry("mission-grader", GRADER_RULES)
        shadow = self.agent_entry("Build", GRADER_RULES, source="mission-grader")
        shadow["id"] = "mission-grader"
        proc = self.run_script("grade", [ours, shadow])
        self.assertRefused(proc, "2 agents named mission-grader")

    def test_guard_refuses_a_description_that_is_not_ours(self):
        """Path: the agent of our name has a description different from our file.

        Control: removing the description comparison passes this listing (the
        body still matches), and the test fails.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES, description="Something else entirely.")
        proc = self.run_script("grade", [entry])
        self.assertRefused(proc, "agent mission-grader description is not the one in")

    def test_guard_refuses_a_definition_that_is_not_ours(self):
        """Path: the agent of our name has a different body from our file.

        Control: removing the body comparison passes this listing (the
        description still matches), and the test fails.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES, system="# Some other agent\n\nDo things.\n")
        proc = self.run_script("grade", [entry])
        self.assertRefused(proc, "agent mission-grader system prompt is not the body of")

    def test_guard_refuses_a_mode_that_is_not_the_declared_one(self):
        """Path: the agent of our name is a subagent, while our file declares primary.

        Control: removing the mode comparison passes this listing, and the test fails.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES, mode="subagent")
        proc = self.run_script("grade", [entry])
        self.assertRefused(proc, "agent mission-grader has mode 'subagent', but")

    def test_guard_refuses_a_listing_with_missing_or_empty_permissions(self):
        """Path: the agent carries no permission rules, missing or as an empty list.

        Control: a guard that accepts an agent with no rules passes both
        subtests, and each fails. The text is the same for both, because both
        are the same path: no rules to check.
        """
        missing = self.agent_entry("mission-grader", GRADER_RULES)
        del missing["permissions"]
        empty = self.agent_entry("mission-grader", [])
        for label, entry in (("missing", missing), ("empty", empty)):
            with self.subTest(permissions=label):
                if os.path.exists(self.run_args):
                    os.remove(self.run_args)
                proc = self.run_script("grade", [entry])
                self.assertRefused(proc, "agent mission-grader lists no permission rules")

    def test_guard_refuses_an_edit_allowed_after_the_deny(self):
        """Path: a later rule allows edit everywhere, overriding the deny.

        Control: a check that accepts an edit allow after the final deny passes
        this listing, and the test fails.
        """
        rules = GRADER_RULES + [{"action": "edit", "resource": "*", "effect": "allow"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "(edit on *: allow) comes after the final deny")

    def test_guard_refuses_an_edit_allowed_for_some_paths(self):
        """Path: a later rule allows edit only under src/, which is still an edit permission.

        Control: a check that looks only at rules covering every resource
        passes this listing, and the test fails.
        """
        rules = GRADER_RULES + [{"action": "edit", "resource": "src/*", "effect": "allow"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "(edit on src/*: allow) comes after the final deny")

    def test_guard_refuses_a_shell_allowed_after_the_deny(self):
        """Path: a later rule allows bash, the shell action in v2.

        Control: a check that does not name bash as disallowed passes this
        listing, and the test fails.
        """
        rules = GRADER_RULES + [{"action": "bash", "resource": "*", "effect": "allow"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "(bash on *: allow) comes after the final deny")

    def test_guard_refuses_an_ask_for_a_shell_action_after_the_deny(self):
        """Path: a later rule asks before shell, which is neither a deny nor an allowed action.

        Control: a check that reads only `allow` as a grant passes this listing,
        and the test fails.
        """
        rules = GRADER_RULES + [{"action": "shell", "resource": "*", "effect": "ask"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "(shell on *: ask) comes after the final deny")

    def test_guard_refuses_a_bishop_action_outside_the_triage_prefix(self):
        """Path: a bishop-named action that is not bishop-triage_ is allowed after the deny.

        Control: a prefix test that matches any `bishop` action passes this
        listing, and the test fails.
        """
        rules = GRADER_RULES + [{"action": "bishop-other_tool", "resource": "*", "effect": "allow"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "(bishop-other_tool on *: allow) comes after the final deny")

    def test_guard_refuses_a_last_covering_rule_that_is_an_allow(self):
        """Path: the last rule covering every action and resource is an allow, not the deny.

        Control: a check that does not test the effect of the last covering rule
        passes this listing, and the test fails.
        """
        rules = [dict(r) for r in GRADER_RULES]
        rules[4]["effect"] = "allow"
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "the last rule covering every action and resource (rule 4) is not a deny")

    def test_guard_refuses_a_listing_with_no_rule_covering_every_action(self):
        """Path: no rule covers every action on every resource, so nothing is denied by default.

        The `**` and `*/*` subtests use a deny that looks like a covering rule
        and is not one, so they must fail closed as well.
        Control: a check that returns a pass when no covering rule exists passes
        these listings, and each subtest fails.
        """
        base = [r for r in GRADER_RULES if not (r["action"] == "*" and r["resource"] == "*")]
        for lookalike in ("**", "*/*"):
            with self.subTest(action=lookalike):
                if os.path.exists(self.run_args):
                    os.remove(self.run_args)
                rules = base + [{"action": lookalike, "resource": "*", "effect": "deny"}]
                proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
                self.assertRefused(proc, "no rule denies every action on every resource")

    def test_guard_refuses_a_stale_listing_whose_definition_grants_more(self):
        """Path: the listing matches the old definition, but the file now grants edit.

        The listing is the one the guard would accept. The file's own permission
        block is read and checked, so the loosened file is refused while the
        listing is stale.
        Control: removing the check of the definition's permission block accepts
        this run, and the test fails.
        """
        path = self.definition_path("mission-grader")
        with open(path, encoding="utf8") as fh:
            text = fh.read()
        loosened = text.replace("  bishop-triage_grade_write: allow\n",
                                "  bishop-triage_grade_write: allow\n  edit: allow\n", 1)
        self.assertNotEqual(loosened, text)
        with open(path, "w", encoding="utf8") as fh:
            fh.write(loosened)
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertRefused(proc, "(edit on *: allow) comes after the final deny")

    def test_guard_refuses_a_definition_with_no_permission_block(self):
        """Path: the definition has no permission block at all; the listing is the old one.

        Control: a guard that never reads the definition's permission block
        accepts this run, and the test fails.
        """
        path = self.definition_path("mission-grader")
        with open(path, encoding="utf8") as fh:
            lines = fh.read().split("\n")
        end = lines.index("---", 1)
        kept = []
        in_block = False
        for line in lines[:end]:
            if line == "permission:":
                in_block = True
                continue
            if in_block and line.startswith(" "):
                continue
            in_block = False
            kept.append(line)
        with open(path, "w", encoding="utf8") as fh:
            fh.write("\n".join(kept + lines[end:]))
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertRefused(proc, "definition " + path + ": no rule denies every action on every resource")


class GuardCannotCheck(TriageRunCase):
    def test_guard_cannot_check_when_opencode_debug_fails(self):
        """Path: `opencode debug agents` exits non-zero, with its stderr, and prints nothing.

        The stderr is logged, and the log says the guard could not check. It
        does not say the agent cannot be resolved, which would name a service
        failure as an agent problem.
        Control: a guard that treats a failing listing as a refusal exits 2, and
        the exit assertion fails. A guard that drops the stderr from the log
        fails the stderr assertion.
        """
        proc = self.run_script("grade", [], debug_rc=1, debug_err="stub: service unreachable")
        self.assertCannotCheck(proc, "exited 1 (stderr: stub: service unreachable)")

    def test_guard_cannot_check_when_opencode_debug_fails_even_with_a_listing(self):
        """Path: `opencode debug agents` exits non-zero but prints a valid listing.

        A failing call is not trusted, whatever it printed.
        Control: a guard that reads a non-zero listing as success accepts this
        run and calls the run stub, so the exit assertion fails.
        """
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)], debug_rc=1)
        self.assertCannotCheck(proc, "exited 1")

    def test_guard_cannot_check_when_opencode_debug_hangs(self):
        """Path: `opencode debug agents` does not finish within the guard's bound.

        The perl alarm kills the stub at the bound, so the guard exits 4 with a
        timed-out reason. The stub sleeps 600 seconds when hung; the bound here
        is 2 seconds.
        Expected failure under a removed alarm: the stub sleeps on, the test's
        own 30-second subprocess limit is reached, and the test errors.
        Control: removing the alarm lets the stub sleep, and this test errors.
        """
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)],
                               hang=True, guard_timeout=2)
        self.assertCannotCheck(proc, "timed out after 2s")

    def test_guard_retries_an_empty_listing_and_accepts_the_next_one(self):
        """Path: a cold service answers with an empty listing once, then with the agents.

        Control: a guard that does not retry refuses on the first empty listing
        and exits 4, and the test fails. The debug count shows two calls.
        """
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)],
                               cold=1, cold_out="")
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())
        self.assertEqual(self.debug_calls(), 2)

    def test_guard_cannot_check_an_empty_listing_after_the_retries(self):
        """Path: the service stays cold, and every listing is empty.

        Control: a guard that retries without bound never returns, so the test
        errors on its own limit; a guard that accepts an empty listing exits 0
        and the test fails. The debug count shows the three attempts.
        """
        proc = self.run_script("grade", [], cold=99, cold_out="")
        self.assertCannotCheck(proc, "gave an empty listing on 3 attempts")
        self.assertEqual(self.debug_calls(), 3)

    def test_guard_cannot_check_a_cold_listing_of_no_agents(self):
        """Path: the service stays cold and answers with a listing of no agents, `[]`.

        Control: a guard that treats `[]` as a listing to check exits 2, not 4,
        and the test fails.
        """
        proc = self.run_script("grade", [], cold=99, cold_out="[]")
        self.assertCannotCheck(proc, "gave an empty listing on 3 attempts")
        self.assertEqual(self.debug_calls(), 3)

    def test_guard_cannot_check_an_unparseable_agent_listing(self):
        """Path: the listing command exits 0 but prints something that is not JSON.

        Control: a guard that swallows the parse error and returns a pass
        calls the run stub, and the test fails. A guard that reports the
        failure as a refusal exits 2, and the test fails.
        """
        proc = self.run_script("grade", "not json {")
        self.assertCannotCheck(proc, "the agent listing is not JSON")

    def test_guard_cannot_check_a_definition_line_it_cannot_read(self):
        """Path: a line in our definition's permission block is in a shape the guard cannot read.

        Control: a parser that skips unreadable lines accepts this run, and the
        test fails.
        """
        path = self.definition_path("mission-grader")
        with open(path, encoding="utf8") as fh:
            text = fh.read()
        broken = text.replace("  bishop-triage_grade_write: allow\n", "  bishop-triage_grade_write allow\n", 1)
        self.assertNotEqual(broken, text)
        with open(path, "w", encoding="utf8") as fh:
            fh.write(broken)
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertCannotCheck(proc, "cannot read permission line")

    def test_guard_cannot_check_without_perl_to_bound_the_listing(self):
        """Path: perl is not on PATH, so the listing cannot be bounded, and nothing runs.

        The PATH the run gets holds every tool the script needs except perl.
        Control: a guard that runs the listing unbounded when perl is absent
        calls the stub and exits 0, so the exit assertion fails.
        """
        bin_dir = os.path.join(self.tmp, "no-perl-bin")
        os.makedirs(bin_dir)
        original = os.environ.get("PATH", "/usr/bin:/bin")
        for name in TOOLS_WITHOUT_PERL:
            found = shutil.which(name, path=original)
            if found:
                os.symlink(found, os.path.join(bin_dir, name))
        self.assertIsNone(shutil.which("perl", path=bin_dir))
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)], path=bin_dir)
        self.assertCannotCheck(proc, "perl is not on PATH")
        self.assertEqual(self.debug_calls(), 0)


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

        This models an exit-0 stream with an error event, which OpenCode v2 does
        not produce (it exits 1; see the next test). It checks the summary parser
        alone. Control: reading the message from error.name, as v1 did, drops the
        text and the log assertion fails. A run that exits 0 with no step is also
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


class HelpText(TriageRunCase):
    def test_help_as_the_first_argument_prints_the_exit_codes(self):
        """Path: `--help` as the first argument prints the header through its Exit codes paragraph.

        Control: a `--help` that prints a fixed line range stops before the Exit
        codes paragraph, and the final assertion fails.
        """
        proc = self.run_help("--help")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("Exit codes:", proc.stdout)
        self.assertIn("64 usage error.", proc.stdout)

    def test_help_after_a_kind_prints_the_exit_codes(self):
        """Path: `grade --help` prints the same header, through the Exit codes paragraph.

        Control: a `--help` that prints a fixed line range stops before the Exit
        codes paragraph, and the final assertion fails.
        """
        proc = self.run_help("grade", "--help")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("Exit codes:", proc.stdout)
        self.assertIn("64 usage error.", proc.stdout)


if __name__ == "__main__":
    unittest.main()

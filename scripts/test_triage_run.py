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
import time
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)  # the mutation runner starts this file with -I, which leaves out its directory
import opencode_config_probe as probe  # noqa: E402  (must follow the sys.path insert)
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
    if [ -n "${STUB_DEBUG_SLEEP:-}" ]; then sleep "$STUB_DEBUG_SLEEP"; fi
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

# The same tools without python3, for the test of a missing python3. perl is not
# in the list either, so the guard would fail on perl if the python3 check were
# absent and reached.
TOOLS_WITHOUT_PYTHON = tuple(t for t in TOOLS_WITHOUT_PERL if t != "python3")

# The rule count of the unrelated agent in the live-size test. At this count the
# pretty-printed listing is over 30 KB, with dense whitespace. The live listing was
# reported as about 30 KB. On this host's /bin/bash (3.2.57) the pattern
# substitution that GUARD-STRIP-BASH plants took 5 s on a 10.6 KB listing and did
# not finish in 60 s on 32 KB (timings in testdata/README.md), so this size
# exposes it.
LARGE_FILLER_RULES = 300


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

    def replace_in_definition(self, agent, old, new):
        """
        Replace one occurrence of `old` in the copied definition with `new`. The
        text must occur exactly once, so the edit lands where the test means it to.
        """
        path = self.definition_path(agent)
        with open(path, encoding="utf8") as fh:
            text = fh.read()
        if text.count(old) != 1:
            raise AssertionError("expected one occurrence of %r in %s, found %d" % (old, path, text.count(old)))
        with open(path, "w", encoding="utf8") as fh:
            fh.write(text.replace(old, new, 1))

    def drop_definition_line(self, agent, prefix):
        """Remove the one line of the copied definition that starts with `prefix`."""
        path = self.definition_path(agent)
        with open(path, encoding="utf8") as fh:
            lines = fh.read().split("\n")
        hits = [i for i, line in enumerate(lines) if line.startswith(prefix)]
        if len(hits) != 1:
            raise AssertionError("expected one line starting %r in %s, found %d" % (prefix, path, len(hits)))
        del lines[hits[0]]
        with open(path, "w", encoding="utf8") as fh:
            fh.write("\n".join(lines))

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
                   cold=0, cold_out="", hang=False, guard_timeout=30, path=None, slow=None,
                   flags=(), extra_env=None, env_file_text=None):
        """Run the copied script for one kind. The listing is JSON, or raw text when a string, or raw bytes.

        `cold` makes the first that many `debug` calls print `cold_out`. `hang`
        makes every `debug` call sleep until the guard's alarm stops it. `slow`
        makes every `debug` call sleep that many seconds, then answer normally.
        `flags` are given after `--engine opencode`, `extra_env` is added to the
        environment, and `env_file_text` is written to the file TRIAGE_ENV_FILE names.
        """
        if isinstance(listing, bytes):
            data = listing
        else:
            data = (listing if isinstance(listing, str) else json.dumps(listing, ensure_ascii=False)).encode("utf8")
        with open(self.debug_out, "wb") as fh:
            fh.write(data)
        with open(self.run_out, "w") as fh:
            fh.write(run_out)
        env = self.base_env(path)
        env.update({
            "STUB_DEBUG_RC": str(debug_rc),
            "STUB_DEBUG_ERR": debug_err or "",
            "STUB_DEBUG_COLD": str(cold),
            "STUB_DEBUG_COLD_OUT": cold_out,
            "STUB_DEBUG_HANG": "1" if hang else "",
            "STUB_DEBUG_SLEEP": str(slow) if slow else "",
            "STUB_RUN_RC": str(run_rc),
            "TRIAGE_GUARD_TIMEOUT_SEC": str(guard_timeout),
        })
        if env_file_text is not None:
            env_file = os.path.join(self.tmp, "run.env")
            with open(env_file, "w", encoding="utf8") as fh:
                fh.write(env_file_text)
            env["TRIAGE_ENV_FILE"] = env_file
        env.update(extra_env or {})
        # The script runs in its own session, so a hung run can be killed with
        # every process it started. Past the limit the test fails with a message.
        proc = subprocess.Popen(
            ["/bin/bash", self.script, kind, "--engine", "opencode", *flags],
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

    def run_claude(self, guard_timeout):
        """
        Run the copied script on the claude engine, with no claude CLI on the
        path, and with the given TRIAGE_GUARD_TIMEOUT_SEC. The claude engine does
        not use the guard, so the bad value must not be reported as exit 64; the
        CLI check is the next thing that can stop the run, and it exits 2.
        """
        env = self.base_env()
        env.update({"CLAUDE_BIN": os.path.join(self.tmp, "no-such-claude"),
                    "TRIAGE_GUARD_TIMEOUT_SEC": guard_timeout})
        return subprocess.run(["/bin/bash", self.script, "grade", "--engine", "claude"], env=env,
                              stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=30)

    def run_help(self, *args):
        """Run the copied script with the given arguments and return the completed process."""
        return subprocess.run(["/bin/bash", self.script, *args], env=self.base_env(),
                              stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=30)

    def generated_config(self, kind):
        """The layered config the run for `kind` hands the stub, parsed. The stub's copy is removed first, so a stale one is never read."""
        agent = KIND_AGENT[kind]
        if os.path.exists(self.run_config):
            os.remove(self.run_config)
        proc = self.run_script(kind, [self.agent_entry(agent, RULES[agent])])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        with open(self.run_config, encoding="utf8") as fh:
            return json.loads(fh.read())

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
        definition fails here rather than in a silent mismatch.
        """
        self.assertEqual(self.definition("mission-grader")[0], LITERAL_GRADER_DESCRIPTION)
        entry = self.agent_entry("mission-grader", GRADER_RULES, description=LITERAL_GRADER_DESCRIPTION)
        proc = self.run_script("grade", [entry])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_accepts_an_entry_matched_by_name_when_its_id_differs(self):
        """Path: the one entry is matched by its name, and its id is something else.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES)
        entry["id"] = "some-other-id"
        proc = self.run_script("grade", [entry])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_run_is_started_standalone_without_pure(self):
        """Path: the run invocation under v2 passes --standalone and no --pure.

        This test reads the arguments only; the layered config is read in
        LayeredConfig. Mutants: GUARD-RUN-STANDALONE-OFF (--standalone is left
        out) and GUARD-RUN-PURE-ADD (--pure is passed) each fail the argument
        assertion for their own flag.
        """
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        args = self.args()
        self.assertNotIn("--pure", args)
        self.assertIn("--standalone", args)

    def test_guard_accepts_an_external_directory_allow_after_the_final_deny(self):
        """Path: the layered config's external_directory allow is named by the listing after the deny.
        """
        rules = GRADER_RULES + [{"action": "external_directory", "resource": "/some/dir/*", "effect": "allow"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_accepts_a_deny_on_a_narrower_resource_after_the_final_deny(self):
        """Path: a deny for one resource after the final deny denies, and the guard accepts it.
        """
        rules = GRADER_RULES + [{"action": "edit", "resource": "src/*", "effect": "deny"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_accepts_a_deny_on_every_resource_after_the_final_deny(self):
        """Path: a deny for every resource after the final deny is accepted.
        """
        rules = GRADER_RULES + [{"action": "edit", "resource": "*", "effect": "deny"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())


    def test_guard_accepts_a_deny_directly_after_the_final_deny(self):
        """Path: the first rule after the final deny is a deny, which the guard skips.

        Mutant: a guard that refuses a deny in that first slot (GUARD-FIRST-DENY-REFUSE)
        refuses this listing, and the test fails.
        """
        rules = GRADER_RULES[:5] + [{"action": "edit", "resource": "*", "effect": "deny"}] + GRADER_RULES[5:]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_accepts_an_ask_on_an_allowed_action_after_the_final_deny(self):
        """Path: an ask is not a deny. An ask on an allowed action after the final deny is accepted.

        Mutant: a guard that treats an ask as a refusal (GUARD-ASK-REFUSE) refuses
        this listing, and the test fails.
        """
        rules = GRADER_RULES + [{"action": "read", "resource": "src/*", "effect": "ask"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_accepts_a_deny_on_a_resource_in_a_nested_block_after_the_final_deny(self):
        """Path: the definition's nested block denies one resource of an action, after the final deny.

        The nested form `edit:` with `"src/*": deny` is read with that resource.
        Mutant: a parse that turns the nested deny into an allow (GUARD-NEST-DENY-SWAP)
        refuses this file, and the test fails.
        """
        self.replace_in_definition("mission-grader", "  bishop-triage_grade_write: allow\n",
                                   "  bishop-triage_grade_write: allow\n  edit:\n    \"src/*\": deny\n")
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_accepts_a_top_level_key_after_the_permission_block(self):
        """Path: a top-level key after the permission block ends it, and the file is accepted.
        """
        self.replace_in_definition("mission-grader", "  bishop-triage_grade_write: allow\n---\n",
                                   "  bishop-triage_grade_write: allow\ntemperature: 0.2\n---\n")
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_accepts_an_indented_block_before_the_permission_block_it_does_not_read(self):
        """Path: an indented block under another key, before the permission block, is not read as rules.

        Mutant: GUARD-INBLOCK-OFF (every indented line is read) reads `foo: bar`
        as a rule with an effect that is not allow, ask or deny, and the guard
        exits 4, not 0.
        """
        self.replace_in_definition("mission-grader", "permission:\n", "options:\n  foo: bar\npermission:\n")
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_accepts_a_definition_with_single_quoted_keys_and_values(self):
        """Path: the permission block quotes its keys and effects with single quotes, as YAML allows.

        The file says '*': deny and 'read': ... and the guard reads the same rules
        as the double-quoted form.
        """
        path = self.definition_path("mission-grader")
        with open(path, encoding="utf8") as fh:
            text = fh.read()
        quoted = text.replace('  "*": deny\n', "  '*': deny\n", 1).replace('    "*": allow\n', "    '*': allow\n", 1)
        self.assertNotEqual(quoted, text)
        with open(path, "w", encoding="utf8") as fh:
            fh.write(quoted)
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_accepts_a_definition_whose_body_is_not_ascii(self):
        """Path: the body of the definition holds a non-ASCII character, and the listing matches it.

        The definition is read as UTF-8. Mutant: GUARD-READ-ASCII (the definition is read as ASCII) gives exit 4 for this file, and the test fails.
        """
        self.replace_in_definition("mission-grader", "# Mission Grader\n", "# Mission Grader — graded\n")
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_accepts_a_listing_entry_that_is_not_ours_with_non_ascii_text(self):
        """Path: another agent in the listing carries non-ASCII text, and the listing is read as UTF-8.

        Mutant: GUARD-LISTING-UTF8-ASCII (the listing is decoded as ASCII) cannot
        decode it, and the guard exits 4, not 0.
        """
        other = self.agent_entry("Build", GRADER_RULES, source="mission-grader")
        other["description"] = "Builds the thing — quickly."
        proc = self.run_script("grade", [other, self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_accepts_our_entry_beside_a_bare_string_in_the_listing(self):
        """Path: the listing holds a string where an agent object should be, beside our entry.

        The string is not an entry of ours, so it is skipped, and our entry is
        the one named. Mutant: GUARD-NAMED-DICT-OFF (the object test is removed)
        calls .get on the string, and the guard exits 4, not 0.

        """
        listing = [self.agent_entry("mission-grader", GRADER_RULES), "not an agent"]
        proc = self.run_script("grade", listing)
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_accepts_our_entry_beside_a_bare_string_that_spells_its_name(self):
        """Path: the listing holds our entry and, beside it, a string equal to the agent's name.

        A string is not an agent entry, so it does not count as a second one with
        our name, and our entry is still the only one named.
        """
        listing = [self.agent_entry("mission-grader", GRADER_RULES), "mission-grader"]
        proc = self.run_script("grade", listing)
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_completes_a_listing_of_live_size_within_the_bound(self):
        """Path: a listing of about the live size, with dense whitespace, is read within the bound.

        The listing is pretty-printed JSON with a large unrelated agent. It runs
        through the file-fed listing check, which is quick. Mutant: GUARD-STRIP-BASH
        (a bash pattern substitution in place of tr) does not finish the listing
        within run_script's 30-second limit, so the test fails on that limit, not
        on the bound below. That kill depends on the host's /bin/bash being slow
        on the substitution, as the 3.2.57 here is. The bound below is a backstop
        and the mutant does not reach it.
        """
        filler = self.agent_entry("Build", GRADER_RULES, source="mission-grader")
        filler["permissions"] = [{"action": "read", "resource": "/some/path/%d" % i, "effect": "allow"}
                                 for i in range(LARGE_FILLER_RULES)]
        listing = [self.agent_entry("mission-grader", GRADER_RULES), filler]
        text = json.dumps(listing, indent=2, ensure_ascii=False)
        self.assertGreater(len(text), 30000)
        started = time.monotonic()
        proc = self.run_script("grade", text)
        elapsed = time.monotonic() - started
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertLess(elapsed, 10, "the guard took %.1f s on a %d-byte listing" % (elapsed, len(text)))

    def test_guard_accepts_a_slow_listing_that_finishes_inside_the_bound(self):
        """Path: the listing takes 2 seconds and the bound is 5, so the listing is read.

        Mutant: a bound that is not the configured one (GUARD-TIMEOUT-SHORT, a
        constant 1 second) stops this listing, and the exit assertion fails.
        """
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)],
                               slow=2, guard_timeout=5)
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_accepts_a_guard_timeout_of_one_second(self):
        """Path: TRIAGE_GUARD_TIMEOUT_SEC=1 is a whole number of seconds at least 1, and is accepted.

        Mutant: a bound check that rejects 1 (GUARD-TMO-STRICT) exits 64, and the
        test fails.
        """
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)], guard_timeout=1)
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())


class GuardRefuses(TriageRunCase):
    def test_guard_refuses_an_unknown_agent_name(self):
        """Path: the named agent is absent from a non-empty listing.

        Mutant GUARD-COUNT-LOW (`len(named) > 1`) reads named[0] of an empty match, raises IndexError
        and exits 4 rather than 2. The kill is on the exit code (4 != 2).
        """
        proc = self.run_script("grade", [self.agent_entry("Build", GRADER_RULES, source="mission-grader")])
        self.assertRefused(proc, "0 agents named mission-grader")

    def test_guard_refuses_two_entries_with_the_same_name(self):
        """Path: two agents share the name, so the guard cannot tell which one runs.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES)
        proc = self.run_script("grade", [entry, dict(entry)])
        self.assertRefused(proc, "2 agents named mission-grader")

    def test_guard_refuses_a_shadowing_entry_whose_id_is_the_agent_name(self):
        """Path: a second entry has our name as its id, so the name is held twice.
        """
        ours = self.agent_entry("mission-grader", GRADER_RULES)
        shadow = self.agent_entry("Build", GRADER_RULES, source="mission-grader")
        shadow["id"] = "mission-grader"
        proc = self.run_script("grade", [ours, shadow])
        self.assertRefused(proc, "2 agents named mission-grader")

    def test_guard_refuses_a_description_that_is_not_ours(self):
        """Path: the agent of our name has a description different from our file.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES, description="Something else entirely.")
        proc = self.run_script("grade", [entry])
        self.assertRefused(proc, "agent mission-grader description is not the one in")

    def test_guard_refuses_a_definition_that_is_not_ours(self):
        """Path: the agent of our name has a different body from our file.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES, system="# Some other agent\n\nDo things.\n")
        proc = self.run_script("grade", [entry])
        self.assertRefused(proc, "agent mission-grader system prompt is not the body of")

    def test_guard_refuses_a_mode_that_is_not_the_declared_one(self):
        """Path: the agent of our name is a subagent, while our file declares primary.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES, mode="subagent")
        proc = self.run_script("grade", [entry])
        self.assertRefused(proc, "agent mission-grader has mode 'subagent', but")

    def test_guard_refuses_a_listing_with_missing_or_empty_permissions(self):
        """Path: the agent carries no permission rules, missing or as an empty list.

        Mutant GUARD-EMPTY-OFF (the permission-list test removed): the missing subtest raises TypeError
        and exits 4 (4 != 2); the empty subtest is refused for another reason. Mutant
        GUARD-RULES-EMPTY-OFF (only the type is tested) fails the empty subtest on the
        reason text and leaves the missing subtest passing. The runner reports the
        first failing subtest; the mutant's log holds each one.
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
        """
        rules = GRADER_RULES + [{"action": "edit", "resource": "*", "effect": "allow"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "(edit on *: allow) comes after the final deny")

    def test_guard_refuses_an_edit_allowed_for_some_paths(self):
        """Path: a later rule allows edit only under src/, which is still an edit permission.
        """
        rules = GRADER_RULES + [{"action": "edit", "resource": "src/*", "effect": "allow"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "(edit on src/*: allow) comes after the final deny")

    def test_guard_refuses_a_shell_allowed_after_the_deny(self):
        """Path: a later rule allows shell, which the allowlist does not name.

        shell is not in ALLOWED_ACTIONS, so the guard refuses an allow for it after the final deny.
        Mutant: a guard that does not check the action name (GUARD-ACTION-OFF)
        passes this listing, and the test fails.
        """
        rules = GRADER_RULES + [{"action": "shell", "resource": "*", "effect": "allow"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "(shell on *: allow) comes after the final deny")

    def test_guard_refuses_an_ask_for_a_shell_action_after_the_deny(self):
        """Path: a later rule asks before shell, which is neither a deny nor an allowed action.
        """
        rules = GRADER_RULES + [{"action": "shell", "resource": "*", "effect": "ask"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "(shell on *: ask) comes after the final deny")

    def test_guard_refuses_a_bishop_action_outside_the_triage_prefix(self):
        """Path: a bishop-named action that is not bishop-triage_ is allowed after the deny.
        """
        rules = GRADER_RULES + [{"action": "bishop-other_tool", "resource": "*", "effect": "allow"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "(bishop-other_tool on *: allow) comes after the final deny")

    def test_guard_refuses_a_last_covering_rule_that_is_an_allow(self):
        """Path: the last rule covering every action and resource is an allow, not the deny.

        Mutant: GUARD-COVER-DENY-ONLY (only a deny counts as covering) finds no
        covering rule, so the refusal names that instead of rule 4 being an allow,
        and the reason assertion fails.
        """
        rules = [dict(r) for r in GRADER_RULES]
        rules[4]["effect"] = "allow"
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "the last rule covering every action and resource (rule 4) is not a deny")

    def test_guard_refuses_a_listing_with_no_rule_covering_every_action(self):
        """Path: no rule covers every action on every resource, so nothing is denied by default.

        The `**` and `*/*` subtests use a deny that looks like a covering rule
        and is not one, so they must fail closed as well.
        Mutant: GUARD-NOCOVER-OFF (the refusal for no covering rule is removed) passes these listings; each subtest
        fails on the exit code (0 != 2).
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
        Mutant: GUARD-FILE-OFF (the definition's permission block is not checked) accepts this run; the kill is on
        the exit code.
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

    def test_guard_refuses_an_edit_allowed_directly_after_the_final_deny(self):
        """Path: the first rule after the final deny is an allow for edit, in the listing.

        Mutant: a check that starts its loop one rule late (GUARD-FIRST-SKIP)
        never reads this rule and accepts the listing, so the test fails.
        """
        rules = GRADER_RULES[:5] + [{"action": "edit", "resource": "*", "effect": "allow"}] + GRADER_RULES[5:]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "(edit on *: allow) comes after the final deny")

    def test_guard_refuses_an_edit_allowed_directly_after_the_final_deny_in_the_file(self):
        """Path: the first rule after the final deny is an allow for edit, in the definition.

        The listing is the old one. The file's own block puts `edit: allow`
        directly after its `"*": deny`, so the refusal comes from the file check.
        Mutant: the same off-by-one (GUARD-FIRST-SKIP) accepts the file, and the
        test fails.
        """
        self.replace_in_definition("mission-grader", '  "*": deny\n  read:\n',
                                   '  "*": deny\n  edit: allow\n  read:\n')
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertRefused(proc, "definition " + self.definition_path("mission-grader")
                           + ": rule 1 (edit on *: allow) comes after the final deny")

    def test_guard_refuses_a_nested_edit_allow_in_the_definition(self):
        """Path: the definition's nested block `edit:` allows every resource, after the final deny.

        The nested form is read with resource `*`, so the refusal is for `edit on *`.
        Mutant: a parse that drops the nested branch (GUARD-NEST-DROP) reads nothing
        under `edit:` and accepts the file, so the test fails.
        """
        self.replace_in_definition("mission-grader", "  bishop-triage_grade_write: allow\n",
                                   '  bishop-triage_grade_write: allow\n  edit:\n    "*": allow\n')
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertRefused(proc, "(edit on *: allow) comes after the final deny")

    def test_guard_refuses_a_nested_edit_allow_for_some_paths_in_the_definition(self):
        """Path: the nested block allows edit for one resource, src/*, after the final deny.

        Mutant: a parse that reads every nested rule as resource `*` (GUARD-NEST-RES)
        names the wrong resource in the refusal, and the text assertion fails.
        """
        self.replace_in_definition("mission-grader", "  bishop-triage_grade_write: allow\n",
                                   '  bishop-triage_grade_write: allow\n  edit:\n    "src/*": allow\n')
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertRefused(proc, "(edit on src/*: allow) comes after the final deny")

    def test_guard_refuses_a_covering_deny_for_some_resources_only(self):
        """Path: the only rule that names every action is a deny for one resource, src/*.

        A deny for some resources does not cover every resource, so no rule
        denies everything. Mutant: GUARD-COVER-OR (a rule covers when either its
        action or its resource is `*`) takes this deny as the covering rule and
        accepts the listing, exit 0, not 2.

        """
        base = [r for r in GRADER_RULES if not (r["action"] == "*" and r["resource"] == "*")]
        rules = base + [{"action": "*", "resource": "src/*", "effect": "deny"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "no rule denies every action on every resource")

    def test_guard_refuses_a_covering_deny_for_one_action_only(self):
        """Path: the only deny is for one action, read, on every resource.

        A deny for one action does not deny every action, so no rule covers.
        Mutant: GUARD-COVER-OR takes this deny as the covering rule and accepts
        the listing, exit 0, not 2.

        """
        base = [r for r in GRADER_RULES if not (r["action"] == "*" and r["resource"] == "*")]
        rules = base + [{"action": "read", "resource": "*", "effect": "deny"}]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertRefused(proc, "no rule denies every action on every resource")

    def test_guard_refuses_a_permission_block_that_is_an_object(self):
        """Path: the agent's permissions are a JSON object, not a list of rules.

        Mutant: GUARD-RULES-TYPE-OFF (only an empty value is refused) lets the
        object reach the rule check, which cannot read rule 0, so the guard exits
        4, not 2.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES)
        entry["permissions"] = {"read": "allow"}
        proc = self.run_script("grade", [entry])
        self.assertRefused(proc, "agent mission-grader lists no permission rules")


class GuardCannotCheck(TriageRunCase):
    def test_guard_cannot_check_when_opencode_debug_fails(self):
        """Path: `opencode debug agents` exits non-zero, with its stderr, and prints nothing.

        The stderr is logged, and the log says the guard could not check. It
        does not say the agent cannot be resolved, which would name a service
        failure as an agent problem.

        """
        proc = self.run_script("grade", [], debug_rc=1, debug_err="stub: service unreachable")
        self.assertCannotCheck(proc, "exited 1 (stderr: stub: service unreachable)")

    def test_guard_cannot_check_when_opencode_debug_fails_even_with_a_listing(self):
        """Path: `opencode debug agents` exits non-zero but prints a valid listing.

        A failing call is not trusted, whatever it printed.
        Mutant: GUARD-DEBUG-ACCEPT (a non-zero listing is read as success) accepts this run; the kill is on the exit
        code.
        """
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)], debug_rc=1)
        self.assertCannotCheck(proc, "exited 1")

    def test_guard_cannot_check_when_opencode_debug_hangs(self):
        """Path: `opencode debug agents` does not finish within the guard's bound.

        The perl alarm kills the stub at the bound, so the guard exits 4 with a
        timed-out reason. The stub sleeps 600 seconds when hung; the bound here
        is 2 seconds, and the run must end well inside 10 seconds, so a bound
        pinned to some other value (a constant) is seen as a late stop.
        Expected failure under a removed alarm: the stub sleeps on, the test's
        own 30-second subprocess limit is reached, and the test errors.
        Mutant: removing the alarm lets the stub sleep, and this test errors
        (GUARD-TIMEOUT-OFF). A bound pinned to 1 second is not this test's
        control; GUARD-TIMEOUT-SHORT is killed by the slow-listing test.
        """
        started = time.monotonic()
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)],
                               hang=True, guard_timeout=2)
        elapsed = time.monotonic() - started
        self.assertCannotCheck(proc, "timed out after 2s")
        self.assertLess(elapsed, 10, "the listing was stopped %.1f s after it started, not at the 2 s bound" % elapsed)

    def test_guard_retries_an_empty_listing_and_accepts_the_next_one(self):
        """Path: a cold service answers with an empty listing once, then with the agents.

        Mutant GUARD-RETRY-NONE (no retry): the guard exits 4 on the first empty listing.
        """
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)],
                               cold=1, cold_out="")
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())
        self.assertEqual(self.debug_calls(), 2)

    def test_guard_cannot_check_an_empty_listing_after_the_retries(self):
        """Path: the service stays cold, and every listing is empty.

        Mutant GUARD-RETRY-UNBOUNDED (the retries never stop): the test errors on its own limit.
        """
        proc = self.run_script("grade", [], cold=99, cold_out="")
        self.assertCannotCheck(proc, "gave an empty listing on 3 attempts")
        self.assertEqual(self.debug_calls(), 3)

    def test_guard_cannot_check_a_cold_listing_of_no_agents(self):
        """Path: the service stays cold and answers with a listing of no agents, `[]`.
        """
        proc = self.run_script("grade", [], cold=99, cold_out="[]")
        self.assertCannotCheck(proc, "gave an empty listing on 3 attempts")
        self.assertEqual(self.debug_calls(), 3)

    def test_guard_cannot_check_an_unparseable_agent_listing(self):
        """Path: the listing command exits 0 but prints something that is not JSON.
        """
        proc = self.run_script("grade", "not json {")
        self.assertCannotCheck(proc, "the agent listing is not JSON")

    def test_guard_cannot_check_a_definition_line_it_cannot_read(self):
        """Path: a line in our definition's permission block is in a shape the guard cannot read.
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
        Mutant: GUARD-PERL-OFF (the perl check removed) starts the listing through the absent perl. The wrapper exits
        127, the guard logs 'exited 127' and exits 4, and the kill is on the perl-absent reason text. The stub
        is never reached, since perl is not found. The mutant does not run the listing unbounded, so no
        recorded mutant shows that case; this test does not show it either.
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

    def test_guard_cannot_check_without_python3_before_the_listing(self):
        """Path: python3 is not on PATH, so the guard cannot run, and nothing is listed.

        The check runs before the work check, which also uses python3, so the
        exit and its reason come from the guard's own precondition.
        Mutant: a guard without the precondition (GUARD-PY3-OFF) runs the work
        check, which fails on python3 with no log line, and the reason assertion fails.
        """
        bin_dir = os.path.join(self.tmp, "no-python-bin")
        os.makedirs(bin_dir)
        original = os.environ.get("PATH", "/usr/bin:/bin")
        for name in TOOLS_WITHOUT_PYTHON:
            found = shutil.which(name, path=original)
            if found:
                os.symlink(found, os.path.join(bin_dir, name))
        self.assertIsNone(shutil.which("python3", path=bin_dir))
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)], path=bin_dir)
        self.assertCannotCheck(proc, "python3 is not on PATH")
        self.assertEqual(self.debug_calls(), 0)

    def test_guard_cannot_check_a_definition_that_is_missing_at_once(self):
        """Path: the definition file is gone. The guard says so before it asks for the listing.

        Mutant: a guard that asks for the listing before it reads the definition
        (GUARD-DEF-PRECHECK-OFF) calls the stub once, so the call count assertion
        fails. The reason is the guard's own, not a raw traceback.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES)
        os.remove(self.definition_path("mission-grader"))
        proc = self.run_script("grade", [entry])
        self.assertCannotCheck(proc, "cannot read the definition " + self.definition_path("mission-grader")
                               + ": No such file or directory")
        self.assertEqual(self.debug_calls(), 0)

    def test_guard_cannot_check_a_definition_that_is_not_utf8(self):
        """Path: the definition holds bytes that are not UTF-8.

        Mutants: GUARD-DEF-UTF8-OFF (the UnicodeDecodeError clause is removed)
        gives exit 4 with the generic handler's reason, so the reason assertion
        fails. GUARD-DEF-UTF8-LAX (the file is read with errors="replace") reads
        it, the guard goes on to the listing and refuses the entry, and the exit
        code is 2, not 4.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES)
        with open(self.definition_path("mission-grader"), "ab") as fh:
            fh.write(b"\n\xff\n")
        proc = self.run_script("grade", [entry])
        self.assertCannotCheck(proc, "the definition " + self.definition_path("mission-grader") + " is not UTF-8 text")

    def test_guard_cannot_check_a_definition_with_no_frontmatter(self):
        """Path: the definition does not open with its frontmatter delimiter.

        Mutant: GUARD-DEF-FIRST-OFF (the first-line test is dropped) takes the
        file's later delimiter as the end of the frontmatter and the description
        line as its start, so the head lacks the description. The guard exits 4
        with "no quoted description in" and the reason assertion fails; it does
        not accept the file.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES)
        self.replace_in_definition("mission-grader", "---\ndescription:", "description:")
        proc = self.run_script("grade", [entry])
        self.assertCannotCheck(proc, "no frontmatter in")

    def test_guard_cannot_check_a_definition_with_no_closing_delimiter(self):
        """Path: the frontmatter never closes.

        Mutant: a check that drops the closing-delimiter test (GUARD-DEF-CLOSE-OFF)
        calls index() on a missing delimiter, which raises, and the reason
        assertion fails.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES)
        self.replace_in_definition("mission-grader", "  bishop-triage_grade_write: allow\n---\n",
                                   "  bishop-triage_grade_write: allow\n")
        proc = self.run_script("grade", [entry])
        self.assertCannotCheck(proc, "no frontmatter in")

    def test_guard_cannot_check_a_definition_with_no_description(self):
        """Path: the definition has no description line.

        Mutant: a guard that accepts a missing description (GUARD-DEF-DESC-OFF)
        compares None with the listing, refuses with exit 2, and the test fails.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES)
        self.drop_definition_line("mission-grader", "description:")
        proc = self.run_script("grade", [entry])
        self.assertCannotCheck(proc, "no quoted description in")

    def test_guard_cannot_check_a_definition_with_no_mode(self):
        """Path: the definition has no mode line.

        Mutant: a guard that accepts a missing mode (GUARD-DEF-MODE-OFF) compares
        None with the listing, refuses with exit 2, and the test fails.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES)
        self.drop_definition_line("mission-grader", "mode:")
        proc = self.run_script("grade", [entry])
        self.assertCannotCheck(proc, "no mode in")

    def test_guard_cannot_check_a_description_with_an_escaped_quote(self):
        """Path: the description holds an escaped quote, which the line parser does not read.

        Mutant: GUARD-DESC-RE-LOOSE (the description pattern takes any text) reads
        it with its backslashes, the listing's description then differs from it,
        and the guard refuses with exit 2, not 4.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES)
        self.replace_in_definition("mission-grader", "Scheduled grader for finished",
                                   'Scheduled \\"grader\\" for finished')
        proc = self.run_script("grade", [entry])
        self.assertCannotCheck(proc, "no quoted description in")

    def test_guard_cannot_check_a_quoted_mode(self):
        """Path: the mode is quoted, which the line parser does not read.

        Mutant: GUARD-MODE-RE-LOOSE (the mode pattern takes any text) reads it with
        its quotes, the listing's mode then differs from it, and the guard refuses
        with exit 2, not 4.
        """
        entry = self.agent_entry("mission-grader", GRADER_RULES)
        self.replace_in_definition("mission-grader", "mode: primary", 'mode: "primary"')
        proc = self.run_script("grade", [entry])
        self.assertCannotCheck(proc, "no mode in")

    def test_guard_cannot_check_a_listing_that_is_not_a_list(self):
        """Path: the listing is valid JSON, but an object or a scalar rather than a list.

        Two subtests: an object that holds our entry as a value, and a number.
        Mutant: a guard without the list test (GUARD-LIST-OFF) iterates the
        object's keys and refuses with exit 2 ("0 agents named"), and iterates
        the number and exits 4 with a TypeError. Each subtest's own text fails.
        """
        cases = (
            ("object", json.dumps({"mission-grader": self.agent_entry("mission-grader", GRADER_RULES)})),
            ("scalar", "5"),
        )
        for label, text in cases:
            with self.subTest(listing=label):
                proc = self.run_script("grade", text)
                self.assertCannotCheck(proc, "the agent listing is not a list")

    def test_guard_cannot_check_a_rule_that_is_not_an_object(self):
        """Path: one permission rule is a string, not an object.
        """
        self.assert_rule_is_malformed(GRADER_RULES + ["edit"])

    def test_guard_cannot_check_a_rule_whose_action_is_not_a_string(self):
        """Path: one permission rule has a number as its action.
        """
        self.assert_rule_is_malformed(GRADER_RULES + [{"action": 5, "resource": "*", "effect": "allow"}])

    def test_guard_cannot_check_a_rule_whose_resource_is_not_a_string(self):
        """Path: one permission rule has a number as its resource.
        """
        self.assert_rule_is_malformed(GRADER_RULES + [{"action": "read", "resource": 5, "effect": "allow"}])

    def test_guard_cannot_check_a_rule_with_an_unknown_effect(self):
        """Path: one permission rule has an effect that is not allow, ask or deny.
        """
        self.assert_rule_is_malformed(GRADER_RULES + [{"action": "read", "resource": "*", "effect": "maybe"}])

    def test_guard_cannot_check_an_unmatched_quote_in_a_rule(self):
        """Path: a rule's effect opens a quote it does not close, so it is not an effect.
        """
        self.replace_in_definition("mission-grader", "  bishop-triage_grade_write: allow\n",
                                   '  bishop-triage_grade_write: allow\n  edit: "allow\n')
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertCannotCheck(proc, "is malformed")


    def test_guard_accepts_an_indented_comment_in_the_permission_block(self):
        """Path: a comment indented inside the permission block is skipped, and the file is accepted.

        Mutant: GUARD-COMMENT-INDENT, which skips only column-0 comments, so the
        indented comment is a line the parser cannot read and the file is CannotCheck.
        """
        self.replace_in_definition("mission-grader", "  bishop-triage_grade_write: allow\n",
                                   "  bishop-triage_grade_write: allow\n  # why\n")
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_refuses_an_edit_allow_after_a_column_zero_comment_in_the_permission_block(self):
        """Path: a comment at column 0 sits inside the permission block, with an edit allow after it.

        The comment is skipped and does not end the block, so the edit allow after
        it is read, and the file is refused for granting edit after the final deny.
        Mutant: GUARD-COMMENT-COL0, which does not skip a column-0 comment, so the
        block is CannotCheck and the test fails on the exit code.
        """
        self.replace_in_definition("mission-grader", "  bishop-triage_grade_write: allow\n",
                                   "  bishop-triage_grade_write: allow\n# why\n  edit: allow\n")
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertRefused(proc, "(edit on *: allow) comes after the final deny")

    def test_guard_cannot_check_a_column_zero_line_in_the_permission_block_that_is_not_a_key(self):
        """Path: a column-0 line in the permission block that is not a top-level key.

        A quoted key at column 0 does not end the permission block, so the block is
        CannotCheck and the file is not read as if its last rule were absent.
        Mutant: GUARD-TOPKEY-OFF, which ends the block at any column-0 line.
        """
        self.replace_in_definition("mission-grader", "  bishop-triage_grade_write: allow\n",
                                   '  bishop-triage_grade_write: allow\n"edit": allow\n')
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertCannotCheck(proc, "cannot read permission line")

    def test_guard_cannot_check_a_tab_indented_rule_in_the_permission_block(self):
        """Path: a rule indented with two tabs inside the permission block.

        YAML does not allow tabs for indentation. The line is not read and the
        block is CannotCheck.

        """
        self.replace_in_definition("mission-grader", "  bishop-triage_grade_write: allow\n",
                                   "  bishop-triage_grade_write: allow\n\t\tedit: allow\n")
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertCannotCheck(proc, "cannot read permission line")

    def test_guard_cannot_check_an_odd_indent_in_the_permission_block(self):
        """Path: a rule indented by three spaces, which is neither the action nor the resource indent.
        """
        self.replace_in_definition("mission-grader", "  bishop-triage_grade_write: allow\n",
                                   "  bishop-triage_grade_write: allow\n   edit: allow\n")
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertCannotCheck(proc, "cannot read permission line")

    def test_guard_cannot_check_a_nested_permission_line_with_no_value(self):
        """Path: a nested resource line under edit has no effect after its colon.
        """
        self.replace_in_definition("mission-grader", "  bishop-triage_grade_write: allow\n",
                                   '  bishop-triage_grade_write: allow\n  edit:\n    "*":\n')
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertCannotCheck(proc, "cannot read permission line")

    def test_guard_cannot_check_a_nested_permission_line_with_no_action(self):
        """Path: a line indented four spaces appears directly under permission, with no action above it.
        """
        self.replace_in_definition("mission-grader", "permission:\n", "permission:\n    edit: allow\n")
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)])
        self.assertCannotCheck(proc, "cannot read permission line")

    def test_guard_retries_a_cold_listing_of_only_whitespace(self):
        """Path: a cold service answers with whitespace only, which is an empty listing.

        Mutant: a guard that does not strip whitespace before its empty test
        (GUARD-STRIP-NONE) reads the whitespace as a listing, the parse fails with
        "not JSON", and the empty-listing reason assertion fails.
        """
        proc = self.run_script("grade", [], cold=99, cold_out="\n  \n")
        self.assertCannotCheck(proc, "gave an empty listing on 3 attempts")
        self.assertEqual(self.debug_calls(), 3)

    def test_guard_cannot_check_a_listing_that_is_not_utf8(self):
        """Path: the listing holds bytes that are not UTF-8.

        Mutant: GUARD-LISTING-UTF8-LAX (the listing is decoded with the "replace"
        handler) turns the bad byte into a character, finds no agent named, and
        refuses with exit 2, not 4.
        """
        proc = self.run_script("grade", b'[{"name": "\xff"}]')
        self.assertCannotCheck(proc, "UnicodeDecodeError")

    def assert_rule_is_malformed(self, rules):
        """
        Shared check for the rule-shape tests: the listing's permissions hold a
        rule the shape check rejects, and the guard exits 4 with the rule named
        as malformed.
        """
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertCannotCheck(proc, "is malformed")


class GuardTimeoutValue(TriageRunCase):
    def test_guard_timeout_that_is_not_a_whole_number_of_seconds_exits_64(self):
        """Path: TRIAGE_GUARD_TIMEOUT_SEC is 0, 1.5, abc, 30x or -1. None of these is a
        whole number of seconds at least 1.

        Each subtest exits 64 with the usage message, before any listing is asked for.
        Mutant: a bound check that accepts 0 (GUARD-TMO-LOOSE) runs the guard for
        "0" and exits 0, so that subtest fails.
        """
        for value in ("0", "1.5", "abc", "30x", "-1"):
            with self.subTest(value=value):
                if os.path.exists(self.run_args):
                    os.remove(self.run_args)
                if os.path.exists(self.debug_count):
                    os.remove(self.debug_count)
                proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)],
                                       guard_timeout=value)
                self.assertEqual(proc.returncode, 64, proc.stderr + self.log())
                self.assertIn("TRIAGE_GUARD_TIMEOUT_SEC must be a whole number of seconds", proc.stderr)
                self.assertEqual(self.debug_calls(), 0)
                self.assertFalse(self.ran())

    def test_guard_timeout_is_checked_only_for_the_opencode_engine(self):
        """Path: on the claude engine the guard does not run, so a bad guard timeout is not a usage error.

        The run stops at the claude CLI check with exit 2.
        Mutant: a check that runs on every engine (GUARD-TMO-ENGINE-ALWAYS) exits
        64 here, and the exit assertion fails.
        """
        proc = self.run_claude("0")
        self.assertEqual(proc.returncode, 2, proc.stderr + self.log())
        self.assertIn("claude CLI not found", self.log())

    def test_guard_timeout_that_is_bad_is_refused_on_the_opencode_engine(self):
        """Path: on the opencode engine a bad guard timeout is refused at once, before the listing.

        Mutant: a check that is off for opencode (GUARD-TMO-ENGINE-NEVER) lets the
        bad value through, the run proceeds to the listing, and the exit assertion
        fails.
        """
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)], guard_timeout="0")
        self.assertEqual(proc.returncode, 64, proc.stderr + self.log())
        self.assertEqual(self.debug_calls(), 0)


class GuardPositiveControl(TriageRunCase):
    def test_guard_accepts_an_edit_allow_directly_before_the_final_deny(self):
        """Path: an edit allow directly before the final deny is overridden by it, so the agent is still denied.

        This is the opposite edge of the first-rule refusals: the rule the loop
        must not read is the one just before the deny, not the one just after.

        """
        rules = GRADER_RULES[:4] + [{"action": "edit", "resource": "*", "effect": "allow"}] + GRADER_RULES[4:]
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())

    def test_guard_accepts_an_edit_allow_that_a_later_deny_overrides(self):
        """Path: an edit allow that comes before the final deny is overridden, so the agent is still denied.

        This is the positive control for the edit refusals above. A guard that
        refuses every edit allow, wherever it sits, fails this test.
        """
        rules = [{"action": "edit", "resource": "*", "effect": "allow"}] + GRADER_RULES
        proc = self.run_script("grade", [self.agent_entry("mission-grader", rules)])
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertTrue(self.ran())


class LayeredConfig(TriageRunCase):
    """
    The config the opencode run layers over the user's own, which the script
    hands to OpenCode in OPENCODE_CONFIG_CONTENT. Each test starts the copied
    script on its main path (`run_script`, the stub OPENCODE_BIN) and reads back
    the text the stub received in that variable. The one builder serves classify,
    process and grade, so each test runs all three kinds.
    """

    def test_run_declares_bishop_triage_as_a_v2_server_outside_code_mode(self):
        """Path: the config for each kind is parsed, and the one MCP server is read as OpenCode v2 reads it.

        v2 reads MCP servers from `mcp.servers` and routes a server's tools
        through Code Mode unless `codemode` is false (so the model cannot call
        them by name). The legacy shape `mcp.<name>` is rewritten into
        `mcp.servers` by OpenCode, and the rewrite drops `codemode`. Observed on
        2.0.32: the config listing carries `disabled` and drops `enabled`
        (LayeredConfigRealOpenCode.test_real_opencode_lists_disabled_and_drops_enabled),
        so the two assertions on those keys keep a dead key and a disabled flag
        out of the config; not observed: what a live session does with
        `disabled`. Each assertion carries its own message. Mutants, each with its mirror: GUARD-MCP-LEGACY-SHAPE and
        GUARD-MCP-LEGACY-ALSO (the shape); GUARD-MCP-CODEMODE-DROP and
        GUARD-MCP-CODEMODE-TRUE (codemode); GUARD-MCP-ENABLED-KEY and
        GUARD-MCP-DISABLED-TRUE (the server is on); GUARD-MCP-COMMAND-STR and
        GUARD-MCP-COMMAND-ARG (the command); GUARD-MCP-PROFILE-OFF and
        GUARD-MCP-PROFILE-FULL (the triage profile); GUARD-MCP-URL-OFF and
        GUARD-MCP-URL-DEFAULT (the service URL). The server's `type` and the
        BISHOP_HARNESS value are not asserted, and have no row.
        """
        mcpd = os.path.join(self.root, "bin", "mcpd")
        for kind in KIND_AGENT:
            with self.subTest(kind=kind):
                mcp = self.generated_config(kind)["mcp"]
                self.assertEqual(sorted(mcp), ["servers"], "kind=%s: mcp holds keys other than the v2 `servers` map" % kind)
                self.assertEqual(sorted(mcp["servers"]), ["bishop-triage"],
                                 "kind=%s: mcp.servers is not the one bishop-triage server" % kind)
                server = mcp["servers"]["bishop-triage"]
                self.assertIn("codemode", server, "kind=%s: the server sets no codemode, so v2 routes its tools through Code Mode" % kind)
                self.assertIs(server["codemode"], False, "kind=%s: codemode is not the JSON false" % kind)
                self.assertNotIn("enabled", server, "kind=%s: the server carries an `enabled` key, which the 2.0.32 listing drops (observed)" % kind)
                self.assertIsNot(server.get("disabled"), True, "kind=%s: the server is disabled" % kind)
                self.assertIsInstance(server["command"], list, "kind=%s: command is not an argv list" % kind)
                self.assertEqual([os.path.realpath(c) for c in server["command"]], [os.path.realpath(mcpd)],
                                 "kind=%s: command is not the mcpd path alone" % kind)
                self.assertEqual(server["environment"].get("MCPD_PROFILE"), "triage",
                                 "kind=%s: the server is not started in the triage profile" % kind)
                self.assertEqual(server["environment"].get("BISHOP_MEMORY_URL"), self.url,
                                 "kind=%s: the server is not given the run's service URL" % kind)


class LayeredConfigRealOpenCode(TriageRunCase):
    """
    The layered config against the real OpenCode binary, with no model and no
    session. `opencode api --standalone GET /api/config` starts a private
    server, lists the config documents it loaded, and exits. It runs with HOME
    and the working directory in the test's temporary directory, so none of the
    operator's own config, plugins or keys is read, and the no-op mcpd of the
    temporary root is the only command the config names. The invocation and the
    classification of its answer are in scripts/opencode_config_probe.py, whose
    branches ConfigProbeClassification drives with a stub binary. A host with no
    opencode skips these tests as "could not check".
    """

    def real_opencode(self):
        """The real opencode (OPENCODE_BIN of the test's own environment, else `opencode`) resolved on PATH, or None."""
        return shutil.which(os.environ.get("OPENCODE_BIN") or "opencode")

    def probe(self, config, agent, steps):
        """
        Run the probe on `config` and return the status and reason. Calls
        opencode_config_probe.probe_config, the entry point the tests below share,
        with the real binary. The `infos` it returns are in `self.infos`.
        """
        status, reason, self.infos = probe.probe_config(self.real_opencode(), config, agent, steps, self.tmp)
        return status, reason

    def checked_infos(self, config, agent, steps):
        """The `info` of each document carrying the step cap. A check that could not be made skips the test; a check that found the premise false fails it."""
        status, reason = self.probe(config, agent, steps)
        if status == probe.COULD_NOT_CHECK:
            self.skipTest("could not check: " + reason)
        if status == probe.FAILED:
            self.fail(reason)
        return self.infos

    def test_real_opencode_keeps_codemode_off_in_the_config_the_run_builds(self):
        """Path: the grade run's config (built by the script, read back from the stub) is loaded by `opencode api --standalone GET /api/config`.

        The config document is found by the step cap the script set for the
        grader, the positive control that the binary read OPENCODE_CONFIG_CONTENT
        at all. A binary that ran and listed no such document, or that refused
        the config with a non-zero exit its own empty-config run does not
        share, fails the test. Without a real opencode, or when it cannot be
        started, times out, prints something that is not JSON, or exits non-zero
        on an empty config too, the check could not be made and the test is
        skipped with that reason (DIR-002). The classification is
        opencode_config_probe.probe_config; ConfigProbeClassification drives each
        of its branches. The bishop-triage server the binary lists must report
        `codemode` false and must not be disabled. With the legacy `mcp.<name>`
        shape the binary reports the server with no codemode, and the first
        assertion fails. Mutant: GUARD-MCP-LEGACY-SHAPE.
        """
        config = self.generated_config("grade")
        steps = config["agent"]["mission-grader"]["steps"]
        for info in self.checked_infos(config, "mission-grader", steps):
            server = info["mcp"]["servers"]["bishop-triage"]
            self.assertIs(server.get("codemode"), False, "the binary reports codemode %r for the bishop-triage server" % (server.get("codemode"),))
            self.assertIsNot(server.get("disabled"), True, "the binary reports the bishop-triage server as disabled")

    def test_real_opencode_lists_disabled_and_drops_enabled(self):
        """Path: a config that gives one server both `enabled` false and `disabled` true goes through `opencode api --standalone GET /api/config`.

        Observed on 2.0.32: the listing carries `disabled` and carries no
        `enabled`, whatever its value. That is all this pins. What a live
        session does with `disabled` is not observed here. The `disabled`
        assertion is the positive control for the absence of `enabled`: the
        server entry reached the listing, and only `enabled` did not. The config
        is the probe's own, not the script's, so no mutant of ours bears on it;
        the test fails only if a later OpenCode changes what it lists.
        """
        mcpd = os.path.join(self.root, "bin", "mcpd")
        config = {"agent": {"probe-agent": {"steps": 7}},
                  "mcp": {"servers": {"s": {"type": "local", "command": [mcpd], "codemode": False,
                                            "enabled": False, "disabled": True}}}}
        for info in self.checked_infos(config, "probe-agent", 7):
            server = info["mcp"]["servers"]["s"]
            self.assertIs(server.get("disabled"), True, "the binary does not list `disabled` for the server it was given")
            self.assertNotIn("enabled", server, "the binary lists an `enabled` key for the server")

    def test_real_opencode_that_drops_the_config_is_a_failure_and_not_a_skip(self):
        """Path: two configs go through the probe, one the binary carries and one it drops with exit 0.

        Observed on 2.0.32: a config whose agent `steps` is the string "seven"
        exits 0 and lists no document with that agent, and no config is refused
        with a non-zero exit. The control is the same call with a numeric
        `steps`, which must come back CHECKED: a probe that reports every call as
        FAILED fails here, not only a probe that reports none. A host with no
        opencode skips the test. Mutants: GUARD-OCPROBE-NOMATCH-SKIP and
        GUARD-OCPROBE-MATCH-NONE.
        """
        carried = {"agent": {"probe-agent": {"steps": 7}}}
        dropped = {"agent": {"probe-agent": {"steps": "seven"}}}
        status, reason = self.probe(carried, "probe-agent", 7)
        if status == probe.COULD_NOT_CHECK:
            self.skipTest("could not check: " + reason)
        self.assertEqual(status, probe.CHECKED, "the control config was not carried: " + reason)
        status, reason = self.probe(dropped, "probe-agent", "seven")
        self.assertEqual(status, probe.FAILED, "a config the binary drops with exit 0 was not reported as a failure: " + reason)
        self.assertIn("did not carry OPENCODE_CONFIG_CONTENT", reason)


class ConfigProbeClassification(unittest.TestCase):
    """
    Each branch of opencode_config_probe.probe_config, driven by a stub in place
    of the opencode binary. The stub answers one way for the layered config and
    another for the empty control config (it tells them apart by the value of
    OPENCODE_CONFIG_CONTENT), so every case fixes both. These tests call
    probe_config directly, with a stub path; nothing here runs the real opencode.
    """

    AGENT = "probe-agent"
    STEPS = 7
    CONFIG = {"agent": {AGENT: {"steps": STEPS}}}
    OK = "printf '%s' '[{\"info\": {\"agents\": {\"probe-agent\": {\"steps\": 7}}}}]'"
    OTHER_STEPS = "printf '%s' '[{\"info\": {\"agents\": {\"probe-agent\": {\"steps\": 8}}}}]'"
    TWICE = ("printf '%s' '[{\"info\": {\"agents\": {\"probe-agent\": {\"steps\": 7}}}},"
             " {\"info\": {\"agents\": {\"probe-agent\": {\"steps\": 7}}}}]'")
    EMPTY_LIST = "echo '[]'"
    NOT_A_LIST = "echo '{}'"
    NOT_JSON = "echo x"
    REFUSES = "echo 'refused: bad mcp' >&2; exit 3"
    BROKEN = "echo 'no writable home' >&2; exit 3"
    CONTROL_BROKEN = "echo 'control could not start' >&2; exit 4"
    HANGS = "exec sleep 600"

    # name, layered-config answer, control answer, status, strings the reason holds, documents returned
    CASES = (
        ("carried", OK, EMPTY_LIST, probe.CHECKED, (), 1),
        ("carried-by-two-documents", TWICE, EMPTY_LIST, probe.CHECKED, (), 2),
        ("empty-list", EMPTY_LIST, EMPTY_LIST, probe.FAILED, ("lists no document", "documents listed: 0"), 0),
        ("other-step-cap", OTHER_STEPS, EMPTY_LIST, probe.FAILED, ("lists no document with step cap 7",), 0),
        ("not-a-list", NOT_A_LIST, EMPTY_LIST, probe.FAILED, ("lists no document", "documents listed: dict"), 0),
        ("not-json", NOT_JSON, EMPTY_LIST, probe.COULD_NOT_CHECK, ("not JSON", "'x\\n'"), 0),
        ("refused-where-control-runs", REFUSES, EMPTY_LIST, probe.FAILED, ("exited 3", "refused the layered config", "refused: bad mcp"), 0),
        ("exit-where-control-exits", BROKEN, CONTROL_BROKEN, probe.COULD_NOT_CHECK,
         ("exited 3", "empty config it exited 4", "does not run on this host", "no writable home", "control could not start"), 0),
        ("exit-where-control-hangs", REFUSES, HANGS, probe.COULD_NOT_CHECK, ("exited 3", "did not answer within 1 seconds"), 0),
        ("no-answer", HANGS, EMPTY_LIST, probe.COULD_NOT_CHECK, ("did not answer within 1 seconds",), 0),
    )

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="config-probe-test-")
        self.count = 0

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def stub(self, layered, control):
        """The path of a stub that runs `layered` for the layered config and `control` for the empty one."""
        self.count += 1
        path = os.path.join(self.tmp, "stub-%d" % self.count)
        with open(path, "w") as fh:
            fh.write('#!/bin/sh\ncase "$OPENCODE_CONFIG_CONTENT" in\n  "%s") %s ;;\n  *) %s ;;\nesac\n'
                     % (probe.CONTROL_CONFIG, control, layered))
        os.chmod(path, 0o755)
        return path

    def test_probe_config_classifies_each_answer_of_the_binary(self):
        """Path: probe_config is called with a stub for each answer in CASES, one subtest each.

        Each case names the answer to the layered config and to the control, and
        the status, the reason text and the document count expected. The case
        name is in each assertion's first line, so a failure says which case. The CHECKED
        cases are the positive controls for the rest: a probe that reports every
        call as FAILED or as COULD_NOT_CHECK fails them. Mutants, each with its
        mirror: GUARD-OCPROBE-NONZERO-SKIP and GUARD-OCPROBE-NONZERO-FAIL (a
        non-zero exit, with the control); GUARD-OCPROBE-NOMATCH-SKIP and
        GUARD-OCPROBE-NOTJSON-FAIL (JSON that lists no document, and output
        that is not JSON); GUARD-OCPROBE-MATCH-ANY and GUARD-OCPROBE-MATCH-NONE
        (the step cap that finds the document); GUARD-OCPROBE-ONE-EXACT and
        GUARD-OCPROBE-ZERO-OK (how many documents may carry it);
        GUARD-OCPROBE-TIMEOUT-REFUSED and GUARD-OCPROBE-UNSTARTABLE-REFUSED (no
        answer read as a refusal here, no start read as one in the next test);
        GUARD-OCPROBE-STDERR-OFF and GUARD-OCPROBE-CONTROL-STDERR-OFF (the
        stderr in the reasons).
        """
        for name, layered, control, status, strings, documents in self.CASES:
            with self.subTest(case=name):
                got, reason, infos = probe.probe_config(self.stub(layered, control), self.CONFIG, self.AGENT,
                                                        self.STEPS, self.tmp, timeout=1)
                self.assertEqual((name, got), (name, status), "reason: " + reason)
                for text in strings:
                    self.assertIn(text, reason, "case=%s: the reason does not say %r" % (name, text))
                self.assertEqual((name, len(infos)), (name, documents), "documents returned")

    def test_probe_config_could_not_check_without_a_usable_binary(self):
        """Path: probe_config is called with no binary, and with a path that is a file but not executable.

        Neither is reported as a pass or a failure of the config. Mutants:
        GUARD-OCPROBE-NOBIN-OFF and GUARD-OCPROBE-NOBIN-ALWAYS (no binary);
        GUARD-OCPROBE-UNSTARTABLE-REFUSED (the file that cannot be started).
        """
        path = os.path.join(self.tmp, "not-executable")
        with open(path, "w") as fh:
            fh.write("#!/bin/sh\n")
        os.chmod(path, 0o644)
        for name, binary, text in (("none", None, "no opencode on PATH"), ("not-executable", path, "api --standalone could not be started")):
            with self.subTest(binary=name):
                got, reason, infos = probe.probe_config(binary, self.CONFIG, self.AGENT, self.STEPS, self.tmp, timeout=1)
                self.assertEqual((name, got), (name, probe.COULD_NOT_CHECK), "reason: " + reason)
                self.assertIn(text, reason, "binary=%s: the reason does not say %r" % (name, text))
                self.assertEqual((name, infos), (name, []), "documents returned")


ERROR_EVENT = (
    '{"type":"error","error":{"type":"unknown",'
    '"message":"Agent not found: \\"mission-grader\\""}}\n'
)


class RunReportsErrors(TriageRunCase):
    def test_run_error_event_message_reaches_the_log(self):
        """Path: the run exits 0 but its stream holds a v2 error event; the summary reads error.message.

        This models an exit-0 stream with an error event, which OpenCode v2 does
        not produce (it exits 1; see the next test). It checks the summary parser
        alone.
        """
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)],
                               run_out=ERROR_EVENT, run_rc=0)
        self.assertEqual(proc.returncode, 1, proc.stderr + self.log())
        self.assertIn("Agent not found", self.log())

    def test_run_nonzero_exit_points_at_the_result_file(self):
        """Path: OpenCode v2 exits 1 on an error event, so the summary is not read.

        The log names the result file that holds the error, and exit is 1.

        """
        proc = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)],
                               run_out=ERROR_EVENT, run_rc=1)
        self.assertEqual(proc.returncode, 1, proc.stderr + self.log())
        self.assertIn("agent exited 1; see", self.log())


class FlagAndFilePrecedence(TriageRunCase):
    """
    The order of authority for a setting is the flag, then the environment, then
    the env file, then the default. Each test runs the script through the grade
    kind on the opencode engine with --dry-run, so the model it would hand to
    OpenCode, and whether it would run at all, show on stderr and in the stub.
    """

    def dry_run(self, **kwargs):
        """Run `grade --dry-run` against the accepted grader listing; kwargs go to run_script."""
        flags = tuple(kwargs.pop("flags", ())) + ("--dry-run",)
        return self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)], flags=flags, **kwargs)

    def test_run_takes_a_setting_from_the_env_file_when_nothing_else_sets_it(self):
        """Path: the env file sets TRIAGE_GRADE_MODEL, and no flag or environment value does.

        This is the positive control for the other tests here: the file is read,
        and its value reaches the command. Mutant: GUARD-PREC-FILE-IGNORED (the
        file is not sourced) leaves the default model, and the assertion fails.
        """
        proc = self.dry_run(env_file_text="TRIAGE_GRADE_MODEL=opencode-go/from-file\n")
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertIn("--model  opencode-go/from-file", proc.stderr)

    def test_run_prefers_the_environment_to_the_env_file(self):
        """Path: TRIAGE_GRADE_MODEL is set in the environment and in the env file, with different values.

        The environment's value reaches the command and the file's does not.
        Mutant: GUARD-PREC-FILE-OVER-ENV (the snapshot of the environment is not
        restored after the file is sourced) lets the file's value win, and the
        assertion fails.
        """
        proc = self.dry_run(env_file_text="TRIAGE_GRADE_MODEL=opencode-go/from-file\n",
                            extra_env={"TRIAGE_GRADE_MODEL": "opencode-go/from-env"})
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertIn("--model  opencode-go/from-env", proc.stderr)
        self.assertNotIn("from-file", proc.stderr)

    def test_run_prefers_the_model_flag_to_the_environment(self):
        """Path: --model and TRIAGE_GRADE_MODEL are both given, with different values.

        The flag's value reaches the command and the environment's does not.
        Mutant: GUARD-PREC-ENV-OVER-FLAG (the environment is read before the flag
        in the model default) lets the environment win, and the assertion fails.
        """
        proc = self.dry_run(flags=("--model", "opencode-go/from-flag"),
                            extra_env={"TRIAGE_GRADE_MODEL": "opencode-go/from-env"})
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertIn("--model  opencode-go/from-flag", proc.stderr)
        self.assertNotIn("from-env", proc.stderr)

    def test_run_prefers_every_flag_to_the_same_name_in_the_env_file(self):
        """Path: the env file sets the names the flags are read into, and the command line gives the flags.

        The file says MODEL, LIMIT, DRY_RUN=0 and ENGINE_FLAG=claude. The flags
        say --model, --limit, --dry-run and (from run_script) --engine opencode.
        Every flag wins: the command carries the flag's model and limit, it is a
        dry run so the stub is not called, and the engine is opencode. Mutant:
        GUARD-PREC-FILE-OVER-FLAG (the file is sourced again after the flags are
        read) lets the file win, and the test fails on the exit code, 2 for the
        claude engine.
        """
        proc = self.dry_run(
            env_file_text="MODEL=opencode-go/from-file\nLIMIT=3\nDRY_RUN=0\nENGINE_FLAG=claude\n",
            flags=("--model", "opencode-go/from-flag", "--limit", "7"))
        self.assertEqual(proc.returncode, 0, proc.stderr + self.log())
        self.assertIn("--model  opencode-go/from-flag", proc.stderr)
        self.assertIn("limit=7", proc.stderr)
        self.assertNotIn("from-file", proc.stderr)
        self.assertFalse(self.ran(), "the run stub was called on a dry run")


class HelpText(TriageRunCase):
    def test_help_is_answered_before_the_env_file_is_sourced(self):
        """Path: --help as the first argument prints the help before the .env file is sourced.

        The env file here exits the shell when sourced. Help must still print and
        exit 0, so help has no side effects and works with a broken .env. Mutant:
        GUARD-HELP-OFF (no argument is looked at before the env file) sources the
        file first, and the shell exits 7, not 0.

        """
        env_file = os.path.join(self.tmp, "broken.env")
        with open(env_file, "w", encoding="utf8") as fh:
            fh.write("exit 7\n")
        env = self.base_env()
        env["TRIAGE_ENV_FILE"] = env_file
        proc = subprocess.run(["/bin/bash", self.script, "--help"], env=env, stdin=subprocess.DEVNULL,
                              capture_output=True, text=True, timeout=30)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("Exit codes:", proc.stdout)

    def test_help_as_the_first_argument_prints_the_exit_codes(self):
        """Path: `--help` as the first argument prints the header through its Exit codes paragraph.
        """
        proc = self.run_help("--help")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("Exit codes:", proc.stdout)
        self.assertIn("64 usage error.", proc.stdout)

    def test_help_alone_in_either_spelling(self):
        """Path: `--help` and `-h`, each as the only argument, run through run_help (the copied script as a subprocess).

        The two exit codes are compared as one pair, so the message of a failure
        says which spelling failed. Mutants, each first killed here with its own
        pair: GUARD-HELP-SKIP-FIRST (the first argument is not looked at, so
        both fail), GUARD-HELP-H-OFF (`-h` is not help) and GUARD-HELP-LONG-OFF
        (`--help` is not help).
        """
        codes = tuple(self.run_help(arg).returncode for arg in ("--help", "-h"))
        self.assertEqual(codes, (0, 0), "exit codes of `--help` alone and `-h` alone")

    def test_help_after_a_kind_prints_the_exit_codes(self):
        """Path: `grade --help` prints the same header, through the Exit codes paragraph.
        """
        proc = self.run_help("grade", "--help")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("Exit codes:", proc.stdout)
        self.assertIn("64 usage error.", proc.stdout)

    def test_help_wins_wherever_it_stands_and_does_nothing_else(self):
        """Path: `--help` or `-h` after another argument, as the value of a flag, or `-h` alone.

        Each argument list runs the script as a subprocess, and must print the
        help on stdout, exit 0, write nothing to stderr, and do nothing else: the
        env file is not sourced (it writes a marker file when it is), the log
        directory is not created, and the OpenCode stub is called for neither
        `debug` nor `run`. The same env file, log directory and stub are first
        used by a run that is not help. That run is the positive control for each
        of those absences: the marker is written, the log directory exists, and
        the stub's `debug` and `run` are called.

        Mutants, each paired with its mirror: GUARD-HELP-FIRST-TWO (only the
        first two arguments are looked at) and GUARD-HELP-SKIP-FIRST (the first
        argument is not looked at); GUARD-HELP-H-OFF (`-h` is not help) and
        GUARD-HELP-LONG-OFF (`--help` is not help); GUARD-HELP-SOURCES-ENV (help
        sources the env file) and GUARD-HELP-LOG-DIR (help creates the log
        directory); GUARD-HELP-DEBUG-CALL (help calls the stub for `debug`) and
        GUARD-HELP-RUN-CALL (help calls the stub for `run`).
        """
        marker = os.path.join(self.tmp, "env-file-was-sourced")
        env_file = os.path.join(self.tmp, "marking.env")
        with open(env_file, "w", encoding="utf8") as fh:
            fh.write("echo sourced > '%s'\n" % marker)
        os.chmod(env_file, 0o600)

        def reset():
            """Remove every trace a run leaves, so each argument list starts from none."""
            for path in (marker, self.run_args, self.debug_count):
                if os.path.exists(path):
                    os.remove(path)
            shutil.rmtree(self.log_dir, ignore_errors=True)

        control = self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)],
                                  extra_env={"TRIAGE_ENV_FILE": env_file})
        self.assertEqual(control.returncode, 0, "control: " + control.stderr + self.log())
        self.assertTrue(os.path.exists(marker), "control: a run that is not help did not source the env file")
        self.assertTrue(os.path.isdir(self.log_dir), "control: a run that is not help did not create the log directory")
        self.assertGreaterEqual(self.debug_calls(), 1, "control: a run that is not help did not call the stub for `debug`")
        self.assertTrue(self.ran(), "control: a run that is not help did not call the stub for `run`")

        env = self.base_env()
        env["TRIAGE_ENV_FILE"] = env_file
        for argv in (("--bogus", "--help"), ("grade", "--bogus", "--help"),
                     ("--model", "--help"), ("grade", "--model", "--help"),
                     ("-h",), ("grade", "-h")):
            with self.subTest(argv=" ".join(argv)):
                reset()
                proc = subprocess.run(["/bin/bash", self.script, *argv], env=env, stdin=subprocess.DEVNULL,
                                      capture_output=True, text=True, timeout=30)
                self.assertEqual(proc.returncode, 0, "argv=%s: %s" % (argv, proc.stderr))
                self.assertIn("Exit codes:", proc.stdout, "argv=%s: no help on stdout" % (argv,))
                self.assertEqual(proc.stderr, "", "argv=%s: help wrote to stderr" % (argv,))
                self.assertFalse(os.path.exists(marker), "argv=%s: help sourced the env file" % (argv,))
                self.assertFalse(os.path.exists(self.log_dir), "argv=%s: help created the log directory" % (argv,))
                self.assertEqual(self.debug_calls(), 0, "argv=%s: help called the stub for `debug`" % (argv,))
                self.assertFalse(self.ran(), "argv=%s: help called the stub for `run`" % (argv,))


class GradeService(FakeMemory):
    """
    The bishop-memory routes the --mission path reads, beyond FakeMemory's. The
    HUD list answers from the server's `hud` pair, (status, text). A status of 0
    drops the connection with no answer, as a service that went away would. DELETE
    answers 200 and is recorded in `deletes`; every GET path is recorded in `gets`.
    """

    def do_GET(self):
        self.server.gets.append(self.path)
        if self.path.split("?", 1)[0] != "/v1/hud/missions":
            return super().do_GET()
        status, text = self.server.hud
        if status == 0:
            self.close_connection = True
            return
        payload = text.encode("utf8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_DELETE(self):
        self.server.deletes.append(self.path)
        payload = json.dumps({"mission_id": self.path.rsplit("/", 1)[-1], "deleted": True}).encode("utf8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def hud_answer(states):
    """The HUD list's JSON for a {mission id: grade_state} map, as the service writes it."""
    return json.dumps({"missions": [{"id": m, "grade_state": s} for m, s in states.items()], "harnesses": []})


class GradeExcluded(TriageRunCase):
    """
    The --mission path of the grade kind, with excluded (N/A) missions. The HUD
    list answers from GradeService; the grader listing is the accepted one, so a
    run reaches the run stub unless the script refuses first. Each test asserts one
    path, so a failure names that path alone. The HUD stub answers graded, excluded
    and ungraded rows alike, so the script must choose the excluded ones itself.
    """

    def serve_hud(self, status=200, text=None, states=None):
        """
        Replace the setUp service with a GradeService whose HUD list answers as
        given: `text` verbatim with `status`, or else the JSON for `states` with 200.
        The setUp service is shut down here; tearDown shuts this one down.
        """
        self.server.shutdown()
        self.server.server_close()
        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), GradeService)
        self.server.hud = (status, hud_answer(states or {}) if text is None else text)
        self.server.deletes = []
        self.server.gets = []
        self.url = "http://127.0.0.1:%d" % self.server.server_address[1]
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def grade(self, *flags):
        """Run the grade kind on the opencode engine, with the accepted grader listing and the given flags."""
        return self.run_script("grade", [self.agent_entry("mission-grader", GRADER_RULES)], flags=flags)

    def regrade_b_excluded(self):
        """Regrade A, B and C where B is excluded, A is graded and C is ungraded. Returns the process."""
        self.serve_hud(states={"A": "graded", "B": "excluded", "C": None})
        return self.grade("--mission", "A,B,C", "--regrade")

    def log_text(self):
        """The runner's log with each line's timestamp removed, so a message built from it reads the same on every run."""
        return " | ".join(re.sub(r"^\S+ \S+ UTC ", "", line) for line in self.log().splitlines())

    def skips(self):
        """The mission ids the log names as skipped for being excluded, in log order."""
        return re.findall(r"\[grade\] skipping (\S+): excluded \(N/A\) by the operator", self.log())

    def claimed(self):
        """The mission ids the grader was asked to claim, or None when no grader was started."""
        if not self.ran():
            return None
        for line in self.args():
            found = re.search(r"mission_ids=(\[[^\]]*\])", line)
            if found:
                return json.loads(found.group(1))
        return None

    def assert_lookup_failure(self, proc):
        """
        The could-not-check outcome for a lookup that failed: the HUD list was asked
        for, the run exited 4 with the could-not-check line in the log, and nothing
        was deleted or claimed. The HUD request is asserted first, so a pass cannot
        come from the lookup never having been made.
        """
        self.assertIn("/v1/hud/missions?grade=excluded", self.server.gets,
                      "the HUD list was not asked for the excluded missions")
        self.assertEqual(proc.returncode, 4, "lookup failure exited %d; expected 4; log: %s" % (proc.returncode, self.log_text()))
        self.assertIn("could not check whether the named missions are excluded", self.log(),
                      "no could-not-check line; log: %s" % self.log_text())
        self.assertEqual(self.server.deletes, [], "a failed lookup still sent DELETE %r" % (self.server.deletes,))
        self.assertFalse(self.ran(), "a failed lookup still started the grader")

    def test_regrade_logs_one_skip_line_for_the_excluded_mission(self):
        """Path: --regrade --mission A,B,C with B excluded logs a skip line for B, and only for B.

        The HUD answers A graded, B excluded and C ungraded. A predicate that reads
        any grade state as excluded also skips the graded A (GRADE-SKIP-STATE-LOOSE).
        A predicate that matches no state skips nothing (GRADE-SKIP-STATE-OFF). The
        print gate that reports every named mission logs three skips
        (GRADE-SKIP-PRINT-ALL), and the one that reports none logs none
        (GRADE-SKIP-PRINT-OFF). The claim is named in each message, so the
        messages differ.
        """
        proc = self.regrade_b_excluded()
        self.assertEqual(proc.returncode, 0, "regrade with B excluded exited %d; log: %s" % (proc.returncode, self.log_text()))
        skips, claimed = self.skips(), self.claimed()
        # assertTrue, not assertEqual: the message comes first on the failure line, so the
        # observed skips and claim are what a failure reports, not a list diff.
        self.assertTrue(skips == ["B"], "skip lines were %r, claim %r; expected skip lines ['B']" % (skips, claimed))

    def test_regrade_sends_delete_for_the_others_and_never_for_the_excluded_one(self):
        """Path: --regrade sends DELETE for A and for C, and none for B.

        Presence and absence are checked through one list of every DELETE the stub
        received. Mutant GRADE-KEEP-OFF (no drop: B is deleted too) and GRADE-KEEP-ONLY
        (only the excluded one is kept) fail it.
        """
        proc = self.regrade_b_excluded()
        self.assertEqual(proc.returncode, 0, "regrade with B excluded exited %d; log: %s" % (proc.returncode, self.log_text()))
        self.assertEqual(sorted(self.server.deletes), ["/v1/mission-grades/A", "/v1/mission-grades/C"],
                         "DELETE set was %r; expected A and C, never B" % (self.server.deletes,))

    def test_regrade_claims_only_the_reopened_missions(self):
        """Path: the grader is asked to claim A and C, the missions reopened, and not B.

        Mutants GRADE-KEEP-OFF and GRADE-KEEP-ONLY change the claim as well as the
        DELETE set; this test reads the claim the grader was given.
        """
        proc = self.regrade_b_excluded()
        self.assertEqual(proc.returncode, 0, "regrade with B excluded exited %d; log: %s" % (proc.returncode, self.log_text()))
        self.assertEqual(self.claimed(), ["A", "C"], "claim was %r; expected ['A', 'C']" % (self.claimed(),))

    def test_all_named_missions_excluded_exits_0_with_nothing_run(self):
        """Path: --regrade --mission A,B with both excluded logs both skips, exits 0, deletes nothing and starts no grader.

        Mutant GRADE-ALLEX-OFF (the all-excluded test no longer matches) starts the
        grader with an empty claim, and this test catches it.
        """
        self.serve_hud(states={"A": "excluded", "B": "excluded"})
        proc = self.grade("--mission", "A,B", "--regrade")
        self.assertEqual(proc.returncode, 0, "every named mission excluded exited %d; log: %s" % (proc.returncode, self.log_text()))
        self.assertEqual(self.server.deletes, [], "DELETE set %r; expected none" % (self.server.deletes,))
        self.assertFalse(self.ran(), "the grader was started for a request whose missions are all excluded")

    def test_an_ungraded_mission_beside_an_excluded_one_still_runs(self):
        """Path (control for the all-excluded exit): --mission A,B with A excluded and B ungraded starts the grader for B alone.

        The same run through the same path as the test above, with one mission left
        to claim. Mutant GRADE-ALLEX-ANY (every claimable request exits without
        running) fails it.
        """
        self.serve_hud(states={"A": "excluded", "B": None})
        proc = self.grade("--mission", "A,B")
        self.assertEqual(proc.returncode, 0, "control with B ungraded exited %d; log: %s" % (proc.returncode, self.log_text()))
        self.assertTrue(self.ran(), "control: the grader was not started with B to claim")
        self.assertEqual(self.claimed(), ["B"], "control claim was %r; expected ['B']" % (self.claimed(),))

    def test_dry_run_reports_the_skip_for_the_excluded_mission(self):
        """Path: --dry-run with --regrade reports the same skip line for B as a real run does."""
        self.serve_hud(states={"A": "graded", "B": "excluded", "C": None})
        proc = self.grade("--mission", "A,B,C", "--regrade", "--dry-run")
        self.assertEqual(proc.returncode, 0, "dry run exited %d; log: %s" % (proc.returncode, self.log_text()))
        self.assertEqual(self.skips(), ["B"], "dry run skip lines were %r; expected ['B']" % (self.skips(),))

    def test_dry_run_says_it_would_delete_the_others_and_not_the_excluded_one(self):
        """Path: --dry-run names the verdicts it would delete for A and C, and not for B.

        The presence of the A line is the control: the same dry-run path reports a
        would-delete line for a mission that is not excluded. Mutant GRADE-KEEP-OFF
        makes the dry run say it would delete B. The dry run sends no DELETE.
        """
        self.serve_hud(states={"A": "graded", "B": "excluded", "C": None})
        proc = self.grade("--mission", "A,B,C", "--regrade", "--dry-run")
        self.assertEqual(proc.returncode, 0, "dry run exited %d; log: %s" % (proc.returncode, self.log_text()))
        self.assertIn("would delete the verdict on A", self.log(), "control: dry run gave no would-delete line for A")
        self.assertNotIn("would delete the verdict on B", self.log(), "dry run says it would delete the excluded B")
        self.assertEqual(self.server.deletes, [], "dry run sent DELETE %r" % (self.server.deletes,))
        self.assertFalse(self.ran(), "dry run started the grader")

    def test_a_lookup_error_status_exits_4_and_deletes_nothing(self):
        """Path: the HUD list answers 500, so the lookup could not check and nothing is deleted or claimed.

        Mutant GRADE-LOOKUP-OFF (a failed lookup no longer stops the run) fails it.
        """
        self.serve_hud(status=500, text="{}")
        self.assert_lookup_failure(self.grade("--mission", "A,B,C", "--regrade"))

    def test_a_lookup_answer_that_is_not_json_exits_4_and_deletes_nothing(self):
        """Path: the HUD list answers 200 with text that is not JSON, so the lookup could not check.

        Mutant GRADE-LOOKUP-OFF fails it as well.
        """
        self.serve_hud(status=200, text="not json")
        self.assert_lookup_failure(self.grade("--mission", "A,B,C", "--regrade"))

    def test_a_lookup_with_no_answer_exits_4_and_deletes_nothing(self):
        """Path: the service drops the connection during the lookup, so the lookup could not check.

        Mutant GRADE-LOOKUP-OFF fails it as well.
        """
        self.serve_hud(status=0)
        self.assert_lookup_failure(self.grade("--mission", "A,B,C", "--regrade"))

    def test_a_lookup_answer_without_a_missions_list_names_the_fault(self):
        """Path: the HUD list answers JSON with no missions array, so the lookup could not check and the log says why.

        Mutant GRADE-VALID-OFF (the shape check is skipped) fails the reason check:
        the run still exits 4, but the log names a KeyError, not the missing array.
        """
        self.serve_hud(status=200, text='{"harnesses": []}')
        proc = self.grade("--mission", "A,B,C", "--regrade")
        self.assertEqual(proc.returncode, 4, "answer without a missions array exited %d; expected 4" % (proc.returncode,))
        self.assertIn("the HUD list has no missions array", self.log(),
                      "the log does not name the missing missions array; log: %s" % self.log_text())
        self.assertEqual(self.server.deletes, [], "an answer without a missions array still sent DELETE %r" % (self.server.deletes,))

    def test_plain_mission_drops_the_excluded_one_from_the_claim(self):
        """Path: --mission without --regrade drops excluded B from the claim, and sends no DELETE.

        A and C are ungraded here, so the claim is the one the grader would get.
        Mutants GRADE-KEEP-OFF and GRADE-KEEP-ONLY change this claim.
        """
        self.serve_hud(states={"A": None, "B": "excluded", "C": None})
        proc = self.grade("--mission", "A,B,C")
        self.assertEqual(proc.returncode, 0, "plain --mission with B excluded exited %d; log: %s" % (proc.returncode, self.log_text()))
        self.assertEqual(self.server.deletes, [], "plain --mission sent DELETE %r" % (self.server.deletes,))
        self.assertEqual(self.claimed(), ["A", "C"], "plain claim was %r; expected ['A', 'C']" % (self.claimed(),))

    def test_plain_mission_with_no_excluded_one_claims_every_named_mission(self):
        """Path (control for the plain drop): --mission A,B,C with none excluded claims all three and skips none.

        Mutant GRADE-KEEP-ONLY (only the excluded ones are kept) fails the claim
        check; mutant GRADE-SKIP-PRINT-ALL (every named mission is reported) fails
        the skip check.
        """
        self.serve_hud(states={"A": None, "B": None, "C": None})
        proc = self.grade("--mission", "A,B,C")
        self.assertEqual(proc.returncode, 0, "control with none excluded exited %d; log: %s" % (proc.returncode, self.log_text()))
        self.assertEqual(self.claimed(), ["A", "B", "C"], "control claim was %r; expected ['A', 'B', 'C']" % (self.claimed(),))
        self.assertEqual(self.skips(), [], "control skip lines were %r; expected none" % (self.skips(),))


if __name__ == "__main__":
    unittest.main()

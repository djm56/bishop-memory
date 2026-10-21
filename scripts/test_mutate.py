"""
Unit tests for scripts/mutate.py: the DIR-017 mutation runner.

Nothing here touches the real repository or the network. Each test builds a
temporary git repository (or a temporary directory) and points mutate.ROOT at
it. Tests that need a killer's verdict either patch run_killers() with canned
results, or run a small real unittest suite written into the temporary
directory. The real spec is never run by `make test`; `make mutate` does that.

Each test name says the edge it covers.
"""

import argparse
import contextlib
import io
import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import mutate  # noqa: E402  (must follow the sys.path insert)
import check_mutants as cm  # noqa: E402

RULE = mutate.RULE
TARGET = "def f():\n    return 1\n    return 2\n"


def git_run(repo, *args):
    """
    Run git in a temporary repository with a fixed identity. Raises on failure.
    GIT_* variables are dropped from the child's environment, so an inherited
    GIT_DIR or GIT_INDEX_FILE (a git hook sets these) cannot aim it at the real repository.
    """
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    return subprocess.run(
        ["git", "-C", repo, "-c", "user.name=test", "-c", "user.email=test@example.invalid",
         "-c", "commit.gpgsign=false", *args],
        capture_output=True, text=True, check=True, env=env)


def make_repo(files):
    """A temporary git repository holding `files` (relpath to text), committed once."""
    repo = tempfile.mkdtemp(prefix="mutate-test-repo-")
    git_run(repo, "init", "-q")
    for relpath, text in files.items():
        with open(os.path.join(repo, relpath), "w", encoding="utf-8") as handle:
            handle.write(text)
    git_run(repo, "add", "-A")
    git_run(repo, "commit", "-q", "-m", "init")
    return repo


def entry(ident="M1", file="f.py", line=2, before="return 1", after="return 3",
          killers=("T.test_x",), suite="suite.py", language="py", **extra):
    """A spec row as load_spec() would return it, against TARGET."""
    data = {
        "id": ident,
        "status": "active",
        "language": language,
        "file": file,
        "line": line,
        "before": before,
        "after": after,
        "property": "a property",
        "mirror": ident + "-MIRROR",
        "killers": list(killers),
        "suite": suite,
    }
    if language == "go":
        data.pop("suite")
        data["package"] = "./internal/x"
    data.update(extra)
    return data


def read(path):
    """The bytes of a file."""
    with open(path, "rb") as handle:
        return handle.read()


def pid_gone(pid, seconds=5.0):
    """True when process `pid` no longer exists within `seconds`; False when it is still alive at the end."""
    deadline = time.monotonic() + seconds
    while True:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return True
        if time.monotonic() >= deadline:
            return False
        time.sleep(0.05)


def wait_for_pids(path, count, seconds=15.0):
    """
    The `count` pids written to the file `path`, waiting up to `seconds` for them to
    appear. Anything other than `count` whole pids means "not yet". Raises
    AssertionError, naming the file, when they never do.
    """
    deadline = time.monotonic() + seconds
    while True:
        try:
            with open(path, encoding="utf-8") as handle:
                words = handle.read().split()
            if len(words) == count and all(w.isdigit() for w in words):
                return [int(w) for w in words]
        except FileNotFoundError:
            pass
        if time.monotonic() >= deadline:
            raise AssertionError(f"{path} did not hold {count} pid(s) within {seconds:g}s")
        time.sleep(0.05)


def kill_quietly(pid):
    """SIGKILL `pid` if it is still there. Used by cleanups, so a process that already exited is no error."""
    try:
        os.kill(pid, signal.SIGKILL)
    except OSError:
        pass


# A killer that tells the runner's process to stop. It runs after the mutant is planted, and checks the
# planted text first, so a missing plant fails the run instead of looking like an interrupt.
SIGNAL_SUITE = """import os, signal, time, unittest
TARGET_FILE = {target!r}
class Case(unittest.TestCase):
    def test_signal(self):
        with open(TARGET_FILE, encoding='utf-8') as handle:
            self.assertIn('return 3', handle.read())
        os.kill(os.getppid(), signal.SIGTERM)
        time.sleep(30)
if __name__ == '__main__':
    unittest.main()
"""

# The same killer, but it first stages the planted file, so the restore's git-clean check fails.
STAGE_SUITE = """import os, signal, subprocess, time, unittest
TARGET_FILE = {target!r}
REPO = {repo!r}
class Case(unittest.TestCase):
    def test_stage(self):
        with open(TARGET_FILE, encoding='utf-8') as handle:
            self.assertIn('return 3', handle.read())
        subprocess.run(['git', '-C', REPO, 'add', '--', 'f.py'], check=True)
        os.kill(os.getppid(), signal.SIGTERM)
        time.sleep(30)
if __name__ == '__main__':
    unittest.main()
"""

# A killer that starts a grandchild, records its pid, and then runs past any timeout.
SPAWN_SUITE = """import subprocess, time, unittest
class Case(unittest.TestCase):
    def test_spawns(self):
        child = subprocess.Popen(['sleep', '60'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        with open({pidfile!r}, 'w', encoding='utf-8') as handle:
            handle.write(str(child.pid))
        time.sleep(60)
if __name__ == '__main__':
    unittest.main()
"""

# A Go package whose test starts a `sleep` child, records both pids in pids.txt, and runs past any timeout.
GO_HANG_TEST = """package hang

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestHang(t *testing.T) {
	sleeper := exec.Command("sleep", "120")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	pids := fmt.Sprintf("%d %d\\n", os.Getpid(), sleeper.Process.Pid)
	if err := os.WriteFile("pids.txt", []byte(pids), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Second)
}
"""


class _RepoTest(unittest.TestCase):
    """Base: a temporary repository holding f.py, with mutate.ROOT pointed at it."""

    def setUp(self):
        self.repo = make_repo({"f.py": TARGET})
        self.logs = tempfile.mkdtemp(prefix="mutate-test-logs-")
        patcher = mock.patch.object(mutate, "ROOT", self.repo)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.addCleanup(shutil.rmtree, self.repo, True)
        self.addCleanup(shutil.rmtree, self.logs, True)

    def path(self, relpath="f.py"):
        """Absolute path of a file in the temporary repository."""
        return os.path.join(self.repo, relpath)


class ParsePythonTests(unittest.TestCase):
    """parse_python() over unittest -v text."""

    def test_failure_header_gives_failed_with_its_message(self):
        output = (
            "test_one (mod.Case.test_one) ... ok\n"
            "test_two (mod.Case.test_two) ... FAIL\n"
            "\n" + RULE + "\n"
            "FAIL: test_two (mod.Case.test_two)\n"
            "Traceback (most recent call last):\n"
            "  File \"x.py\", line 4, in test_two\n"
            "    self.assertEqual(1, 2)\n"
            "AssertionError: 1 != 2\n"
            + RULE + "\n"
            "Ran 2 tests\n")
        results = mutate.parse_python(output, ["Case.test_one", "Case.test_two", "Case.test_three"])
        self.assertEqual(results, [
            ("Case.test_one", "ok", None),
            ("Case.test_two", "FAIL", "AssertionError: 1 != 2"),
            ("Case.test_three", "NOT-RUN", None),
        ])

    def test_error_header_gives_error(self):
        output = (
            RULE + "\nERROR: test_e (mod.Case.test_e)\n"
            "Traceback (most recent call last):\n"
            "KeyError: 'missing'\n" + RULE + "\n")
        self.assertEqual(mutate.parse_python(output, ["Case.test_e"]),
                         [("Case.test_e", "ERROR", "KeyError: 'missing'")])

    def test_documented_test_is_ok(self):
        # unittest -v prints a documented test's name and docstring line on two lines.
        output = "test_doc (mod.Case.test_doc)\nA documented test ... ok\n"
        self.assertEqual(mutate.parse_python(output, ["Case.test_doc"]), [("Case.test_doc", "ok", None)])


class ParseGoTests(unittest.TestCase):
    """parse_go() over go test -v text."""

    def test_message_comes_from_the_failing_subtests_own_header(self):
        output = (
            "=== RUN   TestParent\n"
            "=== RUN   TestParent/alpha\n"
            "    x_test.go:10: alpha went wrong\n"
            "=== RUN   TestParent/beta\n"
            "    x_test.go:20: beta went wrong\n"
            "--- FAIL: TestParent (0.00s)\n"
            "    --- FAIL: TestParent/alpha (0.00s)\n"
            "        x_test.go:10: alpha went wrong\n"
            "    --- FAIL: TestParent/beta (0.00s)\n"
            "        x_test.go:20: beta went wrong\n"
            "FAIL\n")
        self.assertEqual(mutate.parse_go(output, "TestParent", "beta"),
                         ("FAIL", "x_test.go:20: beta went wrong"))

    def test_passing_subtest_is_ok(self):
        output = "=== RUN   TestParent/alpha\n--- PASS: TestParent/alpha (0.00s)\n"
        self.assertEqual(mutate.parse_go(output, "TestParent", "alpha"), ("ok", None))

    def test_selector_matching_no_subtest_is_not_run(self):
        output = "=== RUN   TestParent/alpha\n--- PASS: TestParent/alpha (0.00s)\n"
        self.assertEqual(mutate.parse_go(output, "TestParent", "gamma"), ("NOT-RUN", None))

    def test_parallel_subtest_message_follows_its_cont_header(self):
        # alpha pauses, beta starts, then alpha resumes with CONT: its output belongs to alpha.
        output = (
            "=== RUN   TestParent/alpha\n"
            "=== PAUSE TestParent/alpha\n"
            "=== RUN   TestParent/beta\n"
            "=== CONT  TestParent/alpha\n"
            "    x_test.go:10: alpha went wrong\n"
            "=== NAME  TestParent/alpha\n"
            "--- FAIL: TestParent/alpha (0.00s)\n")
        self.assertEqual(mutate.parse_go(output, "TestParent", "alpha"),
                         ("FAIL", "x_test.go:10: alpha went wrong"))


class RealPythonRunTests(unittest.TestCase):
    """run_killers() and mutate_one() against a real unittest suite in a temporary directory."""

    def setUp(self):
        self.repo = make_repo({"f.py": TARGET})
        self.tmp = tempfile.mkdtemp(prefix="mutate-test-suite-")
        self.logs = tempfile.mkdtemp(prefix="mutate-test-logs-")
        patcher = mock.patch.object(mutate, "ROOT", self.repo)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.addCleanup(shutil.rmtree, self.repo, True)
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.addCleanup(shutil.rmtree, self.logs, True)

    def path(self):
        """Absolute path of f.py in the temporary repository."""
        return os.path.join(self.repo, "f.py")

    def suite(self, body):
        """Write a suite file that imports f from the temporary repository, and return its path."""
        path = os.path.join(self.tmp, "suite_case.py")
        with open(path, "w", encoding="utf-8") as handle:
            handle.write(f"import sys, unittest\nsys.path.insert(0, {self.repo!r})\nfrom f import f\n{body}\n"
                         "if __name__ == '__main__':\n    unittest.main()\n")
        return path

    def test_mixed_suite_gives_ok_fail_and_not_run(self):
        suite = self.suite(
            "class Case(unittest.TestCase):\n"
            "    def test_pass(self):\n        self.assertTrue(True)\n"
            "    def test_fail(self):\n        self.assertEqual(f(), 2)\n"
            "    def test_documented(self):\n        \"\"\"A documented test.\"\"\"\n        self.assertTrue(True)\n")
        ent = entry(suite=suite, killers=["Case.test_pass", "Case.test_fail", "Case.test_documented", "Case.test_absent"])
        with open(os.path.join(self.logs, "x.log"), "w", encoding="utf-8") as log:
            results = mutate.run_killers(ent, log, 120)
        self.assertEqual([(sel, st) for sel, st, _ in results], [
            ("Case.test_pass", "ok"), ("Case.test_fail", "FAIL"),
            ("Case.test_documented", "ok"), ("Case.test_absent", "NOT-RUN")])

    def test_syntax_error_in_suite_gives_module_does_not_parse(self):
        path = os.path.join(self.tmp, "suite_bad.py")
        with open(path, "w", encoding="utf-8") as handle:
            handle.write("def broken(:\n")
        ent = entry(suite=path, killers=["Case.test_a", "Case.test_b"])
        with open(os.path.join(self.logs, "x.log"), "w", encoding="utf-8") as log:
            results = mutate.run_killers(ent, log, 120)
        self.assertEqual(results, [("Case.test_a", "NOT-RUN", "module does not parse"),
                                   ("Case.test_b", "NOT-RUN", "module does not parse")])

    def test_word_syntaxerror_in_test_output_is_not_a_module_error(self):
        # The words appear in printed output, not in a traceback, so the killer still runs.
        suite = self.suite(
            "class Case(unittest.TestCase):\n"
            "    def test_talks(self):\n        print('SyntaxError: this is only output')\n"
            "        self.assertTrue(True)\n")
        ent = entry(suite=suite, killers=["Case.test_talks"])
        with open(os.path.join(self.logs, "x.log"), "w", encoding="utf-8") as log:
            results = mutate.run_killers(ent, log, 120)
        self.assertEqual(results, [("Case.test_talks", "ok", None)])

    def test_run_past_timeout_gives_timeout_for_each_killer(self):
        suite = self.suite(
            "import time\n"
            "class Case(unittest.TestCase):\n"
            "    def test_hangs(self):\n        time.sleep(60)\n")
        ent = entry(suite=suite, killers=["Case.test_hangs"])
        with open(os.path.join(self.logs, "x.log"), "w", encoding="utf-8") as log:
            results = mutate.run_killers(ent, log, 1)
        self.assertEqual(results, [("Case.test_hangs", "TIMEOUT", "timed out after 1s")])

    def test_killed_mutant_is_planted_and_restored(self):
        suite = self.suite(
            "class Case(unittest.TestCase):\n"
            "    def test_value(self):\n        self.assertEqual(f(), 1)\n")
        ent = entry(suite=suite, killers=["Case.test_value"])
        out = mutate.mutate_one(ent, self.logs, 120)
        self.assertEqual(out.verdict, "KILLED")
        self.assertEqual(out.first_failure[0], "Case.test_value")
        self.assertTrue(out.restore_ok, out.problems)
        self.assertEqual(read(self.path()).decode("utf-8"), TARGET)

    def test_surviving_mutant_is_reported_as_survived(self):
        # The edit is on line 3, after the return, so the value the suite checks never changes.
        suite = self.suite(
            "class Case(unittest.TestCase):\n"
            "    def test_value(self):\n        self.assertEqual(f(), 1)\n")
        ent = entry(suite=suite, line=3, before="return 2", after="return 4", killers=["Case.test_value"])
        out = mutate.mutate_one(ent, self.logs, 120)
        self.assertEqual(out.verdict, "SURVIVED")
        self.assertEqual(out.killers, [("Case.test_value", "ok", None)])
        self.assertEqual(read(self.path()).decode("utf-8"), TARGET)


class TreeCleanTests(_RepoTest):
    """tree_clean() against a real repository: clean means git status prints nothing for the path."""

    def test_committed_file_is_clean(self):
        self.assertTrue(mutate.tree_clean("f.py"))

    def test_unstaged_edit_is_dirty(self):
        with open(self.path(), "a", encoding="utf-8") as handle:
            handle.write("# edit\n")
        self.assertFalse(mutate.tree_clean("f.py"))

    def test_staged_edit_is_dirty(self):
        # A staged edit matches the working tree but not HEAD; a diff against the index would miss it.
        with open(self.path(), "a", encoding="utf-8") as handle:
            handle.write("# edit\n")
        git_run(self.repo, "add", "--", "f.py")
        self.assertFalse(mutate.tree_clean("f.py"))

    def test_untracked_file_is_dirty(self):
        with open(self.path("g.py"), "w", encoding="utf-8") as handle:
            handle.write("x = 1\n")
        self.assertFalse(mutate.tree_clean("g.py"))

    def test_ignored_file_is_dirty(self):
        with open(self.path(".gitignore"), "w", encoding="utf-8") as handle:
            handle.write("ign.py\n")
        with open(self.path("ign.py"), "w", encoding="utf-8") as handle:
            handle.write("x = 1\n")
        self.assertFalse(mutate.tree_clean("ign.py"))

    def test_git_that_cannot_answer_raises_tool_error(self):
        # Outside a repository git status exits non-zero; that must not read as a dirty file.
        bare = tempfile.mkdtemp(prefix="mutate-test-notrepo-")
        self.addCleanup(shutil.rmtree, bare, True)
        with mock.patch.object(mutate, "ROOT", bare):
            with self.assertRaises(mutate.ToolError):
                mutate.tree_clean("f.py")

    def test_missing_git_raises_tool_error(self):
        with mock.patch.object(mutate.subprocess, "run", side_effect=FileNotFoundError("git")):
            with self.assertRaises(mutate.ToolError):
                mutate.tree_clean("f.py")


class MutateOneTests(_RepoTest):
    """mutate_one() verdicts and restore checks, with run_killers() patched to canned results."""

    def run_with(self, results, **kwargs):
        """mutate_one() on the default entry with run_killers() returning `results`."""
        with mock.patch.object(mutate, "run_killers", return_value=results) as runner:
            out = mutate.mutate_one(entry(**kwargs), self.logs, 5)
        return out, runner

    def test_failing_killer_kills(self):
        out, _ = self.run_with([("T.test_x", "FAIL", "AssertionError: 3 != 1")])
        self.assertEqual(out.verdict, "KILLED")
        self.assertEqual(out.first_failure, ("T.test_x", "AssertionError: 3 != 1"))
        self.assertEqual(read(self.path()).decode("utf-8"), TARGET)

    def test_passing_killers_survive(self):
        out, _ = self.run_with([("T.test_x", "ok", None)])
        self.assertEqual(out.verdict, "SURVIVED")
        self.assertIsNone(out.first_failure)

    def test_selector_matching_nothing_is_invalid(self):
        out, _ = self.run_with([("T.test_x", "NOT-RUN", None)])
        self.assertEqual(out.verdict, "INVALID")
        self.assertIn("killer(s) did not run: T.test_x", out.problems)

    def test_failing_killer_beside_a_missing_one_is_still_invalid(self):
        # A kill does not count when another selector of the same mutant matched nothing.
        out, _ = self.run_with([("T.a", "FAIL", "AssertionError: x"), ("T.b", "NOT-RUN", None)])
        self.assertEqual(out.verdict, "INVALID")

    def test_build_failure_is_invalid(self):
        out, _ = self.run_with([("T.test_x", "NOT-RUN", "build failed")])
        self.assertEqual(out.verdict, "INVALID")
        self.assertEqual(out.killers, [("T.test_x", "NOT-RUN", "build failed")])

    def test_timeout_is_invalid_with_its_reason(self):
        out, _ = self.run_with([("T.test_x", "TIMEOUT", "timed out after 5s")])
        self.assertEqual(out.verdict, "INVALID")
        self.assertIn("killer(s) timed out after 5s: T.test_x", out.problems)

    def test_anchor_mismatch_is_invalid_and_plants_nothing(self):
        with mock.patch.object(mutate, "run_killers") as runner:
            out = mutate.mutate_one(entry(line=9), self.logs, 5)
        self.assertEqual(out.verdict, "INVALID")
        runner.assert_not_called()
        self.assertEqual(read(self.path()).decode("utf-8"), TARGET)

    def test_absent_file_is_invalid_and_plants_nothing(self):
        with mock.patch.object(mutate, "run_killers") as runner:
            out = mutate.mutate_one(entry(file="nope.py"), self.logs, 5)
        self.assertEqual(out.verdict, "INVALID")
        self.assertIn("nope.py does not exist", out.problems)
        runner.assert_not_called()

    def test_uncommitted_file_is_refused_and_not_touched(self):
        with open(self.path(), "a", encoding="utf-8") as handle:
            handle.write("# local edit\n")
        before = read(self.path())
        with mock.patch.object(mutate, "run_killers") as runner:
            out = mutate.mutate_one(entry(), self.logs, 5)
        self.assertEqual(out.verdict, "INVALID")
        self.assertTrue(any("refusing to plant" in p for p in out.problems), out.problems)
        runner.assert_not_called()
        self.assertEqual(read(self.path()), before)

    def test_restore_failure_is_reported(self):
        # A killer that stages the mutated file leaves the index differing from HEAD after
        # the bytes are restored, so the git-clean verification fails.
        def stage_then_pass(ent, log, timeout):
            git_run(self.repo, "add", "--", "f.py")
            return [("T.test_x", "ok", None)]
        with mock.patch.object(mutate, "run_killers", side_effect=stage_then_pass):
            out = mutate.mutate_one(entry(), self.logs, 5)
        git_run(self.repo, "reset", "-q", "--", "f.py")
        self.assertFalse(out.restore_ok)
        self.assertTrue(any(p.startswith("RESTORE FAILED for f.py") and "git-clean=False" in p
                            for p in out.problems), out.problems)
        self.assertEqual(read(self.path()).decode("utf-8"), TARGET)


def readme_exit_rows(text):
    """
    The rows of the exit-status table in a README text, as (code, meaning) pairs in
    the order written. The table is the one headed "| Status | Meaning |", and it
    ends at the first line that does not start with "|". A row that does not match
    the "| code | meaning |" shape comes back as (None, the line).
    Raises ValueError when there is no such table.
    """
    lines = text.splitlines()
    start = lines.index("| Status | Meaning |")  # raises ValueError when the header is absent
    rows = []
    for line in lines[start + 2:]:  # start + 1 is the |---|---| separator
        if not line.startswith("|"):
            break
        match = re.match(r"^\| (\d+) \| (.*) \|$", line)
        rows.append((int(match.group(1)), match.group(2)) if match else (None, line))
    return rows


def exit_table_errors(text, doc):
    """
    Every difference between the README exit table and `doc` (EXIT_DOC), checked in
    both directions. Empty when they agree.
    """
    try:
        rows = readme_exit_rows(text)
    except ValueError:
        return ["README has no '| Status | Meaning |' table"]
    errors, seen = [], set()
    for code, meaning in rows:
        if code is None:
            errors.append(f"malformed row: {meaning!r}")
            continue
        if code in seen:
            errors.append(f"exit {code} has more than one README row")
        seen.add(code)
        if code not in doc:
            errors.append(f"README row {code} has no EXIT_DOC entry")
        elif doc[code] != meaning:
            errors.append(f"exit {code}: README says {meaning!r}, EXIT_DOC says {doc[code]!r}")
    for code in doc:
        if code not in seen:
            errors.append(f"EXIT_DOC entry {code} has no README row")
    return errors


class ReadmeExitTableTests(unittest.TestCase):
    """The exit-status table in testdata/README.md against EXIT_DOC, the source of --help."""

    def setUp(self):
        readme = os.path.join(HERE, "..", "testdata", "README.md")
        with open(readme, encoding="utf-8") as handle:
            self.text = handle.read()

    def test_readme_exit_table_matches_exit_doc_both_ways(self):
        # Positive control: the real table parses to every EXIT_DOC code, and no difference is reported.
        self.assertEqual(len(readme_exit_rows(self.text)), len(mutate.EXIT_DOC))
        self.assertEqual(exit_table_errors(self.text, mutate.EXIT_DOC), [])

    def test_row_with_no_exit_doc_entry_is_reported(self):
        # An invented status 6 in the README: the table has a row EXIT_DOC does not.
        text = self.text.replace("| 5 |", "| 6 | invented status |\n| 5 |", 1)
        self.assertIn("README row 6 has no EXIT_DOC entry", exit_table_errors(text, mutate.EXIT_DOC))

    def test_exit_doc_entry_with_no_row_is_reported(self):
        # Row 4 removed from the README: EXIT_DOC keeps a code the table no longer lists.
        text = "\n".join(line for line in self.text.splitlines() if not line.startswith("| 4 |"))
        self.assertIn("EXIT_DOC entry 4 has no README row", exit_table_errors(text, mutate.EXIT_DOC))

    def test_changed_meaning_is_reported(self):
        text = self.text.replace("interrupted by a signal or Ctrl-C", "stopped by a signal", 1)
        errors = exit_table_errors(text, mutate.EXIT_DOC)
        self.assertEqual(len(errors), 1, errors)
        self.assertIn("exit 5: README says", errors[0])


class MainTests(_RepoTest):
    """main(): exit statuses, refusals, and the SHARED-TEXT verdict."""

    def setUp(self):
        super().setUp()
        self.items = [entry("A", mirror="B"), entry("B", line=3, before="return 2", after="return 4", mirror="A")]
        self.tmp = tempfile.mkdtemp(prefix="mutate-test-suites-")
        self.addCleanup(shutil.rmtree, self.tmp, True)

    def marker_path(self):
        """The unrestored-plant marker of the temporary repository, in its git directory."""
        return os.path.join(self.repo, ".git", mutate.MARKER_NAME)

    def marker_entries(self):
        """The marker's entries, keyed by file. Empty when the marker is absent."""
        return mutate.read_marker(self.marker_path())

    def record_plant(self, relpath, ident, log_dir, orig_exists=True):
        """Record an earlier run's unrestored plant of `relpath`, with its saved original in `log_dir`."""
        saved = os.path.join(log_dir, f"{ident}.orig")
        if orig_exists:
            with open(saved, "wb") as handle:
                handle.write(TARGET.encode("utf-8"))
        mutate.record_plant(self.marker_path(), relpath, ident, log_dir, saved)
        return saved

    def write_suite(self, text, name="suite_case.py"):
        """Write a killer suite into the test's directory and return its path."""
        path = os.path.join(self.tmp, name)
        with open(path, "w", encoding="utf-8") as handle:
            handle.write(text)
        return path

    def test_plant_is_recorded_during_the_run_and_cleared_after_a_verified_restore(self):
        seen = []

        def killer(ent, log, timeout):
            seen.append(self.marker_entries())  # the entry must exist while the mutant is planted
            return [("T.test_x", "FAIL", "AssertionError: x")]
        with mock.patch.object(mutate, "run_killers", side_effect=killer):
            status, out = self.run_main(["--ids", "A", "--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_OK, out)
        self.assertIn("f.py", seen[0], "the plant was not recorded before planting")
        self.assertEqual(seen[0]["f.py"]["log_dir"], self.logs)
        self.assertEqual(seen[0]["f.py"]["orig"], os.path.join(self.logs, "A.orig"))
        self.assertNotIn("f.py", self.marker_entries())

    def test_refusal_names_the_log_dir_that_holds_the_files_original(self):
        with open(self.path(), "a", encoding="utf-8") as handle:
            handle.write("# local edit\n")
        saved = self.record_plant("f.py", "A", self.logs)
        status, out = self.run_main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_REFUSED, out)
        self.assertIn(f"its logs in {self.logs} hold its saved original {saved}", out)

    def test_refusal_does_not_name_a_log_dir_without_the_files_original(self):
        with open(self.path(), "a", encoding="utf-8") as handle:
            handle.write("# local edit\n")
        self.record_plant("f.py", "A", self.logs, orig_exists=False)
        status, out = self.run_main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_REFUSED)
        self.assertNotIn(self.logs, out)
        self.assertIn("the marker records no saved original of an earlier run for it", out)

    def test_refusal_does_not_name_another_files_original(self):
        with open(self.path(), "a", encoding="utf-8") as handle:
            handle.write("# local edit\n")
        self.record_plant("g.py", "G", self.logs)  # the original exists, but for a different file
        status, out = self.run_main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_REFUSED)
        self.assertNotIn(self.logs, out)

    def run_main(self, argv, items=None):
        """main() with the spec replaced and git and go reported present; returns (status, stdout)."""
        out = io.StringIO()
        with mock.patch.object(cm, "load_spec", return_value={"mutants": items or self.items}), \
                mock.patch.object(mutate.shutil, "which", side_effect=lambda name: "/usr/bin/" + name), \
                contextlib.redirect_stdout(out):
            status = mutate.main(argv)
        return status, out.getvalue()

    def test_unknown_id_is_refused(self):
        status, out = self.run_main(["--ids", "NOPE", "--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_REFUSED, out)
        self.assertIn("not an active mutant: NOPE", out)

    def test_id_with_shell_metacharacters_is_refused(self):
        status, out = self.run_main(["--ids", "A;rm", "--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_REFUSED, out)
        self.assertIn("not a mutant id", out)

    def test_non_positive_timeout_is_a_usage_error(self):
        with self.assertRaises(SystemExit) as ctx, contextlib.redirect_stderr(io.StringIO()):
            self.run_main(["--timeout", "0", "--log-dir", self.logs])
        self.assertEqual(ctx.exception.code, 2)

    def test_missing_git_exits_no_tool(self):
        out = io.StringIO()
        with mock.patch.object(cm, "load_spec", return_value={"mutants": self.items}), \
                mock.patch.object(mutate.shutil, "which", return_value=None), \
                contextlib.redirect_stdout(out):
            status = mutate.main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_NO_TOOL)
        self.assertIn("git is not installed", out.getvalue())

    def test_missing_go_exits_no_tool_only_for_a_go_mutant(self):
        go_items = [entry("G", language="go", file="f.py", line=2, before="return 1", after="return 3",
                          killers=["TestX/sub"], mirror="G")]
        out = io.StringIO()
        with mock.patch.object(cm, "load_spec", return_value={"mutants": go_items}), \
                mock.patch.object(mutate.shutil, "which", side_effect=lambda name: None if name == "go" else "/usr/bin/git"), \
                contextlib.redirect_stdout(out):
            status = mutate.main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_NO_TOOL)
        self.assertIn("go is not installed", out.getvalue())

    def test_dirty_tree_is_refused_and_names_the_earlier_runs_saved_original(self):
        with open(self.path(), "a", encoding="utf-8") as handle:
            handle.write("# local edit\n")
        earlier = os.path.join(self.tmp, "earlier-run")
        os.makedirs(earlier)
        saved = self.record_plant("f.py", "A", earlier)
        status, out = self.run_main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_REFUSED, out)
        self.assertIn("f.py has changes against HEAD", out)
        self.assertIn(f"its logs in {earlier} hold its saved original {saved}", out)

    def test_dirty_tree_whose_earlier_original_was_deleted_says_so(self):
        with open(self.path(), "a", encoding="utf-8") as handle:
            handle.write("# local edit\n")
        earlier = os.path.join(self.tmp, "empty-run")
        os.makedirs(earlier)
        self.record_plant("f.py", "A", earlier, orig_exists=False)
        status, out = self.run_main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_REFUSED)
        self.assertIn("the marker records no saved original of an earlier run for it", out)

    def test_dirty_tree_with_no_earlier_run_says_so(self):
        with open(self.path(), "a", encoding="utf-8") as handle:
            handle.write("# local edit\n")
        status, out = self.run_main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_REFUSED)
        self.assertIn("the marker records no saved original of an earlier run for it", out)

    def test_all_killed_exits_ok(self):
        with mock.patch.object(mutate, "run_killers", side_effect=lambda ent, log, t: [("T.test_x", "FAIL", "AssertionError: a" + ent["id"])]):
            status, out = self.run_main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_OK, out)
        self.assertIn("killed 2, survived 0, invalid 0, restore failures 0, shared-text 0", out)

    def test_survived_mutant_exits_failed(self):
        with mock.patch.object(mutate, "run_killers", return_value=[("T.test_x", "ok", None)]):
            status, out = self.run_main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_FAILED, out)
        self.assertIn("survived 2", out)
        self.assertEqual(read(self.path()).decode("utf-8"), TARGET)

    def test_shared_text_between_mirrors_exits_shared(self):
        same = [("T.test_x", "FAIL", "AssertionError: 3 != 1")]
        with mock.patch.object(mutate, "run_killers", return_value=same):
            status, out = self.run_main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_SHARED, out)
        self.assertEqual(out.count("SHARED-TEXT: A and B fail with the same message"), 1, out)

    def test_distinct_text_between_mirrors_exits_ok(self):
        def distinct(ent, log, timeout):
            return [("T.test_x", "FAIL", "AssertionError: " + ent["id"])]
        with mock.patch.object(mutate, "run_killers", side_effect=distinct):
            status, out = self.run_main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_OK, out)
        self.assertNotIn("SHARED-TEXT", out)

    def pair_with_signal_killer(self, text, killer="test_signal"):
        """The A/B mirror pair, both killed by method `killer` of the suite `text` (written to the test's directory)."""
        suite = self.write_suite(text)
        return [entry("A", suite=suite, killers=[f"Case.{killer}"], mirror="B"),
                entry("B", line=3, before="return 2", after="return 4", suite=suite,
                      killers=[f"Case.{killer}"], mirror="A")]

    def test_real_sigterm_during_a_mutant_restores_the_file_and_exits_5(self):
        # No mock of mutate_one or run_killers: a real killer sends SIGTERM to the runner's process
        # while f.py holds the planted text, and the run must restore the file before it stops.
        items = self.pair_with_signal_killer(SIGNAL_SUITE.format(target=self.path()))
        status, out = self.run_main(["--ids", "A", "--log-dir", self.logs], items=items)
        self.assertEqual(status, mutate.EXIT_INTERRUPTED, out)
        self.assertIn("interrupted by SIGTERM", out)
        self.assertIn("every restore is verified; no planted file is left mutated", out)
        self.assertEqual(read(self.path()).decode("utf-8"), TARGET)
        self.assertNotIn("f.py", self.marker_entries())

    def test_real_sigterm_whose_restore_fails_exits_1_and_names_the_original(self):
        # The killer stages the planted file, so the restore's git-clean check fails after the
        # bytes are back. The run must report that, not 5, and name the saved original.
        items = self.pair_with_signal_killer(STAGE_SUITE.format(target=self.path(), repo=self.repo), "test_stage")
        status, out = self.run_main(["--ids", "A", "--log-dir", self.logs], items=items)
        saved = os.path.join(self.logs, "A.orig")
        self.assertEqual(status, mutate.EXIT_FAILED, out)
        self.assertNotIn("every restore is verified", out)
        self.assertIn(f"RESTORE FAILED for f.py: bytes-equal=True cmp-equal=True git-clean=False; original saved at {saved}", out)
        self.assertIn(f"mutate: RESTORE FAILED for f.py; the original is saved at {saved}", out)
        self.assertEqual(read(self.path()).decode("utf-8"), TARGET)
        self.assertEqual(self.marker_entries()["f.py"]["orig"], saved, "an unverified restore keeps its marker entry")

    def test_interrupt_positive_control_same_suite_without_a_signal_is_killed_not_interrupted(self):
        # Positive control for the two tests above: the same suite, told not to signal, is a normal kill.
        text = SIGNAL_SUITE.format(target=self.path()).replace("os.kill(os.getppid(), signal.SIGTERM)", "pass")
        items = self.pair_with_signal_killer(text.replace("time.sleep(30)", ""))
        status, out = self.run_main(["--ids", "A", "--log-dir", self.logs], items=items)
        self.assertNotEqual(status, mutate.EXIT_INTERRUPTED, out)
        self.assertNotIn("interrupted by", out)
        self.assertEqual(read(self.path()).decode("utf-8"), TARGET)

    def run_double_signal(self, retry):
        """
        A real killer sends SIGTERM while f.py is planted; then a second SIGTERM lands at the start of the
        restore, before restore_and_verify has begun deferring signals (the unwinding window). With `retry`
        False, restore_until_done is replaced by a single call, which is the behaviour the retry exists to fix.
        Returns (status, stdout, restore_and_verify call count).
        """
        items = self.pair_with_signal_killer(SIGNAL_SUITE.format(target=self.path()))
        real = mutate.restore_and_verify
        calls = []

        def second_signal_first(*args, **kwargs):
            calls.append(1)
            if len(calls) == 1:
                os.kill(os.getpid(), signal.SIGTERM)  # the handler raises Interrupted here: nothing is deferred yet
            return real(*args, **kwargs)
        patches = [mock.patch.object(mutate, "restore_and_verify", side_effect=second_signal_first)]
        if not retry:
            patches.append(mock.patch.object(mutate, "restore_until_done",
                                             side_effect=lambda *a: mutate.restore_and_verify(*a)))
        with contextlib.ExitStack() as stack:
            for patch in patches:
                stack.enter_context(patch)
            status, out = self.run_main(["--ids", "A", "--log-dir", self.logs], items=items)
        return status, out, len(calls)

    def test_a_second_signal_during_the_unwinding_still_restores_and_verifies(self):
        status, out, calls = self.run_double_signal(retry=True)
        self.assertEqual(calls, 2, "the restore must have run again after the second signal")
        self.assertEqual(status, mutate.EXIT_INTERRUPTED, out)
        self.assertIn("every restore is verified; no planted file is left mutated", out)
        self.assertEqual(read(self.path()).decode("utf-8"), TARGET)
        self.assertNotIn("f.py", self.marker_entries())

    def test_a_restore_that_never_ran_is_reported_failed_never_verified(self):
        # The control for the test above: the same two signals with no retry leave f.py mutated.
        # The run must say RESTORE FAILED, and must not print the "verified" line.
        status, out, calls = self.run_double_signal(retry=False)
        saved = os.path.join(self.logs, "A.orig")
        self.assertEqual(calls, 1)
        self.assertIn("return 3", read(self.path()).decode("utf-8"), "the control must leave the file mutated")
        self.assertEqual(status, mutate.EXIT_FAILED, out)
        self.assertNotIn("every restore is verified", out)
        self.assertIn(f"mutate: RESTORE FAILED for f.py; the original is saved at {saved}", out)
        self.assertEqual(self.marker_entries()["f.py"]["orig"], saved, "the marker still points at the saved original")

    def test_unparseable_marker_refuses_the_run_before_anything_is_planted(self):
        with open(self.marker_path(), "w", encoding="utf-8") as handle:
            handle.write("")
        with mock.patch.object(mutate, "run_killers") as runner:
            status, out = self.run_main(["--ids", "A", "--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_REFUSED, out)
        self.assertIn(self.marker_path(), out)
        self.assertIn("git checkout -- <file>", out)
        runner.assert_not_called()
        self.assertEqual(read(self.path()).decode("utf-8"), TARGET)

    def test_a_second_run_is_refused_while_another_holds_the_lock_and_runs_once_it_is_released(self):
        lock = mutate.acquire_run_lock(os.path.join(self.repo, ".git", mutate.LOCK_NAME))
        try:
            with mock.patch.object(mutate, "run_killers") as runner:
                status, out = self.run_main(["--ids", "A", "--log-dir", self.logs])
            self.assertEqual(status, mutate.EXIT_REFUSED, out)
            self.assertIn("another mutate run holds", out)
            runner.assert_not_called()
            self.assertEqual(read(self.path()).decode("utf-8"), TARGET)
        finally:
            os.close(lock)
        with mock.patch.object(mutate, "run_killers", return_value=[("T.test_x", "FAIL", "AssertionError: x")]):
            status, out = self.run_main(["--ids", "A", "--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_OK, out)

    def test_a_finished_run_releases_the_lock(self):
        with mock.patch.object(mutate, "run_killers", return_value=[("T.test_x", "FAIL", "AssertionError: x")]):
            first = self.run_main(["--ids", "A", "--log-dir", self.logs])
            second = self.run_main(["--ids", "A", "--log-dir", self.logs])
        self.assertEqual((first[0], second[0]), (mutate.EXIT_OK, mutate.EXIT_OK), (first[1], second[1]))

    def test_a_platform_without_posix_is_refused_with_a_message(self):
        with mock.patch.object(mutate, "POSIX", False), mock.patch.object(mutate, "run_killers") as runner:
            status, out = self.run_main(["--ids", "A", "--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_REFUSED, out)
        self.assertIn("POSIX only", out)
        runner.assert_not_called()
        with mock.patch.object(mutate, "run_killers", return_value=[("T.test_x", "FAIL", "AssertionError: x")]):
            status, out = self.run_main(["--ids", "A", "--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_OK, "positive control: the same run on this platform proceeds: " + out)

    def test_tool_error_during_run_exits_no_tool(self):
        # mutate_one itself is replaced here, so this checks only how a ToolError maps to an exit status.
        with mock.patch.object(mutate, "mutate_one", side_effect=mutate.ToolError("git status failed")):
            status, out = self.run_main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_NO_TOOL, out)

    def test_git_status_failing_before_the_loop_is_a_tool_error_not_a_traceback(self):
        real_git = mutate.git

        def failing(args):
            if args == ["status", "--porcelain"]:
                raise mutate.ToolError("git status failed (test)")
            return real_git(args)
        with mock.patch.object(mutate, "git", side_effect=failing), \
                mock.patch.object(mutate, "run_killers") as runner:
            status, out = self.run_main(["--ids", "A", "--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_NO_TOOL, out)
        self.assertIn("git status failed (test)", out)
        runner.assert_not_called()
        self.assertEqual(read(self.path()).decode("utf-8"), TARGET)

    def test_signal_before_the_loop_is_handled_and_handlers_are_restored(self):
        # The handlers are in place before the first git call, so a signal there stops the run as an interrupt.
        real_git = mutate.git
        before = signal.getsignal(signal.SIGTERM)

        def signalled(args):
            if args == ["status", "--porcelain"]:
                os.kill(os.getpid(), signal.SIGTERM)
            return real_git(args)
        with mock.patch.object(mutate, "git", side_effect=signalled), \
                mock.patch.object(mutate, "run_killers") as runner:
            status, out = self.run_main(["--ids", "A", "--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_INTERRUPTED, out)
        self.assertIn("interrupted by SIGTERM", out)
        runner.assert_not_called()
        self.assertIs(signal.getsignal(signal.SIGTERM), before)

    def test_timeout_option_reaches_each_killer_run(self):
        with mock.patch.object(mutate, "run_killers", return_value=[("T.test_x", "FAIL", "AssertionError: x")]) as runner:
            self.run_main(["--timeout", "7.5", "--ids", "A", "--log-dir", self.logs])
        self.assertEqual(runner.call_args[0][2], 7.5)

    def test_handler_is_installed_during_the_run_and_the_previous_one_restored_after(self):
        # Positive control: the run observes its own handler, so the restore check below cannot pass
        # merely because no handler was ever installed.
        before = {sig: signal.getsignal(sig) for sig in mutate.HANDLED_SIGNALS}
        during = []

        def killer(ent, log, timeout):
            during.extend(signal.getsignal(sig) is mutate._interrupt for sig in mutate.HANDLED_SIGNALS)
            return [("T.test_x", "FAIL", "AssertionError: x")]
        with mock.patch.object(mutate, "run_killers", side_effect=killer):
            self.run_main(["--ids", "A", "--log-dir", self.logs])
        self.assertEqual(during, [True] * len(mutate.HANDLED_SIGNALS))
        self.assertEqual({sig: signal.getsignal(sig) for sig in mutate.HANDLED_SIGNALS}, before)

    def test_sighup_ignored_by_the_parent_stays_ignored_during_and_after_the_run(self):
        before = signal.getsignal(signal.SIGHUP)
        self.addCleanup(signal.signal, signal.SIGHUP, signal.SIG_DFL if before is None else before)
        signal.signal(signal.SIGHUP, signal.SIG_IGN)  # what nohup leaves behind
        seen = {}

        def killer(ent, log, timeout):
            seen["hup"] = signal.getsignal(signal.SIGHUP)
            seen["term"] = signal.getsignal(signal.SIGTERM)
            return [("T.test_x", "FAIL", "AssertionError: x")]
        with mock.patch.object(mutate, "run_killers", side_effect=killer):
            self.run_main(["--ids", "A", "--log-dir", self.logs])
        self.assertEqual(seen["hup"], signal.SIG_IGN)
        self.assertIs(seen["term"], mutate._interrupt, "SIGTERM is still routed, so the control is live")
        self.assertEqual(signal.getsignal(signal.SIGHUP), signal.SIG_IGN)

    def test_none_previous_handler_is_restored_to_default(self):
        # getsignal() returns None for a handler not set from Python; signal() cannot take None back.
        before = signal.getsignal(signal.SIGTERM)
        self.addCleanup(signal.signal, signal.SIGTERM, signal.SIG_DFL if before is None else before)
        mutate.restore_handlers({signal.SIGTERM: None})
        self.assertEqual(signal.getsignal(signal.SIGTERM), signal.SIG_DFL)


class MarkerTests(_RepoTest):
    """The unrestored-plant marker in the git directory: its writes, its refusals, and what a refusal may name."""

    def marker(self):
        """The marker path in the temporary repository's git directory."""
        return os.path.join(self.repo, ".git", mutate.MARKER_NAME)

    def test_git_dir_is_the_repositorys_git_directory(self):
        self.assertEqual(os.path.realpath(mutate.git_dir()), os.path.realpath(os.path.join(self.repo, ".git")))

    def test_record_then_clear_keeps_the_other_entries(self):
        mutate.record_plant(self.marker(), "f.py", "A", self.logs, os.path.join(self.logs, "A.orig"))
        mutate.record_plant(self.marker(), "g.py", "G", self.logs, os.path.join(self.logs, "G.orig"))
        mutate.clear_plant(self.marker(), "f.py")
        self.assertEqual(list(mutate.read_marker(self.marker())), ["g.py"])

    def test_symlinked_marker_is_not_followed_on_write(self):
        victim = os.path.join(self.logs, "victim.txt")
        with open(victim, "w", encoding="utf-8") as handle:
            handle.write("keep me\n")
        os.symlink(victim, self.marker())
        with self.assertRaises(OSError):
            mutate.record_plant(self.marker(), "f.py", "A", self.logs, "x")
        self.assertEqual(read(victim), b"keep me\n")

    def test_unparseable_marker_is_not_overwritten_and_the_refusal_says_what_to_do(self):
        # "" is what a kill between truncate and write used to leave; the others are partial or wrong-shaped text.
        for text in ("", '{"f.py": {"id": ', "not json", "[]"):
            with self.subTest(text=text):
                with open(self.marker(), "w", encoding="utf-8") as handle:
                    handle.write(text)
                with self.assertRaises(ValueError) as ctx:
                    mutate.record_plant(self.marker(), "f.py", "A", self.logs, "x")
                self.assertIn(self.marker(), str(ctx.exception))
                self.assertIn("git checkout -- <file>", str(ctx.exception))
                self.assertIn(f"delete {self.marker()}", str(ctx.exception))
                self.assertEqual(read(self.marker()), text.encode("utf-8"))

    def test_write_is_atomic_leaves_no_temporary_file_and_is_private(self):
        mutate.write_marker(self.marker(), {"old": {}})
        names = os.listdir(os.path.dirname(self.marker()))
        self.assertEqual([n for n in names if n.startswith(mutate.MARKER_NAME)], [mutate.MARKER_NAME])
        self.assertEqual(os.stat(self.marker()).st_mode & 0o777, 0o600)
        self.assertEqual(mutate.read_marker(self.marker()), {"old": {}})

    def test_a_write_that_fails_part_way_keeps_the_old_marker_and_removes_its_temporary_file(self):
        mutate.write_marker(self.marker(), {"old": {}})
        before = os.listdir(os.path.dirname(self.marker()))
        for name in ("replace", "dump"):
            with self.subTest(failing=name):
                target = (mock.patch.object(mutate.os, "replace", side_effect=OSError("disk gone")) if name == "replace"
                          else mock.patch.object(mutate.json, "dump", side_effect=OSError("disk full")))
                with target, self.assertRaises(OSError):
                    mutate.write_marker(self.marker(), {"new": {}})
                self.assertEqual(mutate.read_marker(self.marker()), {"old": {}})
                self.assertEqual(os.listdir(os.path.dirname(self.marker())), before)
        mutate.write_marker(self.marker(), {"new": {}})  # control: the same call without a failure replaces it
        self.assertEqual(mutate.read_marker(self.marker()), {"new": {}})

    def test_write_over_a_symlink_replaces_the_link_and_leaves_its_target_alone(self):
        victim = os.path.join(self.logs, "victim.txt")
        with open(victim, "w", encoding="utf-8") as handle:
            handle.write("keep me\n")
        os.symlink(victim, self.marker())
        mutate.write_marker(self.marker(), {"a": {}})
        self.assertEqual(read(victim), b"keep me\n")
        self.assertFalse(os.path.islink(self.marker()))
        self.assertEqual(mutate.read_marker(self.marker()), {"a": {}})

    def test_mutate_one_refuses_to_plant_when_the_plant_cannot_be_recorded(self):
        victim = os.path.join(self.logs, "victim.txt")
        with open(victim, "w", encoding="utf-8") as handle:
            handle.write("keep me\n")
        os.symlink(victim, self.marker())
        with mock.patch.object(mutate, "run_killers") as runner:
            out = mutate.mutate_one(entry(), self.logs, 5)
        self.assertFalse(out.planted)
        self.assertTrue(out.restore_ok, "a refused plant wrote nothing, so there is nothing unrestored")
        self.assertTrue(any(p.startswith("cannot record the plant") for p in out.problems), out.problems)
        runner.assert_not_called()
        self.assertEqual(read(self.path()).decode("utf-8"), TARGET)
        self.assertEqual(read(victim), b"keep me\n")

    def test_mutate_one_plants_when_the_plant_can_be_recorded(self):
        # Positive control for the refusal above: with a writable marker the same mutant is planted and killed.
        with mock.patch.object(mutate, "run_killers", return_value=[("T.test_x", "FAIL", "AssertionError: x")]):
            out = mutate.mutate_one(entry(), self.logs, 5)
        self.assertTrue(out.planted)
        self.assertEqual(out.verdict, "KILLED")
        self.assertEqual(read(self.path()).decode("utf-8"), TARGET)

    def test_pointer_reads_nothing_through_a_symlinked_marker(self):
        saved = os.path.join(self.logs, "A.orig")
        with open(saved, "wb") as handle:
            handle.write(TARGET.encode("utf-8"))
        victim = os.path.join(self.logs, "victim.json")
        mutate.write_marker(victim, {"f.py": {"id": "A", "log_dir": self.logs, "orig": saved}})
        os.symlink(victim, self.marker())
        self.assertIn("the marker records no saved original", mutate.saved_original_note("f.py", self.marker()))

    def test_pointer_needs_the_original_to_sit_in_the_recorded_log_dir(self):
        other = tempfile.mkdtemp(prefix="mutate-test-other-")
        self.addCleanup(shutil.rmtree, other, True)
        saved = os.path.join(self.logs, "A.orig")
        with open(saved, "wb") as handle:
            handle.write(TARGET.encode("utf-8"))
        mutate.write_marker(self.marker(), {"f.py": {"id": "A", "log_dir": other, "orig": saved}})
        self.assertIn("the marker records no saved original", mutate.saved_original_note("f.py", self.marker()))
        mutate.write_marker(self.marker(), {"f.py": {"id": "A", "log_dir": self.logs, "orig": saved}})
        self.assertIn(f"its logs in {self.logs} hold its saved original {saved}",
                      mutate.saved_original_note("f.py", self.marker()))


class RestoreTests(_RepoTest):
    """restore_and_verify() on a real planted file, with the runner's real signal handler installed where stated."""

    def planted(self):
        """Record a plant of f.py and write its mutated text, as a run would. Returns (out, original, saved, marker)."""
        original = read(self.path())
        saved = os.path.join(self.logs, "A.orig")
        with open(saved, "wb") as handle:
            handle.write(original)
        marker = os.path.join(self.repo, ".git", mutate.MARKER_NAME)
        mutate.record_plant(marker, "f.py", "A", self.logs, saved)
        with open(self.path(), "wb") as handle:
            handle.write(TARGET.replace("return 1", "return 3").encode("utf-8"))
        out = mutate.Outcome("A")
        out.planted = True
        out.restore_ok = False  # as mutate_one leaves it: unverified until restore_and_verify says otherwise
        return out, original, saved, marker

    def test_verified_restore_clears_the_marker_entry(self):
        out, original, saved, marker = self.planted()
        self.assertFalse(out.restore_ok, "positive control: a planted file reads as unrestored until the restore runs")
        mutate.restore_and_verify(out, self.path(), "f.py", original, saved, marker)
        self.assertTrue(out.restore_ok, out.problems)
        self.assertNotIn("f.py", mutate.read_marker(marker))
        self.assertEqual(read(self.path()), original)

    def test_signal_during_a_restore_is_held_until_the_restore_is_verified(self):
        out, original, saved, marker = self.planted()
        real_tree_clean = mutate.tree_clean

        def clean_after_a_signal(relpath):
            os.kill(os.getpid(), signal.SIGTERM)  # arrives mid-restore, with the runner's handler in place
            return real_tree_clean(relpath)
        installed = {}
        mutate.install_handlers(installed)
        try:
            with mock.patch.object(mutate, "tree_clean", side_effect=clean_after_a_signal):
                mutate.restore_and_verify(out, self.path(), "f.py", original, saved, marker)
        finally:
            mutate.restore_handlers(installed)
        self.assertTrue(out.restore_ok, out.problems)
        self.assertEqual(out.late_signals, ["SIGTERM"])
        self.assertEqual(read(self.path()), original)

    def test_the_same_signal_outside_a_restore_raises_interrupted(self):
        # Positive control for the test above: with no restore running, the handler raises.
        installed = {}
        mutate.install_handlers(installed)
        try:
            with self.assertRaises(mutate.Interrupted):
                os.kill(os.getpid(), signal.SIGTERM)
        finally:
            mutate.restore_handlers(installed)


class PlantWindowTests(_RepoTest):
    """mutate_one() between the moment a plant is recorded and the moment its restore is verified."""

    def setUp(self):
        super().setUp()
        self.installed = {}
        mutate.install_handlers(self.installed)
        self.addCleanup(mutate.restore_handlers, self.installed)

    def marker(self):
        """The marker path in the temporary repository's git directory."""
        return os.path.join(self.repo, ".git", mutate.MARKER_NAME)

    def test_a_signal_right_after_the_plant_is_recorded_still_restores_and_clears_the_entry(self):
        # The signal lands after record_plant returns and before the file is written. The restore still runs
        # in the finally block, and the entry must not be left behind for a file nobody mutated.
        real = mutate.record_plant

        def record_then_signal(*args):
            real(*args)
            os.kill(os.getpid(), signal.SIGTERM)
        out = mutate.Outcome("M1")
        with mock.patch.object(mutate, "record_plant", side_effect=record_then_signal), \
                mock.patch.object(mutate, "run_killers") as runner:
            with self.assertRaises(mutate.Interrupted):
                mutate.mutate_one(entry(), self.logs, 5, out)
        runner.assert_not_called()
        self.assertTrue(out.planted)
        self.assertTrue(out.restore_ok, out.problems)
        self.assertNotIn("f.py", mutate.read_marker(self.marker()))
        self.assertEqual(read(self.path()).decode("utf-8"), TARGET)

    def test_a_restore_that_never_ran_leaves_the_outcome_unrestored(self):
        # restore_until_done is replaced by a function that fails before restoring anything. The Outcome
        # must read planted and not restored, and finish_early must report it, whatever the status.
        out = mutate.Outcome("M1")
        with mock.patch.object(mutate, "run_killers", return_value=[("T.test_x", "FAIL", "AssertionError: x")]), \
                mock.patch.object(mutate, "restore_until_done", side_effect=RuntimeError("never ran")):
            with self.assertRaises(RuntimeError):
                mutate.mutate_one(entry(), self.logs, 5, out)
        self.assertTrue(out.planted)
        self.assertFalse(out.restore_ok)
        self.assertIn("return 3", read(self.path()).decode("utf-8"))
        printed = io.StringIO()
        with contextlib.redirect_stdout(printed):
            status = mutate.finish_early([], out, mutate.EXIT_INTERRUPTED)
        self.assertEqual(status, mutate.EXIT_FAILED)
        self.assertIn("RESTORE FAILED", printed.getvalue())
        self.assertNotIn("every restore is verified", printed.getvalue())

    def test_a_restore_that_runs_leaves_the_outcome_restored(self):
        # Positive control for the test above: the same run with the real restore ends restore_ok True.
        out = mutate.Outcome("M1")
        with mock.patch.object(mutate, "run_killers", return_value=[("T.test_x", "FAIL", "AssertionError: x")]):
            mutate.mutate_one(entry(), self.logs, 5, out)
        self.assertTrue(out.planted)
        self.assertTrue(out.restore_ok, out.problems)
        printed = io.StringIO()
        with contextlib.redirect_stdout(printed):
            status = mutate.finish_early([], out, mutate.EXIT_INTERRUPTED)
        self.assertEqual(status, mutate.EXIT_INTERRUPTED)


class RunLockTests(_RepoTest):
    """acquire_run_lock() on a real file."""

    def lock_path(self):
        """The run lock path in the temporary repository's git directory."""
        return os.path.join(self.repo, ".git", mutate.LOCK_NAME)

    def test_second_holder_is_refused_and_a_released_lock_can_be_taken_again(self):
        first = mutate.acquire_run_lock(self.lock_path())
        with self.assertRaises(mutate.RunLocked) as ctx:
            mutate.acquire_run_lock(self.lock_path())
        self.assertIn(self.lock_path(), str(ctx.exception))
        os.close(first)
        os.close(mutate.acquire_run_lock(self.lock_path()))  # control: the refusal came from the held lock

    def test_lock_file_is_private_and_a_symlink_is_not_followed(self):
        os.close(mutate.acquire_run_lock(self.lock_path()))
        self.assertEqual(os.stat(self.lock_path()).st_mode & 0o777, 0o600)
        os.unlink(self.lock_path())
        victim = os.path.join(self.logs, "victim.txt")
        with open(victim, "w", encoding="utf-8") as handle:
            handle.write("keep me\n")
        os.symlink(victim, self.lock_path())
        with self.assertRaises(mutate.RunLocked):
            mutate.acquire_run_lock(self.lock_path())
        self.assertEqual(read(victim), b"keep me\n")


class GroupKillTests(unittest.TestCase):
    """A killer that times out takes its whole process group with it, grandchildren included."""

    def setUp(self):
        self.repo = make_repo({"f.py": TARGET})
        self.tmp = tempfile.mkdtemp(prefix="mutate-test-group-")
        self.logs = tempfile.mkdtemp(prefix="mutate-test-logs-")
        patcher = mock.patch.object(mutate, "ROOT", self.repo)
        patcher.start()
        self.addCleanup(patcher.stop)
        for path in (self.repo, self.tmp, self.logs):
            self.addCleanup(shutil.rmtree, path, True)

    def run_spawn(self, control=False):
        """
        Run a suite whose test starts a `sleep` grandchild and runs past a 2-second timeout. With
        `control`, _kill_group is replaced by a kill of the leader alone. Returns the grandchild's pid.
        """
        pidfile = os.path.join(self.tmp, "grandchild.pid")
        suite = os.path.join(self.tmp, "suite_spawn.py")
        with open(suite, "w", encoding="utf-8") as handle:
            handle.write(SPAWN_SUITE.format(pidfile=pidfile))
        ent = entry(suite=suite, killers=["Case.test_spawns"])
        patch = (mock.patch.object(mutate, "_kill_group", side_effect=lambda proc: proc.kill())
                 if control else contextlib.nullcontext())
        with open(os.path.join(self.logs, "x.log"), "w", encoding="utf-8") as log, patch:
            results = mutate.run_killers(ent, log, 2)
        self.assertEqual(results, [("Case.test_spawns", "TIMEOUT", "timed out after 2s")])
        (grandchild,) = wait_for_pids(pidfile, 1)
        self.addCleanup(kill_quietly, grandchild)
        return grandchild

    def test_timeout_kills_the_grandchild_with_its_killer(self):
        grandchild = self.run_spawn()
        self.assertTrue(pid_gone(grandchild), "the grandchild outlived the timeout")

    def test_control_killing_only_the_leader_leaves_the_grandchild_running(self):
        grandchild = self.run_spawn(control=True)
        self.assertFalse(pid_gone(grandchild, 1.0), "the grandchild should survive a leader-only kill")


@unittest.skipUnless(shutil.which("go"), "go is not installed")
class GoGroupKillTests(unittest.TestCase):
    """The Go branch: `go test` leads a group whose test binary and sleep child must die with it."""

    def setUp(self):
        self.repo = make_repo({"go.mod": "module hang\n\ngo 1.18\n", "hang_test.go": GO_HANG_TEST})
        self.logs = tempfile.mkdtemp(prefix="mutate-test-logs-")
        patcher = mock.patch.object(mutate, "ROOT", self.repo)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.addCleanup(shutil.rmtree, self.repo, True)
        self.addCleanup(shutil.rmtree, self.logs, True)
        # Build the package first, so the timed run is spent on the hang and not on the compile.
        subprocess.run(["go", "test", "-count=1", "-run", "^$", "./"], cwd=self.repo, capture_output=True, check=True)

    def run_hang(self, control=False):
        """Run TestHang under a 3-second timeout. Returns (test binary pid, sleep pid)."""
        ent = entry(language="go", package="./", killers=["TestHang"])
        patch = (mock.patch.object(mutate, "_kill_group", side_effect=lambda proc: proc.kill())
                 if control else contextlib.nullcontext())
        with open(os.path.join(self.logs, "x.log"), "w", encoding="utf-8") as log, patch:
            results = mutate.run_killers(ent, log, 3)
        self.assertEqual(results, [("TestHang", "TIMEOUT", "timed out after 3s")])
        test_pid, sleeper = wait_for_pids(os.path.join(self.repo, "pids.txt"), 2)
        self.addCleanup(kill_quietly, test_pid)
        self.addCleanup(kill_quietly, sleeper)
        return test_pid, sleeper

    def test_timeout_kills_the_go_test_binary_and_its_sleep_child(self):
        test_pid, sleeper = self.run_hang()
        self.assertTrue(pid_gone(test_pid), "the test binary outlived the timeout")
        self.assertTrue(pid_gone(sleeper), "the sleep started by the test outlived the timeout")

    def test_control_killing_only_the_go_leader_leaves_the_sleep_running(self):
        _, sleeper = self.run_hang(control=True)
        self.assertFalse(pid_gone(sleeper, 1.0), "the sleep should survive a leader-only kill")


class KillerLaunchTests(_RepoTest):
    """A killer that cannot start is a ToolError. Only a Go killer needs go on PATH."""

    def setUp(self):
        super().setUp()
        self.empty = tempfile.mkdtemp(prefix="mutate-test-path-")
        self.addCleanup(shutil.rmtree, self.empty, True)
        self.no_path = mock.patch.dict(os.environ, {"PATH": self.empty})

    def test_go_killer_with_no_go_on_path_is_a_tool_error(self):
        ent = entry(language="go", package="./", killers=["TestX"])
        with self.no_path, open(os.path.join(self.logs, "x.log"), "w", encoding="utf-8") as log:
            with self.assertRaises(mutate.ToolError) as ctx:
                mutate.run_killers(ent, log, 5)
        self.assertIn("cannot run go", str(ctx.exception))

    def test_python_killer_still_runs_with_an_empty_path(self):
        # Positive control: sys.executable is an absolute path, so the empty PATH does not stop a Python killer.
        suite = os.path.join(self.logs, "suite_ok.py")
        with open(suite, "w", encoding="utf-8") as handle:
            handle.write("import unittest\nclass Case(unittest.TestCase):\n    def test_ok(self):\n"
                         "        self.assertTrue(True)\nif __name__ == '__main__':\n    unittest.main()\n")
        ent = entry(suite=suite, killers=["Case.test_ok"])
        with self.no_path, open(os.path.join(self.logs, "x.log"), "w", encoding="utf-8") as log:
            results = mutate.run_killers(ent, log, 30)
        self.assertEqual(results, [("Case.test_ok", "ok", None)])


class HandlerDispositionTests(unittest.TestCase):
    """install_handlers() and restore_handlers() on the real process signal table."""

    def test_handlers_are_routed_then_put_back_exactly(self):
        before = {sig: signal.getsignal(sig) for sig in mutate.HANDLED_SIGNALS}
        installed = {}
        mutate.install_handlers(installed)
        try:
            self.assertTrue(all(signal.getsignal(sig) is mutate._interrupt for sig in installed))
        finally:
            mutate.restore_handlers(installed)
        self.assertEqual({sig: signal.getsignal(sig) for sig in mutate.HANDLED_SIGNALS}, before)

    def test_an_ignored_signal_is_left_ignored_and_not_recorded(self):
        before = signal.getsignal(signal.SIGHUP)
        self.addCleanup(signal.signal, signal.SIGHUP, signal.SIG_DFL if before is None else before)
        signal.signal(signal.SIGHUP, signal.SIG_IGN)
        installed = {}
        mutate.install_handlers(installed)
        try:
            self.assertNotIn(signal.SIGHUP, installed)
            self.assertEqual(signal.getsignal(signal.SIGHUP), signal.SIG_IGN)
            self.assertIs(signal.getsignal(signal.SIGTERM), mutate._interrupt)  # positive control: routing happened
        finally:
            mutate.restore_handlers(installed)


class RestoreHandlersTests(unittest.TestCase):
    """restore_handlers() with a signal arriving while the dispositions go back."""

    def test_a_signal_during_the_restore_is_held_then_delivered_to_the_restored_handler(self):
        hits = []

        def recorder(signum, _frame):
            hits.append(signum)
        saved = {sig: signal.getsignal(sig) for sig in mutate.HANDLED_SIGNALS}
        for sig in mutate.HANDLED_SIGNALS:
            self.addCleanup(signal.signal, sig, signal.SIG_DFL if saved[sig] is None else saved[sig])
            signal.signal(sig, recorder)
        installed = {}
        mutate.install_handlers(installed)
        self.assertIs(signal.getsignal(signal.SIGTERM), mutate._interrupt, "control: the runner's handler is in place")
        real_signal = signal.signal
        sent = []

        def signal_then_set(sig, handler):
            if not sent:
                sent.append(sig)
                os.kill(os.getpid(), signal.SIGTERM)  # unmasked, the runner's handler would raise Interrupted here
            return real_signal(sig, handler)
        with mock.patch.object(mutate.signal, "signal", side_effect=signal_then_set):
            mutate.restore_handlers(installed)  # must not raise
        self.assertEqual(sent and hits, [signal.SIGTERM])
        self.assertTrue(all(signal.getsignal(sig) is recorder for sig in mutate.HANDLED_SIGNALS))


class WaitForPidsTests(unittest.TestCase):
    """wait_for_pids(): the poll the group-kill tests rely on."""

    def setUp(self):
        self.dir = tempfile.mkdtemp(prefix="mutate-test-pids-")
        self.addCleanup(shutil.rmtree, self.dir, True)
        self.path = os.path.join(self.dir, "pids.txt")

    def test_waits_for_a_file_that_appears_late_and_whole(self):
        def late():
            time.sleep(0.3)
            with open(self.path, "w", encoding="utf-8") as handle:
                handle.write("12 34\n")
        thread = threading.Thread(target=late)
        thread.start()
        self.addCleanup(thread.join)
        self.assertEqual(wait_for_pids(self.path, 2, seconds=10), [12, 34])

    def test_a_file_that_never_appears_or_never_fills_is_a_clear_failure(self):
        with self.assertRaises(AssertionError) as ctx:
            wait_for_pids(self.path, 1, seconds=0.2)
        self.assertIn(self.path, str(ctx.exception))
        with open(self.path, "w", encoding="utf-8") as handle:
            handle.write("12\n")
        with self.assertRaises(AssertionError):
            wait_for_pids(self.path, 2, seconds=0.2)  # one pid where two are due


def signal_names(text):
    """The signal names (SIGxxx words) that appear in `text`."""
    return set(re.findall(r"\bSIG[A-Z0-9]+\b", text))


def mutation_section(readme):
    """The README text from '## Mutation testing' up to the next '## ' heading."""
    start = readme.index("\n## Mutation testing\n")
    end = readme.index("\n## ", start + 1)
    return readme[start:end]


class SignalProseTests(unittest.TestCase):
    """The prose points at HANDLED_SIGNALS instead of naming its members, so it cannot go stale (DIR-014)."""

    def setUp(self):
        with open(os.path.join(HERE, "..", "testdata", "README.md"), encoding="utf-8") as handle:
            self.section = mutation_section(handle.read())
        self.handled = {signal.Signals(sig).name for sig in mutate.HANDLED_SIGNALS}

    def test_neither_the_module_docstring_nor_the_readme_names_a_handled_signal(self):
        self.assertEqual(signal_names(mutate.__doc__) & self.handled, set())
        self.assertEqual(signal_names(self.section) & self.handled, set())

    def test_control_the_scan_reads_both_texts_and_flags_a_handled_name(self):
        self.assertIn("HANDLED_SIGNALS", mutate.__doc__)
        self.assertIn("HANDLED_SIGNALS", self.section)
        self.assertEqual(signal_names(mutate.__doc__ + " SIGTERM") & self.handled, {"SIGTERM"})
        self.assertEqual(signal_names(self.section + " SIGHUP") & self.handled, {"SIGHUP"})


class GitHelperTests(_RepoTest):
    """The test git helper and its environment."""

    def test_git_run_ignores_inherited_git_directory_variables(self):
        bogus = {"GIT_DIR": os.path.join(self.repo, "not-a-git-dir"),
                 "GIT_INDEX_FILE": os.path.join(self.repo, "not-an-index")}
        with mock.patch.dict(os.environ, bogus):
            proc = git_run(self.repo, "rev-parse", "--git-dir")
        self.assertEqual(proc.stdout.strip(), ".git")

    def test_control_plain_git_is_aimed_by_the_same_variables(self):
        # Positive control: the variables do redirect a plain git call, so the test above is not vacuous.
        bogus = {"GIT_DIR": os.path.join(self.repo, "not-a-git-dir")}
        with mock.patch.dict(os.environ, bogus):
            proc = subprocess.run(["git", "-C", self.repo, "rev-parse", "--git-dir"], capture_output=True, text=True)
        self.assertNotEqual(proc.returncode, 0)


class TimeoutArgumentTests(unittest.TestCase):
    """--timeout accepts a positive, finite number of seconds and nothing else."""

    def test_positive_finite_seconds_are_accepted(self):
        self.assertEqual(mutate._positive_timeout("7.5"), 7.5)

    def test_zero_negative_nan_and_infinite_are_rejected(self):
        for text in ("0", "-3", "nan", "inf", "-inf"):
            with self.subTest(text=text), self.assertRaises(argparse.ArgumentTypeError):
                mutate._positive_timeout(text)


if __name__ == "__main__":
    unittest.main()

"""
Unit tests for scripts/mutate.py: the DIR-017 mutation runner.

Nothing here touches the real repository or the network. Each test builds a
temporary git repository (or a temporary directory) and points mutate.ROOT at
it. Tests that need a killer's verdict either patch run_killers() with canned
results, or run a small real unittest suite written into the temporary
directory. The real spec is never run by `make test`; `make mutate` does that.

Each test name says the edge it covers.
"""

import contextlib
import io
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import mutate  # noqa: E402  (must follow the sys.path insert)
import check_mutants as cm  # noqa: E402

RULE = mutate.RULE
TARGET = "def f():\n    return 1\n    return 2\n"


def git_run(repo, *args):
    """Run git in a temporary repository with a fixed identity. Raises on failure."""
    return subprocess.run(
        ["git", "-C", repo, "-c", "user.name=test", "-c", "user.email=test@example.invalid",
         "-c", "commit.gpgsign=false", *args],
        capture_output=True, text=True, check=True)


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
    """tree_clean(): clean against HEAD means no staged, unstaged, untracked or ignored change."""

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


class ReadmeExitTableTests(unittest.TestCase):
    """The exit-status table in testdata/README.md against EXIT_DOC, the source of --help."""

    def test_readme_exit_table_matches_exit_doc(self):
        readme = os.path.join(HERE, "..", "testdata", "README.md")
        with open(readme, encoding="utf-8") as handle:
            text = handle.read()
        for code, meaning in mutate.EXIT_DOC.items():
            self.assertIn(f"| {code} | {meaning} |", text, f"exit {code} row missing or different")


class MainTests(_RepoTest):
    """main(): exit statuses, refusals, and the SHARED-TEXT verdict."""

    def setUp(self):
        super().setUp()
        self.items = [entry("A", mirror="B"), entry("B", line=3, before="return 2", after="return 4", mirror="A")]
        # The temporary directory holds the last-run marker, so it is private to each test.
        self.scratch = tempfile.mkdtemp(prefix="mutate-test-scratch-")
        self.addCleanup(shutil.rmtree, self.scratch, True)
        patcher = mock.patch.object(tempfile, "gettempdir", return_value=self.scratch)
        patcher.start()
        self.addCleanup(patcher.stop)

    def write_marker(self, path):
        """Name `path` as the last run's log directory, as a previous run would have."""
        with open(mutate.last_run_marker(), "w", encoding="utf-8") as handle:
            handle.write(path + "\n")

    def test_run_records_its_log_directory_in_the_marker(self):
        with mock.patch.object(mutate, "run_killers", return_value=[("T.test_x", "FAIL", "AssertionError: x")]):
            self.run_main(["--ids", "A", "--log-dir", self.logs])
        with open(mutate.last_run_marker(), encoding="utf-8") as handle:
            self.assertEqual(handle.read(), os.path.abspath(self.logs) + "\n")

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

    def test_dirty_tree_is_refused_and_names_the_last_runs_saved_originals(self):
        with open(self.path(), "a", encoding="utf-8") as handle:
            handle.write("# local edit\n")
        saved = os.path.join(self.scratch, "earlier-run")
        os.makedirs(saved)
        with open(os.path.join(saved, "A.orig"), "wb") as handle:
            handle.write(TARGET.encode("utf-8"))
        self.write_marker(saved)
        status, out = self.run_main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_REFUSED, out)
        self.assertIn("f.py has changes against HEAD", out)
        self.assertIn("the last run's saved originals are in " + saved, out)

    def test_dirty_tree_whose_last_run_kept_no_originals_says_so(self):
        with open(self.path(), "a", encoding="utf-8") as handle:
            handle.write("# local edit\n")
        empty = os.path.join(self.scratch, "empty-run")
        os.makedirs(empty)
        self.write_marker(empty)
        status, out = self.run_main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_REFUSED)
        self.assertIn("no saved originals from an earlier run", out)

    def test_dirty_tree_with_no_earlier_run_says_so(self):
        with open(self.path(), "a", encoding="utf-8") as handle:
            handle.write("# local edit\n")
        status, out = self.run_main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_REFUSED)
        self.assertIn("no saved originals from an earlier run", out)

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

    def test_interrupt_exits_interrupted(self):
        with mock.patch.object(mutate, "mutate_one", side_effect=mutate.Interrupted("SIGTERM")):
            status, out = self.run_main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_INTERRUPTED, out)
        self.assertIn("interrupted by SIGTERM", out)

    def test_tool_error_during_run_exits_no_tool(self):
        with mock.patch.object(mutate, "mutate_one", side_effect=mutate.ToolError("git status failed")):
            status, out = self.run_main(["--log-dir", self.logs])
        self.assertEqual(status, mutate.EXIT_NO_TOOL, out)

    def test_timeout_option_reaches_each_killer_run(self):
        with mock.patch.object(mutate, "run_killers", return_value=[("T.test_x", "FAIL", "AssertionError: x")]) as runner:
            self.run_main(["--timeout", "7.5", "--ids", "A", "--log-dir", self.logs])
        self.assertEqual(runner.call_args[0][2], 7.5)

    def test_signal_handlers_are_restored_after_the_run(self):
        before = signal.getsignal(signal.SIGTERM)
        with mock.patch.object(mutate, "run_killers", return_value=[("T.test_x", "FAIL", "AssertionError: x")]):
            self.run_main(["--ids", "A", "--log-dir", self.logs])
        self.assertIs(signal.getsignal(signal.SIGTERM), before)


if __name__ == "__main__":
    unittest.main()

"""
Tests for scripts/install-triage-schedule.sh: the --root option, and the default
it replaces.

Each test runs the script with HOME in a temporary directory, so the agents
directory and the log directory are temporary too. Every run that is not a dry
run puts a recording stub named launchctl first on PATH. The stub only appends
its arguments to a file the test names, so no test calls the real launchctl or
touches a live com.bishop-memory.* job.

Expected behaviour of the stub, stated before any run: a non-dry-run install with
a valid root records three "load -w" lines, one per kind; an uninstall records one
"unload" line per plist it removes; a refused run records nothing. The refusal
tests assert the last of these, and their control is the valid-root test, which
records the first.

The script is macOS-only, so the module is skipped elsewhere.
"""

import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT_SRC = os.path.join(HERE, "install-triage-schedule.sh")
TEMPLATE_SRC = os.path.join(HERE, "com.bishop-memory.triage.plist")
KINDS = ("classify", "process", "grade")
DRY_RUN_MARKER = "[install-triage] --dry-run: would write "

STUB_LAUNCHCTL = """#!/bin/sh
# Test stub for launchctl: records its arguments in $STUB_LAUNCHCTL_LOG and does nothing else.
printf '%s\\n' "$*" >> "$STUB_LAUNCHCTL_LOG"
exit 0
"""


def verdict(proc):
    """The return code and both streams of a finished run, for an assertion message."""
    return "rc=%s stdout=%r stderr=%r" % (proc.returncode, proc.stdout, proc.stderr)


def plist_block(out, kind):
    """The dry-run text of the plist for one kind, or an empty string when the output has none for it."""
    marker = "com.bishop-memory.triage-%s.plist:" % kind
    for chunk in out.split(DRY_RUN_MARKER)[1:]:
        if chunk.split("\n", 1)[0].endswith(marker):
            return chunk
    return ""


def plist_paths(block):
    """The program root and the working directory in one rendered plist: the directory above scripts/triage-run.sh, and WorkingDirectory."""
    program = re.search(r"<string>([^<]*)/scripts/triage-run\.sh</string>", block)
    working = re.search(r"<key>WorkingDirectory</key>\s*<string>([^<]*)</string>", block)
    return (program.group(1) if program else None, working.group(1) if working else None)


@unittest.skipUnless(sys.platform == "darwin", "install-triage-schedule.sh is macOS-only (launchd)")
class InstallCase(unittest.TestCase):
    """The fixture: a temporary HOME, a recording launchctl stub, and the helpers the tests share."""

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="install-triage-test-")
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.home = os.path.join(self.tmp, "home")
        os.makedirs(self.home)
        self.agents = os.path.join(self.home, "Library", "LaunchAgents")
        self.logs = os.path.join(self.home, "Library", "Logs", "bishop-memory")
        self.calls = os.path.join(self.tmp, "launchctl.calls")
        self.stub_dir = os.path.join(self.tmp, "stubs")
        os.makedirs(self.stub_dir)
        stub = os.path.join(self.stub_dir, "launchctl")
        with open(stub, "w") as fh:
            fh.write(STUB_LAUNCHCTL)
        os.chmod(stub, 0o755)

    def make_root(self, name="checkout", triage_run=True, run_mode=0o755, mcpd=True, mcpd_mode=0o755):
        """A directory shaped like a checkout: scripts/triage-run.sh and bin/mcpd. The modes and the presence of each file can be varied."""
        root = os.path.join(self.tmp, name)
        os.makedirs(os.path.join(root, "scripts"))
        os.makedirs(os.path.join(root, "bin"))
        if triage_run:
            path = os.path.join(root, "scripts", "triage-run.sh")
            with open(path, "w") as fh:
                fh.write("#!/bin/sh\nexit 0\n")
            os.chmod(path, run_mode)
        if mcpd:
            path = os.path.join(root, "bin", "mcpd")
            with open(path, "w") as fh:
                fh.write("#!/bin/sh\nexit 0\n")
            os.chmod(path, mcpd_mode)
        return root

    def run_install(self, *args, cwd=None, script=SCRIPT_SRC):
        """Run the install script with the given arguments. The stub launchctl is first on PATH, and HOME is the temporary directory."""
        env = {
            "PATH": self.stub_dir + os.pathsep + os.environ.get("PATH", "/usr/bin:/bin"),
            "HOME": self.home,
            "STUB_LAUNCHCTL_LOG": self.calls,
        }
        return subprocess.run(["/bin/bash", script, *args], cwd=cwd or self.tmp, env=env,
                              capture_output=True, text=True, stdin=subprocess.DEVNULL)

    def calls_text(self):
        """What the launchctl stub recorded, or an empty string when it was never called."""
        if not os.path.exists(self.calls):
            return ""
        with open(self.calls) as fh:
            return fh.read()

    def plist_path(self, kind):
        """Where the installed plist for one kind lives."""
        return os.path.join(self.agents, "com.bishop-memory.triage-%s.plist" % kind)

    def assert_refused_before_any_write(self, proc, message):
        """The run exits 64 with the message, and it wrote no agents directory, no log directory, and called no launchctl."""
        self.assertEqual(proc.returncode, 64, verdict(proc))
        self.assertIn(message, proc.stderr, verdict(proc))
        self.assertFalse(os.path.exists(self.agents), "nothing may be written under the agents directory")
        self.assertFalse(os.path.exists(self.logs), "nothing may be written under the log directory")
        self.assertEqual(self.calls_text(), "", "launchctl must not be called on a refused run")


class RootRendering(InstallCase):
    """What --root puts into the plists."""

    def test_dry_run_with_root_renders_every_job_from_that_root(self):
        """Path: --dry-run --root DIR, for each of the three kinds. The program path and WorkingDirectory of every rendered plist are DIR, and no placeholder is left.

        The control is that each kind's block is found at all; an empty block fails the test.
        Recorded against mutant INSTALL-ROOT-IGNORED, which renders the default root whatever --root says, and fails here.
        """
        root = self.make_root()
        proc = self.run_install("--dry-run", "--root", root)
        self.assertEqual(proc.returncode, 0, verdict(proc))
        for kind in KINDS:
            with self.subTest(kind=kind):
                block = plist_block(proc.stdout, kind)
                self.assertTrue(block, "control: no dry-run plist was found for %s: %s" % (kind, verdict(proc)))
                program, working = plist_paths(block)
                self.assertEqual(os.path.realpath(program or ""), os.path.realpath(root), block)
                self.assertEqual(os.path.realpath(working or ""), os.path.realpath(root), block)
                self.assertNotIn("__BISHOP_ROOT__", block)

    def test_without_root_the_jobs_run_from_the_checkout_that_holds_the_script(self):
        """Path: the script is run from a copy of itself in a temporary tree, with no --root. The default root is the tree that holds the script, so the plists name that tree. The usage text documents --root.

        Recorded against mutant INSTALL-DEFAULT-ROOT-LOST, which renders an empty root when --root is absent, and fails here.
        """
        tree = os.path.join(self.tmp, "tree")
        os.makedirs(os.path.join(tree, "scripts"))
        script = os.path.join(tree, "scripts", "install-triage-schedule.sh")
        shutil.copy(SCRIPT_SRC, script)
        shutil.copy(TEMPLATE_SRC, os.path.join(tree, "scripts", "com.bishop-memory.triage.plist"))
        proc = self.run_install("--dry-run", script=script)
        self.assertEqual(proc.returncode, 0, verdict(proc))
        for kind in KINDS:
            with self.subTest(kind=kind):
                block = plist_block(proc.stdout, kind)
                self.assertTrue(block, "control: no dry-run plist was found for %s: %s" % (kind, verdict(proc)))
                program, working = plist_paths(block)
                self.assertEqual(os.path.realpath(program or ""), os.path.realpath(tree), block)
                self.assertEqual(os.path.realpath(working or ""), os.path.realpath(tree), block)
        helped = self.run_install("--help", script=script)
        self.assertEqual(helped.returncode, 0, verdict(helped))
        self.assertIn("--root DIR", helped.stdout, "the usage text must document --root")

    def test_root_in_the_equals_form_renders_the_same_plists_as_the_separate_form(self):
        """Path: --root=DIR and --root DIR, each as a dry run. The two outputs are identical, and the plists name DIR.

        Recorded against mutant INSTALL-ROOT-EQUALS-DROPPED, which removes the --root=DIR case so the flag is refused as unknown, and fails here.
        """
        root = self.make_root()
        separate = self.run_install("--dry-run", "--root", root)
        equals = self.run_install("--dry-run", "--root=" + root)
        self.assertEqual(separate.returncode, 0, verdict(separate))
        self.assertEqual(equals.returncode, 0, verdict(equals))
        self.assertEqual(equals.stdout, separate.stdout, "the two forms must render the same output")
        program, _ = plist_paths(plist_block(equals.stdout, "classify"))
        self.assertEqual(os.path.realpath(program or ""), os.path.realpath(root), "control: the equals form names DIR")

    def test_a_relative_root_is_written_into_the_plists_as_an_absolute_path(self):
        """Path: --root with a relative DIR, run from the directory that holds it. Every plist names the absolute path of DIR.

        Recorded against mutant INSTALL-ROOT-RELATIVE-KEPT, which writes the argument as given, and fails here.
        """
        root = self.make_root("checkout")
        proc = self.run_install("--dry-run", "--root", "checkout", cwd=self.tmp)
        self.assertEqual(proc.returncode, 0, verdict(proc))
        for kind in KINDS:
            with self.subTest(kind=kind):
                block = plist_block(proc.stdout, kind)
                self.assertTrue(block, "control: no dry-run plist was found for %s: %s" % (kind, verdict(proc)))
                program, working = plist_paths(block)
                self.assertTrue(program and os.path.isabs(program), "the program path must be absolute: %r" % program)
                self.assertTrue(working and os.path.isabs(working), "WorkingDirectory must be absolute: %r" % working)
                self.assertEqual(os.path.realpath(program), os.path.realpath(root), block)


class RootValidation(InstallCase):
    """--root is refused before anything is written, for each of the three conditions."""

    def test_a_root_that_is_not_a_directory_is_refused_before_any_write(self):
        """Path: --root names a path that does not exist. The run exits 64, names the path as not a directory, and writes nothing.

        The control is test_a_valid_root_writes_the_three_jobs_and_records_each_load, which passes the same checks.
        Recorded against mutant INSTALL-ROOT-DIR-UNCHECKED, which drops the directory test, and fails here.
        """
        missing = os.path.join(self.tmp, "nowhere")
        proc = self.run_install("--root", missing)
        self.assert_refused_before_any_write(proc, "--root %s is not a directory" % missing)

    def test_a_root_without_an_executable_triage_run_is_refused_before_any_write(self):
        """Path: --root is a directory with no scripts/triage-run.sh, and with one that is present but not executable. Each is refused with the message that names scripts/triage-run.sh, and nothing is written.

        Recorded against mutant INSTALL-ROOT-RUN-UNCHECKED, which drops the scripts/triage-run.sh test, and fails here for the missing case and the non-executable case.
        """
        cases = (
            ("missing", {"triage_run": False}),
            ("not executable", {"run_mode": 0o644}),
        )
        for label, options in cases:
            with self.subTest(case=label):
                root = self.make_root("root-" + label.replace(" ", "-"), **options)
                proc = self.run_install("--root", root)
                self.assert_refused_before_any_write(proc, "--root %s has no executable scripts/triage-run.sh" % root)

    def test_a_root_without_an_executable_mcpd_is_refused_before_any_write(self):
        """Path: --root is a directory whose bin/mcpd is missing, and one whose bin/mcpd is present but not executable. Each is refused with the message that names bin/mcpd, and nothing is written.

        Recorded against mutant INSTALL-ROOT-MCPD-UNCHECKED, which drops the bin/mcpd test, and fails here for both cases.
        """
        cases = (
            ("missing", {"mcpd": False}),
            ("not executable", {"mcpd_mode": 0o644}),
        )
        for label, options in cases:
            with self.subTest(case=label):
                root = self.make_root("root-" + label.replace(" ", "-"), **options)
                proc = self.run_install("--root", root)
                self.assert_refused_before_any_write(proc, "--root %s has no executable bin/mcpd" % root)

    def test_a_valid_root_writes_the_three_jobs_and_records_each_load(self):
        """Path: --root names a valid checkout and the run is not a dry run. The three plists are written under the agents directory, each names the root, and the stub records one load for each.

        This is the control for the refusal tests above: the same arguments, with a valid root, reach the write and the launchctl calls.
        """
        root = self.make_root()
        proc = self.run_install("--root", root)
        self.assertEqual(proc.returncode, 0, verdict(proc))
        calls = self.calls_text()
        self.assertTrue(calls, "control: the stub must record the launchctl calls")
        for kind in KINDS:
            with self.subTest(kind=kind):
                path = self.plist_path(kind)
                self.assertTrue(os.path.exists(path), "%s was not written" % path)
                with open(path) as fh:
                    program, working = plist_paths(fh.read())
                self.assertEqual(os.path.realpath(program or ""), os.path.realpath(root))
                self.assertEqual(os.path.realpath(working or ""), os.path.realpath(root))
                self.assertIn("load -w " + path, calls)


class UninstallIgnoresRoot(InstallCase):
    """--uninstall does not look at --root."""

    def test_uninstall_ignores_root_and_removes_the_jobs_through_the_stub(self):
        """Path: --uninstall with a --root that is not a checkout. The run exits 0, which it would not if the root were checked, and removes the three plists, calling launchctl unload for each.

        Recorded against mutant INSTALL-UNINSTALL-HONOURS-ROOT, which runs the root check before the uninstall branch, and fails here.
        """
        os.makedirs(self.agents)
        paths = [self.plist_path(kind) for kind in KINDS]
        for path in paths:
            with open(path, "w") as fh:
                fh.write("placeholder\n")
        proc = self.run_install("--uninstall", "--root", os.path.join(self.tmp, "not-a-checkout"))
        self.assertEqual(proc.returncode, 0, verdict(proc))
        for path in paths:
            self.assertFalse(os.path.exists(path), "%s must be removed" % path)
        for path in paths:
            self.assertIn("unload " + path, self.calls_text(), "control: launchctl unload is recorded for %s" % path)


if __name__ == "__main__":
    unittest.main()

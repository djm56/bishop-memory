"""
Tests for scripts/install-triage-schedule.sh: the --root option, the --env-file
option, and the defaults they replace; and for the ENV_FILE passthrough in the
Makefile's triage-install target.

Each install test runs the script with HOME in a temporary directory, so the agents
directory and the log directory are temporary too. Every run that is not a dry
run puts a recording stub named launchctl first on PATH. The stub only appends
its arguments to a file the test names, so no test calls the real launchctl or
touches a live com.bishop-memory.* job.

Expected behaviour of the stub, stated before any run: a non-dry-run install with
a valid root records three "load -w" lines, one per kind; an uninstall records one
"unload" line per plist it removes; a refused run records nothing. The refusal
tests assert the last of these, and their control is the valid-root test, which
records the first.

No install test opens an env file: each names a temporary file, or a path the
run never opens. The Makefile tests run make with -n, which runs no recipe, and
with HOME and BISHOP_MEMORY_CLIENT_ENV pointed at a temporary directory.

The install classes are skipped off macOS, because the script is macOS-only. The
Makefile class runs wherever make does.
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
REPO_ROOT = os.path.dirname(HERE)
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


ENV_FILE_RE = re.compile(r"<key>TRIAGE_ENV_FILE</key>\s*<string>([^<]*)</string>")


def env_file_of(text):
    """The TRIAGE_ENV_FILE value in one rendered plist or dry-run block, or None when the key or its value is absent."""
    found = ENV_FILE_RE.search(text)
    return found.group(1) if found else None


def real(path):
    """The resolved path, so two spellings of one directory compare equal; None stays None."""
    return None if path is None else os.path.realpath(path)


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

    def copy_checkout(self, name="tree"):
        """A temporary copy of the installer and its plist template, laid out as a checkout. Returns the tree and the copied script's path. The tree has no .env, so a run from it finds no env file at its default."""
        tree = os.path.join(self.tmp, name)
        os.makedirs(os.path.join(tree, "scripts"))
        script = os.path.join(tree, "scripts", "install-triage-schedule.sh")
        shutil.copy(SCRIPT_SRC, script)
        shutil.copy(TEMPLATE_SRC, os.path.join(tree, "scripts", "com.bishop-memory.triage.plist"))
        return tree, script

    def make_env_file(self, *parts):
        """An existing, empty file at the path the parts name under the temporary directory. The install checks it is a readable file and never opens it."""
        path = os.path.join(self.tmp, *parts)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        open(path, "w").close()
        return path

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


class EnvFileDefault(InstallCase):
    """What TRIAGE_ENV_FILE holds when --env-file is absent: the .env of the checkout that holds the script, whatever --root says."""

    def test_without_env_file_every_job_names_the_env_file_of_the_checkout_that_holds_the_script(self):
        """Path: the script is run from a copy of itself in a temporary tree, as a dry run, with no --root and no --env-file. Every rendered plist carries TRIAGE_ENV_FILE with the path of .env in that tree, and no placeholder is left.

        The control is that each kind's block is found and that its program path names the tree.
        Recorded against mutant ENVFILE-DEFAULT-LOST, which renders an empty TRIAGE_ENV_FILE, and fails here.
        Also recorded against ENVFILE-PLIST-KEY-RENAMED, which renames the key, and ENVFILE-PLIST-UNRENDERED, which leaves the placeholder in place; both fail here.
        """
        tree, script = self.copy_checkout()
        proc = self.run_install("--dry-run", script=script)
        self.assertEqual(proc.returncode, 0, verdict(proc))
        for kind in KINDS:
            with self.subTest(kind=kind):
                block = plist_block(proc.stdout, kind)
                self.assertTrue(block, "control: no dry-run plist was found for %s: %s" % (kind, verdict(proc)))
                program, _ = plist_paths(block)
                self.assertEqual(real(program), real(tree), "control: the program path must name the tree: " + block)
                self.assertEqual(real(env_file_of(block)), real(os.path.join(tree, ".env")), block)
                self.assertNotIn("__ENV_FILE__", block)

    def test_with_root_and_no_env_file_the_env_file_is_still_the_checkout_env_file(self):
        """Path: the script is run from a copy of itself in a temporary tree, as a dry run, with --root naming a second checkout and no --env-file. Every rendered plist carries TRIAGE_ENV_FILE with the .env of the tree, not the .env of the second checkout.

        The control is that the program path names the second checkout, so --root took effect.
        Recorded against mutant ENVFILE-DEFAULT-FOLLOWS-ROOT, which takes the default from the --root directory, and fails here.
        """
        tree, script = self.copy_checkout()
        root = self.make_root()
        proc = self.run_install("--dry-run", "--root", root, script=script)
        self.assertEqual(proc.returncode, 0, verdict(proc))
        for kind in KINDS:
            with self.subTest(kind=kind):
                block = plist_block(proc.stdout, kind)
                self.assertTrue(block, "control: no dry-run plist was found for %s: %s" % (kind, verdict(proc)))
                program, _ = plist_paths(block)
                self.assertEqual(real(program), real(root), "control: --root must name the second checkout: " + block)
                self.assertEqual(real(env_file_of(block)), real(os.path.join(tree, ".env")), block)


class EnvFileGiven(InstallCase):
    """--env-file FILE: the file every job is given, in either form, and how a relative or absent FILE is handled."""

    def test_env_file_names_the_file_in_every_plist_and_warns_about_nothing(self):
        """Path: --dry-run --env-file FILE, where FILE is an existing empty file under the temporary directory, for each kind. Every rendered plist carries FILE, and stderr carries no warning about it.

        The control is each kind's block being found. The absence of the warning is what test_a_missing_env_file_is_named_on_stderr_and_the_install_still_writes_the_jobs sets against a missing file.
        Recorded against mutant ENVFILE-GIVEN-IGNORED, which renders the checkout's .env whatever --env-file says, and against mutant ENVFILE-ABSOLUTE-BROKEN, which prefixes an absolute FILE with the working directory; both fail here.
        Also recorded against ENVFILE-SEPARATE-DROPPED, which removes the separate form, ENVFILE-WARNING-INVERTED, which warns about a present file, ENVFILE-UNSAFE-OVER-REFUSED, which refuses every path, and ENVFILE-EMPTY-INVERTED, which refuses every non-empty value; each fails here.
        """
        _, script = self.copy_checkout()
        env_file = self.make_env_file("creds", "triage.env")
        proc = self.run_install("--dry-run", "--env-file", env_file, script=script)
        self.assertEqual(proc.returncode, 0, verdict(proc))
        self.assertNotIn("is not a readable file", proc.stderr, "an existing FILE must not be warned about: " + verdict(proc))
        for kind in KINDS:
            with self.subTest(kind=kind):
                block = plist_block(proc.stdout, kind)
                self.assertTrue(block, "control: no dry-run plist was found for %s: %s" % (kind, verdict(proc)))
                self.assertEqual(real(env_file_of(block)), real(env_file), block)
                self.assertNotIn("__ENV_FILE__", block)

    def test_env_file_in_the_equals_form_renders_the_same_output_as_the_separate_form(self):
        """Path: --env-file=FILE and --env-file FILE, each as a dry run. The two outputs are identical, and the plists name FILE.

        The control is that the equals form names FILE in the classify plist.
        Recorded against mutant ENVFILE-EQUALS-DROPPED, which removes the equals form so it is refused as an unknown flag, and against mutant ENVFILE-EQUALS-MISPARSED, which keeps the flag name as the path; both fail here.
        """
        _, script = self.copy_checkout()
        env_file = self.make_env_file("creds", "triage.env")
        separate = self.run_install("--dry-run", "--env-file", env_file, script=script)
        equals = self.run_install("--dry-run", "--env-file=" + env_file, script=script)
        self.assertEqual(separate.returncode, 0, verdict(separate))
        self.assertEqual(equals.returncode, 0, verdict(equals))
        self.assertEqual(equals.stdout, separate.stdout, "the two forms must render the same output")
        block = plist_block(equals.stdout, "classify")
        self.assertEqual(real(env_file_of(block)), real(env_file), "control: the equals form names FILE: " + block)

    def test_a_relative_env_file_is_written_into_the_plists_as_an_absolute_path(self):
        """Path: --env-file with a relative FILE, run from the directory that holds it, as a dry run. Every plist names the absolute path of FILE.

        The control is that each kind's block is found and that the named file is the one the run was given, so the value is checked against a real file.
        Recorded against mutant ENVFILE-RELATIVE-KEPT, which writes the argument as given, and fails here.
        """
        _, script = self.copy_checkout()
        env_file = self.make_env_file("creds", "triage.env")
        proc = self.run_install("--dry-run", "--env-file", os.path.join("creds", "triage.env"), cwd=self.tmp, script=script)
        self.assertEqual(proc.returncode, 0, verdict(proc))
        for kind in KINDS:
            with self.subTest(kind=kind):
                block = plist_block(proc.stdout, kind)
                self.assertTrue(block, "control: no dry-run plist was found for %s: %s" % (kind, verdict(proc)))
                value = env_file_of(block)
                self.assertTrue(value and os.path.isabs(value), "the env file must be absolute: %r" % value)
                self.assertEqual(real(value), real(env_file), block)

    def test_a_missing_env_file_is_named_on_stderr_and_the_install_still_writes_the_jobs(self):
        """Path: a real install (not a dry run) under the stub launchctl, with --env-file naming a path that does not exist. The run exits 0. Stderr names the path and says it is not a readable file. The three plists are written, each carrying the path, and launchctl is called for each.

        The control is the same run with an existing FILE, which exits 0 with no such warning on stderr.
        Recorded against mutant ENVFILE-WARNING-DROPPED, which drops the warning, and against mutant ENVFILE-WARNING-INVERTED, which warns when the file is present; both fail here.
        """
        _, script = self.copy_checkout()
        present = self.make_env_file("present.env")
        control = self.run_install("--dry-run", "--env-file", present, script=script)
        self.assertEqual(control.returncode, 0, verdict(control))
        self.assertNotIn("is not a readable file", control.stderr, "control: an existing FILE is not warned about: " + verdict(control))
        missing = os.path.join(self.tmp, "absent", "triage.env")
        proc = self.run_install("--env-file", missing, script=script)
        self.assertEqual(proc.returncode, 0, verdict(proc))
        self.assertIn(missing, proc.stderr, verdict(proc))
        self.assertIn("is not a readable file", proc.stderr, verdict(proc))
        for kind in KINDS:
            with self.subTest(kind=kind):
                path = self.plist_path(kind)
                self.assertTrue(os.path.exists(path), "%s was not written" % path)
                with open(path) as fh:
                    self.assertEqual(real(env_file_of(fh.read())), real(missing))
                self.assertIn("load -w " + path, self.calls_text())


class EnvFileRefused(InstallCase):
    """An env file path the plist render cannot carry, and an --env-file with no value, are refused before anything is written."""

    def test_an_env_file_path_with_a_character_the_render_cannot_carry_is_refused_before_any_write(self):
        """Path: --env-file names a file whose path contains a pipe, an ampersand, a backslash, a less-than, a greater-than, or a newline. Each is refused with exit 64, naming the path, and nothing is written. Then the same check is run on a relative name made absolute in a directory whose name holds a pipe, and on the default .env of a checkout whose path holds one.

        The control is test_env_file_names_the_file_in_every_plist_and_warns_about_nothing, which admits a plain path through the same checks.
        Recorded against mutant ENVFILE-UNSAFE-UNCHECKED, which removes the refusal, and fails here. Recorded against mutant ENVFILE-UNSAFE-OVER-REFUSED, which refuses every path; the control kills that one, not this test.
        """
        _, script = self.copy_checkout()
        characters = {"pipe": "|", "ampersand": "&", "backslash": "\\", "less-than": "<", "greater-than": ">", "newline": "\n"}
        for label, char in sorted(characters.items()):
            with self.subTest(character=label):
                path = os.path.join(self.tmp, "a%sb.env" % char)
                proc = self.run_install("--env-file", path, script=script)
                self.assert_refused_before_any_write(proc, "env file path %s holds" % path)
        pipe_dir = os.path.join(self.tmp, "d|x")
        os.makedirs(pipe_dir)
        proc = self.run_install("--env-file", "creds.env", cwd=pipe_dir, script=script)
        self.assert_refused_before_any_write(proc, "holds | & \\ < > or a newline")
        tree, tree_script = self.copy_checkout(name="t|x")
        proc = self.run_install("--dry-run", script=tree_script)
        self.assert_refused_before_any_write(proc, "holds | & \\ < > or a newline")
        self.assertIn(os.path.basename(tree), proc.stderr, "the refusal must name the default path: " + verdict(proc))

    def test_an_env_file_without_a_value_is_refused_before_any_write(self):
        """Path: --env-file with an empty value, --env-file= with nothing after it, and --env-file as the last argument. Each is refused with exit 64, naming the flag, and nothing is written.

        The control is test_env_file_names_the_file_in_every_plist_and_warns_about_nothing, which runs the same flag with a value.
        Recorded against mutant ENVFILE-EMPTY-UNCHECKED, which drops the empty-value test, and fails here for the empty value. Recorded against mutant ENVFILE-EMPTY-INVERTED, which refuses every non-empty value; the control kills that one, not this test.
        """
        _, script = self.copy_checkout()
        cases = (
            ("empty value", ["--env-file", ""]),
            ("equals with nothing after", ["--env-file="]),
            ("no argument after the flag", ["--env-file"]),
        )
        for label, args in cases:
            with self.subTest(case=label):
                proc = self.run_install(*args, script=script)
                self.assert_refused_before_any_write(proc, "--env-file needs a file")


class MakeEnvFile(unittest.TestCase):
    """make triage-install passes ENV_FILE on as --env-file, and passes no flag when it is unset."""

    def make_dry(self, *assignments):
        """make -n triage-install with the given assignments on the command line. HOME and BISHOP_MEMORY_CLIENT_ENV point at a temporary directory, so the Makefile's read of the client env file finds nothing. ROOT, ENV_FILE, BISHOP_MEMORY_URL and BISHOP_MEMORY_CLIENT_ENV are removed from the environment, so only the command line can set them."""
        tmp = tempfile.mkdtemp(prefix="install-triage-make-")
        self.addCleanup(shutil.rmtree, tmp, True)
        env = {k: v for k, v in os.environ.items()
               if k not in ("ROOT", "ENV_FILE", "BISHOP_MEMORY_URL", "BISHOP_MEMORY_CLIENT_ENV")}
        env["HOME"] = tmp
        env["BISHOP_MEMORY_CLIENT_ENV"] = os.path.join(tmp, "absent.env")
        return subprocess.run(["make", "-n", "-C", REPO_ROOT, "triage-install", *assignments],
                              env=env, capture_output=True, text=True, stdin=subprocess.DEVNULL)

    def test_make_triage_install_passes_env_file_as_the_flag(self):
        """Path: make -n triage-install ENV_FILE=/triage-test/creds.env. The printed installer command carries --env-file "/triage-test/creds.env". make -n runs no recipe, so no file is read or written.

        The control is that the installer command is printed at all, which test_make_triage_install_without_env_file_passes_no_flag also relies on.
        Recorded against mutant MAKE-ENVFILE-DROPPED, which misspells the variable in the passthrough so the flag is never passed, and fails here.
        """
        proc = self.make_dry("ENV_FILE=/triage-test/creds.env")
        self.assertEqual(proc.returncode, 0, verdict(proc))
        self.assertIn("scripts/install-triage-schedule.sh", proc.stdout, "control: the installer command must be printed: " + verdict(proc))
        self.assertIn('--env-file "/triage-test/creds.env"', proc.stdout, verdict(proc))

    def test_make_triage_install_without_env_file_passes_no_flag(self):
        """Path: make -n triage-install with no ENV_FILE on the command line, and none in the environment. The printed installer command has no --env-file, so the installer takes its default.

        The control is that the installer command is printed, so the absence of the flag is read from a real command line.
        Recorded against mutant MAKE-ENVFILE-UNCONDITIONAL, which passes --env-file "" whatever the variable holds, and fails here.
        """
        proc = self.make_dry()
        self.assertEqual(proc.returncode, 0, verdict(proc))
        self.assertIn("scripts/install-triage-schedule.sh", proc.stdout, "control: the installer command must be printed: " + verdict(proc))
        self.assertNotIn("--env-file", proc.stdout, verdict(proc))


class UninstallIgnoresEnvFile(InstallCase):
    """--uninstall does not look at --env-file, as it does not look at --root."""

    def test_uninstall_ignores_env_file_even_one_the_install_would_refuse(self):
        """Path: --uninstall with --env-file naming a path that holds a pipe, which an install would refuse, and the three plists present. The run exits 0, removes the plists, calls launchctl unload for each, and writes no env-file message on stderr.

        The control is the plist removal and the unload calls, which happen only when the uninstall branch runs.
        Recorded against mutant UNINSTALL-HONOURS-ENVFILE, which sends an uninstall with --env-file down the install path, where the refusal fires, and against mutant UNINSTALL-ENVFILE-SKIPS-REMOVAL, which keeps the plists; both fail here.
        """
        os.makedirs(self.agents)
        paths = [self.plist_path(kind) for kind in KINDS]
        for path in paths:
            with open(path, "w") as fh:
                fh.write("placeholder\n")
        proc = self.run_install("--uninstall", "--env-file", os.path.join(self.tmp, "a|b.env"))
        self.assertEqual(proc.returncode, 0, verdict(proc))
        self.assertNotIn("env file", proc.stderr, verdict(proc))
        for path in paths:
            self.assertFalse(os.path.exists(path), "%s must be removed" % path)
            self.assertIn("unload " + path, self.calls_text(), "control: launchctl unload is recorded for %s" % path)


class EnvFileRendering(InstallCase):
    """The plists a real install writes with --env-file still pass plutil -lint."""

    def test_jobs_installed_with_an_env_file_pass_plutil_lint(self):
        """Path: a real install (not a dry run) under the stub launchctl, with --env-file naming an existing file, from a copy of the script in a temporary tree. The run exits 0. Every installed plist passes plutil -lint, and each carries the file's path in TRIAGE_ENV_FILE.

        The control is the stub's launchctl record of the three loads, which shows the install reached the write and the load for every job.
        Recorded against mutant ENVFILE-PLIST-UNCLOSED, which leaves the value element unclosed so the installer's own plutil -lint fails, and against mutant ENVFILE-PLIST-KEY-UNCLOSED, which does the same to the key; both fail here.
        """
        _, script = self.copy_checkout()
        env_file = self.make_env_file("creds", "triage.env")
        proc = self.run_install("--env-file", env_file, script=script)
        self.assertEqual(proc.returncode, 0, verdict(proc))
        self.assertTrue(self.calls_text(), "control: the stub must record the launchctl calls")
        for kind in KINDS:
            with self.subTest(kind=kind):
                path = self.plist_path(kind)
                lint = subprocess.run(["plutil", "-lint", path], capture_output=True, text=True)
                self.assertEqual(lint.returncode, 0, "plutil -lint rejected %s: %s" % (path, verdict(lint)))
                with open(path) as fh:
                    self.assertEqual(real(env_file_of(fh.read())), real(env_file))


if __name__ == "__main__":
    unittest.main()

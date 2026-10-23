"""
Tests for scripts/triage-deploy.sh and the triage-deploy make target.

Each test builds its own temporary tree: a bare repository as origin, a
development clone that holds a copy of the script and the real Makefile, and a
deploy path under the temporary directory. HOME is the temporary directory too,
so the default deploy path is never the real one. Git runs with no global or
system configuration, and the GIT_* and make variables of the calling process
are dropped. No test touches the real origin, the real checkout's worktrees, or
~/bishop-memory-deploy.

The go build uses the real GOCACHE, GOMODCACHE and GOPATH, read once with go env
before HOME is replaced, so a build is quick. GOPROXY is off and GOTOOLCHAIN is
local, so nothing is downloaded.

Each test name says the path it exercises. Each docstring says which mutants in
testdata/mutants.json it is recorded against, and names the control where a
refusal or an absence is asserted.
"""

import os
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)
SCRIPT_SRC = os.path.join(HERE, "triage-deploy.sh")
MAKEFILE_SRC = os.path.join(REPO, "Makefile")
GO_MOD = "module example.test/deploy\n\ngo 1.25.5\n"
DROP_PREFIXES = ("GIT_",)
DROP_NAMES = ("MAKEFLAGS", "MAKELEVEL", "MFLAGS")

_GO_PATHS = None


def go_paths():
    """The real GOCACHE, GOMODCACHE and GOPATH, read once. They are read before HOME is replaced, so the build cache is shared."""
    global _GO_PATHS
    if _GO_PATHS is None:
        out = subprocess.run(["go", "env", "GOCACHE", "GOMODCACHE", "GOPATH"],
                             capture_output=True, text=True, check=True).stdout.split("\n")
        _GO_PATHS = {"GOCACHE": out[0], "GOMODCACHE": out[1], "GOPATH": out[2]}
    return _GO_PATHS


def make_env(home, gitconfig):
    """The environment every git, make and go call in these tests runs with: temporary HOME, no git configuration, no network."""
    env = {k: v for k, v in os.environ.items() if not k.startswith(DROP_PREFIXES) and k not in DROP_NAMES}
    env.update({
        "HOME": home,
        "GIT_CONFIG_GLOBAL": gitconfig,
        "GIT_CONFIG_NOSYSTEM": "1",
        "GIT_AUTHOR_NAME": "fixture",
        "GIT_AUTHOR_EMAIL": "fixture@example.test",
        "GIT_COMMITTER_NAME": "fixture",
        "GIT_COMMITTER_EMAIL": "fixture@example.test",
        "GOTOOLCHAIN": "local",
        "GOPROXY": "off",
    })
    env.update(go_paths())
    return env


def main_go(version):
    """The source of the fixture's mcpd: one line that prints the version it was built with."""
    return 'package main\n\nimport "fmt"\n\nfunc main() {\n\tfmt.Println("mcpd fixture %s")\n}\n' % version


def write_text(path, text):
    """Write text to path, creating its parent directories."""
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf8") as fh:
        fh.write(text)


def read_text(path):
    """The text of the file at path."""
    with open(path, encoding="utf8") as fh:
        return fh.read()


def verdict(proc):
    """The return code and both streams of a finished run, for an assertion message."""
    return "rc=%s stdout=%r stderr=%r" % (proc.returncode, proc.stdout, proc.stderr)


class DeployCase(unittest.TestCase):
    """The fixture: a bare origin, a development clone that holds the script, and the helpers the tests share."""

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="triage-deploy-test-")
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.home = os.path.join(self.tmp, "home")
        os.makedirs(self.home)
        gitconfig = os.path.join(self.tmp, "gitconfig")
        open(gitconfig, "w").close()
        self.env = make_env(self.home, gitconfig)
        self.origin = os.path.join(self.tmp, "origin.git")
        self.seed = os.path.join(self.tmp, "seed")
        self.dev = os.path.join(self.tmp, "dev")
        self.deploy = os.path.join(self.tmp, "deploy")

        self.git(self.tmp, "init", "-q", "--bare", "-b", "main", self.origin)
        self.git(self.tmp, "init", "-q", "-b", "main", self.seed)
        write_text(os.path.join(self.seed, "Makefile"), read_text(MAKEFILE_SRC))
        write_text(os.path.join(self.seed, ".gitignore"), "/bin/\n")
        write_text(os.path.join(self.seed, "go.mod"), GO_MOD)
        write_text(os.path.join(self.seed, "cmd", "mcpd", "main.go"), main_go("v1"))
        self.git(self.seed, "add", "Makefile", ".gitignore", "go.mod", "cmd/mcpd/main.go")
        self.git(self.seed, "commit", "-q", "-m", "fixture v1")
        self.git(self.seed, "remote", "add", "origin", self.origin)
        self.git(self.seed, "push", "-q", "origin", "main")

        self.git(self.tmp, "clone", "-q", self.origin, self.dev)
        os.makedirs(os.path.join(self.dev, "scripts"))
        shutil.copy(SCRIPT_SRC, os.path.join(self.dev, "scripts", "triage-deploy.sh"))
        os.chmod(os.path.join(self.dev, "scripts", "triage-deploy.sh"), 0o755)

    def git(self, cwd, *args):
        """Run git in cwd and return its stdout. A failing git call fails the test with git's message."""
        proc = subprocess.run(["git", *args], cwd=cwd, env=self.env, capture_output=True, text=True)
        if proc.returncode != 0:
            raise AssertionError("git %s in %s failed: %s" % (" ".join(args), cwd, proc.stderr.strip()))
        return proc.stdout.strip()

    def sha(self, path):
        """The full commit at HEAD of the worktree or repository at path."""
        return self.git(path, "rev-parse", "--verify", "HEAD^{commit}")

    def origin_main(self):
        """The commit origin's main branch points at."""
        return self.git(self.origin, "rev-parse", "--verify", "main^{commit}")

    def advance_origin(self, version=None, body=None):
        """Commit a new main.go to origin/main through the seed repository, and push it. Returns the new origin/main commit."""
        write_text(os.path.join(self.seed, "cmd", "mcpd", "main.go"), body if body is not None else main_go(version))
        self.git(self.seed, "add", "cmd/mcpd/main.go")
        self.git(self.seed, "commit", "-q", "-m", "fixture %s" % (version or "broken"))
        self.git(self.seed, "push", "-q", "origin", "main")
        return self.origin_main()

    def is_detached(self, path):
        """True when HEAD of the worktree at path is detached rather than on a branch."""
        proc = subprocess.run(["git", "symbolic-ref", "-q", "HEAD"], cwd=path, env=self.env, capture_output=True, text=True)
        return proc.returncode != 0

    def run_script(self, *args, cwd=None, path=None):
        """Run the copy of triage-deploy.sh in the development clone, with the given arguments. path, when given, replaces PATH."""
        env = dict(self.env)
        if path is not None:
            env["PATH"] = path
        return subprocess.run(["/bin/bash", os.path.join(self.dev, "scripts", "triage-deploy.sh"), *args],
                              cwd=cwd or self.dev, env=env, capture_output=True, text=True)

    def run_make(self, *args):
        """Run make in the development clone with the given arguments, after the target name."""
        return subprocess.run(["make", "-C", self.dev, *args], env=dict(self.env), capture_output=True, text=True)

    def says(self, deploy):
        """What the bin/mcpd in the deploy worktree prints when it runs. The binary must run cleanly."""
        proc = subprocess.run([os.path.join(deploy, "bin", "mcpd")], env=self.env, capture_output=True, text=True)
        self.assertEqual(proc.returncode, 0, verdict(proc))
        return proc.stdout.strip()

    def mcpd_inode(self, deploy):
        """The inode of bin/mcpd in the deploy worktree. A rebuilt binary is a new file, so its inode changes."""
        return os.stat(os.path.join(deploy, "bin", "mcpd")).st_ino

    def deploy_once(self):
        """Deploy to self.deploy and require success. Returns the run."""
        proc = self.run_script("--dir", self.deploy)
        self.assertEqual(proc.returncode, 0, verdict(proc))
        return proc


class DeployCreatesAndUpdates(DeployCase):
    """Creating the worktree, and moving it forward to origin/main."""

    def test_no_deploy_path_under_make_creates_the_worktree_at_origin_main_under_home_and_builds_mcpd(self):
        """Path: make triage-deploy with no DEPLOY_DIR, so the default is HOME/bishop-memory-deploy, with HOME in the temporary directory. The clone is one commit ahead of origin/main, so the worktree must land on origin/main, not on the clone's HEAD.

        Recorded against mutant DEPLOY-CREATE-AT-HEAD, which creates the worktree at the clone's HEAD, and fails here.
        """
        self.git(self.dev, "commit", "-q", "--allow-empty", "-m", "local commit not on origin")
        proc = self.run_make("triage-deploy")
        self.assertEqual(proc.returncode, 0, verdict(proc))
        home_deploy = os.path.join(self.home, "bishop-memory-deploy")
        self.assertNotEqual(self.sha(self.dev), self.origin_main(), "control: the clone must be ahead of origin/main")
        self.assertEqual(self.sha(home_deploy), self.origin_main(), verdict(proc))
        self.assertTrue(self.is_detached(home_deploy), "the deploy worktree must be detached")
        self.assertEqual(self.says(home_deploy), "mcpd fixture v1")

    def test_a_worktree_behind_origin_main_is_fast_forwarded_and_rebuilt(self):
        """Path: an existing deploy worktree at the old origin/main, and origin/main advanced by one commit. The worktree moves to the new commit and bin/mcpd is built from it.

        Recorded against mutant DEPLOY-NO-FAST-FORWARD, which skips the move, and DEPLOY-NO-REBUILD, which skips the build, both of which fail here. The control is the binary before the advance: it prints v1.
        """
        self.deploy_once()
        self.assertEqual(self.says(self.deploy), "mcpd fixture v1", "control: the first deploy builds v1")
        inode = self.mcpd_inode(self.deploy)
        new = self.advance_origin("v2")
        self.deploy_once()
        self.assertEqual(self.sha(self.deploy), new, "HEAD must be the new origin/main")
        self.assertEqual(self.says(self.deploy), "mcpd fixture v2", "bin/mcpd must be built from the new commit")
        self.assertNotEqual(self.mcpd_inode(self.deploy), inode, "bin/mcpd must be a new file, not an overwrite")

    def test_a_worktree_already_at_origin_main_keeps_its_head_and_is_rebuilt(self):
        """Path: the deploy worktree is at origin/main already. The run exits 0, HEAD is unchanged, the script says it is already there, and bin/mcpd is rebuilt (a new inode).

        Recorded against mutant DEPLOY-REFUSES-CURRENT, which refuses a worktree already at origin/main, and fails here.
        """
        self.deploy_once()
        self.assertEqual(self.sha(self.deploy), self.origin_main(), "control: the first deploy is at origin/main")
        head = self.sha(self.deploy)
        inode = self.mcpd_inode(self.deploy)
        proc = self.run_script("--dir", self.deploy)
        self.assertEqual(proc.returncode, 0, verdict(proc))
        self.assertEqual(self.sha(self.deploy), head, "HEAD must not move")
        self.assertIn("already at origin/main", proc.stdout, verdict(proc))
        self.assertNotEqual(self.mcpd_inode(self.deploy), inode, "bin/mcpd is rebuilt on every run")
        self.assertEqual(self.says(self.deploy), "mcpd fixture v1")


class DeployRefuses(DeployCase):
    """Refusals: each leaves HEAD where it was, names the path, and exits 1."""

    def test_a_modified_tracked_file_is_refused_naming_the_path_and_head_is_kept(self):
        """Path: a tracked file in the deploy worktree is modified, and origin/main has advanced. The run is refused, names cmd/mcpd/main.go, and HEAD does not move.

        The control reverts the edit; the same run then succeeds and moves HEAD to the new commit.
        Recorded against mutant DEPLOY-DIRTY-IGNORED, which skips the status check, and fails here.
        """
        self.deploy_once()
        write_text(os.path.join(self.deploy, "cmd", "mcpd", "main.go"), main_go("local-edit"))
        new = self.advance_origin("v2")
        head = self.sha(self.deploy)
        proc = self.run_script("--dir", self.deploy)
        self.assertEqual(proc.returncode, 1, verdict(proc))
        self.assertIn("cmd/mcpd/main.go", proc.stderr, verdict(proc))
        self.assertEqual(self.sha(self.deploy), head, "HEAD must not move")
        self.git(self.deploy, "checkout", "--", "cmd/mcpd/main.go")
        self.deploy_once()
        self.assertEqual(self.sha(self.deploy), new, "control: with the edit reverted the same run deploys")

    def test_an_untracked_file_is_refused_like_a_modified_one(self):
        """Path: an untracked file in the deploy worktree. The run is refused, names scratch.txt, and HEAD does not move. bin/mcpd is present and ignored, so it must not be what blocks the run.

        The control removes the file; the same run then succeeds with bin/mcpd still present.
        Recorded against mutant DEPLOY-UNTRACKED-IGNORED, which lists tracked changes only, and fails here.
        """
        self.deploy_once()
        self.assertTrue(os.path.exists(os.path.join(self.deploy, "bin", "mcpd")), "control: the build output is present")
        write_text(os.path.join(self.deploy, "scratch.txt"), "untracked\n")
        new = self.advance_origin("v2")
        head = self.sha(self.deploy)
        proc = self.run_script("--dir", self.deploy)
        self.assertEqual(proc.returncode, 1, verdict(proc))
        self.assertIn("scratch.txt", proc.stderr, verdict(proc))
        self.assertEqual(self.sha(self.deploy), head, "HEAD must not move")
        os.remove(os.path.join(self.deploy, "scratch.txt"))
        self.deploy_once()
        self.assertEqual(self.sha(self.deploy), new, "control: with the file removed the same run deploys")

    def test_a_commit_origin_main_lacks_is_refused_when_diverged(self):
        """Path: the deploy worktree has a commit on top of v1 and origin/main has moved to v2, so the two have diverged. The run is refused, names origin/main, and HEAD does not move.

        The control resets the worktree to v1; the same run then fast-forwards it to v2.
        Recorded against mutant DEPLOY-ANCESTRY-SKIPPED, which moves HEAD to origin/main without the ancestry check, and fails here.
        """
        self.deploy_once()
        base = self.sha(self.deploy)
        write_text(os.path.join(self.deploy, "cmd", "mcpd", "main.go"), main_go("local-commit"))
        self.git(self.deploy, "commit", "-q", "-am", "local commit")
        local = self.sha(self.deploy)
        new = self.advance_origin("v2")
        proc = self.run_script("--dir", self.deploy)
        self.assertEqual(proc.returncode, 1, verdict(proc))
        self.assertIn("origin/main", proc.stderr, verdict(proc))
        self.assertEqual(self.sha(self.deploy), local, "HEAD must not move")
        self.git(self.deploy, "reset", "-q", "--hard", base)
        self.deploy_once()
        self.assertEqual(self.sha(self.deploy), new, "control: back at v1 the same run fast-forwards to v2")

    def test_a_worktree_ahead_of_origin_main_is_refused(self):
        """Path: the deploy worktree has a commit on top of origin/main, which has not moved. The run is refused and HEAD does not move. This is the ahead edge; the diverged edge is in the test above.

        The control resets the worktree to origin/main; the same run then exits 0 and HEAD is origin/main.
        Recorded against mutant DEPLOY-AHEAD-ACCEPTED, which accepts a worktree ahead of origin/main, and fails here.
        """
        self.deploy_once()
        base = self.sha(self.deploy)
        write_text(os.path.join(self.deploy, "cmd", "mcpd", "main.go"), main_go("local-commit"))
        self.git(self.deploy, "commit", "-q", "-am", "local commit")
        local = self.sha(self.deploy)
        self.assertEqual(self.origin_main(), base, "control: origin/main has not moved")
        proc = self.run_script("--dir", self.deploy)
        self.assertEqual(proc.returncode, 1, verdict(proc))
        self.assertIn("does not have", proc.stderr, verdict(proc))
        self.assertEqual(self.sha(self.deploy), local, "HEAD must not move")
        self.git(self.deploy, "reset", "-q", "--hard", base)
        self.deploy_once()
        self.assertEqual(self.sha(self.deploy), base, "control: at origin/main the same run exits 0")

    def test_a_plain_directory_at_the_deploy_path_is_refused_and_keeps_its_files(self):
        """Path: the deploy path is a plain directory holding a file. The run is refused, names the path as not a git worktree, and leaves the file and the directory in place.

        The control removes the directory; the same run then creates the worktree.
        Recorded against mutant DEPLOY-PLAIN-DIR-ADOPTED, which treats any existing directory as a worktree, and fails here.
        """
        os.makedirs(self.deploy)
        write_text(os.path.join(self.deploy, "keep.txt"), "keep\n")
        proc = self.run_script("--dir", self.deploy)
        self.assertEqual(proc.returncode, 1, verdict(proc))
        self.assertIn("is not a git worktree", proc.stderr, verdict(proc))
        self.assertEqual(read_text(os.path.join(self.deploy, "keep.txt")), "keep\n", "the file must survive the refusal")
        self.assertFalse(os.path.exists(os.path.join(self.deploy, ".git")), "the refusal must not create a worktree")
        shutil.rmtree(self.deploy)
        self.deploy_once()
        self.assertEqual(self.sha(self.deploy), self.origin_main(), "control: with the directory gone the same run creates the worktree")

    def test_a_worktree_of_another_repository_at_the_deploy_path_is_refused(self):
        """Path: the deploy path is a worktree of a different repository. The run is refused, names another repository, and that worktree's HEAD and file survive.

        The control removes that worktree; the same run then creates the deploy worktree.
        Recorded against mutant DEPLOY-FOREIGN-WORKTREE-ADOPTED, which skips the common-directory check, and fails here.
        """
        other = os.path.join(self.tmp, "other")
        self.git(self.tmp, "init", "-q", "-b", "main", other)
        write_text(os.path.join(other, "keep.txt"), "other\n")
        self.git(other, "add", "keep.txt")
        self.git(other, "commit", "-q", "-m", "other repository")
        self.git(other, "worktree", "add", "-q", "--detach", self.deploy, "HEAD")
        head = self.sha(self.deploy)
        proc = self.run_script("--dir", self.deploy)
        self.assertEqual(proc.returncode, 1, verdict(proc))
        self.assertIn("another repository", proc.stderr, verdict(proc))
        self.assertEqual(self.sha(self.deploy), head, "the other worktree's HEAD must not move")
        self.assertEqual(read_text(os.path.join(self.deploy, "keep.txt")), "other\n")
        self.git(other, "worktree", "remove", "--force", self.deploy)
        self.deploy_once()
        self.assertEqual(self.sha(self.deploy), self.origin_main(), "control: with the other worktree removed the same run deploys")

    def test_the_main_checkout_as_deploy_path_is_refused_and_not_moved(self):
        """Path: --dir names the development clone itself, its main checkout. The run is refused, names the main checkout, and the clone's HEAD does not move.

        The control runs against a fresh deploy path; that run succeeds.
        Recorded against mutant DEPLOY-MAIN-CHECKOUT-ADOPTED, which skips the git-dir check, and fails here.
        """
        head = self.sha(self.dev)
        proc = self.run_script("--dir", self.dev)
        self.assertEqual(proc.returncode, 1, verdict(proc))
        self.assertIn("main checkout", proc.stderr, verdict(proc))
        self.assertEqual(self.sha(self.dev), head, "the development checkout's HEAD must not move")
        self.deploy_once()
        self.assertEqual(self.sha(self.deploy), self.origin_main(), "control: a fresh deploy path succeeds")

    def test_a_worktree_on_a_branch_is_refused(self):
        """Path: the deploy worktree has been checked out onto a branch. The run is refused, names the branch, and the branch stays.

        The control detaches HEAD again; the same run then exits 0.
        Recorded against mutant DEPLOY-BRANCH-ADOPTED, which skips the symbolic-ref check, and fails here.
        """
        self.deploy_once()
        self.git(self.deploy, "checkout", "-q", "-b", "feature")
        proc = self.run_script("--dir", self.deploy)
        self.assertEqual(proc.returncode, 1, verdict(proc))
        self.assertIn("branch feature", proc.stderr, verdict(proc))
        self.assertEqual(self.git(self.deploy, "symbolic-ref", "-q", "HEAD"), "refs/heads/feature", "the branch must stay")
        self.git(self.deploy, "checkout", "-q", "--detach")
        proc = self.run_script("--dir", self.deploy)
        self.assertEqual(proc.returncode, 0, "control: detached again, the same run exits 0: " + verdict(proc))


class DeployBuildFetchAndUsage(DeployCase):
    """The build, the fetch, the deploy path override, and the usage and tool checks."""

    def test_make_triage_deploy_honours_the_deploy_dir_override(self):
        """Path: make triage-deploy with DEPLOY_DIR set on the command line. The worktree is created at that path and not at the default under HOME.

        The control is the presence of the worktree at the override path; the absence under HOME is the check that the default was not used.
        Recorded against mutant DEPLOY-DIR-OVERRIDE-IGNORED, which hard-codes the default, and fails here.
        """
        proc = self.run_make("triage-deploy", "DEPLOY_DIR=" + self.deploy)
        self.assertEqual(proc.returncode, 0, verdict(proc))
        self.assertEqual(self.sha(self.deploy), self.origin_main(), "control: the worktree is at the override path")
        self.assertFalse(os.path.exists(os.path.join(self.home, "bishop-memory-deploy")),
                         "the default path under HOME must not be used when DEPLOY_DIR is set")

    def test_a_build_failure_exits_3_and_removes_the_old_binary(self):
        """Path: origin/main advances to a commit whose main.go does not compile. The run exits 3 with a message that says the build failed, and bin/mcpd is gone, so no earlier binary runs.

        The control advances origin/main to a commit that builds; the same run then exits 0 and bin/mcpd prints v3.
        Recorded against mutant DEPLOY-BUILD-FAILURE-IGNORED, which turns the build failure into success, and fails here.
        """
        self.deploy_once()
        self.assertEqual(self.says(self.deploy), "mcpd fixture v1", "control: an old binary exists before the broken build")
        self.advance_origin(body="package main\n\nfunc main() {\n")
        proc = self.run_script("--dir", self.deploy)
        self.assertEqual(proc.returncode, 3, verdict(proc))
        self.assertIn("build failed", proc.stderr, verdict(proc))
        self.assertFalse(os.path.exists(os.path.join(self.deploy, "bin", "mcpd")), "the old binary must be removed")
        new = self.advance_origin("v3")
        self.deploy_once()
        self.assertEqual(self.sha(self.deploy), new, "control: with a buildable commit the same run deploys")
        self.assertEqual(self.says(self.deploy), "mcpd fixture v3")

    def test_a_fetch_failure_exits_2_and_leaves_the_worktree_unchanged(self):
        """Path: origin is unreachable, so the fetch fails. The run exits 2, names the fetch, and the worktree's HEAD does not move.

        The control points origin back at the real remote and advances it; the same run then deploys.
        Recorded against mutant DEPLOY-FETCH-FAILURE-IGNORED, which carries on after a failed fetch, and fails here.
        """
        self.deploy_once()
        head = self.sha(self.deploy)
        self.git(self.dev, "remote", "set-url", "origin", os.path.join(self.tmp, "missing.git"))
        proc = self.run_script("--dir", self.deploy)
        self.assertEqual(proc.returncode, 2, verdict(proc))
        self.assertIn("fetch", proc.stderr, verdict(proc))
        self.assertEqual(self.sha(self.deploy), head, "HEAD must not move")
        self.git(self.dev, "remote", "set-url", "origin", self.origin)
        new = self.advance_origin("v2")
        self.deploy_once()
        self.assertEqual(self.sha(self.deploy), new, "control: with origin reachable the same run deploys")

    def test_a_relative_deploy_path_is_a_usage_error_and_creates_nothing(self):
        """Path: --dir with a relative path. The run exits 64, names the absolute-path rule, and creates nothing in the working directory.

        The control gives an absolute path; the same run then creates the worktree there.
        Recorded against mutant DEPLOY-RELATIVE-ACCEPTED, which accepts a relative path, and fails here.
        """
        proc = self.run_script("--dir", "relative-deploy")
        self.assertEqual(proc.returncode, 64, verdict(proc))
        self.assertIn("absolute", proc.stderr, verdict(proc))
        self.assertFalse(os.path.exists(os.path.join(self.dev, "relative-deploy")), "nothing may be created under the working directory")
        self.deploy_once()
        self.assertEqual(self.sha(self.deploy), self.origin_main(), "control: an absolute path is accepted")

    def test_a_missing_make_or_git_on_path_exits_2_before_anything_changes(self):
        """Path: PATH holds only dirname and one of git or make. The run exits 2 and names the missing tool, before any fetch or any worktree is created.

        Each case is run with a PATH that has the tool it needs; the control is the full PATH, under which the same run deploys.
        Mutant DEPLOY-TOOL-CHECK-REMOVED, which drops the command -v check, is recorded against this test, because without the check the run fails later with a message about the build, not the tool.
        """
        for missing, keep in (("make", "git"), ("git", "make")):
            with self.subTest(missing=missing):
                stub = os.path.join(self.tmp, "path-without-" + missing)
                os.makedirs(stub)
                for tool in ("dirname", keep):
                    os.symlink(shutil.which(tool), os.path.join(stub, tool))
                proc = self.run_script("--dir", self.deploy, path=stub)
                self.assertEqual(proc.returncode, 2, verdict(proc))
                self.assertIn("%s is not on PATH" % missing, proc.stderr, verdict(proc))
                self.assertFalse(os.path.exists(self.deploy), "nothing may be created when a tool is missing")
        self.deploy_once()
        self.assertEqual(self.sha(self.deploy), self.origin_main(), "control: with the full PATH the same run deploys")


if __name__ == "__main__":
    unittest.main()

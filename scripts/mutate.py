#!/usr/bin/env python3
"""
Mutation runner for the DIR-017 spec, testdata/mutants.json.

For each active mutant, or each one named with --ids:

  1. refuse to plant unless the file is clean against HEAD: `git status
     --porcelain --ignored -- <file>` must print nothing, so a staged edit, an
     unstaged edit, an untracked file and an ignored file all refuse;
  2. locate `before` exactly as scripts/check_mutants.py does, and stop if it is not there;
  3. save a copy of the file, plant the edit alone, and run the mutant's killers,
     each run bounded by --timeout;
  4. restore the original bytes in a finally block, then verify the restore:
     the bytes equal the original, cmp matches the saved copy, and the file is
     clean against HEAD.

A planted file is recorded in mutate-unrestored.json in the git directory before it is planted.
The entry is removed only when its restore is verified, so a refusal on a dirty
file can name the directory that holds that file's saved original. The marker is
replaced atomically (a temporary file in the git directory, fsynced, then renamed),
so a kill cannot leave it half-written. A marker that does not parse refuses the
run, and the message names the file and the recovery step.

One run at a time: a run holds an exclusive lock on mutate.lock in the git
directory, and a second run refuses. Two runs on one working tree would plant over
each other's files and read each other's mutants, so the lock is a correctness
rule and not only a guard for the marker. The lock is released when the process
ends, however it ends.

POSIX only: the runner needs process groups, flock and O_NOFOLLOW, and main()
refuses on a platform without them.

The signals in HANDLED_SIGNALS are handled while the run lasts. The first one
raises Interrupted where the run is, and the finally block restores the file. A
signal that arrives during a restore is recorded and acted on once the restore is
done. A handled signal the parent set to ignore, as nohup does, stays ignored.
Each killer runs in its own process group, and a timeout or an interrupt SIGKILLs
that group. A process the killer started that stays in the group stops with it. A
process that leaves the group (setsid, a double fork) is not reached, and the
group is not killed when the killer exits normally.

Each killer's full, untruncated output goes to a per-mutant log. The summary
prints one line per mutant: its verdict, how many killers failed, and the first
failure line. A mutant is KILLED when at least one killer fails and none did
not run or timed out. A killer that passes under the mutant is listed, because
the record says it should kill.

SHARED-TEXT is reported when a mutant and its mirror produce the same first
failure message. A kill that reads the same on both edges is not two kills
(DIR-017), so the run fails with its own exit status.

Exit status is listed under --help (EXIT_DOC below).

Usage (from anywhere):

    python3 -I scripts/mutate.py                   run every active mutant
    python3 -I scripts/mutate.py --ids "GO-KEY M2" run the named mutants only
    python3 -I scripts/mutate.py --log-dir DIR     keep the logs in DIR
    python3 -I scripts/mutate.py --timeout 300     bound each killer run to 300 seconds

Python killers run as one unittest process per mutant, with
PYTHONDONTWRITEBYTECODE=1 and python3 -B -I. Go killers run one `go test -run`
each, in the mutant's package.
"""

import argparse
import filecmp
import json
import math
import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile

try:
    import fcntl
except ImportError:  # not POSIX: the import is guarded only so main() can refuse with a message
    fcntl = None

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import check_mutants as cm  # noqa: E402  (must follow the sys.path insert)

ROOT = cm.ROOT
RULE = "=" * 70
DEFAULT_TIMEOUT = 600.0  # seconds per killer run; matches go test's own default of ten minutes
MARKER_NAME = "mutate-unrestored.json"  # in the git directory: one entry per planted file not yet verified restored
LOCK_NAME = "mutate.lock"  # in the git directory: held, exclusively, for the length of a run
POSIX = os.name == "posix" and fcntl is not None
ID_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9-]*$")
# The signals that stop a run. Each is routed to _interrupt while the run lasts. The
# names are looked up so the module still imports off POSIX, where main() refuses.
HANDLED_SIGNALS = tuple(getattr(signal, name) for name in ("SIGINT", "SIGTERM", "SIGHUP") if hasattr(signal, name))
# The traceback Python prints for a suite that does not compile: a File line with
# no ", in <name>" suffix (a runtime traceback frame has one), then the
# SyntaxError or IndentationError line, which starts at column 0.
SYNTAX_PATTERN = re.compile(r'^  File "[^"]+", line \d+\n(?:[^\n]*\n)*?(?:SyntaxError|IndentationError): ', re.M)

EXIT_OK = 0
EXIT_FAILED = 1
EXIT_REFUSED = 2
EXIT_NO_TOOL = 3
EXIT_SHARED = 4
EXIT_INTERRUPTED = 5
EXIT_DOC = {
    EXIT_OK: "every selected mutant KILLED, every restore verified, tree unchanged, no SHARED-TEXT",
    EXIT_FAILED: "a mutant SURVIVED or was INVALID, a restore failed, or the tree changed",
    EXIT_REFUSED: "refused before anything was planted; the message names the cause",
    EXIT_NO_TOOL: "a required tool (git, or go for a Go mutant) is not installed or cannot answer",
    EXIT_SHARED: "every mutant KILLED, but a mutant and its mirror fail with the same message",
    EXIT_INTERRUPTED: "interrupted by a signal or Ctrl-C; the planted file, if any, was restored and verified",
}


class Outcome:
    """What happened to one mutant. Filled in by mutate_one()."""

    def __init__(self, ident):
        self.id = ident
        self.verdict = "INVALID"          # KILLED, SURVIVED or INVALID
        self.killers = []                 # (selector, status, message) per killer
        self.first_failure = None         # (selector, message) of the first failing killer
        self.problems = []                # reasons a verdict is INVALID or a restore failed
        self.restore_ok = True            # while planted: False until the restore is verified and its marker entry cleared
        self.planted = False              # True from just before the plant is recorded; reset if recording is refused
        self.relpath = None               # the file this mutant edits, relative to the repository
        self.saved = None                 # the saved copy of that file's original bytes
        self.log = None
        self.late_signals = []            # signal names that arrived during the restore, to act on after it


class ToolError(Exception):
    """git or go could not run, or git could not answer. The run cannot be trusted."""


class RunLocked(Exception):
    """Another run holds the lock, or the lock cannot be taken. Nothing has been planted."""


class Interrupted(Exception):
    """Raised by the signal handler so that the finally blocks run and the file is restored."""


def git(args):
    """Run git in the repository root. Returns the CompletedProcess. Raises ToolError if git is absent."""
    try:
        return subprocess.run(["git", "-C", ROOT] + args, capture_output=True, text=True)
    except OSError as exc:
        raise ToolError(f"cannot run git: {exc}") from exc


def tree_clean(relpath):
    """
    True when the path has no change against HEAD: no staged or unstaged edit,
    and no untracked or ignored file. Raises ToolError when git cannot answer,
    so a failing git is never read as a dirty file.
    """
    proc = git(["status", "--porcelain", "--ignored", "--", relpath])
    if proc.returncode != 0:
        raise ToolError(f"git status failed for {relpath}: {proc.stderr.strip()}")
    return proc.stdout == ""


def git_dir():
    """The absolute path of the repository's git directory. Raises ToolError when git cannot answer."""
    proc = git(["rev-parse", "--absolute-git-dir"])
    if proc.returncode != 0:
        raise ToolError(f"git rev-parse failed: {proc.stderr.strip()}")
    return proc.stdout.strip()


def read_marker(path):
    """
    The unrestored plants recorded at `path`: a dict keyed by repository-relative
    file path, each value {"id", "log_dir", "orig"}. A missing marker is empty.
    Refuses to follow a symlink. Raises OSError when the marker cannot be opened
    and ValueError when it does not parse, so a caller never overwrites what it
    cannot read. The ValueError names the file and the recovery step.
    """
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    except FileNotFoundError:
        return {}
    with os.fdopen(fd, encoding="utf-8") as handle:
        try:
            data = json.load(handle)
        except ValueError as exc:  # JSONDecodeError and UnicodeDecodeError are both ValueErrors
            raise ValueError(_unreadable_marker(path, str(exc))) from exc
    if not isinstance(data, dict):
        raise ValueError(_unreadable_marker(path, "it does not hold a JSON object"))
    return data


def _unreadable_marker(path, reason):
    """The refusal text for a marker that cannot be parsed: the file, why, and what to do."""
    return (f"the marker {path} cannot be parsed ({reason}). A run that was killed may have left a file "
            f"planted: run `git status`, restore any modified file with `git checkout -- <file>`, "
            f"then delete {path} and run again")


def write_marker(path, data):
    """
    Replace the marker at `path` with `data`, atomically: the text goes to a new
    temporary file in the same directory (created 0600, O_EXCL and O_NOFOLLOW),
    is fsynced, and is renamed over `path`. A kill leaves the old marker or the new
    one, never a partial file. The rename replaces a symlink at `path` rather than
    following it, so a planted link cannot redirect the write. A kill can leave the
    temporary file behind; it is not read. Raises OSError on failure.
    """
    tmp = f"{path}.{os.getpid()}.{os.urandom(4).hex()}.tmp"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(data, handle, indent=2, sort_keys=True)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise
    try:  # make the rename itself durable; the marker is already in place if this fails
        dir_fd = os.open(os.path.dirname(path) or ".", os.O_RDONLY)
        try:
            os.fsync(dir_fd)
        finally:
            os.close(dir_fd)
    except OSError:
        pass


def acquire_run_lock(path):
    """
    Take the exclusive run lock: flock on `path`, created 0600, never following a
    symlink, non-blocking. Returns the open descriptor; closing it releases the lock,
    and so does the process ending. Raises RunLocked when another run holds it or
    the lock cannot be taken.
    """
    try:
        fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    except OSError as exc:
        raise RunLocked(f"cannot open the run lock {path}: {exc}") from exc
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError as exc:
        os.close(fd)
        raise RunLocked(f"another mutate run holds {path}; two runs on one working tree would "
                        f"plant over each other") from exc
    except OSError as exc:
        os.close(fd)
        raise RunLocked(f"cannot take the run lock {path}: {exc}") from exc
    return fd


def record_plant(path, relpath, ident, log_dir, saved):
    """Record that `relpath` is planted by mutant `ident`, whose original is saved at `saved` in `log_dir`."""
    data = read_marker(path)
    data[relpath] = {"id": ident, "log_dir": log_dir, "orig": saved}
    write_marker(path, data)


def clear_plant(path, relpath):
    """Remove the entry for `relpath`: its restore is verified. Other entries are kept."""
    data = read_marker(path)
    data.pop(relpath, None)
    write_marker(path, data)


def saved_original_note(relpath, path):
    """
    What a refusal says about `relpath`. It names the log directory of an earlier
    run's saved original only when the marker records an unrestored plant of that
    file and that original still exists in the directory the marker names.
    """
    try:
        entry = read_marker(path).get(relpath)
    except (OSError, ValueError):
        entry = None
    if (isinstance(entry, dict) and isinstance(entry.get("log_dir"), str)
            and isinstance(entry.get("orig"), str)
            and os.path.dirname(entry["orig"]) == entry["log_dir"]
            and os.path.isfile(entry["orig"])):
        return (f"an earlier run left it planted; its logs in {entry['log_dir']} hold its saved original "
                f"{entry['orig']}")
    return "the marker records no saved original of an earlier run for it"


def _short(dotted):
    """The Class.method pair of a dotted unittest name."""
    return ".".join(dotted.split(".")[-2:])


def parse_python(output, killers):
    """
    Classify each killer from unittest -v output. A killer that appears in a
    FAIL or ERROR header failed, and the first error line of its block is its
    message. A killer that appears on a "... " line ran and passed. Anything
    else did not run. The status token on the "... " line is not used: stderr
    written during a test can push it off that line.

    A test with a docstring prints its name and its first docstring line on
    separate lines, with " ... " after the docstring line. That form is matched
    too, so a documented killer is not reported as not run.
    """
    ran = {_short(m.group(1)) for m in re.finditer(r"^\w+ \(([\w.]+)\)(?:\n[^\n]*?)? \.\.\. ", output, re.M)}
    failed = {}
    for block in output.split(RULE):
        header = re.search(r"^(FAIL|ERROR): \w+ \(([\w.]+)\)", block, re.M)
        if not header:
            continue
        key = _short(header.group(2))
        message = None
        for line in block.splitlines()[1:]:
            if re.search(r"(AssertionError|KeyError|\w+Error:)", line):
                message = line.strip()
                break
        failed.setdefault(key, (header.group(1), message))
    results = []
    for selector in killers:
        if selector in failed:
            label, message = failed[selector][0], failed[selector][1]
        elif selector in ran:
            label, message = "ok", None
        else:
            label, message = "NOT-RUN", None
        results.append((selector, label, message))
    return results


def parse_go(output, parent, sub):
    """
    The status of one Go selector from `go test -v` output. Returns
    (status, message): FAIL when a matching subtest failed, ok when one passed,
    NOT-RUN when none matched. The message is the first _test.go line printed
    under that subtest's own header. A line is attributed to the subtest named
    by the most recent === RUN, === NAME or === CONT header. === PAUSE leaves
    the attribution where it was. The --- FAIL lines come after every header,
    so the lines are keyed by subtest name, not by position.
    """
    run_header = re.compile(r"^\s*=== (RUN|NAME|CONT)\s+(\S+)")
    verdict = re.compile(r"^\s*--- (PASS|FAIL): (\S+) \(")
    current = None
    printed = {}
    seen = []
    message = None
    for line in output.splitlines():
        header = run_header.match(line)
        if header:
            current = header.group(2)
            printed.setdefault(current, [])
            continue
        if current is not None and re.search(r"_test\.go:\d+:", line):
            printed[current].append(line.strip())
            continue
        match = verdict.match(line)
        if not match:
            continue
        name = match.group(2)
        top, _, rest = name.partition("/")
        if top != parent:
            continue
        if sub and not re.search(sub, rest):
            continue
        if not sub and rest:
            continue
        seen.append(match.group(1))
        if match.group(1) == "FAIL" and message is None:
            lines = printed.get(name) or []
            message = lines[0] if lines else "(failed with no _test.go line)"
    if "FAIL" in seen:
        return "FAIL", message
    if "PASS" in seen:
        return "ok", None
    return "NOT-RUN", None


def _kill_group(proc):
    """
    SIGKILL the process group that `proc` leads: every process still in that group
    dies with it. A process that left the group is not reached. Called only while
    `proc` is not yet reaped, so its group id cannot have been reused. A group that
    has already gone is not an error.
    """
    try:
        os.killpg(proc.pid, signal.SIGKILL)
    except OSError:
        pass


def _run_group(cmd, cwd, env, timeout):
    """
    subprocess.run() for one killer. The killer leads its own session and process
    group. Returns a CompletedProcess. On a timeout or an interrupt the group is
    killed (see _kill_group), the leader is reaped, and the exception is re-raised.
    A normal exit does not kill the group. The pipes are not read after the kill, because a grandchild that
    outlived the group could hold them open.
    """
    proc = subprocess.Popen(cmd, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            text=True, start_new_session=True)
    try:
        stdout, stderr = proc.communicate(timeout=timeout)
    except BaseException:
        _kill_group(proc)
        proc.wait()
        proc.stdout.close()
        proc.stderr.close()
        raise
    return subprocess.CompletedProcess(cmd, proc.returncode, stdout, stderr)


def run_killers(entry, log, timeout):
    """
    Run every killer of one mutant with the mutation planted. Writes the full
    output of each run to `log`. Returns a list of (selector, status, message),
    where status is FAIL, ok, NOT-RUN or TIMEOUT. Raises ToolError when go
    cannot be started.
    """
    if entry["language"] == "py":
        cmd = [sys.executable, "-I", "-B", entry["suite"], "-v"] + entry["killers"]
        env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1")
        try:
            proc = _run_group(cmd, ROOT, env, timeout)
        except subprocess.TimeoutExpired:
            log.write(f"$ PYTHONDONTWRITEBYTECODE=1 {' '.join(cmd)}\nTIMED OUT after {timeout:g}s\n{RULE}\n")
            return [(k, "TIMEOUT", f"timed out after {timeout:g}s") for k in entry["killers"]]
        output = proc.stdout + proc.stderr
        log.write(f"$ PYTHONDONTWRITEBYTECODE=1 {' '.join(cmd)}\n{output}\n{RULE}\n")
        if SYNTAX_PATTERN.search(output):
            return [(k, "NOT-RUN", "module does not parse") for k in entry["killers"]]
        return parse_python(output, entry["killers"])
    results = []
    for selector in entry["killers"]:
        parent, _, sub = selector.partition("/")
        # Subtest names may contain "/", which -run cannot select, so the parent
        # runs whole and parse_go() keeps only the subtests this selector matches.
        cmd = ["go", "test", "-v", "-count=1", "-run", f"^{parent}$", entry["package"]]
        try:
            proc = _run_group(cmd, ROOT, None, timeout)
        except subprocess.TimeoutExpired:
            log.write(f"$ {' '.join(cmd)}\nTIMED OUT after {timeout:g}s\n{RULE}\n")
            results.append((selector, "TIMEOUT", f"timed out after {timeout:g}s"))
            continue
        except OSError as exc:
            raise ToolError(f"cannot run go: {exc}") from exc
        output = proc.stdout + proc.stderr
        log.write(f"$ {' '.join(cmd)}\n{output}\n{RULE}\n")
        if "[build failed]" in output or "[setup failed]" in output:
            results.append((selector, "NOT-RUN", "build failed"))
            continue
        status, message = parse_go(output, parent, sub)
        results.append((selector, status, message))
    return results


_DEFERRED = None  # the list restore_and_verify() records signals in; None outside a restore


def _interrupt(signum, _frame):
    """
    Signal handler for HANDLED_SIGNALS. Outside a restore it raises Interrupted,
    so the finally blocks run. During a restore it records the signal and returns,
    so the restore finishes; main acts on the record afterwards.
    """
    name = signal.Signals(signum).name
    if _DEFERRED is not None:
        _DEFERRED.append(name)
        return
    raise Interrupted(name)


def restore_and_verify(out, path, relpath, original, saved, marker):
    """
    Write the original bytes back, verify the restore (bytes, cmp against the
    saved copy, clean against HEAD), and remove the file's entry from the marker
    only when all three hold. The verdict goes on `out`: restore_ok becomes True
    only after the entry is cleared, and stays False with a RESTORE FAILED problem
    naming the saved copy when a check fails. OSError, ValueError and ToolError are
    caught. Once it has set _DEFERRED, signals are recorded in out.late_signals
    rather than raised; a signal that lands before that raises Interrupted out of
    this function, and restore_until_done() runs it again.
    """
    global _DEFERRED
    _DEFERRED = out.late_signals
    try:
        try:
            with open(path, "wb") as handle:
                handle.write(original)
            with open(path, "rb") as handle:
                same_bytes = handle.read() == original
            same_copy = filecmp.cmp(saved, path, shallow=False)
            clean = tree_clean(relpath)
            if not (same_bytes and same_copy and clean):
                out.restore_ok = False
                out.problems.append(
                    f"RESTORE FAILED for {relpath}: bytes-equal={same_bytes} "
                    f"cmp-equal={same_copy} git-clean={clean}; original saved at {saved}")
                return
            clear_plant(marker, relpath)
            out.restore_ok = True
        except (OSError, ValueError, ToolError) as exc:
            out.restore_ok = False
            out.problems.append(f"RESTORE FAILED for {relpath}: {exc}; original saved at {saved}")
    finally:
        _DEFERRED = None


def restore_until_done(out, path, relpath, original, saved, marker):
    """
    restore_and_verify(), run again when a signal lands before it has started
    deferring signals. It is safe to repeat: it writes the same bytes and clearing an
    absent marker entry changes nothing. The signal is recorded in out.late_signals.
    """
    while True:
        try:
            restore_and_verify(out, path, relpath, original, saved, marker)
            return
        except Interrupted as exc:
            out.late_signals.append(str(exc))


def mutate_one(entry, log_dir, timeout, out=None):
    """
    Plant one mutant, run its killers, restore, and verify the restore.
    Returns an Outcome. `out`, when given, is the Outcome to fill in, so a caller
    that catches an interrupt still holds the restore verdict. From just before
    the plant is recorded, out.planted is True and out.restore_ok is False until
    the restore is verified, so a restore that never ran reads as a failed one. The
    restore runs in a finally block that covers the recording, the write and the
    killers; it is skipped only when recording the plant was refused, and then
    nothing was written. Raises ToolError when git cannot answer the clean-tree
    check, and Interrupted when a signal lands during the run.
    """
    if out is None:
        out = Outcome(entry["id"])
    relpath = entry["file"]
    out.relpath = relpath
    path = os.path.join(ROOT, relpath)
    out.log = os.path.join(log_dir, f"{entry['id']}.log")
    if not os.path.isfile(path):
        out.problems.append(f"{relpath} does not exist")
        return out
    if not tree_clean(relpath):
        out.problems.append(f"{relpath} has changes against HEAD or is untracked; refusing to plant over it")
        return out
    with open(path, "rb") as handle:
        original = handle.read()
    text = original.decode("utf-8")
    try:
        pos = cm.locate(text, entry["line"], entry["before"])
    except cm.AnchorError as exc:
        out.problems.append(f"{relpath}:{entry['line']}: {exc}")
        return out
    saved = os.path.join(log_dir, f"{entry['id']}.orig")
    out.saved = saved
    with open(saved, "wb") as handle:
        handle.write(original)
    mutated = text[:pos] + entry["after"] + text[pos + len(entry["before"]):]
    marker = os.path.join(git_dir(), MARKER_NAME)
    out.planted = True      # before the marker is written: from here an exit that skips the restore reads as unrestored
    out.restore_ok = False
    refused = False
    try:
        try:
            record_plant(marker, relpath, entry["id"], log_dir, saved)
        except (OSError, ValueError) as exc:
            refused = True
            out.problems.append(f"cannot record the plant in {marker}: {exc}; refusing to plant")
            return out
        with open(out.log, "w", encoding="utf-8") as log:
            log.write(f"mutant {entry['id']}  file {relpath}  line {entry['line']}\n")
            log.write(f"before: {entry['before']!r}\nafter:  {entry['after']!r}\n{RULE}\n")
            with open(path, "wb") as handle:
                handle.write(mutated.encode("utf-8"))
            out.killers = run_killers(entry, log, timeout)
    finally:
        if refused:  # nothing was recorded and nothing was written
            out.planted = False
            out.restore_ok = True
        else:
            restore_until_done(out, path, relpath, original, saved, marker)
    failing = [(sel, msg) for sel, st, msg in out.killers if st in ("FAIL", "ERROR")]
    not_run = [sel for sel, st, _ in out.killers if st == "NOT-RUN"]
    timed_out = [sel for sel, st, _ in out.killers if st == "TIMEOUT"]
    if not out.killers:
        out.problems.append("no killers ran")
    if not_run:
        out.problems.append(f"killer(s) did not run: {', '.join(not_run)}")
    if timed_out:
        out.problems.append(f"killer(s) timed out after {timeout:g}s: {', '.join(timed_out)}")
    if not out.killers or not_run or timed_out:
        out.verdict = "INVALID"  # a selector that matched nothing, or a run that never finished, is not a kill
    elif failing:
        out.verdict = "KILLED"
        out.first_failure = failing[0]
    else:
        out.verdict = "SURVIVED"
    return out


def describe(out, total):
    """One summary line per mutant, then the killers that did not fail."""
    failing = sum(1 for _, st, _ in out.killers if st in ("FAIL", "ERROR"))
    line = f"{out.id:<22} {out.verdict:<9} {failing}/{total} killers fail"
    if out.first_failure:
        sel, msg = out.first_failure
        line += f"  first: {sel}: {msg or '(no message)'}"
    lines = [line]
    for sel, st, _ in out.killers:
        if st not in ("FAIL", "ERROR"):
            lines.append(f"    not failing: {sel} [{st}]")
    for problem in out.problems:
        lines.append(f"    PROBLEM: {problem}")
    return lines


def install_handlers(installed):
    """
    Route HANDLED_SIGNALS to _interrupt, recording each previous disposition in
    `installed`. A signal the parent set to SIG_IGN (nohup, a background job) is
    left alone, so the run keeps the ignore the parent asked for.
    """
    for sig in HANDLED_SIGNALS:
        prior = signal.getsignal(sig)
        if prior == signal.SIG_IGN:
            continue
        installed[sig] = prior
        signal.signal(sig, _interrupt)


def restore_handlers(installed):
    """
    Put back the dispositions install_handlers() replaced. getsignal() returns None
    for a handler that was not set from Python, and signal() cannot take None back,
    so that one is restored to SIG_DFL. The handled signals are blocked while the
    dispositions go back, so a signal in that window is held and then delivered to
    the restored disposition, instead of raising Interrupted out of main() with the
    rest not yet put back.
    """
    old_mask = signal.pthread_sigmask(signal.SIG_BLOCK, HANDLED_SIGNALS)
    try:
        for sig, prior in installed.items():
            signal.signal(sig, signal.SIG_DFL if prior is None else prior)
    finally:
        signal.pthread_sigmask(signal.SIG_SETMASK, old_mask)


def finish_early(outcomes, current, exit_status):
    """
    The exit status of a run that stopped early, by an interrupt or a tool failure.
    A planted file that is not restored and verified, from `current` or from an
    earlier mutant, is named with RESTORE FAILED and its saved original, and the
    status is EXIT_FAILED whatever stopped the run. Otherwise the status is
    `exit_status`; the run then reports that every restore is verified.
    """
    suspects = list(outcomes)
    if current is not None and current not in suspects:
        suspects.append(current)
    failed = [o for o in suspects if o.planted and not o.restore_ok]
    for out in failed:
        if out not in outcomes:  # describe() has not printed the in-flight mutant's problems yet
            for problem in out.problems:
                print(f"    PROBLEM: {problem}")
        print(f"mutate: RESTORE FAILED for {out.relpath}; the original is saved at {out.saved}")
    if failed:
        return EXIT_FAILED
    print("mutate: every restore is verified; no planted file is left mutated")
    return exit_status


def _positive_timeout(text):
    """argparse type for --timeout: a positive, finite number of seconds."""
    value = float(text)
    if not math.isfinite(value) or value <= 0:
        raise argparse.ArgumentTypeError("must be a positive, finite number of seconds")
    return value


def main(argv=None):
    """Command-line entry point. Returns the process exit status (see EXIT_DOC)."""
    epilog = "exit status:\n" + "\n".join(f"  {code}  {text}" for code, text in sorted(EXIT_DOC.items()))
    parser = argparse.ArgumentParser(
        description=__doc__.split("\n", 1)[0], epilog=epilog,
        formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--ids", default="", help="space-separated mutant ids to run; default every active mutant")
    parser.add_argument("--log-dir", help="directory for the logs (default: a fresh temporary directory)")
    parser.add_argument("--timeout", type=_positive_timeout, default=DEFAULT_TIMEOUT,
                        help=f"seconds allowed for each killer run (default {DEFAULT_TIMEOUT:g})")
    args = parser.parse_args(argv)
    if not POSIX:
        print("mutate: POSIX only; the runner needs process groups, flock and O_NOFOLLOW, "
              "and this platform has none of them. Nothing was planted")
        return EXIT_REFUSED
    try:
        spec = cm.load_spec()
    except (OSError, ValueError) as exc:
        print(f"mutate: cannot load the spec: {exc}")
        return EXIT_REFUSED
    items = spec["mutants"]
    spec_errors = cm.schema_errors(items)
    if spec_errors:
        for err in spec_errors:
            print(f"mutate: spec error: {err}")
        return EXIT_REFUSED
    chosen = cm.active(items)
    requested = args.ids.split()
    for ident in requested:
        if not ID_PATTERN.match(ident):
            print(f"mutate: not a mutant id: {ident!r}")
            return EXIT_REFUSED
    if requested:
        known = {m["id"]: m for m in chosen}
        unknown = [i for i in requested if i not in known]
        if unknown:
            print(f"mutate: not an active mutant: {', '.join(unknown)}")
            return EXIT_REFUSED
        chosen = [known[i] for i in requested]
    if shutil.which("git") is None:
        print("mutate: git is not installed or not on PATH; the clean-tree check cannot run")
        return EXIT_NO_TOOL
    if any(e["language"] == "go" for e in chosen) and shutil.which("go") is None:
        print("mutate: go is not installed or not on PATH; the selected mutants include Go killers")
        return EXIT_NO_TOOL
    try:
        if git(["rev-parse", "--git-dir"]).returncode != 0:
            print("mutate: not a git checkout; the clean-tree guard cannot run")
            return EXIT_REFUSED
        git_directory = git_dir()
    except ToolError as exc:
        print(f"mutate: {exc}")
        return EXIT_NO_TOOL
    # The lock comes before any read of the tree or the marker, so another run's planted file
    # is never mistaken for a leftover of an earlier one.
    try:
        lock = acquire_run_lock(os.path.join(git_directory, LOCK_NAME))
    except RunLocked as exc:
        print(f"mutate: {exc}")
        return EXIT_REFUSED
    try:
        return run_locked(args, chosen, os.path.join(git_directory, MARKER_NAME))
    finally:
        os.close(lock)


def run_locked(args, chosen, marker):
    """The part of a run that holds the run lock: the clean-tree check, the loop, and the verdict. Returns the exit status."""
    try:
        try:
            read_marker(marker)
        except ValueError as exc:  # unparseable: refuse before planting anything, with its recovery step
            print(f"mutate: {exc}")
            return EXIT_REFUSED
        except OSError:
            pass  # an unopenable marker is reported by the refusal below or by the plant, with its cause
        dirty = sorted({e["file"] for e in chosen if not tree_clean(e["file"])})
        notes = {relpath: saved_original_note(relpath, marker) for relpath in dirty}
    except ToolError as exc:
        print(f"mutate: {exc}")
        return EXIT_NO_TOOL
    if dirty:
        for relpath in dirty:
            print(f"mutate: {relpath} has changes against HEAD or is untracked; refusing to plant over it")
            print(f"mutate: {relpath}: {notes[relpath]}")
        return EXIT_REFUSED

    log_dir = args.log_dir or tempfile.mkdtemp(prefix="mutate-")
    os.makedirs(log_dir, exist_ok=True)
    print(f"mutate: {len(chosen)} mutant(s); killer timeout {args.timeout:g}s; logs in {log_dir}")
    installed = {}
    outcomes = []
    current = None  # the Outcome of the mutant in progress, so an early stop can still read its restore verdict
    interrupted = None
    tool_failure = None
    try:
        install_handlers(installed)  # before any git call, so a signal from the start is handled too
        before_status = git(["status", "--porcelain"]).stdout
        for entry in chosen:
            current = Outcome(entry["id"])
            out = mutate_one(entry, log_dir, args.timeout, current)
            outcomes.append(out)
            for line in describe(out, len(entry["killers"])):
                print(line)
            sys.stdout.flush()
            if out.late_signals:  # arrived during this mutant's restore; the file is restored, so stop here
                raise Interrupted(out.late_signals[0])
        after_status = git(["status", "--porcelain"]).stdout
    except (Interrupted, KeyboardInterrupt) as exc:
        interrupted = str(exc) or "Ctrl-C"
    except ToolError as exc:
        tool_failure = str(exc)
    finally:
        restore_handlers(installed)

    if interrupted is not None:
        print(f"mutate: interrupted by {interrupted}")
        return finish_early(outcomes, current, EXIT_INTERRUPTED)
    if tool_failure is not None:
        print(f"mutate: {tool_failure}")
        return finish_early(outcomes, current, EXIT_NO_TOOL)

    by_id = {o.id: o for o in outcomes}
    mirror_of = {m["id"]: m["mirror"] for m in chosen}
    shared = []
    seen_pairs = set()
    for out in outcomes:
        partner = by_id.get(mirror_of.get(out.id))
        if partner is None or out.first_failure is None or partner.first_failure is None:
            continue
        pair = tuple(sorted((out.id, partner.id)))
        if pair in seen_pairs:
            continue
        seen_pairs.add(pair)
        if out.first_failure[1] and out.first_failure[1] == partner.first_failure[1]:
            shared.append((pair, out.first_failure[1]))
    for (first, second), text in shared:
        print(f"SHARED-TEXT: {first} and {second} fail with the same message: {text}")

    tree_ok = before_status == after_status
    if not tree_ok:
        print("mutate: the working tree changed during the run; see git status")

    killed = sum(1 for o in outcomes if o.verdict == "KILLED")
    survived = sum(1 for o in outcomes if o.verdict == "SURVIVED")
    invalid = sum(1 for o in outcomes if o.verdict == "INVALID")
    restore_bad = sum(1 for o in outcomes if not o.restore_ok)
    print(f"mutate: killed {killed}, survived {survived}, invalid {invalid}, "
          f"restore failures {restore_bad}, shared-text {len(shared)}; logs in {log_dir}")
    if survived or invalid or restore_bad or not tree_ok:
        return EXIT_FAILED
    if shared:
        return EXIT_SHARED
    return EXIT_OK


if __name__ == "__main__":
    sys.exit(main())

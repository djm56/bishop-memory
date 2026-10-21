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
  4. restore the original bytes in a finally block, then verify the restore
     three ways: the bytes equal the original, cmp matches the saved copy, and
     the file is clean against HEAD.

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
import glob
import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import check_mutants as cm  # noqa: E402  (must follow the sys.path insert)

ROOT = cm.ROOT
RULE = "=" * 70
DEFAULT_TIMEOUT = 600.0  # seconds per killer run; matches go test's own default of ten minutes
ID_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9-]*$")
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
    EXIT_REFUSED: "refused before planting: usage error, malformed spec, not a git checkout, or uncommitted changes",
    EXIT_NO_TOOL: "a required tool (git, or go for a Go mutant) is not installed or cannot answer",
    EXIT_SHARED: "every mutant KILLED, but a mutant and its mirror fail with the same message",
    EXIT_INTERRUPTED: "interrupted by a signal or Ctrl-C after planting began; the file was restored",
}


class Outcome:
    """What happened to one mutant. Filled in by mutate_one()."""

    def __init__(self, ident):
        self.id = ident
        self.verdict = "INVALID"          # KILLED, SURVIVED or INVALID
        self.killers = []                 # (selector, status, message) per killer
        self.first_failure = None         # (selector, message) of the first failing killer
        self.problems = []                # reasons a verdict is INVALID or a restore failed
        self.restore_ok = True
        self.log = None


class ToolError(Exception):
    """git or go could not run, or git could not answer. The run cannot be trusted."""


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


def last_saved_originals():
    """
    The most recently written mutate-* directory in the temporary directory that
    holds a saved .orig copy, or None. A run leaves its originals there so that a
    refused or interrupted run can be checked against them.
    """
    base = glob.escape(tempfile.gettempdir())
    candidates = []
    for path in glob.glob(os.path.join(base, "mutate-*")):
        origs = glob.glob(os.path.join(glob.escape(path), "*.orig"))
        if os.path.isdir(path) and origs:
            candidates.append((max(os.path.getmtime(o) for o in origs), path))
    return max(candidates)[1] if candidates else None


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
            proc = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True, env=env, timeout=timeout)
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
            proc = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True, timeout=timeout)
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


def mutate_one(entry, log_dir, timeout):
    """
    Plant one mutant, run its killers, restore, and verify the restore.
    Returns an Outcome. The file is restored in a finally block whatever happens.
    Raises ToolError when git cannot answer the clean-tree check.
    """
    out = Outcome(entry["id"])
    relpath = entry["file"]
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
    with open(saved, "wb") as handle:
        handle.write(original)
    mutated = text[:pos] + entry["after"] + text[pos + len(entry["before"]):]
    with open(out.log, "w", encoding="utf-8") as log:
        log.write(f"mutant {entry['id']}  file {relpath}  line {entry['line']}\n")
        log.write(f"before: {entry['before']!r}\nafter:  {entry['after']!r}\n{RULE}\n")
        try:
            with open(path, "wb") as handle:
                handle.write(mutated.encode("utf-8"))
            out.killers = run_killers(entry, log, timeout)
        finally:
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
            except (OSError, ToolError) as exc:
                out.restore_ok = False
                out.problems.append(f"RESTORE FAILED for {relpath}: {exc}; original saved at {saved}")
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


def _interrupt(signum, _frame):
    """Signal handler: turn SIGTERM or SIGHUP into Interrupted so the restore in finally runs."""
    raise Interrupted(signal.Signals(signum).name)


def _positive_timeout(text):
    """argparse type for --timeout: a positive number of seconds."""
    value = float(text)
    if value <= 0:
        raise argparse.ArgumentTypeError("must be a positive number of seconds")
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
        dirty = sorted({e["file"] for e in chosen if not tree_clean(e["file"])})
    except ToolError as exc:
        print(f"mutate: {exc}")
        return EXIT_NO_TOOL
    if dirty:
        for relpath in dirty:
            print(f"mutate: {relpath} has changes against HEAD or is untracked; refusing to plant over it")
        saved = last_saved_originals()
        if saved:
            print(f"mutate: the last run's saved originals are in {saved}")
        else:
            print("mutate: no saved originals from an earlier run were found in the temporary directory")
        return EXIT_REFUSED

    log_dir = args.log_dir or tempfile.mkdtemp(prefix="mutate-")
    os.makedirs(log_dir, exist_ok=True)
    before_status = git(["status", "--porcelain"]).stdout

    handled = [signal.SIGTERM] + ([signal.SIGHUP] if hasattr(signal, "SIGHUP") else [])
    previous = {sig: signal.signal(sig, _interrupt) for sig in handled}
    print(f"mutate: {len(chosen)} mutant(s); killer timeout {args.timeout:g}s; logs in {log_dir}")
    outcomes = []
    interrupted = None
    tool_failure = None
    try:
        for entry in chosen:
            out = mutate_one(entry, log_dir, args.timeout)
            outcomes.append(out)
            for line in describe(out, len(entry["killers"])):
                print(line)
            sys.stdout.flush()
    except (Interrupted, KeyboardInterrupt) as exc:
        interrupted = str(exc) or "Ctrl-C"
    except ToolError as exc:
        tool_failure = str(exc)
    finally:
        for sig, handler in previous.items():
            signal.signal(sig, handler)

    if interrupted is not None:
        print(f"mutate: interrupted by {interrupted}; the mutant in progress was restored by its finally block")
        return EXIT_INTERRUPTED
    if tool_failure is not None:
        print(f"mutate: {tool_failure}")
        return EXIT_NO_TOOL

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

    after_status = git(["status", "--porcelain"]).stdout
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

#!/usr/bin/env python3
"""
Mutation runner for the DIR-017 spec, testdata/mutants.json.

For each active mutant, or each one named with --id:

  1. refuse to plant unless the file is clean against HEAD (git diff --quiet);
  2. locate `before` exactly as scripts/check_mutants.py does, and stop if it is not there;
  3. save a copy of the file, plant the edit alone, and run the mutant's killers;
  4. restore the original bytes in a finally block, then verify the restore
     three ways: the bytes equal the original, cmp matches the saved copy, and
     git reports the file clean.

Each killer's full, untruncated output goes to a per-mutant log. The summary
prints one line per mutant: its verdict, how many killers failed, and the first
failure line. A mutant is KILLED when at least one killer fails. A killer that
passes under the mutant is listed, because the record says it should kill.

SHARED-TEXT is reported when a mutant and its mirror produce the same first
failure message. A kill that reads the same on both edges is not two kills
(DIR-017). It is a warning and does not change the exit status.

Exit status: 0 when every selected mutant is KILLED and every restore verifies;
1 otherwise; 2 for a usage error or a malformed spec.

Usage (from anywhere):

    python3 -I scripts/mutate.py                  run every active mutant
    python3 -I scripts/mutate.py --id GO-KEY      run one (repeatable)
    python3 -I scripts/mutate.py --log-dir DIR    keep the logs in DIR

Python killers run as one unittest process per mutant, with
PYTHONDONTWRITEBYTECODE=1 and python3 -B -I. Go killers run one `go test -run`
each, in the mutant's package.
"""

import argparse
import filecmp
import os
import re
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import check_mutants as cm  # noqa: E402  (must follow the sys.path insert)

ROOT = cm.ROOT
RULE = "=" * 70


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


def git(args):
    """Run git in the repository root. Returns the CompletedProcess."""
    return subprocess.run(["git", "-C", ROOT] + args, capture_output=True, text=True)


def tree_clean(relpath):
    """True when the file matches HEAD. A file outside git counts as dirty."""
    return git(["diff", "--quiet", "--", relpath]).returncode == 0


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
    """
    ran = {_short(m.group(1)) for m in re.finditer(r"^\w+ \(([\w.]+)\) \.\.\.", output, re.M)}
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
    NOT-RUN when none matched; message is the first _test.go line before the
    failing subtest's --- FAIL line.
    """
    run_header = re.compile(r"^\s*=== RUN\s+(\S+)")
    verdict = re.compile(r"^\s*--- (PASS|FAIL): (\S+) \(")
    pending = []
    seen = []
    message = None
    for line in output.splitlines():
        if run_header.match(line):
            pending = []
            continue
        if "_test.go:" in line and re.search(r"_test\.go:\d+:", line):
            pending.append(line.strip())
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
            message = pending[0] if pending else "(failed with no _test.go line)"
    if "FAIL" in seen:
        return "FAIL", message
    if "PASS" in seen:
        return "ok", None
    return "NOT-RUN", None


def run_killers(entry, log):
    """
    Run every killer of one mutant with the mutation planted. Writes the full
    output of each run to `log`. Returns a list of (selector, status, message).
    """
    if entry["language"] == "py":
        cmd = [sys.executable, "-I", "-B", entry["suite"], "-v"] + entry["killers"]
        env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1")
        proc = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True, env=env)
        output = proc.stdout + proc.stderr
        log.write(f"$ PYTHONDONTWRITEBYTECODE=1 {' '.join(cmd)}\n{output}\n{RULE}\n")
        if "SyntaxError" in output or "IndentationError" in output:
            return [(k, "NOT-RUN", "module does not parse") for k in entry["killers"]]
        return parse_python(output, entry["killers"])
    results = []
    for selector in entry["killers"]:
        parent, _, sub = selector.partition("/")
        # Subtest names may contain "/", which -run cannot select, so the parent
        # runs whole and parse_go() keeps only the subtests this selector matches.
        cmd = ["go", "test", "-v", "-count=1", "-run", f"^{parent}$", entry["package"]]
        proc = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True)
        output = proc.stdout + proc.stderr
        log.write(f"$ {' '.join(cmd)}\n{output}\n{RULE}\n")
        if "[build failed]" in output or "[setup failed]" in output:
            results.append((selector, "NOT-RUN", "build failed"))
            continue
        status, message = parse_go(output, parent, sub)
        results.append((selector, status, message))
    return results


def mutate_one(entry, log_dir):
    """
    Plant one mutant, run its killers, restore, and verify the restore.
    Returns an Outcome. The file is restored in a finally block whatever happens.
    """
    out = Outcome(entry["id"])
    relpath = entry["file"]
    path = os.path.join(ROOT, relpath)
    out.log = os.path.join(log_dir, f"{entry['id']}.log")
    if not tree_clean(relpath):
        out.problems.append(f"{relpath} has uncommitted changes; refusing to plant over them")
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
            out.killers = run_killers(entry, log)
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
                        f"cmp-equal={same_copy} git-clean={clean}")
            except OSError as exc:
                out.restore_ok = False
                out.problems.append(f"RESTORE FAILED for {relpath}: {exc}")
    failing = [(sel, msg) for sel, st, msg in out.killers if st in ("FAIL", "ERROR")]
    not_run = [sel for sel, st, _ in out.killers if st == "NOT-RUN"]
    if not out.killers:
        out.problems.append("no killers ran")
    elif not_run:
        out.problems.append(f"killer(s) did not run: {', '.join(not_run)}")
    if not_run:
        out.verdict = "INVALID"  # a selector that matched nothing is a broken record, not a kill
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


def main(argv=None):
    """Command-line entry point. Returns the process exit status."""
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    parser.add_argument("--id", action="append", default=[], help="run only this mutant id (repeatable)")
    parser.add_argument("--log-dir", help="directory for the logs (default: a fresh temporary directory)")
    args = parser.parse_args(argv)
    try:
        spec = cm.load_spec()
    except (OSError, ValueError) as exc:
        print(f"mutate: cannot load the spec: {exc}")
        return 2
    items = spec["mutants"]
    spec_errors = cm.schema_errors(items)
    if spec_errors:
        for err in spec_errors:
            print(f"mutate: spec error: {err}")
        return 2
    chosen = cm.active(items)
    if args.id:
        known = {m["id"]: m for m in chosen}
        unknown = [i for i in args.id if i not in known]
        if unknown:
            print(f"mutate: not an active mutant: {', '.join(unknown)}")
            return 2
        chosen = [known[i] for i in args.id]
    log_dir = args.log_dir or tempfile.mkdtemp(prefix="mutate-")
    os.makedirs(log_dir, exist_ok=True)
    if git(["rev-parse", "--git-dir"]).returncode != 0:
        print("mutate: not a git checkout; the clean-tree guard cannot run")
        return 2
    before_status = git(["status", "--porcelain"]).stdout

    print(f"mutate: {len(chosen)} mutant(s); logs in {log_dir}")
    outcomes = []
    for entry in chosen:
        out = mutate_one(entry, log_dir)
        outcomes.append(out)
        for line in describe(out, len(entry["killers"])):
            print(line)
        sys.stdout.flush()

    by_id = {o.id: o for o in outcomes}
    mirror_of = {m["id"]: m["mirror"] for m in chosen}
    for out in outcomes:
        partner = by_id.get(mirror_of.get(out.id))
        if partner is None or out.first_failure is None or partner.first_failure is None:
            continue
        if out.first_failure[1] and out.first_failure[1] == partner.first_failure[1]:
            print(f"SHARED-TEXT: {out.id} and {partner.id} fail with the same message: "
                  f"{out.first_failure[1]}")

    after_status = git(["status", "--porcelain"]).stdout
    tree_ok = before_status == after_status
    if not tree_ok:
        print("mutate: the working tree changed during the run; see git status")

    killed = sum(1 for o in outcomes if o.verdict == "KILLED")
    survived = sum(1 for o in outcomes if o.verdict == "SURVIVED")
    invalid = sum(1 for o in outcomes if o.verdict == "INVALID")
    restore_bad = sum(1 for o in outcomes if not o.restore_ok)
    print(f"mutate: killed {killed}, survived {survived}, invalid {invalid}, "
          f"restore failures {restore_bad}; logs in {log_dir}")
    ok = survived == 0 and invalid == 0 and restore_bad == 0 and tree_ok
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""
Record check for the DIR-017 mutant spec, testdata/mutants.json.

Reads files and runs no tests, so it is fast enough for every `make test` and
every CI run. The checks live in this module, and each error prints its own
message. The README table is checked byte for byte against the rendering of the
spec.

Retired rows must carry a reason, and every other row's id that the reason names
must be an active row (see _reason_errors). Their anchors and mirrors are not
checked, because their targets are gone.

The anchors are read from the working tree. In a clean checkout that is HEAD.

Usage (from the repository root, or any directory):

    python3 -I scripts/check_mutants.py                 check; exit 1 on any error
    python3 -I scripts/check_mutants.py --write-readme  regenerate the README table

scripts/mutate.py imports this module for anchor location and rendering, so the
two cannot disagree about what a mutant's edit is.
"""

import argparse
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SPEC_PATH = os.path.join(ROOT, "testdata", "mutants.json")
README_PATH = os.path.join(ROOT, "testdata", "README.md")
BEGIN = "<!-- mutants:begin -->"
END = "<!-- mutants:end -->"
LANGUAGES = ("py", "go")
STATUSES = ("active", "retired")


class AnchorError(Exception):
    """Raised when a mutant's `before` text is not at its stated location."""


def load_spec(path=SPEC_PATH):
    """Return the parsed spec. Raises ValueError on malformed JSON or a missing list."""
    with open(path, encoding="utf-8") as handle:
        spec = json.load(handle)
    if not isinstance(spec, dict) or not isinstance(spec.get("mutants"), list):
        raise ValueError(f"{path}: top level must be an object with a 'mutants' list")
    return spec


def active(items):
    """The active rows, in spec order."""
    return [m for m in items if isinstance(m, dict) and m.get("status") == "active"]


def _text_fields(row, keys):
    """Names of the keys in `keys` that are missing or not non-empty strings on `row`."""
    return [k for k in keys if not isinstance(row.get(k), str) or not row.get(k)]


def schema_errors(items):
    """
    Structural errors for every row, one string each. An active row is held to
    more than a retired one.
    """
    errors = []
    first_seen = {}
    for index, row in enumerate(items):
        if not isinstance(row, dict):
            errors.append(f"row {index}: not an object")
            continue
        ident = row.get("id")
        if not isinstance(ident, str) or not ident:
            errors.append(f"row {index}: missing id")
            continue
        if ident in first_seen:
            errors.append(f"{ident}: duplicate id (rows {first_seen[ident]} and {index})")
        else:
            first_seen[ident] = index
        if row.get("status") not in STATUSES:
            errors.append(f"{ident}: status must be one of {', '.join(STATUSES)}")
            continue
        if row.get("language") not in LANGUAGES:
            errors.append(f"{ident}: language must be one of {', '.join(LANGUAGES)}")
        missing = _text_fields(row, ("file", "property", "mirror"))
        if missing:
            errors.append(f"{ident}: missing or empty {', '.join(missing)}")
        if row["status"] == "retired":
            if _text_fields(row, ("reason",)):
                errors.append(f"{ident}: a retired row must carry a reason")
            continue
        if not isinstance(row.get("line"), int) or isinstance(row.get("line"), bool) or row["line"] < 1:
            errors.append(f"{ident}: line must be a positive integer")
        if not isinstance(row.get("after"), str):
            errors.append(f"{ident}: after must be a string (empty to delete)")
        missing = _text_fields(row, ("before",))
        if missing:
            errors.append(f"{ident}: before must be a non-empty string")
        if isinstance(row.get("before"), str) and row.get("before") == row.get("after"):
            errors.append(f"{ident}: after equals before, so the mutant changes nothing")
        killers = row.get("killers")
        if not isinstance(killers, list) or not killers or not all(isinstance(k, str) and k for k in killers):
            errors.append(f"{ident}: killers must be a non-empty list of selectors")
        if row.get("language") == "py" and _text_fields(row, ("suite",)):
            errors.append(f"{ident}: a py mutant needs a suite (the unittest file)")
        if row.get("language") == "go" and _text_fields(row, ("package",)):
            errors.append(f"{ident}: a go mutant needs a package")
        if isinstance(row.get("file"), str) and (os.path.isabs(row["file"]) or ".." in row["file"].split("/")):
            errors.append(f"{ident}: file must be a path inside the repository")
        needs_summary = (
            isinstance(row.get("after"), str) and isinstance(row.get("before"), str)
            and (row["after"] == "" or "\n" in row["before"] or "\n" in row["after"])
        )
        if needs_summary and not row.get("summary"):
            errors.append(f"{ident}: an edit that deletes or spans lines needs a summary for the table")
    return errors


def anchor_offsets(text, line, before):
    """Offsets where `before` occurs and begins on the 1-based `line`."""
    hits = []
    start = 0
    while True:
        pos = text.find(before, start)
        if pos < 0:
            return hits
        if text.count("\n", 0, pos) + 1 == line:
            hits.append(pos)
        start = pos + 1


def locate(text, line, before):
    """
    The offset at which `before` must be replaced. Exactly one occurrence must
    begin on `line`. Raises AnchorError with the lines the text was found on.
    """
    hits = anchor_offsets(text, line, before)
    if len(hits) == 1:
        return hits[0]
    if hits:
        raise AnchorError(f"'before' occurs {len(hits)} times starting on line {line}; it must be unique")
    found = []
    start = 0
    while True:
        pos = text.find(before, start)
        if pos < 0:
            break
        found.append(text.count("\n", 0, pos) + 1)
        start = pos + 1
    where = ", ".join(str(n) for n in sorted(set(found))) if found else "nowhere in the file"
    lines = text.split("\n")
    actual = lines[line - 1] if 0 < line <= len(lines) else "(line past end of file)"
    raise AnchorError(
        f"'before' is not at line {line} (found at: {where}); "
        f"line {line} reads: {actual.strip()[:160]!r}"
    )


def _anchor_fields_ok(row):
    """True when `before` is a non-empty string and `line` a positive integer, the inputs locate() needs."""
    line = row.get("line")
    return (isinstance(row.get("before"), str) and row.get("before") != ""
            and isinstance(line, int) and not isinstance(line, bool) and line >= 1)


# A mutant id as a retired row's reason writes it: capitals, digits and hyphens, at least one hyphen.
REASON_ID = re.compile(r"\b[A-Z][A-Z0-9]*(?:-[A-Z0-9]+)+\b")


def _reason_errors(row, by_id):
    """
    Errors for a retired row whose reason names a mutant id (see REASON_ID) that is
    absent or not active, or names another row's id (any form) that is not active.
    A reason that names no id has none.
    """
    reason = row.get("reason")
    if not isinstance(reason, str):
        return []
    named = set(REASON_ID.findall(reason))
    named |= {i for i in by_id if re.search(rf"(?<![\w-]){re.escape(i)}(?![\w-])", reason)}
    named.discard(row["id"])
    errors = []
    for ident in sorted(named):
        target = by_id.get(ident)
        if target is None:
            errors.append(f"{row['id']}: reason names {ident}, which does not exist")
        elif target.get("status") != "active":
            errors.append(f"{row['id']}: reason names {ident}, which is not active")
    return errors


def check_record(items, read_file):
    """
    Full record check. `read_file(relpath)` returns the file text, or None when
    the file is absent. Returns a list of error strings; empty means clean.
    """
    errors = schema_errors(items)
    by_id = {r["id"]: r for r in items if isinstance(r, dict) and isinstance(r.get("id"), str)}
    for row in items:
        if isinstance(row, dict) and row.get("status") == "retired" and isinstance(row.get("id"), str):
            errors.extend(_reason_errors(row, by_id))
        if not isinstance(row, dict) or row.get("status") != "active" or not isinstance(row.get("id"), str):
            continue
        ident = row["id"]
        mirror = row.get("mirror")
        if mirror == ident:
            errors.append(f"{ident}: mirror points at itself")
        else:
            target = by_id.get(mirror)
            if target is None:
                errors.append(f"{ident}: mirror {mirror!r} does not exist")
            elif target.get("status") != "active":
                errors.append(f"{ident}: mirror {mirror} is not active")
            elif target.get("mirror") != ident:
                errors.append(
                    f"{ident}: mirror {mirror} points back to {target.get('mirror')!r}, not {ident}"
                )
        relpath = row.get("file")
        if not isinstance(relpath, str) or os.path.isabs(relpath) or ".." in relpath.split("/"):
            continue  # already reported by schema_errors
        text = read_file(relpath)
        if text is None:
            errors.append(f"{ident}: file {relpath} does not exist")
            continue
        if not _anchor_fields_ok(row):
            continue  # already reported by schema_errors
        try:
            locate(text, row["line"], row["before"])
        except AnchorError as exc:
            errors.append(f"{ident}: {relpath}:{row['line']}: {exc}")
    return errors


def _cell(text):
    """Escape a value for a Markdown table cell."""
    return text.replace("|", "\\|")


def _code(text):
    """
    Wrap text in a Markdown code span. When the text contains backticks, the
    fence is one backtick longer than the longest run inside it, padded with a
    space on each side, so an inner backtick cannot close the span early.
    """
    longest = max((len(run) for run in re.findall(r"`+", text)), default=0)
    if longest == 0:
        return f"`{text}`"
    fence = "`" * (longest + 1)
    return f"{fence} {text} {fence}"


def _edit(row):
    """The Edit cell for an active row: the exact before/after, or the summary for a multi-line or deleting edit."""
    if row.get("summary") and (row["after"] == "" or "\n" in row["before"] or "\n" in row["after"]):
        return row["summary"]
    return f"{_code(row['before'])} becomes {_code(row['after'])}"


def render_block(items):
    """The README block (markers included) rendered from the spec. Deterministic."""
    act = active(items)
    retired = [r for r in items if isinstance(r, dict) and r.get("status") == "retired"]
    lines = [
        BEGIN,
        "",
        "<!-- Generated from testdata/mutants.json by scripts/check_mutants.py --write-readme. Do not edit by hand. -->",
        "",
        "| Mutant | Edit | Property discriminated | Mirror | Runs in | Killers |",
        "|--------|------|------------------------|--------|---------|---------|",
    ]
    for row in act:
        runs_in = row.get("suite") or row.get("package") or ""
        killers = ", ".join(_code(_cell(k)) for k in row["killers"])
        anchor = _code(f"{row['file']}:{row['line']}")
        cells = [
            row["id"],
            f"{anchor} {_cell(_edit(row))}",
            _cell(row["property"]),
            row["mirror"],
            _code(runs_in),
            killers,
        ]
        lines.append("| " + " | ".join(cells) + " |")
    lines += ["", "Retired rows are not applied; their targets are no longer in the tree:", ""]
    lines += ["| Mutant | Last target | Reason | Mirror |", "|--------|-------------|--------|--------|"]
    for row in retired:
        target = _code(row.get("file", ""))
        lines.append(f"| {row['id']} | {target} | {_cell(row.get('reason', ''))} | {row.get('mirror', '')} |")
    lines += ["", END]
    return "\n".join(lines)


def readme_block(readme_text):
    """The current block between the markers, markers included, or None."""
    begin = readme_text.find(BEGIN)
    end = readme_text.find(END)
    if begin < 0 or end < 0 or end < begin:
        return None
    return readme_text[begin:end + len(END)]


def check_readme(readme_text, rendered):
    """Errors when the README block is missing or differs from the rendering."""
    block = readme_block(readme_text)
    if block is None:
        return [f"testdata/README.md has no {BEGIN} ... {END} block"]
    if block != rendered:
        return ["testdata/README.md mutant table differs from testdata/mutants.json; "
                "run: python3 -I scripts/check_mutants.py --write-readme"]
    return []


def _reader(root):
    """A read_file function bound to the repository root. Returns None for an absent file."""
    def read(relpath):
        path = os.path.join(root, relpath)
        if not os.path.isfile(path):
            return None
        with open(path, encoding="utf-8") as handle:
            return handle.read()
    return read


def main(argv=None):
    """Command-line entry point. Returns the process exit status."""
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    parser.add_argument("--write-readme", action="store_true",
                        help="regenerate the mutant table in testdata/README.md from the spec")
    args = parser.parse_args(argv)
    try:
        spec = load_spec()
    except (OSError, ValueError) as exc:
        print(f"FAIL: cannot load {SPEC_PATH}: {exc}")
        return 1
    items = spec["mutants"]
    errors = check_record(items, _reader(ROOT))
    rendered = render_block(items)
    with open(README_PATH, encoding="utf-8") as handle:
        readme_text = handle.read()
    if args.write_readme:
        if errors:
            for err in errors:
                print(f"FAIL: {err}")
            print("FAIL: README not written; fix the spec first")
            return 1
        if readme_block(readme_text) is None:
            print(f"FAIL: {README_PATH} has no {BEGIN} ... {END} block to replace")
            return 1
        begin = readme_text.find(BEGIN)
        end = readme_text.find(END) + len(END)
        with open(README_PATH, "w", encoding="utf-8") as handle:
            handle.write(readme_text[:begin] + rendered + readme_text[end:])
        print(f"README mutant table written from {SPEC_PATH}")
        return 0
    errors += check_readme(readme_text, rendered)
    for err in errors:
        print(f"FAIL: {err}")
    if errors:
        print(f"mutant record check: {len(errors)} error(s)")
        return 1
    retired = sum(1 for r in items if r.get("status") == "retired")
    print(f"mutant record check: OK ({len(active(items))} active, {retired} retired; "
          f"anchors, mirrors and README table verified)")
    return 0


if __name__ == "__main__":
    sys.exit(main())

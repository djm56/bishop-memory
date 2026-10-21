"""
Unit tests for scripts/check_mutants.py: the DIR-017 record check.

Each test calls check_record() or check_readme() directly on an in-memory spec
and an in-memory file map, so the tests never read the real testdata or tree.
The real spec is checked by the command-line run in `make test` and in CI.

One method per edge: a before-text mismatch, a missing mirror, a non-reciprocal
mirror, a duplicate id, a retired row that is skipped, and a clean pass. A
README drift case sits alongside them.
"""

import importlib.util
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
_spec = importlib.util.spec_from_file_location("check_mutants", os.path.join(HERE, "check_mutants.py"))
check = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(check)

SOURCE = "def f():\n    return 1\n    return 2\n"


def row(ident, mirror, before="return 1", after="return 3", line=2, file="f.py", **extra):
    """An active py row against SOURCE, with the given id and mirror."""
    data = {
        "id": ident,
        "status": "active",
        "language": "py",
        "file": file,
        "line": line,
        "before": before,
        "after": after,
        "property": "a property",
        "mirror": mirror,
        "suite": "scripts/test_x.py",
        "killers": ["T.test_x"],
    }
    data.update(extra)
    return data


def reader(files):
    """A read_file function over a dict of relpath to text."""
    return lambda relpath: files.get(relpath)


class RecordCheckTests(unittest.TestCase):
    """check_record() over in-memory specs."""

    def test_before_text_mismatch_is_reported(self):
        # 'return 2' sits on line 3, so a row claiming line 2 must fail.
        items = [row("A", "B", before="return 2", line=2), row("B", "A", before="return 1", line=2)]
        errors = check.check_record(items, reader({"f.py": SOURCE}))
        self.assertTrue(any(e.startswith("A: f.py:2: 'before' is not at line 2") for e in errors), errors)
        self.assertTrue(any("found at: 3" in e for e in errors), errors)

    def test_missing_mirror_is_reported(self):
        items = [row("A", "NOPE")]
        errors = check.check_record(items, reader({"f.py": SOURCE}))
        self.assertEqual(errors, ["A: mirror 'NOPE' does not exist"])

    def test_non_reciprocal_mirror_is_reported(self):
        # A's mirror is B, but B's mirror is C, so the pair does not point back.
        items = [row("A", "B"), row("B", "C", before="return 2", after="return 4", line=3), row("C", "B", before="return 1", line=2)]
        errors = check.check_record(items, reader({"f.py": SOURCE}))
        self.assertIn("A: mirror B points back to 'C', not A", errors)

    def test_duplicate_id_is_reported(self):
        items = [row("A", "B"), row("A", "B", before="return 2", line=3), row("B", "A", before="return 1", line=2)]
        errors = check.check_record(items, reader({"f.py": SOURCE}))
        self.assertTrue(any(e.startswith("A: duplicate id") for e in errors), errors)

    def test_retired_row_is_skipped(self):
        # The retired row names a file that is gone and an anchor that is wrong;
        # neither is checked, so the only record is the reason.
        retired = {
            "id": "OLD",
            "status": "retired",
            "language": "py",
            "file": "gone.py",
            "reason": "its target was removed",
            "property": "a property",
            "mirror": "OLD2",
        }
        items = [retired, row("A", "B"), row("B", "A", before="return 2", line=3)]
        errors = check.check_record(items, reader({"f.py": SOURCE}))
        self.assertEqual(errors, [], errors)

    def test_clean_pass(self):
        items = [row("A", "B"), row("B", "A", before="return 2", line=3)]
        self.assertEqual(check.check_record(items, reader({"f.py": SOURCE})), [])

    def test_readme_drift_is_reported(self):
        # The table is rendered from the spec; a README block that differs from
        # the rendering must be reported, and a missing block must be reported.
        items = [row("A", "B"), row("B", "A", before="return 2", line=3)]
        block = check.render_block(items)
        self.assertEqual(check.check_readme("x\n" + block + "\ny", block), [])
        drifted = block.replace("return 3", "return 9")
        self.assertTrue(check.check_readme("x\n" + drifted + "\ny", block))
        self.assertTrue(check.check_readme("no block here", block))


if __name__ == "__main__":
    unittest.main()

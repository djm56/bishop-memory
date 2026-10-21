"""
Unit tests for scripts/check_mutants.py: the DIR-017 record check.

Each test calls one entry point of check_mutants, named in its class docstring,
on an in-memory spec and an in-memory file map, or on a temporary directory. The tests never read the real
testdata or tree. The real spec is checked by the command-line run in `make
test` and in CI.

Each test name says the edge it covers.
"""

import contextlib
import importlib.util
import io
import os
import shutil
import sys
import tempfile
import unittest
from unittest import mock

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


def retired(ident, mirror, **extra):
    """A retired row with a reason, for the edges that need an inactive partner."""
    data = {
        "id": ident,
        "status": "retired",
        "language": "py",
        "file": "gone.py",
        "reason": "its target was removed",
        "property": "a property",
        "mirror": mirror,
    }
    data.update(extra)
    return data


def reader(files):
    """A read_file function over a dict of relpath to text."""
    return lambda relpath: files.get(relpath)


def pair():
    """A clean two-row record: A and its mirror B, both anchored in SOURCE."""
    return [row("A", "B"), row("B", "A", before="return 2", after="return 4", line=3)]


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
        retired_row = {
            "id": "OLD",
            "status": "retired",
            "language": "py",
            "file": "gone.py",
            "reason": "its target was removed",
            "property": "a property",
            "mirror": "OLD2",
        }
        items = [retired_row, row("A", "B"), row("B", "A", before="return 2", line=3)]
        errors = check.check_record(items, reader({"f.py": SOURCE}))
        self.assertEqual(errors, [], errors)

    def test_retired_reason_naming_active_ids_passes(self):
        items = [row("A", "B-TWO"), row("B-TWO", "A", before="return 2", line=3),
                 retired("OLD", "OLD2", reason="covered by A and by B-TWO")]
        self.assertEqual(check.check_record(items, reader({"f.py": SOURCE})), [])

    def test_retired_reason_naming_a_missing_hyphenated_id_is_reported(self):
        items = [row("A", "B"), row("B", "A", before="return 2", line=3),
                 retired("OLD", "OLD2", reason="covered by GONE-ID")]
        errors = check.check_record(items, reader({"f.py": SOURCE}))
        self.assertEqual(errors, ["OLD: reason names GONE-ID, which does not exist"])

    def test_retired_reason_naming_a_retired_id_is_reported(self):
        items = [row("A", "B"), row("B", "A", before="return 2", line=3),
                 retired("OLD", "OLD2", reason="covered by OLD2"), retired("OLD2", "OLD")]
        errors = check.check_record(items, reader({"f.py": SOURCE}))
        self.assertEqual(errors, ["OLD: reason names OLD2, which is not active"])

    def test_retired_reason_naming_its_own_id_is_not_reported(self):
        items = [row("A", "B"), row("B", "A", before="return 2", line=3),
                 retired("OLD-ONE", "A", reason="OLD-ONE was replaced by A")]
        self.assertEqual(check.check_record(items, reader({"f.py": SOURCE})), [])

    def test_clean_pass(self):
        items = [row("A", "B"), row("B", "A", before="return 2", line=3)]
        self.assertEqual(check.check_record(items, reader({"f.py": SOURCE})), [])

    def test_file_absent_is_reported(self):
        # reader() returns None for a file it does not hold, as _reader() does for a missing path.
        errors = check.check_record(pair(), reader({}))
        self.assertIn("A: file f.py does not exist", errors)

    def test_self_mirror_is_reported(self):
        items = [row("A", "A")]
        errors = check.check_record(items, reader({"f.py": SOURCE}))
        self.assertEqual(errors, ["A: mirror points at itself"])

    def test_inactive_mirror_is_reported(self):
        # B is retired, so A's mirror exists but is not an applied row.
        items = [row("A", "B"), retired("B", "A")]
        errors = check.check_record(items, reader({"f.py": SOURCE}))
        self.assertEqual(errors, ["A: mirror B is not active"])

    def test_empty_before_is_not_anchored(self):
        # An empty `before` is a schema error; the anchor check must skip it, not loop on it.
        items = [row("A", "B", before="", after="x"), row("B", "A", before="return 2", line=3)]
        errors = check.check_record(items, reader({"f.py": SOURCE}))
        self.assertIn("A: before must be a non-empty string", errors)
        self.assertFalse(any(e.startswith("A: f.py:") for e in errors), errors)

    def test_boolean_line_is_not_anchored(self):
        # True equals 1 in Python; a boolean line must be reported as a schema error, not located.
        items = [row("A", "B", line=True), row("B", "A", before="return 2", line=3)]
        errors = check.check_record(items, reader({"f.py": SOURCE}))
        self.assertIn("A: line must be a positive integer", errors)
        self.assertFalse(any(e.startswith("A: f.py:") for e in errors), errors)


class AnchorTests(unittest.TestCase):
    """locate() and the anchor rule: one occurrence that begins on the stated line."""

    def test_locate_returns_offset_of_the_line_occurrence(self):
        # The second 'return 1' begins on line 2; its offset is after 'return 1\n'.
        self.assertEqual(check.locate("return 1\nreturn 1\n", 2, "return 1"), len("return 1\n"))

    def test_copy_elsewhere_in_file_is_allowed(self):
        # A copy of the text on another line does not make the anchor ambiguous.
        items = [row("A", "B", before="return 1", line=2), row("B", "A", before="return 2", line=3)]
        text = "def f():\n    return 1\n    return 2\nreturn 1\n"
        self.assertEqual(check.check_record(items, reader({"f.py": text})), [])

    def test_two_occurrences_on_the_stated_line_are_reported(self):
        # 'x' begins twice on line 1, so the anchor is ambiguous and must be refused.
        with self.assertRaises(check.AnchorError) as ctx:
            check.locate("x x\n", 1, "x")
        self.assertIn("'before' occurs 2 times starting on line 1; it must be unique", str(ctx.exception))

    def test_text_absent_everywhere_names_nowhere(self):
        with self.assertRaises(check.AnchorError) as ctx:
            check.locate(SOURCE, 2, "no such text")
        self.assertIn("(found at: nowhere in the file)", str(ctx.exception))

    def test_line_past_end_of_file_is_reported(self):
        with self.assertRaises(check.AnchorError) as ctx:
            check.locate(SOURCE, 99, "return 1")
        self.assertIn("(line past end of file)", str(ctx.exception))


class SchemaTests(unittest.TestCase):
    """schema_errors() over single rows: one check per edge."""

    def test_after_equal_to_before_is_reported(self):
        errors = check.schema_errors([row("A", "B", before="return 1", after="return 1")])
        self.assertIn("A: after equals before, so the mutant changes nothing", errors)

    def test_empty_killers_are_reported(self):
        errors = check.schema_errors([row("A", "B", killers=[])])
        self.assertIn("A: killers must be a non-empty list of selectors", errors)

    def test_parent_directory_path_is_reported(self):
        errors = check.schema_errors([row("A", "B", file="../outside.py")])
        self.assertIn("A: file must be a path inside the repository", errors)

    def test_absolute_path_is_reported(self):
        errors = check.schema_errors([row("A", "B", file="/etc/outside.py")])
        self.assertIn("A: file must be a path inside the repository", errors)

    def test_multiline_edit_without_summary_is_reported(self):
        errors = check.schema_errors([row("A", "B", before="return 1\n    return 2")])
        self.assertIn("A: an edit that deletes or spans lines needs a summary for the table", errors)

    def test_deleting_edit_without_summary_is_reported(self):
        errors = check.schema_errors([row("A", "B", after="")])
        self.assertIn("A: an edit that deletes or spans lines needs a summary for the table", errors)

    def test_deleting_edit_with_summary_is_accepted(self):
        errors = check.schema_errors([row("A", "B", after="", summary="the return is removed")])
        self.assertEqual(errors, [])

    def test_retired_row_without_reason_is_reported(self):
        bare = retired("OLD", "OLD2")
        del bare["reason"]
        errors = check.schema_errors([bare])
        self.assertIn("OLD: a retired row must carry a reason", errors)

    def test_unknown_status_is_reported(self):
        errors = check.schema_errors([row("A", "B", status="maybe")])
        self.assertIn("A: status must be one of active, retired", errors)

    def test_unknown_language_is_reported(self):
        errors = check.schema_errors([row("A", "B", language="rs")])
        self.assertIn("A: language must be one of py, go", errors)

    def test_zero_line_is_reported(self):
        errors = check.schema_errors([row("A", "B", line=0)])
        self.assertIn("A: line must be a positive integer", errors)

    def test_missing_id_is_reported(self):
        bare = row("A", "B")
        del bare["id"]
        errors = check.schema_errors([bare])
        self.assertEqual(errors, ["row 0: missing id"])


class RenderTests(unittest.TestCase):
    """render_block() and the escaping of table cells and code spans."""

    def test_render_is_deterministic(self):
        items = pair()
        self.assertEqual(check.render_block(items), check.render_block(items))

    def test_render_is_bracketed_by_the_markers(self):
        block = check.render_block(pair())
        self.assertTrue(block.startswith(check.BEGIN), block[:60])
        self.assertTrue(block.endswith(check.END), block[-60:])

    def test_render_keeps_spec_order(self):
        block = check.render_block([row("A", "B"), row("B", "A", before="return 2", line=3)])
        self.assertLess(block.index("\n| A |"), block.index("\n| B |"))

    def test_pipe_is_escaped_in_cells(self):
        items = [row("A", "B", before="a|b", after="c|d", property="p|q", killers=["T.x|y"]),
                 row("B", "A", before="return 2", line=3)]
        block = check.render_block(items)
        self.assertIn("`a\\|b` becomes `c\\|d`", block)
        self.assertIn("p\\|q", block)
        self.assertIn("`T.x\\|y`", block)

    def test_backtick_gets_a_longer_fence(self):
        # A backtick inside the edit must not close the code span early.
        items = [row("A", "B", before="a`b", after="c"), row("B", "A", before="return 2", line=3)]
        block = check.render_block(items)
        self.assertIn("`` a`b `` becomes `c`", block)

    def test_plain_text_gets_a_single_fence(self):
        self.assertEqual(check._code("x"), "`x`")

    def test_retired_rows_render_after_the_active_table(self):
        items = pair() + [retired("OLD", "OLD2")]
        block = check.render_block(items)
        self.assertLess(block.index("\n| A |"), block.index("Retired rows are not applied"))
        self.assertIn("| OLD | `gone.py` | its target was removed | OLD2 |", block)


class ReadmeTests(unittest.TestCase):
    """check_readme() over a README text and a rendered block."""

    def test_readme_drift_is_reported(self):
        # The table is rendered from the spec; a README block that differs from
        # the rendering must be reported, and a missing block must be reported.
        items = pair()
        block = check.render_block(items)
        self.assertEqual(check.check_readme("x\n" + block + "\ny", block), [])
        drifted = block.replace("return 3", "return 9")
        self.assertTrue(check.check_readme("x\n" + drifted + "\ny", block))
        self.assertTrue(check.check_readme("no block here", block))


class WriteReadmeTests(unittest.TestCase):
    """main(): the check mode, and --write-readme writing only when the record is clean."""

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="check-mutants-test-")
        with open(os.path.join(self.tmp, "f.py"), "w", encoding="utf-8") as handle:
            handle.write(SOURCE)
        self.readme = os.path.join(self.tmp, "README.md")
        self.prose = "# Notes\n\n"

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def _run(self, items, argv):
        """Run check.main() with the spec, root and README redirected into the temporary directory."""
        with open(self.readme, "w", encoding="utf-8") as handle:
            handle.write(self.prose + check.BEGIN + "\n" + check.END + "\n")
        out = io.StringIO()
        with mock.patch.object(check, "load_spec", return_value={"mutants": items}), \
                mock.patch.object(check, "ROOT", self.tmp), \
                mock.patch.object(check, "README_PATH", self.readme), \
                contextlib.redirect_stdout(out):
            status = check.main(argv)
        return status, out.getvalue()

    def _readme(self):
        with open(self.readme, encoding="utf-8") as handle:
            return handle.read()

    def test_write_readme_writes_when_clean(self):
        status, _ = self._run(pair(), ["--write-readme"])
        self.assertEqual(status, 0)
        self.assertEqual(self._readme(), self.prose + check.render_block(pair()) + "\n")

    def test_write_readme_refuses_when_record_is_dirty(self):
        dirty = pair()
        dirty[0]["mirror"] = "NOPE"
        status, out = self._run(dirty, ["--write-readme"])
        self.assertEqual(status, 1)
        self.assertIn("README not written; fix the spec first", out)
        self.assertEqual(self._readme(), self.prose + check.BEGIN + "\n" + check.END + "\n")

    def test_check_mode_reports_readme_drift(self):
        # The README holds an empty block, so the rendered table differs and the check fails.
        status, out = self._run(pair(), [])
        self.assertEqual(status, 1)
        self.assertIn("mutant table differs from testdata/mutants.json", out)

    def test_check_mode_passes_when_readme_matches(self):
        with open(self.readme, "w", encoding="utf-8") as handle:
            handle.write(self.prose + check.render_block(pair()) + "\n")
        out = io.StringIO()
        with mock.patch.object(check, "load_spec", return_value={"mutants": pair()}), \
                mock.patch.object(check, "ROOT", self.tmp), \
                mock.patch.object(check, "README_PATH", self.readme), \
                contextlib.redirect_stdout(out):
            status = check.main([])
        self.assertEqual(status, 0, out.getvalue())


if __name__ == "__main__":
    unittest.main()

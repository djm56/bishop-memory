# Golden files

These files are the gate in `docs/plans/HARNESS-PORTABILITY-PLAN.md` section 8.
A change to the importer or the reconciler merges only when its output matches
the golden, or the difference is an intended fix that ships its own test.

| File | Written by | Checked by |
|------|------------|------------|
| `importer-state.golden` | `SyncHarnessStats` on `testdata/memory`, dumped table by table | `internal/importer/golden_test.go`, `TestGoldenImportState` |
| `reconcile-dry-run.golden` | `scripts/reconcile-memory.py --dry-run` against a fake service | `scripts/test_reconcile_golden.py`, `ReconcileDryRunGolden` |

## Regenerating a golden

An intended change is recorded with update mode:

    UPDATE_GOLDEN=1 go test ./internal/importer -run TestGoldenImportState -count=1
    UPDATE_GOLDEN=1 python3 scripts/test_reconcile_golden.py

Update mode writes the file and then fails the test on purpose. Run the same
command again without `UPDATE_GOLDEN` to confirm. CI sets `UPDATE_GOLDEN` to the
empty string, and a final CI step fails the job if `git diff --exit-code --
testdata/golden` shows any change.

## What the goldens cannot see

- **Request payloads.** The reconciler golden records the method and path of
  each request the fake receives, not the body. A write's payload is not
  compared.
- **Nested mission files.** The fixture has one mission folder with flat files.
  A golden cannot kill a mutant that only changes behaviour for a deeper path.

## Mutant record (DIR-017)

The mutants below were planted at step 4 against the current code, one run each,
then reverted and checked against `git status`. Each row names the property the
mutant changes and its mirror, the opposite edge.

| Mutant | Edit | Property discriminated | Mirror | Killed by |
|--------|------|------------------------|--------|-----------|
| M1 | `internal/importer/importer.go` `kindFromPath`: `case "PROGRESS.md":` becomes `case "PROGRES.md":` | A mission's `PROGRESS.md` is classified as `progress` | M1-mirror | `TestGoldenImportState` and `TestKindFromPath` |
| M1-mirror | `internal/importer/importer.go` `kindFromPath`: `len(parts) == 3` for `missions` becomes `len(parts) >= 3` | A file nested deeper than `missions/<id>/` is not mission metadata | (this is the mirror of M1) | `TestKindFromPath` only. `TestGoldenImportState` still passes. |
| M2 | `scripts/reconcile-memory.py` `parse_steps`: `"PROGRESS.md"` becomes `"PROGRES.md"` | The reconciler reads each mission's step table | not planted | `test_dry_run_matches_golden` |

M1-mirror survives the golden. That is the gap named under "What the goldens
cannot see", and the unit test carries that edge. The reconciler mirror was not
planted. The mutant set from step 2 is not in the repository or the workspace,
so it is not reproduced here.

## Mutating a layout value (seam check)

The layout values read by the importer and the reconciler are declared once,
at the seam. Changing one of them must fail the goldens. Procedure and results
are recorded with the seam commit.

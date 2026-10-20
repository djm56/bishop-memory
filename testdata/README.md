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
empty string, so update mode never runs in CI. The tests fail on any mismatch
against the committed golden.

## What the goldens cannot see

- **Request payloads.** The reconciler golden records the method and path of
  each request the fake receives, not the body. A write's payload is not
  compared.
- **Nested mission files.** The fixture has one mission folder with flat files.
  A golden cannot kill a mutant that only changes behaviour for a deeper path.
- **Values the goldens do not read.** The goldens do not pin: `AgentsDir`
  (`internal/profile/profile.go:51`, read by `AgentsDirFor` in
  `internal/importer/agents.go:19`, which the Go golden does not call), the
  approver default `APPROVER_DEFAULT` in `scripts/reconcile-memory.py:102`
  (used at `:1072`), `DefaultHarness` (`internal/profile/profile.go:61`, read
  by `cmd/mcpd/main.go:98`), and `ReviewerRole` (`internal/profile/profile.go:60`,
  read by `internal/api/crew.go:206` and `internal/api/hud.go:193`). These are
  not on the paths the two golden tests exercise, as read from their call
  sites. None of them was mutation-tested in this record.

## Mutant record (DIR-017)

Planted at step 5a against commit `29d963b`, one run per killer, then reverted.
Each reverted file was checked with `cmp` against a copy saved before planting.
Every mutant names the property it discriminates and its mirror, the opposite
edge. The failure text is the output observed on that run.

| Mutant | Edit (against current code) | Property discriminated | Mirror | Observed failure |
|--------|-----------------------------|------------------------|--------|------------------|
| M1 | `internal/importer/importer.go` `kindFromPath`: `case layout.Progress:` becomes `case "PROGRES.md":` | A mission's `PROGRESS.md` is classified as `progress` | M1-mirror | `TestGoldenImportState`: `golden_test.go:114`, first difference at line 80, `want: kind = "progress"`, `got: kind = "missions"`. `TestKindFromPath/missions/mission-20260904-01/PROGRESS.md`: `importer_test.go:244`, `kindFromPath("missions/mission-20260904-01/PROGRESS.md") = "missions", want "progress"`. Only that subtest fails. |
| M1-mirror | `internal/importer/importer.go` `kindFromPath`: `if len(parts) == 3 && parts[0] == layout.MissionsDir` becomes `len(parts) >= 3 && ...` | A file nested deeper than `missions/<id>/` is not mission metadata | M1 (the depth edge) | `TestGoldenImportState`: passes. `TestKindFromPath/missions/mission-20260904-01/sub/BRIEF.md`: `importer_test.go:244`, `kindFromPath("missions/mission-20260904-01/sub/BRIEF.md") = "brief", want "missions"`. Only that subtest fails. |
| M2 | `scripts/reconcile-memory.py` `parse_steps`: `BISHOP_LAYOUT.progress)` becomes `"PROGRES.md")` | The reconciler reads each mission's step table | M2-mirror | `test_dry_run_matches_golden`: `[steps] 0 parsed, 0 created, 0 updated` where the golden has `2 parsed, 1 created, 1 updated`; `would send 9 requests` where the golden has `11`. |
| M2-mirror | `scripts/reconcile-memory.py` `parse_steps`: `if not os.path.isfile(path):` becomes `if os.path.isfile(path):` | A present `PROGRESS.md` yields its steps (the file-present branch) | M2 (the opposite branch) | `test_dry_run_matches_golden`: the same diff as M2. The mirror is not an independent kill; both hit the same step-table path. |
| SEAM-GO | `internal/profile/profile.go` `Bishop.Layout.MissionsDir`: `"missions"` becomes `"missionz"` | The importer reads the mission folder by its declared name | SEAM-GO-MIRROR | `TestGoldenImportState`: first difference at line 4 (`# stats:`), `got: # stats: {CriteriaSkipped:0 ...}`. `TestKindFromPath`: subtests `BRIEF.md`, `PROGRESS.md`, `DEBRIEF.md` each fail with `kindFromPath(...) = "missions"`. |
| SEAM-GO-MIRROR | `internal/importer/importer.go` `kindFromPath`: `parts[0] == layout.MissionsDir` becomes `parts[0] != layout.MissionsDir` | The missions branch is taken for the declared folder and only that folder (the opposite branch) | SEAM-GO (the same guard, inverted) | `TestGoldenImportState`: first difference at line 4, the same text as SEAM-GO. `TestKindFromPath`: the same three subtests as SEAM-GO. Not an independent kill. |
| SEAM-PY | `scripts/reconcile-memory.py` `Layout.missions_dir`: `"missions"` becomes `"missionz"` | The reconciler reads each mission's folder by its declared name | M2-mirror (shared with M2) | `test_dry_run_matches_golden`: the same diff as M2, `[steps] 0 parsed`, `would send 9 requests`. |

M1-mirror passes the golden. That is the gap named under "What the goldens
cannot see", and the unit test carries that edge.

## Layout values at the seam

The directory and file names the importer and the reconciler read are declared
once. The Go values are `profile.Bishop.Layout` in `internal/profile/profile.go`.
The Python values are `BISHOP_LAYOUT` in `scripts/reconcile-memory.py`.

Only the missions folder name has been checked against the goldens. Changing it
to `missionz` in each language fails the golden in that language (SEAM-GO and
SEAM-PY above). No other layout value is claimed as pinned; see "What the
goldens cannot see" for the values the goldens do not read.

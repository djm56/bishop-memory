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
- **Nested mission files.** The fixture's mission folder holds flat files.
  A golden cannot kill a mutant that only changes behaviour for a deeper path.
- **Values the goldens do not read.** The goldens do not pin: `AgentsDir`
  (`internal/profile/profile.go:51`, read by `AgentsDirFor` in
  `internal/importer/agents.go:19`, which the Go golden does not call), the
  approver default `APPROVER_DEFAULT` in `scripts/reconcile-memory.py:102`
  (used at `:1072`), `DefaultHarness` (`internal/profile/profile.go:61`, read
  by `cmd/mcpd/main.go:98`), and `ReviewerRole` (`internal/profile/profile.go:60`,
  read by `internal/api/crew.go:206` and `internal/api/hud.go:193`). These are
  not on the paths the golden tests exercise, as read from their call
  sites. `APPROVER_DEFAULT` is now pinned by `ApproverDefaultTests` in
  `scripts/test_reconcile_units.py` and is mutation-tested below. `AgentsDir`,
  `DefaultHarness` and `ReviewerRole` are not, and are still unpinned.

## Mutant record (DIR-017)

Planted against commit `7438933`. Production files are unchanged through
`718cb7a`; the commits after `7438933` change only the tests and this record.
Each mutant was planted alone, its killers run, and the file reverted.
Each reverted file was checked with `cmp` against a copy saved before planting.
The failure text is the output observed on that run. Python edges are one test
method each; Go edges are one subtest each.

The **Last run** column gives the step at which each row was last run. `5e`
means the row was re-run at step 5e. `5c` means the row was last run at step 5c
and not re-run at 5e. M1-mirror's text was corrected at 5e; that correction is
an edit, not a run, so its last run is still `5c`.

Every mutant names the property it discriminates and its mirror, the opposite
edge. A mirror is killed by a test whose failure text differs from its
partner's. The kill lines of each mutant and its mirror are disjoint.

| Mutant | Edit (against current code) | Property discriminated | Mirror | Observed failure | Last run |
|--------|-----------------------------|------------------------|--------|------------------|----------|
| M1 | `internal/importer/importer.go:724` `kindFromPath`: `case layout.Progress:` becomes `case "PROGRES.md":` | A mission's `PROGRESS.md` is classified as `progress` | M1-mirror | `TestKindFromPath/missions/mission-20260904-01/PROGRESS.md`: `importer_test.go:244`, `kindFromPath("missions/mission-20260904-01/PROGRESS.md") = "missions", want "progress"`. Also `TestGoldenImportState`: `golden_test.go:114`, first difference at line 80. | 5c |
| M1-mirror | `internal/importer/importer.go:720` `kindFromPath`: `if len(parts) == 3 && parts[0] == layout.MissionsDir` becomes `if len(parts) >= 3 && parts[0] == layout.MissionsDir {` | A file nested deeper than `missions/<id>/` is not mission metadata | M1 (the depth edge) | `TestKindFromPath/missions/mission-20260904-01/sub/BRIEF.md`: `importer_test.go:244`, `kindFromPath("missions/mission-20260904-01/sub/BRIEF.md") = "brief", want "missions"`. `TestGoldenImportState` passes. | 5c |
| M2 | `scripts/reconcile-memory.py:547` `parse_steps`: `BISHOP_LAYOUT.progress)` becomes `"PROGRES.md")` | A present `PROGRESS.md` yields its steps (the file-present branch) | M2-mirror | `StepTableTests.test_present_progress_yields_steps`: `AssertionError: Lists differ: [] != ['1', '2'] : file present yields steps: expected steps 1 and 2 from PROGRESS.md`. `MissionsFolderLookupTests.test_declared_missions_folder_is_read`: `AssertionError: 0 != 2 : declared missions folder is read: expected 2 steps under missions/`. `test_dry_run_matches_golden`: `[steps] 0 parsed`, `would send 9 requests` where the golden has `11`. | 5c |
| M2-mirror | `scripts/reconcile-memory.py:549` `parse_steps`: the file-absent branch returns a sentinel step instead of an empty list: `        return []` becomes `        return [{"step": "0"}]` | An absent `PROGRESS.md` yields no steps (the file-absent branch) | M2 (the opposite branch) | `StepTableTests.test_absent_progress_yields_none`: `AssertionError: Lists differ: [{'step': '0'}] != [] : absent file yields none: parse_steps returned steps with no PROGRESS.md`. `MissionsFolderLookupTests.test_undeclared_folder_name_is_not_read`: `AssertionError: Lists differ: [{'step': '0'}] != [] : undeclared folder notes is not read: parse_steps read notes/ as the mission folder`. `test_dry_run_matches_golden` passes (the golden fixture's file is present). | 5e |
| SEAM-GO | `internal/profile/profile.go:47` `Bishop.Layout.MissionsDir`: `"missions"` becomes `"missionz"` | The importer reads the mission folder by its declared name | SEAM-GO-MIRROR | `TestKindFromPathMissionsGuard/declared_missions_folder_is_read_as_mission_metadata`: `importer_test.go:260`, `declared missions folder not read as mission metadata: kindFromPath("missions/mission-20260904-01/BRIEF.md") = "missions", want "brief"`. `TestSyncMissionFolderLookup/declared_missions_folder_is_imported`: `importer_test.go:306`, `declared missions folder not imported: 0 documents for missions/mission-20260904-01/BRIEF.md, want 1`. Also `TestKindFromPath` subtests `BRIEF.md`, `PROGRESS.md`, `DEBRIEF.md` (`importer_test.go:244`, `= "missions"`), and `TestGoldenImportState` (`golden_test.go:114`, first difference at line 4). | 5c |
| SEAM-GO-MIRROR | `internal/importer/importer.go:720` `kindFromPath`: `parts[0] == layout.MissionsDir` is dropped, so any folder at depth three is mission metadata: `if len(parts) == 3 && parts[0] == layout.MissionsDir {` becomes `if len(parts) == 3 {` | An undeclared folder name is not mission metadata (the opposite edge) | SEAM-GO (the declared edge) | `TestKindFromPathMissionsGuard/undeclared_folder_name_is_not_read_as_mission_metadata`: `importer_test.go:267`, `undeclared folder name read as mission metadata: kindFromPath("notes/mission-20260904-01/BRIEF.md") = "brief", want "notes"`. `TestKindFromPath` and `TestGoldenImportState` pass. | 5c |
| SEAM-GO-LOOKUP | `internal/importer/importer.go:255` `SyncMissionStats`: `profile.Bishop.Layout.MissionsDir, missionID, name)` becomes `"missionz", missionID, name)` | The missions folder lookup reads the declared folder | SEAM-GO-LOOKUP-MIRROR | `TestSyncMissionFolderLookup/declared_missions_folder_is_imported`: `importer_test.go:306`, `declared missions folder not imported: 0 documents for missions/mission-20260904-01/BRIEF.md, want 1`. Same line as SEAM-GO's lookup kill. Discriminating pattern: SEAM-GO-LOOKUP leaves `kindFromPath` alone, so `TestKindFromPathMissionsGuard/declared_missions_folder_is_read_as_mission_metadata` (`importer_test.go:260`), `TestKindFromPath` and `TestGoldenImportState` stay green; SEAM-GO fails all of them. | 5e |
| SEAM-GO-LOOKUP-MIRROR | `internal/importer/importer.go:255` `SyncMissionStats`: after the declared path, a glob over `*/<missionID>/<name>` replaces it when a match exists, so any folder name is read | An undeclared folder name is not read by the lookup (the opposite edge) | SEAM-GO-LOOKUP (the declared edge) | `TestSyncMissionFolderLookup/undeclared_folder_name_is_not_imported`: `importer_test.go:312`, `undeclared folder name imported: 1 documents for notes/mission-20260904-01/BRIEF.md, want 0`. The declared subtest passes. | 5c |
| SEAM-PY | `scripts/reconcile-memory.py:82` `Layout.missions_dir`: `"missions"` becomes `"missionz"` | The reconciler reads each mission's folder by its declared name | SEAM-PY-MIRROR | `MissionsFolderLookupTests.test_declared_missions_folder_is_read`: `AssertionError: 0 != 2 : declared missions folder is read: expected 2 steps under missions/`. `test_dry_run_matches_golden`: `[steps] 0 parsed`, `would send 9 requests` where the golden has `11`. Discriminating pattern: `StepTableTests.test_present_progress_yields_steps` stays green, because it writes into the declared folder; M2 fails it. | 5e |
| SEAM-PY-MIRROR | `scripts/reconcile-memory.py:547` `parse_steps`: the path is found by a glob over `*/<id>/PROGRESS.md` before the declared path is used, so any folder name is read | An undeclared folder name is not read by the reconciler (the opposite edge) | SEAM-PY (the declared edge) | `MissionsFolderLookupTests.test_undeclared_folder_name_is_not_read`: `AssertionError: Lists differ: [{'step': '1', ...}, {'step': '2', ...}] != [] : undeclared folder notes is not read: parse_steps read notes/ as the mission folder`. `test_dry_run_matches_golden` passes (the golden fixture's folder is the declared one). | 5c |
| APPROVER-MUT | `scripts/reconcile-memory.py:1072` `reconcile_findings`: `finding["_approver"] or APPROVER_DEFAULT` becomes `finding["_approver"] or ""` | An empty Approver in the Markdown falls back to `APPROVER_DEFAULT` | APPROVER-MIRROR | `ApproverDefaultTests.test_empty_approver_falls_back_to_default`: `AssertionError: '' != 'FINDINGS.md' : empty Approver: decision did not fall back to APPROVER_DEFAULT`. `test_explicit_approver_is_carried` stays green. | 5e |
| APPROVER-MIRROR | `scripts/reconcile-memory.py:1072` `reconcile_findings`: `finding["_approver"] or APPROVER_DEFAULT` becomes `APPROVER_DEFAULT` | An explicit Approver in the Markdown is carried, not replaced (the opposite edge) | APPROVER-MUT (the default edge) | `ApproverDefaultTests.test_explicit_approver_is_carried`: `AssertionError: 'FINDINGS.md' != 'Donovan' : explicit Approver: decision did not carry the Markdown value`. `test_empty_approver_falls_back_to_default` stays green. | 5e |

Notes on the record.

- **M2-mirror changed.** The previous mirror inverted the `isfile` test. That
  returns `[]` for a present file, the same output M2 gives, so the present-file
  test could not tell the two apart. The mirror now sits on the file-absent
  branch instead. This was reasoned from `parse_steps` and not run as the
  inversion; the sentinel mutant is the one that was run.
- **Shared kill lines.** Some mutants share a failing line, because they
  break the same read. SEAM-GO and SEAM-GO-LOOKUP share
  `importer_test.go:306`. M2 and SEAM-PY share the `MissionsFolderLookupTests`
  declared-folder failure. Neither pair is a mutant and its own mirror. Each pair is told apart
  by a sibling test that stays green: `importer_test.go:260` stays green under
  SEAM-GO-LOOKUP and fails under SEAM-GO; `test_present_progress_yields_steps`
  stays green under SEAM-PY and fails under M2.

M1-mirror and SEAM-PY-MIRROR pass the golden. That is the gap named under "What
the goldens cannot see". The unit tests carry those edges.

## Layout values at the seam

The directory and file names the importer and the reconciler read are declared
once. The Go values are `profile.Bishop.Layout` in `internal/profile/profile.go`.
The Python values are `BISHOP_LAYOUT` in `scripts/reconcile-memory.py`.

Only the missions folder name has been checked, against the goldens and the
unit tests above. Changing it to `missionz` in each language fails the golden
and the lookup tests in that language (SEAM-GO and SEAM-PY above). No other
layout value is claimed as pinned; see "What the goldens cannot see" for the
values the goldens do not read.

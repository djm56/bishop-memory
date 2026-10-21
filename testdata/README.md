# Golden files

These files are the gate in `docs/plans/HARNESS-PORTABILITY-PLAN.md` section 8.
A change to the importer or the reconciler merges only when its output matches
the golden, or the difference is an intended fix that ships its own test.

| File | Written by | Checked by |
|------|------------|------------|
| `importer-state.golden` | `SyncHarnessStats` on `testdata/memory`, dumped table by table | `internal/importer/golden_test.go`, `TestGoldenImportState` |
| `reconcile-dry-run.golden` | `scripts/reconcile-memory.py --dry-run` against a fake service | `scripts/test_reconcile_golden.py`, `ReconcileDryRunGolden` |

## Fixture cases

The shipped fixture carries the inputs for the reconciler's mission-ID and
step-status paths. Each is written in the Markdown shape Bishop writes. The
files are the source; this section does not list them.

- `findings/FINDINGS.md`: each fixture entry has a `**Mission**` field. Its
  Rationale says what the entry exercises.
- `state/MISSION-ARCHIVE.md`: the rows are the known mission ids the
  `**Mission**` fields are checked against. An id with no row here, and no
  folder under `missions/`, is not known.
- `missions/mission-20260904-01/PROGRESS.md`: a step row carries a status
  outside the step set.

Only existing files carry these cases. No file was added, because the
importer test asserts the exact document count. The importer golden embeds the
body of `findings/FINDINGS.md`, so an edit to that file changes it.

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
  (used at `:1146`), `DefaultHarness` (`internal/profile/profile.go:61`, read
  by `cmd/mcpd/main.go:98`), and `ReviewerRole` (`internal/profile/profile.go:60`,
  read by `internal/api/crew.go:206` and `internal/api/hud.go:193`). These are
  not on the paths the golden tests exercise, as read from their call
  sites. `APPROVER_DEFAULT` is now pinned by `ApproverDefaultTests` in
  `scripts/test_reconcile_units.py` and is mutation-tested below. `AgentsDir`,
  `DefaultHarness` and `ReviewerRole` are not, and are still unpinned.
- **Finding links and step status values.** The dry-run labels a finding by
  its date and target, and a step by its number, so a wrong `**Mission**` link
  does not appear in the golden. A finding or step that is warned appears as a
  WARN line on stderr and in the summary's "warned" count, so a warning that
  stops firing is visible; the value a step status was rejected for is in the
  WARN line. A dropped step row does appear, because its line is gone. The
  links are pinned by `FindingMissionLinkTests` and `FindingWarningTests`, and
  the step statuses by `StepStatusTests` and `StepWarningTests`, all in
  `scripts/test_reconcile_units.py`, which read the shipped fixture.
- **Journal paging.** The golden's fake never pages: it answers each
  flight-recorder GET with an empty array and no `has_more`, so the golden
  reads only the path a server that does not page takes. The paging path
  (`fetch_journal` in `scripts/reconcile-memory.py`) is pinned by
  `PagedJournalTests` and `PagedJournalExitTests` in
  `scripts/test_reconcile_units.py`, and the server's `offset` and `has_more`
  by `TestListFlightRecorderPaging` in `internal/api/flight_recorder_list_test.go`.

## Mutant record (DIR-017)

Each mutant is planted alone, its killers are run, and the file is restored and
checked byte for byte. The table below is generated from
[`mutants.json`](mutants.json), which is the single source. Edit the spec, then
run `python3 -I scripts/check_mutants.py --write-readme`. Do not edit the table.

`make test` runs the record check (`scripts/check_mutants.py`), which prints a
message for each error it finds. Failure text is not stored here. `make mutate`
(see Mutation testing below) produces it from a run.

<!-- mutants:begin -->

<!-- Generated from testdata/mutants.json by scripts/check_mutants.py --write-readme. Do not edit by hand. -->

| Mutant | Edit | Property discriminated | Mirror | Runs in | Killers |
|--------|------|------------------------|--------|---------|---------|
| M1 | `internal/importer/importer.go:724` `case layout.Progress:` becomes `case "PROGRES.md":` | A mission's `PROGRESS.md` is classified as `progress` | M1-mirror | `./internal/importer` | `TestKindFromPath/PROGRESS\.md$`, `TestGoldenImportState` |
| M1-mirror | `internal/importer/importer.go:720` `len(parts) == 3 && parts[0] == layout.MissionsDir` becomes `len(parts) >= 3 && parts[0] == layout.MissionsDir` | A file nested deeper than `missions/<id>/` is not mission metadata | M1 | `./internal/importer` | `TestKindFromPath/sub.*BRIEF\.md$` |
| M2 | `scripts/reconcile-memory.py:570` `BISHOP_LAYOUT.progress)` becomes `"PROGRES.md")` | A present `PROGRESS.md` yields its steps (the file-present branch) | M2-mirror | `scripts/test_reconcile_units.py` | `MissionsFolderLookupTests.test_declared_missions_folder_is_read`, `StepStatusTests.test_known_step_status_is_kept`, `StepStatusTests.test_unknown_step_status_is_kept_raw_and_not_sent`, `StepTableTests.test_present_progress_yields_steps`, `StepWarningTests.test_skip_steps_warns_and_counts_nothing`, `StepWarningTests.test_unknown_status_is_not_in_payload`, `StepWarningTests.test_unknown_status_is_warned_and_counted` |
| M2-mirror | `scripts/reconcile-memory.py:572` `return []` becomes `return [{"step": "0"}]` | An absent `PROGRESS.md` yields no steps (the file-absent branch) | M2 | `scripts/test_reconcile_units.py` | `JournalCapExitTests.test_cap_skips_only_exits_1_and_names_cap`, `JournalCapExitTests.test_capped_mission_without_local_events_exits_0`, `JournalCapExitTests.test_no_cap_skips_exits_0`, `MissionsFolderLookupTests.test_undeclared_folder_name_is_not_read`, `StepTableTests.test_absent_progress_yields_none` |
| SEAM-GO | `internal/profile/profile.go:47` `"missions"` becomes `"missionz"` | The importer reads the mission folder by its declared name | SEAM-GO-MIRROR | `./internal/importer` | `TestKindFromPathMissionsGuard/declared_missions_folder_is_read_as_mission_metadata`, `TestSyncMissionFolderLookup/declared_missions_folder_is_imported` |
| SEAM-GO-MIRROR | `internal/importer/importer.go:720` `len(parts) == 3 && parts[0] == layout.MissionsDir` becomes `len(parts) == 3` | An undeclared folder name is not mission metadata (the opposite edge) | SEAM-GO | `./internal/importer` | `TestKindFromPathMissionsGuard/undeclared_folder_name_is_not_read_as_mission_metadata` |
| SEAM-GO-LOOKUP | `internal/importer/importer.go:255` `profile.Bishop.Layout.MissionsDir, missionID, name)` becomes `"missionz", missionID, name)` | The missions folder lookup reads the declared folder | SEAM-GO-LOOKUP-MIRROR | `./internal/importer` | `TestSyncMissionFolderLookup/declared_missions_folder_is_imported` |
| SEAM-GO-LOOKUP-MIRROR | `internal/importer/importer.go:255` after the declared path, a glob over `*/<missionID>/<name>` replaces it when a match exists, so any folder name is read | An undeclared folder name is not read by the lookup (the opposite edge) | SEAM-GO-LOOKUP | `./internal/importer` | `TestSyncMissionFolderLookup/undeclared_folder_name_is_not_imported` |
| SEAM-PY | `scripts/reconcile-memory.py:83` `"missions"` becomes `"missionz"` | The reconciler reads each mission's folder by its declared name | SEAM-PY-MIRROR | `scripts/test_reconcile_units.py` | `MissionsFolderLookupTests.test_declared_missions_folder_is_read`, `StepStatusTests.test_known_step_status_is_kept`, `StepStatusTests.test_unknown_step_status_is_kept_raw_and_not_sent`, `StepWarningTests.test_skip_steps_warns_and_counts_nothing`, `StepWarningTests.test_unknown_status_is_not_in_payload`, `StepWarningTests.test_unknown_status_is_warned_and_counted` |
| SEAM-PY-MIRROR | `scripts/reconcile-memory.py:570` `path = os.path.join(root, BISHOP_LAYOUT.missions_dir, mission_id, BISHOP_LAYOUT.progress)` becomes `path = next((os.path.join(root, d, mission_id, BISHOP_LAYOUT.progress) for d in sorted(os.listdir(root)) if os.path.isdir(os.path.join(root, d, mission_id))), os.path.join(root, BISHOP_LAYOUT.missions_dir, mission_id, BISHOP_LAYOUT.progress))` | An undeclared folder name is not read by the reconciler (the opposite edge) | SEAM-PY | `scripts/test_reconcile_units.py` | `MissionsFolderLookupTests.test_undeclared_folder_name_is_not_read` |
| APPROVER-MUT | `scripts/reconcile-memory.py:1200` `finding["_approver"] or APPROVER_DEFAULT` becomes `finding["_approver"] or ""` | An empty Approver in the Markdown falls back to `APPROVER_DEFAULT` | APPROVER-MIRROR | `scripts/test_reconcile_units.py` | `ApproverDefaultTests.test_empty_approver_falls_back_to_default` |
| APPROVER-MIRROR | `scripts/reconcile-memory.py:1200` `finding["_approver"] or APPROVER_DEFAULT` becomes `APPROVER_DEFAULT` | An explicit Approver in the Markdown is carried, not replaced (the opposite edge) | APPROVER-MUT | `scripts/test_reconcile_units.py` | `ApproverDefaultTests.test_explicit_approver_is_carried` |
| MUT-HYPHEN | `scripts/reconcile-memory.py:155` `(?:-[A-Za-z0-9]+)*` is removed from `MISSION_CANDIDATE`, so the candidate stops at the first hyphen | The candidate runs across hyphens to the end of the id (the hyphen-run edge) | MUT-SPACE-RUN | `scripts/test_reconcile_units.py` | `FindingMissionLinkTests.test_harness_qualified_id_links_to_itself`, `FindingMissionLinkTests.test_retry_suffix_links_to_nothing`, `FindingWarningTests.test_existing_unknown_candidate_is_not_warned`, `FindingWarningTests.test_known_candidate_is_linked_not_warned`, `FindingWarningTests.test_unknown_candidate_is_not_in_payload`, `FindingWarningTests.test_unknown_candidates_are_warned` |
| MUT-SPACE-RUN | `scripts/reconcile-memory.py:155` `(?:-[A-Za-z0-9]+)*` becomes `(?:[- ][A-Za-z0-9()]+)*` | The candidate ends at a space, not after it (the opposite edge: the candidate runs past the space into parenthesised text) | MUT-HYPHEN | `scripts/test_reconcile_units.py` | `FindingMissionLinkTests.test_trailing_text_links_to_id_before_text`, `FindingWarningTests.test_existing_unknown_candidate_is_not_warned`, `FindingWarningTests.test_unknown_candidates_are_warned` |
| MUT-NOLINK | `scripts/reconcile-memory.py:645` `candidate if candidate and candidate in known_missions else ""` becomes `""` | A candidate that equals a known id is linked (the known edge of the guard) | MUT-ACCEPT | `scripts/test_reconcile_units.py` | `FindingMissionLinkTests.test_harness_qualified_id_links_to_itself`, `FindingMissionLinkTests.test_plain_id_links_to_its_mission`, `FindingMissionLinkTests.test_three_digit_counter_links_to_itself`, `FindingMissionLinkTests.test_trailing_text_links_to_id_before_text`, `FindingWarningTests.test_existing_unknown_candidate_is_not_warned`, `FindingWarningTests.test_known_candidate_is_linked_not_warned`, `FindingWarningTests.test_unknown_candidates_are_warned` |
| MUT-ACCEPT | `scripts/reconcile-memory.py:645` `candidate if candidate and candidate in known_missions else ""` becomes `candidate` | A candidate that equals no known id is left unlinked (the unknown edge of the guard) | MUT-NOLINK | `scripts/test_reconcile_units.py` | `FindingMissionLinkTests.test_retry_suffix_links_to_nothing`, `FindingMissionLinkTests.test_unarchived_plain_id_stays_unlinked`, `FindingWarningTests.test_unknown_candidate_is_not_in_payload`, `FindingWarningTests.test_unknown_candidates_are_warned` |
| MUT-DIGITS2 | `scripts/reconcile-memory.py:155` `{8}-[A-Za-z0-9]+(?:` becomes `{8}-\d{2}(?:` | The counter is read whole, so mission-20261009-100 is not read as -10 (the counter edge) | MUT-DIGITS3 | `scripts/test_reconcile_units.py` | `FindingMissionLinkTests.test_harness_qualified_id_links_to_itself`, `FindingMissionLinkTests.test_three_digit_counter_links_to_itself`, `FindingWarningTests.test_known_candidate_is_linked_not_warned` |
| MUT-DIGITS3 | `scripts/reconcile-memory.py:155` `{8}-[A-Za-z0-9]+(?:` becomes `{8}-\d{3}(?:` | A two-digit counter is read whole (the opposite edge: a counter of three digits is required) | MUT-DIGITS2 | `scripts/test_reconcile_units.py` | `FindingMissionLinkTests.test_harness_qualified_id_links_to_itself`, `FindingMissionLinkTests.test_plain_id_links_to_its_mission`, `FindingMissionLinkTests.test_trailing_text_links_to_id_before_text`, `FindingWarningTests.test_known_candidate_is_linked_not_warned`, `FindingWarningTests.test_unknown_candidates_are_warned` |
| MUT-STATUS-PASS | `scripts/reconcile-memory.py:583` `status if status in STEP_STATUSES else ""` becomes `status` | An unknown step status is not sent (the unknown edge of the status guard) | MUT-STATUS-BLANK | `scripts/test_reconcile_units.py` | `StepStatusTests.test_unknown_step_status_is_kept_raw_and_not_sent`, `StepWarningTests.test_unknown_status_is_not_in_payload` |
| MUT-STATUS-BLANK | `scripts/reconcile-memory.py:583` `status if status in STEP_STATUSES else ""` becomes `status if status in STEP_STATUSES[:1] else ""` | A known step status is sent (the known edge; the mutant keeps only `pending`) | MUT-STATUS-PASS | `scripts/test_reconcile_units.py` | `StepStatusTests.test_known_step_status_is_kept`, `StepTableTests.test_present_progress_yields_steps`, `StepWarningTests.test_unknown_status_is_not_in_payload` |
| MUT-WARN-SILENT | `scripts/reconcile-memory.py:911` `status_counts["warned"] += 1` in `reconcile_steps` is removed; the WARN line stays | An unknown step status is counted as warned (the count edge) | MUT-WARN-OVER | `scripts/test_reconcile_units.py` | `StepWarningTests.test_unknown_status_is_warned_and_counted` |
| MUT-WARN-OVER | `scripts/reconcile-memory.py:908` ` and step["_status_raw"] not in STEP_STATUSES:` becomes `:` | A known step status is not counted (the opposite edge: the count and the WARN fire only for unknown values) | MUT-WARN-SILENT | `scripts/test_reconcile_units.py` | `StepWarningTests.test_unknown_status_is_warned_and_counted` |
| CAPEXIT | `scripts/reconcile-memory.py:1531` `total_fetch_failures > 0 or total_journal_cap_skips > 0:` becomes `total_fetch_failures > 0:` | Cap skips alone set the exit status (the exit edge) | CAPEXIT-OVER | `scripts/test_reconcile_units.py` | `JournalCapExitTests.test_cap_skips_only_exits_1_and_names_cap` |
| CAPEXIT-OVER | `scripts/reconcile-memory.py:1531` `total_fetch_failures > 0 or total_journal_cap_skips > 0:` becomes `total_fetch_failures > 0 or total_journal_cap_skips >= 0:` | No cap skips exit 0 (the opposite edge: the run exits 1 with no cap skips) | CAPEXIT | `scripts/test_reconcile_units.py` | `JournalCapExitTests.test_capped_mission_without_local_events_exits_0`, `JournalCapExitTests.test_no_cap_skips_exits_0` |
| CAPFETCH | `scripts/reconcile-memory.py:1081` the cap branch of `reconcile_journal` also counts `fetch_failed` | A journal cap is not a fetch failure (the cap edge of the fetch count) | FETCHCAP | `scripts/test_reconcile_units.py` | `JournalCapExitTests.test_cap_skips_only_exits_1_and_names_cap`, `JournalCapExitTests.test_capped_mission_without_local_events_exits_0`, `JournalCapTests.test_capped_mission_without_local_events_is_warned_not_skipped`, `JournalCapTests.test_mission_at_cap_is_skipped_as_journal_cap` |
| FETCHCAP | `scripts/reconcile-memory.py:1090` the fetch-failure branch of `reconcile_journal` also marks the mission capped | A fetch failure is not a journal-cap skip (the opposite edge) | CAPFETCH | `scripts/test_reconcile_units.py` | `JournalCapTests.test_fetch_failure_is_not_journal_cap` |
| CAPWARN | `scripts/reconcile-memory.py:1081` `status_counts["warned"] += 1` in the cap branch of `reconcile_journal` is removed | A capped mission is warned, once per mission (the warn edge) | CAPWARN-BELOW | `scripts/test_reconcile_units.py` | `JournalCapTests.test_capped_mission_without_local_events_is_warned_not_skipped`, `JournalCapTests.test_mission_at_cap_is_skipped_as_journal_cap` |
| CAPWARN-BELOW | `scripts/reconcile-memory.py:1077` `status_counts["warned"] += 1` is added before each mission's journal fetch, so a mission below the cap is warned too (the record placed it before the below-cap loop; that loop is now the per-mission fetch) | A mission below the cap is not warned (the opposite edge) | CAPWARN | `scripts/test_reconcile_units.py` | `JournalCapTests.test_mission_below_cap_is_reconciled` |
| CAPZERO | `scripts/reconcile-memory.py:1081` the cap branch of `reconcile_journal` also counts `skipped_journal_cap` per mission | A capped mission with no local events skips no event (the zero-event edge) | CAPEVENT-DROP | `scripts/test_reconcile_units.py` | `JournalCapExitTests.test_cap_skips_only_exits_1_and_names_cap`, `JournalCapExitTests.test_capped_mission_without_local_events_exits_0`, `JournalCapTests.test_capped_mission_without_local_events_is_warned_not_skipped`, `JournalCapTests.test_mission_at_cap_is_skipped_as_journal_cap` |
| CAPEVENT-DROP | `scripts/reconcile-memory.py:1101` the per-event `status_counts["skipped_journal_cap"] += 1` in `reconcile_journal` is removed | An event of a capped mission is counted as skipped (the per-event edge) | CAPZERO | `scripts/test_reconcile_units.py` | `JournalCapExitTests.test_cap_skips_only_exits_1_and_names_cap`, `JournalCapTests.test_mission_at_cap_is_skipped_as_journal_cap` |
| SKIPWARN | `scripts/reconcile-memory.py:903` the unknown-status WARN loop is placed before the `if skip_steps:` return, so under `--skip-steps` it still warns and counts | Under `--skip-steps` nothing is warned or counted (the skip edge) | WARN-PRINT | `scripts/test_reconcile_units.py` | `StepWarningTests.test_skip_steps_warns_and_counts_nothing` |
| WARN-PRINT | `scripts/reconcile-memory.py:909` the step WARN `print` in `reconcile_steps` is removed; the count stays | An unknown step status is named in a WARN line on the reconcile path (the opposite edge) | SKIPWARN | `scripts/test_reconcile_units.py` | `StepWarningTests.test_unknown_status_is_warned_and_counted` |
| FWARN | `scripts/reconcile-memory.py:1165` the Mission WARN and its count move before the existing-key check, so an existing entry is warned too | A candidate is warned only for an entry this run creates (the existing-entry edge) | FWARN-OFF | `scripts/test_reconcile_units.py` | `FindingWarningTests.test_existing_unknown_candidate_is_not_warned` |
| FWARN-OFF | `scripts/reconcile-memory.py:1169` the create-path Mission WARN and its count in `reconcile_findings` are removed | An entry this run creates is warned (the create edge) | FWARN | `scripts/test_reconcile_units.py` | `FindingWarningTests.test_unknown_candidates_are_warned` |
| GO-PROBE | `internal/api/flight_recorder.go:225` `args = append(args, limit+1, offset)` becomes `args = append(args, limit, offset)` | The probe row is fetched, so `has_more` is true when a row follows the page | GO-PROBE-MIRROR | `./internal/api` | `TestListFlightRecorderPaging/first_page_reports_rows_after_it` |
| GO-PROBE-MIRROR | `internal/api/flight_recorder.go:273` `len(entries) > limit` becomes `len(entries) >= limit` | A page that is exactly full with no row after it reports `has_more` false (the opposite edge) | GO-PROBE | `./internal/api` | `TestListFlightRecorderPaging/exact_page_with_nothing_after_it_reports_no_more` |
| GO-NEG | `internal/api/flight_recorder.go:203` `err != nil \|\| n < 0` becomes `err != nil` | A negative offset is a 400 | GO-NEG-MIRROR | `./internal/api` | `TestListFlightRecorderPaging/negative_offset_is_rejected` |
| GO-NEG-MIRROR | `internal/api/flight_recorder.go:203` `err != nil \|\| n < 0` becomes `err != nil \|\| n <= 0` | An offset of zero is accepted (the opposite edge of GO-NEG) | GO-NEG | `./internal/api` | `TestListFlightRecorderPaging/first_page_reports_rows_after_it` |
| GO-KEY | `internal/api/flight_recorder.go:279` `if paged {` becomes `if true {` | `has_more` is absent when `offset` is not sent (the unpaged response has no other keys) | GO-KEY-MIRROR | `./internal/api` | `TestListFlightRecorderPaging/absent_offset_keeps_the_unpaged_response` |
| GO-KEY-MIRROR | `internal/api/flight_recorder.go:279` `if paged {` becomes `if false {` | `has_more` is present when `offset` is sent (the opposite edge of GO-KEY) | GO-KEY | `./internal/api` | `TestListFlightRecorderPaging/first_page_reports_rows_after_it` |
| GO-OFFSET | `internal/api/flight_recorder.go:225` `limit+1, offset)` becomes `limit+1, offset*0)` | The `offset` value reaches the query (the offset is applied) | GO-OFFSET-MIRROR | `./internal/api` | `TestListFlightRecorderPaging/last_page_reports_nothing_after_it` |
| GO-OFFSET-MIRROR | `internal/api/flight_recorder.go:225` `limit+1, offset)` becomes `limit+1, offset+1)` | The offset is applied exactly, not shifted by one (the opposite edge of GO-OFFSET) | GO-OFFSET | `./internal/api` | `TestListFlightRecorderPaging/absent_offset_keeps_the_unpaged_response` |
| GO-TIE | `internal/api/flight_recorder.go:224` `created_at DESC, id DESC LIMIT` becomes `created_at DESC, id ASC LIMIT` | Rows sharing `created_at` are ordered by id descending, paged and unpaged | GO-TIE-MIRROR | `./internal/api` | `TestListFlightRecorderPaging/rows_sharing_created_at_page_in_id_order` |
| GO-TIE-MIRROR | `internal/api/flight_recorder.go:224` `created_at DESC, id DESC LIMIT` becomes `id DESC LIMIT` | `created_at` outranks id: where `created_at` differs, the id tie-break does not decide the order (the opposite edge of GO-TIE) | GO-TIE | `./internal/api` | `TestListFlightRecorderPaging/created_at_outranks_id_in_the_order` |
| PY-STOP | `scripts/reconcile-memory.py:1040` `if not body["has_more"]:` becomes `if True:` | The reader stops after the first page whatever `has_more` says | PY-STOP-MIRROR | `scripts/test_reconcile_units.py` | `PagedJournalTests.test_paged_server_reads_every_page_and_is_not_capped` |
| PY-STOP-MIRROR | `scripts/reconcile-memory.py:1040` `if not body["has_more"]:` becomes `if len(page) < JOURNAL_PAGE_SIZE:` | The reader ends on `has_more`, not on page size: a full last page does not end the read (the opposite edge of PY-STOP) | PY-STOP | `scripts/test_reconcile_units.py` | `PagedJournalTests.test_paged_server_exact_two_pages_stops_at_has_more` |
| PY-EXACT | `scripts/reconcile-memory.py:1041` `return rows, False` becomes `return rows, len(rows) >= JOURNAL_PAGE_SIZE` | A paging server's full last page is not capped | PY-OLDCAP | `scripts/test_reconcile_units.py` | `PagedJournalTests.test_paged_server_exact_page_is_not_capped` |
| PY-OLDCAP | `scripts/reconcile-memory.py:1032` `return rows, len(rows) >= JOURNAL_PAGE_SIZE` becomes `return rows, len(rows) > JOURNAL_PAGE_SIZE` | An older server's exactly full page is capped (the same full-page edge on the non-paging branch) | PY-EXACT | `scripts/test_reconcile_units.py` | `JournalCapTests.test_mission_at_cap_is_skipped_as_journal_cap` |
| PY-FAIL-LATER | `scripts/reconcile-memory.py:1047` the GET at `offset` is wrapped in `try`/`except OSError`, returning `(rows, False)` on failure | A failing later page is a fetch failure, not a partial pass | PY-FAIL-FIRST | `scripts/test_reconcile_units.py` | `PagedJournalTests.test_failing_page_is_fetch_failure_not_partial_pass` |
| PY-FAIL-FIRST | `scripts/reconcile-memory.py:1029` the first GET is wrapped in `try`/`except OSError`, returning `([], False)` on failure | A failing first page is a fetch failure (the opposite edge of PY-FAIL-LATER, on the first GET) | PY-FAIL-LATER | `scripts/test_reconcile_units.py` | `JournalCapTests.test_fetch_failure_is_not_journal_cap` |
| PY-EMPTY | `scripts/reconcile-memory.py:1042` `if not page:` becomes `if False:` | `has_more` on an empty page is a fetch failure, so the read cannot loop | PY-EMPTY-MIRROR | `scripts/test_reconcile_units.py` | `PagedJournalTests.test_has_more_on_empty_page_is_fetch_failure` |
| PY-EMPTY-MIRROR | `scripts/reconcile-memory.py:1042` `if not page:` becomes `if page:` | A non-empty page with `has_more` does not raise (the opposite edge of PY-EMPTY) | PY-EMPTY | `scripts/test_reconcile_units.py` | `PagedJournalExitTests.test_paged_service_over_http_exits_0_without_cap` |
| PY-PAGING | `scripts/reconcile-memory.py:1030` `if "has_more" not in body:` becomes `if True:` | A server that pages is recognised by its `has_more` key | PY-PAGING-MIRROR | `scripts/test_reconcile_units.py` | `PagedJournalTests.test_paged_server_reads_every_page_and_is_not_capped` |
| PY-PAGING-MIRROR | `scripts/reconcile-memory.py:1030` `"has_more" not in body` becomes `"has_more" in body` | An older server, which sends no `has_more`, keeps the cap (the opposite edge of PY-PAGING) | PY-PAGING | `scripts/test_reconcile_units.py` | `PagedJournalTests.test_server_that_does_not_page_keeps_the_cap` |
| PY-OFFSET | `scripts/reconcile-memory.py:1009` `"offset": offset})` becomes `"offset": offset + 1})` | The page GET names the page's offset exactly | PY-OFFSET-MIRROR | `scripts/test_reconcile_units.py` | `PagedJournalTests.test_paged_server_reads_every_page_and_is_not_capped` |
| PY-OFFSET-MIRROR | `scripts/reconcile-memory.py:1046` `offset += len(page)` becomes `offset += len(page) + 1` | The next offset is the number of rows received, not one more (the opposite edge of PY-OFFSET) | PY-OFFSET | `scripts/test_reconcile_units.py` | `PagedJournalTests.test_paged_server_reads_every_page_and_is_not_capped` |
| PY-CEIL | `scripts/reconcile-memory.py:1044` `if pages >= JOURNAL_MAX_PAGES:` becomes `if pages > JOURNAL_MAX_PAGES:` | A read still reporting `has_more` after `JOURNAL_MAX_PAGES` pages is a fetch failure (the tests patch the ceiling to three pages) | PY-CEIL-MIRROR | `scripts/test_reconcile_units.py` | `PagedJournalTests.test_read_past_the_page_ceiling_is_fetch_failure` |
| PY-CEIL-MIRROR | `scripts/reconcile-memory.py:1044` `if pages >= JOURNAL_MAX_PAGES:` becomes `if pages >= JOURNAL_MAX_PAGES - 1:` | A read that ends on `has_more` false at exactly the ceiling completes (the opposite edge of PY-CEIL) | PY-CEIL | `scripts/test_reconcile_units.py` | `PagedJournalTests.test_read_of_exactly_the_page_ceiling_is_complete` |
| PY-DEDUPE-REPEAT | `scripts/reconcile-memory.py:1086` `existing_journal[key] = row` becomes `existing_journal.pop(key) if key in existing_journal else existing_journal.__setitem__(key, row)` | A row the service returns twice, across a page boundary, still finds its event: the event is not created again | PY-DEDUPE-NOTE | `scripts/test_reconcile_units.py` | `PagedJournalTests.test_row_repeated_across_a_page_boundary_is_not_created_again` |
| PY-DEDUPE-NOTE | `scripts/reconcile-memory.py:1108` `if key in existing_journal:` becomes `if key[:4] in {k[:4] for k in existing_journal}:` | A row that differs from an event only in its note is not that event (the opposite edge: a near match is not a match) | PY-DEDUPE-REPEAT | `scripts/test_reconcile_units.py` | `PagedJournalTests.test_row_differing_only_in_note_is_not_the_event` |

Retired rows are not applied; their targets are no longer in the tree:

| Mutant | Last target | Reason | Mirror |
|--------|-------------|--------|--------|
| MUT-ANCHOR | `scripts/reconcile-memory.py` | `MISSION_ID` no longer exists; the mission link is `MISSION_CANDIDATE`. Its edges are covered by MUT-HYPHEN, MUT-SPACE-RUN, MUT-DIGITS2 and MUT-DIGITS3. | MUT-SPACE |
| MUT-SPACE | `scripts/reconcile-memory.py` | `MISSION_ID` no longer exists; the mission link is `MISSION_CANDIDATE`. Its edges are covered by MUT-HYPHEN, MUT-SPACE-RUN, MUT-DIGITS2 and MUT-DIGITS3. | MUT-ANCHOR |
| MUT-LOOSE | `scripts/reconcile-memory.py` | `MISSION_ID` no longer exists; the mission link is `MISSION_CANDIDATE`. Its edges are covered by MUT-HYPHEN, MUT-SPACE-RUN, MUT-DIGITS2 and MUT-DIGITS3. | MUT-THREE |
| MUT-THREE | `scripts/reconcile-memory.py` | `MISSION_ID` no longer exists; the mission link is `MISSION_CANDIDATE`. Its edges are covered by MUT-HYPHEN, MUT-SPACE-RUN, MUT-DIGITS2 and MUT-DIGITS3. | MUT-LOOSE |
| MUT-STATUS | `scripts/reconcile-memory.py` | The inline set literal in `parse_steps` was replaced by the `STEP_STATUSES` constant. The status edge is covered by MUT-STATUS-PASS. | MUT-KNOWN |
| MUT-KNOWN | `scripts/reconcile-memory.py` | The inline set literal in `parse_steps` was replaced by the `STEP_STATUSES` constant. The known edge is covered by MUT-STATUS-BLANK. | MUT-STATUS |
| MUT-DROP | `scripts/reconcile-memory.py` | `parse_steps` no longer skips a row by status. Its only skip is an empty step (`if not step: continue`), so there is no edit site for this edge. | MUT-KNOWNDROP |
| MUT-KNOWNDROP | `scripts/reconcile-memory.py` | `parse_steps` no longer skips a row by status. Its only skip is an empty step (`if not step: continue`), so there is no edit site for this edge. | MUT-DROP |
| MUT-CAP-GT | `scripts/reconcile-memory.py` | The journal cap guard moved from `reconcile_journal` into `fetch_journal` (`return rows, len(rows) >= JOURNAL_PAGE_SIZE`). The cap edge is PY-OLDCAP, whose mirror is PY-EXACT. | MUT-CAP-99 |
| MUT-CAP-99 | `scripts/reconcile-memory.py` | The journal cap guard moved from `reconcile_journal` into `fetch_journal`. No active mutant covers the one-below-cap edge that this row named; PY-OLDCAP covers the cap edge itself. | MUT-CAP-GT |

<!-- mutants:end -->

## Mutation testing

`scripts/mutate.py` runs the active mutants in [`mutants.json`](mutants.json).
For each mutant it refuses unless the file is clean against HEAD: `git status
--porcelain --ignored` for the file must print nothing. It then plants the edit
alone, runs the mutant's killers, and restores the file. The restore is verified
(`restore_and_verify` in the script holds the checks) before the file counts as
restored. Each killer's full output goes to a per-mutant log. The runner is POSIX
only and refuses elsewhere.

    make mutate                    # every active mutant
    make mutate ID='GO-KEY M2'     # selected ids only

A mutant is KILLED when at least one of its killers fails and every killer
reached a verdict. A killer that did not reach one is not a kill; the mutant is
INVALID, with the reason printed beside it. `mutate_one` and `run_killers` in
the script decide each case. A killer that passes is listed as not failing.
SHARED-TEXT is printed when a mutant and its mirror fail with the same message;
the run then fails, because a shared kill is not two kills.

Each killer run is bounded by `--timeout SECONDS` (default 600). Python killers
run as one `unittest -v` process per mutant, with `PYTHONDONTWRITEBYTECODE=1`
and `python3 -B -I`, so the timeout bounds that process. Go killers run the
parent test with `go test -v -run`, one process per selector, and each selector
picks the subtests it names. Logs go to a temporary directory unless
`--log-dir` is given. Before a file is planted, its entry is written to
`mutate-unrestored.json` in the git directory (`.git/` in a normal checkout), with the log directory and the saved original. The
entry is removed when that file's restore is verified. A refused run that finds
a dirty file names the saved original of the earlier run that left it planted,
when that copy still exists, so the file can be compared with its copy. The
marker is replaced atomically, so a kill cannot leave it half-written. A marker
that does not parse refuses the run before anything is planted; the message names
the file and the recovery step (`git checkout -- <file>` for any modified file,
then delete the marker).

One run at a time. A run holds an exclusive lock on `mutate.lock` in the git
directory, and a second run refuses. Two runs on one working tree would plant over
each other's files and read each other's mutants, so this is a correctness rule
and not only a guard for the marker. The operating system releases the lock when
the process ends, however it ends.

Exit status is listed under `--help` and here; `--help` prints `EXIT_DOC`. A test
in `scripts/test_mutate.py` fails when the rows of this table and `EXIT_DOC` differ
in either direction.

| Status | Meaning |
|--------|---------|
| 0 | every selected mutant KILLED, every restore verified, tree unchanged, no SHARED-TEXT |
| 1 | a mutant SURVIVED or was INVALID, a restore failed, or the tree changed |
| 2 | refused before anything was planted; the message names the cause |
| 3 | a required tool (git, or go for a Go mutant) is not installed or cannot answer |
| 4 | every mutant KILLED, but a mutant and its mirror fail with the same message |
| 5 | interrupted by a signal or Ctrl-C; the planted file, if any, was restored and verified |

An interrupt by a signal in `HANDLED_SIGNALS` (`scripts/mutate.py`) stops the run.
Each killer runs in its own process group, and a timeout or an interrupt kills
that group; a process that leaves the group (`setsid`, a double fork) is not
reached. If a planted file is not restored and verified, the run prints
`RESTORE FAILED` with the path of the saved original and exits 1 instead of 5,
because the file may still hold the mutant. A restore that never ran counts as
not verified. A signal during a restore does not cut it short: it is retried or
recorded, and the run stops once the restore is verified.

Known limitation: signals are not blocked between a plant and its restore. Three
windows are not covered. A signal that lands just before the restore call, in the
runner's `finally` block, leaves the file planted, and the run reports
`RESTORE FAILED` and exits 1. A signal that lands after the runner marks the plant
and before its restore block begins is reported the same way, though nothing was
written. A signal that lands in a killer's process-group cleanup, before the group
is killed, leaves that group running; the restore still runs and is verified, and
the run does not report the group.

Anything that ends the process without letting the runner's code run skips every
restore. Nothing in the runner can handle that; the next run refuses on the dirty
file and names the saved copy recorded in the git directory's
`mutate-unrestored.json`.

The runner and the record check are tooling, not product code. DIR-017 governs
the product invariants the mutants target, so the tooling is not given mirrored
mutants of its own. It is covered by the per-edge unit tests in
`scripts/test_mutate.py` and `scripts/test_check_mutants.py`, and by controls that
run it against deliberately corrupted inputs (DIR-001).

`make test` runs the record check, not the mutants. The check takes a fraction of
a second; a mutant run takes minutes.

## Layout values at the seam

The directory and file names the importer and the reconciler read are declared
once. The Go values are `profile.Bishop.Layout` in `internal/profile/profile.go`.
The Python values are `BISHOP_LAYOUT` in `scripts/reconcile-memory.py`.

Only the missions folder name has been checked, against the goldens and the
unit tests above. Changing it to `missionz` in each language fails the golden
and the lookup tests in that language (SEAM-GO and SEAM-PY above). No other
layout value is claimed as pinned; see "What the goldens cannot see" for the
values the goldens do not read.

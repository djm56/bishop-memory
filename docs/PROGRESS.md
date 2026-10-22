# Progress — bishop-memory

The follow-along tracker for [ROADMAP.md](ROADMAP.md). The roadmap says what is
outstanding; this file shows the same work by workstream, one checkbox per
item, with evidence for what is done and a link to the plan section.

Last updated: 2026-10-22, against `main` at `cbf229e` (the commit `feat/grade-excluded` was cut from), with the operator N/A grade (PR #TBD). PR #12 (squash-merge `7fc76ba`, "Triage runner on OpenCode v2") is on `main`, and its items are in Triage & grading. Before it, against `fc38064`: harness portability phase 1 is on `main` through PR #8 (`8437f32`). The reconciler fixes are on `main` through PR #9 (`1c4def9`), and the `[flight-recorder]` summary fix and the journal cap exit code ship with the change that carries this update. The live memoryd was redeployed on 2026-10-21 at 06:35 UTC with the flight-recorder paging (operator-reported), and a full journal reconcile was run on it the same day. Live items the repository cannot confirm stay unticked. The operator N/A line under Memory insight is ticked on branch `feat/grade-excluded` (mission-20261022-05) and is not on `main` until PR #TBD merges; the override line stays open.

## Summary

| Workstream | Done | Outstanding | % |
|------------|-----:|------------:|--:|
| Network deployment | 11 | 11 | 50% |
| PostgreSQL | 0 | 5 | 0% |
| Memory insight | 8 | 14 | 36% |
| Mission HUD | 17 | 6 | 74% |
| Triage & grading | 11 | 19 | 37% |
| Reconciler | 5 | 10 | 33% |
| Service hardening | 2 | 4 | 33% |
| Docs/README | 8 | 2 | 80% |
| Harness portability | 1 | 7 | 13% |
| **Total** | **63** | **78** | **45%** |

Percentage is done ÷ (done + outstanding), to the nearest whole number.

## How to update this file

- Tick a box (`- [x]`) only when the work is on `main`, and add its evidence:
  a commit, a PR, or a path.
- Partly done stays unticked, with `(partial: …)` saying what is missing.
  Work that the repository cannot confirm stays unticked, with
  `(unverified: …)` saying what would confirm it.
- An item accepted as it stands is not ticked. It says so: `(accepted: …)`,
  and it is not counted as done.
- When an item ships, move it to **Shipped** in ROADMAP.md as well.
- Change the Last updated line whenever a box changes. Run the count command
  below; the summary table must match it.

Count command, one line per workstream section, then a total. It is anchored
to line start, so the `- [x]` in the bullet above is not counted:

```sh
awk '
/^## / { if (s != "" && d + o > 0) print s "|" d + 0 "|" o + 0; s = substr($0, 4); d = 0; o = 0; next }
/^- \[x\] / { d++; td++ }
/^- \[ \] / { o++; to++ }
END { if (d + o > 0) print s "|" d + 0 "|" o + 0; print "Total|" td + 0 "|" to + 0 }
' docs/PROGRESS.md
```

## Network deployment

Plan: [plans/NETWORK-DEPLOYMENT-PLAN.md](plans/NETWORK-DEPLOYMENT-PLAN.md)

- [x] Named API keys, hashed at rest, with `memoryd keys add|list|revoke` — `internal/auth/keys.go`, `cmd/memoryd/commands.go` ([§3.1](plans/NETWORK-DEPLOYMENT-PLAN.md#31-api-keys))
- [x] Fail-closed start-up guard: no keys off loopback means refusal to start — `cmd/memoryd/main.go:53-66` ([§3.2](plans/NETWORK-DEPLOYMENT-PLAN.md#32-when-keys-are-required))
- [x] Built-in TLS via `TLS_CERT_FILE` and `TLS_KEY_FILE` — `cmd/memoryd/main.go:49-52, 146` ([§3.3](plans/NETWORK-DEPLOYMENT-PLAN.md#33-transport))
- [x] Warning on plain HTTP bound to a network address — `cmd/memoryd/main.go:62-64` ([§3.3](plans/NETWORK-DEPLOYMENT-PLAN.md#33-transport))
- [x] Clients send the key: mcpd, the reconciler, the triage scripts — `cmd/mcpd/clientenv.go` ([§3.1](plans/NETWORK-DEPLOYMENT-PLAN.md#31-api-keys))
- [ ] Browser prompts for a key on 401 and offers Sign out (unverified: the HTML has not been inspected) ([§3.1](plans/NETWORK-DEPLOYMENT-PLAN.md#31-api-keys))
- [x] Push, hashes and delete routes: `POST /v1/documents/push`, `GET /v1/documents/hashes`, `POST /v1/documents/delete` — `internal/api/router.go:146-148`, `internal/api/documents_push.go` ([§4](plans/NETWORK-DEPLOYMENT-PLAN.md#4-files-over-the-network-push-instead-of-pull))
- [x] `scripts/push-memory.py` pushes changed files — `scripts/push-memory.py` ([§4](plans/NETWORK-DEPLOYMENT-PLAN.md#4-files-over-the-network-push-instead-of-pull))
- [ ] Reconciler pushes instead of reading paths (partial: with `--harness` the push is the default, `scripts/reconcile-memory.py`; the fallback to server-side path sync, `POST /v1/documents/sync`, remains for runs without `--harness` and against an older memoryd) ([§4](plans/NETWORK-DEPLOYMENT-PLAN.md#4-files-over-the-network-push-instead-of-pull))
- [x] Embedded schema, so the binary runs from any directory — `db/embed.go` ([§5.1](plans/NETWORK-DEPLOYMENT-PLAN.md#51-one-binary-no-checkout-needed))
- [x] `make dist` for linux and darwin, amd64 and arm64 — `Makefile:49` ([§5.1](plans/NETWORK-DEPLOYMENT-PLAN.md#51-one-binary-no-checkout-needed))
- [x] `install.sh client` writes `client.env` at mode 600 — `scripts/install.sh:30, 265` ([§5.3](plans/NETWORK-DEPLOYMENT-PLAN.md#53-each-client-machine))
- [x] `memoryd backup` writes a consistent copy — `cmd/memoryd/commands.go` ([§5.7](plans/NETWORK-DEPLOYMENT-PLAN.md#57-backups))
- [ ] Server installer run for real on Linux and macOS (partial: `scripts/install.sh server` has not been run) ([§5.2](plans/NETWORK-DEPLOYMENT-PLAN.md#52-the-server))
- [ ] TLS run with a real certificate (unverified) ([§6](plans/NETWORK-DEPLOYMENT-PLAN.md#6-phases))
- [ ] `--data-dir` installer flag (partial: named in the plan, absent from the installers; change the plan or the scripts) ([§5.2](plans/NETWORK-DEPLOYMENT-PLAN.md#52-the-server))
- [ ] Harness-side edits in each harness repository (unverified: outside this repository) ([§5.4](plans/NETWORK-DEPLOYMENT-PLAN.md#54-changes-in-each-harness-outside-this-repository))
- [ ] Phase 2: backup timer set up by the installer ([§5.7](plans/NETWORK-DEPLOYMENT-PLAN.md#57-backups))
- [ ] Phase 2: LaunchDaemon option on macOS ([§6](plans/NETWORK-DEPLOYMENT-PLAN.md#6-phases))
- [ ] Phase 2: Homebrew formula and `.deb` ([§6](plans/NETWORK-DEPLOYMENT-PLAN.md#6-phases))
- [ ] Phase 2: CI building release archives on tags ([§6](plans/NETWORK-DEPLOYMENT-PLAN.md#6-phases))
- [ ] Phase 3: read-only keys, and an audit of key use in the flight recorder ([§6](plans/NETWORK-DEPLOYMENT-PLAN.md#6-phases))

## PostgreSQL

Plan: [plans/POSTGRES-PLAN.md](plans/POSTGRES-PLAN.md)

- [ ] Dialect wrapper and `?` rebinding in `internal/store/` (no `dialect.go` yet) ([§5](plans/POSTGRES-PLAN.md#5-phases))
- [ ] `DB_DRIVER=sqlite|postgres` configuration ([§4.2](plans/POSTGRES-PLAN.md#42-configuration))
- [ ] `InsertReturningID`, numbered migrations, PostgreSQL schema and search ([§5](plans/POSTGRES-PLAN.md#5-phases))
- [ ] Tests and CI service for PostgreSQL ([§4.6](plans/POSTGRES-PLAN.md#46-tests-and-ci))
- [ ] `migrate-db`, for moving existing data ([§4.5](plans/POSTGRES-PLAN.md#45-moving-data))

## Memory insight

Plan: [plans/MEMORY-INSIGHT-PLAN.md](plans/MEMORY-INSIGHT-PLAN.md)

- [x] Phase 0: missions record their harness; `PATCH` fills it only while empty — `internal/api/missions.go`, `ef23e11` ([§2](plans/MEMORY-INSIGHT-PLAN.md#2-phase-0--data-health-built))
- [x] Phase 0: stale board at 48 hours, with a `stale` filter — `internal/api/hud.go:40`, `8426304` ([§2](plans/MEMORY-INSIGHT-PLAN.md#2-phase-0--data-health-built))
- [x] Phase 0: workspace scratch ranked at half weight in search — `internal/api/search.go:212-223`, `ef23e11` ([§2](plans/MEMORY-INSIGHT-PLAN.md#2-phase-0--data-health-built))
- [x] Phase 0: `mission.updated` notes name each changed field; journal shows `occurred_at` — `internal/api/missions.go`, CHANGELOG ([§2](plans/MEMORY-INSIGHT-PLAN.md#2-phase-0--data-health-built))
- [x] Stage 1b: review verdicts parsed on read — `internal/review/verdict.go`, `06e4f8a` ([§9](plans/MEMORY-INSIGHT-PLAN.md#9-build-order-agreed-2026-10-16))
- [ ] Stage 1a, the fixed `Verdict:` line in the agent and skill definitions (partial: the parser accepts the line; no definition carries it yet, pending ratification of P3) ([§9](plans/MEMORY-INSIGHT-PLAN.md#9-build-order-agreed-2026-10-16))
- [x] Stage 2: Crew page at `/crew` and `GET /v1/crew` — `internal/ui/crew.html`, `f0da6af` ([§9](plans/MEMORY-INSIGHT-PLAN.md#9-build-order-agreed-2026-10-16))
- [x] Stage 3: brief prep, `GET /v1/brief-prep` and the `brief_prep` MCP tool — `internal/api/brief_prep.go`, `3112a24` ([§6](plans/MEMORY-INSIGHT-PLAN.md#6-phase-4--feed-the-memory-forward))
- [ ] Phase 2: Library at `/library` ([§4](plans/MEMORY-INSIGHT-PLAN.md#4-phase-2--cross-mission-pages-and-operator-actions))
- [ ] Phase 2: activity feed across harnesses ([§4](plans/MEMORY-INSIGHT-PLAN.md#4-phase-2--cross-mission-pages-and-operator-actions))
- [ ] Phase 2: trends on `/performance` (partial: the grade trend shipped in `c83fdbf`; fix rounds and findings per week have not been built) ([§4](plans/MEMORY-INSIGHT-PLAN.md#4-phase-2--cross-mission-pages-and-operator-actions))
- [ ] Phase 2: global search across missions, findings, patterns and directives ([§4](plans/MEMORY-INSIGHT-PLAN.md#4-phase-2--cross-mission-pages-and-operator-actions))
- [ ] Phase 2: filters in the URL (unverified: not inspected) ([§4](plans/MEMORY-INSIGHT-PLAN.md#4-phase-2--cross-mission-pages-and-operator-actions))
- [x] Phase 2: operator N/A (excluded) state for test missions that should not count, with its `PUT` and `DELETE` routes under `/v1/mission-grades/:missionID/exclusion`, the HUD and Performance labels, and the grader skip — PR #TBD (mission-20261022-05) ([§4](plans/MEMORY-INSIGHT-PLAN.md#4-phase-2--cross-mission-pages-and-operator-actions))
- [ ] Phase 2: operator agree or override of a grade, with a note, and the `mission_grades` operator columns (not started) ([§4](plans/MEMORY-INSIGHT-PLAN.md#4-phase-2--cross-mission-pages-and-operator-actions))
- [ ] Phase 2: park or close a stale mission from the HUD (unverified: not inspected in the UI) ([§4](plans/MEMORY-INSIGHT-PLAN.md#4-phase-2--cross-mission-pages-and-operator-actions))
- [ ] Phase 2: operator notes on a mission, `mission_notes` ([§4](plans/MEMORY-INSIGHT-PLAN.md#4-phase-2--cross-mission-pages-and-operator-actions))
- [ ] Phase 3: cost and usage, `agent_runs`, `model_prices`, `scripts/import-usage.py` ([§5](plans/MEMORY-INSIGHT-PLAN.md#5-phase-3--cost-and-usage))
- [ ] Phase 4: retrieval log ([§6](plans/MEMORY-INSIGHT-PLAN.md#6-phase-4--feed-the-memory-forward))
- [ ] Phase 4: directive citations, `directive_refs` ([§6](plans/MEMORY-INSIGHT-PLAN.md#6-phase-4--feed-the-memory-forward))
- [ ] Phase 4: git deliverables, `mission_commits` ([§6](plans/MEMORY-INSIGHT-PLAN.md#6-phase-4--feed-the-memory-forward))
- [ ] Phase 4: operator interventions as an `operator.decision` journal event ([§6](plans/MEMORY-INSIGHT-PLAN.md#6-phase-4--feed-the-memory-forward))

## Mission HUD

Plan: [plans/MISSION-HUD-PLAN.md](plans/MISSION-HUD-PLAN.md)

- [x] Phase 1: documents carry `mission_id` and `harness` — `internal/store/migrate.go:54-60` ([§2.1](plans/MISSION-HUD-PLAN.md#21-documents-know-their-mission-and-harness))
- [x] Phase 1: sync covers every harness; `PATCH` re-imports a mission's three files — `internal/api/flight_recorder.go:256`, `internal/api/missions.go:854-872` ([§2.2](plans/MISSION-HUD-PLAN.md#22-sync-every-harness-and-sync-on-mission-change))
- [x] Phase 1: new findings take the harness's open mission — `internal/api/findings.go:575-584` ([§2.3](plans/MISSION-HUD-PLAN.md#23-new-writes-fill-the-empty-columns))
- [x] Phase 1: step timing stamped as `started_at` and `ended_at` — `internal/api/missions.go:612-620` ([§2.3](plans/MISSION-HUD-PLAN.md#23-new-writes-fill-the-empty-columns))
- [ ] Phase 1: the `step-sync` note copied into the step summary (unverified: not traced) ([§2.3](plans/MISSION-HUD-PLAN.md#23-new-writes-fill-the-empty-columns))
- [x] Phase 1: backfill script with a dry run, and `make mission-links` — `scripts/backfill-mission-links.py` ([§2.4](plans/MISSION-HUD-PLAN.md#24-one-off-backfill--scriptsbackfill-mission-linkspy))
- [ ] Phase 1: backfill applied to the live database (unverified) ([§2.4](plans/MISSION-HUD-PLAN.md#24-one-off-backfill--scriptsbackfill-mission-linkspy))
- [x] Phase 2: `mission_criteria` table and importer — `c325080` ([§3](plans/MISSION-HUD-PLAN.md#3-phase-2--structure-the-debrief-optional))
- [x] Phase 2: `agent_mistakes` table and importer — `2347527` ([§3](plans/MISSION-HUD-PLAN.md#3-phase-2--structure-the-debrief-optional))
- [x] Phase 2: `mission_deliverables` table and importer — `a53c1ac` ([§3](plans/MISSION-HUD-PLAN.md#3-phase-2--structure-the-debrief-optional))
- [x] Phase 2: `mission_lessons` table and importer — `1e78378` ([§3](plans/MISSION-HUD-PLAN.md#3-phase-2--structure-the-debrief-optional))
- [x] Phase 3: `GET /v1/hud/missions`, `GET /v1/hud/missions/:id`, `GET /v1/findings?mission_id=` — `internal/api/router.go:58-59`, `internal/api/findings.go:111-112` ([§4.1](plans/MISSION-HUD-PLAN.md#41-endpoints))
- [x] Phase 3: `/missions` page and the `/` redirect — `af53c3d`, `internal/ui/missions.html` ([§4.2](plans/MISSION-HUD-PLAN.md#42-pages))
- [x] Phase 3: crew rows from agent definitions — `internal/importer/agents.go` ([§4.3](plans/MISSION-HUD-PLAN.md#43-crew))
- [x] Phase 3: `make scratch-clean` through `scripts/clean-scratch.py` — `Makefile:120` ([§4.4](plans/MISSION-HUD-PLAN.md#44-scratch-cleanup))
- [x] HUD redesign and triage close-run controls merged to `main` through PR #5 — merge `6772e85`, branches `feat/hud-redesign` and `fix/triage-run-actions` ([§8](plans/MISSION-HUD-PLAN.md#8-outstanding))
- [x] MU/TH/UR design and shared shell across the four pages (Triage, Missions, Performance, Crew) — `942b52d`, `1d9b01a` ([ROADMAP, Shipped](ROADMAP.md#shipped))
- [ ] Browser check of the triage close controls (unverified: code reading only) ([§8](plans/MISSION-HUD-PLAN.md#8-outstanding))
- [ ] Triage category rail is mouse-only, with no keyboard access ([§8](plans/MISSION-HUD-PLAN.md#8-outstanding))
- [ ] Pager focus edge cases on the HUD pages ([§8](plans/MISSION-HUD-PLAN.md#8-outstanding))
- [x] Stray `bin/memoryd.new` (about 36 MB, gitignored) removed. Evidence: removed 2026-10-20, mission-20261020-02 step 1; identical rollback kept at `~/.local/libexec/bishop-memory/memoryd.pre-mission-20261018-03`. Rollback identity verified: the removed file's sha1 was recorded at step 1 as `1f9e921b143981614691705920ce60e44d2490c5`, and the rollback's `shasum` is the same. Operational action on the live system, not code, so there is no commit. ([§8](plans/MISSION-HUD-PLAN.md#8-outstanding))
- [ ] Live memoryd matches the plan's `b15baa93` (partial: the `bin/` binary matched `b15baa93` on 2026-10-20; the live process has not been inspected, and no hash of the 2026-10-21 redeploy is recorded here) ([header](plans/MISSION-HUD-PLAN.md))
- [x] `mission-20261009-02` closed complete/failed (abandoned by operator) 2026-10-20 via PATCH. Evidence: a live read of `GET /v1/missions/mission-20261009-02` on 2026-10-20 returns `status` complete, `outcome` failed, `closed_at` 2026-10-20 08:18:34 and `next_action` "Abandoned by operator 2026-10-20 (mission-20261020-02); not run." The read shows the state only; the PATCH method is operator-reported. Operational action on the live system, not code, so there is no commit. ([ROADMAP](ROADMAP.md#planned-work))

## Triage & grading

Plan: none in `docs/plans/`. The areas are in [ROADMAP](ROADMAP.md#findings-triage),
the wiki pages [Mission-Grading](wiki/Mission-Grading.md) and
[Developer-Triage-Agents](wiki/Developer-Triage-Agents.md).

- [x] Mission grading: `mission_grades`, `/v1/mission-grades`, `grade_claim` and `grade_write`, the `mission-grader` agent — `c83fdbf`, merged in `ae14630` ([wiki](wiki/Mission-Grading.md))
- [x] Grader packets readable as text — `5538d6d` ([wiki](wiki/Mission-Grading.md))
- [x] `/performance` page with per-harness averages and grade trend — `c83fdbf`, `internal/ui/performance.html` ([wiki](wiki/Mission-Grading.md))
- [x] `make triage-grade` and `install-triage-schedule.sh --grade-at` — `scripts/install-triage-schedule.sh` ([wiki](wiki/Mission-Grading.md))
- [x] Stale triage-run detection and operator close, `POST /v1/triage/runs/:id/operator-close` — `9756373`, `internal/api/triage.go` ([ROADMAP](ROADMAP.md#shipped))
- [x] Directive proposal reopen and amendment, `POST /v1/directive-proposals/:id/reopen` — `9756373` ([ROADMAP](ROADMAP.md#shipped))
- [x] `export-decisions.py` amends and retires in place; exit codes 0, 1, 2, 3 — `scripts/export-decisions.py` ([ROADMAP](ROADMAP.md#shipped))
- [x] OpenCode triage engine, `TRIAGE_ENGINE=opencode` — PR #4 (`7943ac8`), `54058ff`, `scripts/triage-run.sh` ([wiki](wiki/Developer-Triage-Agents.md))
- [x] Classifier batch size 20 — PR #6 (`f9d8f1f`), `2df2f46` ([ROADMAP](ROADMAP.md#shipped))
- [x] `--reclassify` option — `scripts/triage-run.sh:189-190` ([ROADMAP](ROADMAP.md#findings-triage))
- [x] Triage runner on OpenCode v2 — PR #12, squash-merge `7fc76ba` on 2026-10-22. The guard reads agents through `opencode debug agents` (identity, and a last-match-wins allowlist, with the definition file checked first); a check that cannot finish exits 4; runs use `opencode run --standalone` without `--pure`; the MCP config uses the `mcp.servers` shape with `codemode: false`; `--help` works anywhere in the arguments; the docs require OpenCode v2. Evidence: `CHANGELOG.md` [Unreleased], Changed; `scripts/triage-run.sh`; and one live `make triage-grade` run on 2026-10-22 (run 111, grade written, no 'No tool named' errors) on the mission branch commit `2f63b29`, before the squash. The live run is recorded in mission-20261021-05 DEBRIEF, step 2k (harness repository, `.claude/memory/missions/mission-20261021-05/DEBRIEF.md`) ([ROADMAP](ROADMAP.md#shipped))
- [ ] Grade runs exit 0 when they end with bishop-triage tool errors and write no grade (open: `scripts/triage-run.sh`, grade success check; the 2026-10-21 grade runs at 15:35, 17:21 and 18:00 UTC looked successful for this reason; a follow-up mission is wanted) ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] The launchd triage jobs run the development checkout `/Users/djm56/bishop-memory`, so `make mutate` and in-progress edits can reach scheduled runs (open: fix is a dedicated deploy checkout or worktree) ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] Live classify and process runs on the OpenCode v2 config shape have not been observed; only grade has (unverified: the scheduled runs are the first check) ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] The 1-second real-binary tests in `scripts/test_triage_run.py` are timing-sensitive under load (open, low; review 4b, and three failures under load at step 4a that did not recur) ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] Review follow-ups from mission-20261021-05 (open, low; listed in its DEBRIEF, `Tracker And Reality`, Follow-ups, in the harness repository at `.claude/memory/missions/mission-20261021-05/DEBRIEF.md`) ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] Triage run 89 closed as `failed` (unverified: closed on the live database; no commit, PR or path confirms it) ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] Triage run 99 is still `running` since 2026-10-19 19:20 UTC; close it as failed once the model key is confirmed (decision) ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] Pending recommendations never expire by age; a sweep is needed ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] No acceptance rate per category over time; the data is present (252 accepted, 76 declined on 2026-10-20) ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] Directive drafts: no amend, supersede or duplicate outcome (partial: the processor reads `DIRECTIVES.md`; the operator-side reopen has shipped) ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] `CONVENTIONS.md` import as directives (decision) ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] Patterns are not linked to the findings they answer ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] One-step apply, `scripts/triage-apply.py` (deferred: decision 6, no auto-apply) ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] Taxonomy tuning: `brief-writing` held 22% of findings and `class-closure` 1 on 2026-10-20 (decision, then re-classify) ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] The exporter does not check for a mission in progress (open, low) ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] Export conflicts are reported only by the exporter, not on the review page (open, low) ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] Sub-themes across runs and recurrence detection (idea only) ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] Read-only `/findings-digest` command in the harness (unverified: harness side) ([ROADMAP](ROADMAP.md#findings-triage))
- [ ] Calibration on the last 20 decisions, and per-run accepted and declined counts (unverified: not found in code on 2026-10-20) ([ROADMAP](ROADMAP.md#findings-triage))

## Reconciler

Plan: none. The items are in [ROADMAP](ROADMAP.md#reconciler) and
[ROADMAP](ROADMAP.md#harness-side-bishop-harness-repository).

- [x] An unrecognised step status is warned and counted, not dropped: `parse_steps` in `scripts/reconcile-memory.py` sends the step without a status, names the raw value in a `[WARN]` line, and counts it as `warned` (`scripts/reconcile-memory.py:583, 908-911`; PR #9, `1c4def9`) ([ROADMAP](ROADMAP.md#reconciler))
- [x] The `[flight-recorder]` summary sums to `parsed`: the buckets in `JOURNAL_SUMMED_BUCKETS` (`scripts/reconcile-memory.py`: `reconcile_journal`) add up to it, with an `out of scope` bucket and a detail line per mission; shipped with the change that carries this update, not run against the live service ([ROADMAP](ROADMAP.md#shipped))
- [x] The journal's `warned` counter is incremented once per capped mission, and the cap has its own summary category, `skipped (journal cap)` (`scripts/reconcile-memory.py`: `reconcile_journal`, the cap branch at lines 1079-1081 on `main`; `main()`, the summary note at line 1465 on `main`; PR #9, `1c4def9`) ([ROADMAP](ROADMAP.md#reconciler))
- [x] Flight-recorder paging is on the live memoryd: redeployed 2026-10-21 at 06:35 UTC (operator-reported; a live-system action, no commit; the paging code is PR #9, `1c4def9`) ([ROADMAP](ROADMAP.md#shipped))
- [x] Full journal reconcile against the live service is clean, with 13 rows added on 2026-10-21 (operator-reported; a live-system action, no commit) ([ROADMAP](ROADMAP.md#shipped))
- [ ] `normalize_empty` unescapes `\|` without a reason (open, low) ([ROADMAP](ROADMAP.md#reconciler))
- [ ] A malformed non-string `next_action` triggers a corrective PATCH (unverified; watch only) ([ROADMAP](ROADMAP.md#reconciler))
- [ ] Replaying FINDINGS.md into an empty database fails some status changes (open; not reproduced on 2026-10-20) ([ROADMAP](ROADMAP.md#reconciler))
- [ ] Harness side: `stat -f '%m'` is BSD syntax, so stale-lock reclamation fails on Linux (unverified here) ([ROADMAP](ROADMAP.md#harness-side-bishop-harness-repository))
- [ ] Harness side: the hook runs the reconciler from the working tree (unverified here) ([ROADMAP](ROADMAP.md#harness-side-bishop-harness-repository))
- [ ] Harness side: the hook leaks its `mktemp` file when signalled (unverified here) ([ROADMAP](ROADMAP.md#harness-side-bishop-harness-repository))
- [ ] Harness side: one hook comment claims more than the code guarantees (unverified here) ([ROADMAP](ROADMAP.md#harness-side-bishop-harness-repository))
- [ ] Harness side: the hook's function variables are globals (open, low; unverified here) ([ROADMAP](ROADMAP.md#harness-side-bishop-harness-repository))
- [ ] Harness side: `SIGKILL` mid-reconcile leaves the lock directory (accepted: no fix planned; step H is the backstop; unticked, so not counted as done) ([ROADMAP](ROADMAP.md#harness-side-bishop-harness-repository))
- [ ] Harness side: stderr suppressed while probing in `connect-bishop-memory.sh` (unverified here) ([ROADMAP](ROADMAP.md#harness-side-bishop-harness-repository))

## Service hardening

Plan: [plans/NETWORK-DEPLOYMENT-PLAN.md](plans/NETWORK-DEPLOYMENT-PLAN.md) §3.4 and §4 for the network items.

- [x] Document push body limit of 8 MiB per request — `internal/api/documents_push.go:28, 54` ([§3.4](plans/NETWORK-DEPLOYMENT-PLAN.md#34-other-hardening))
- [x] systemd unit runs as a dedicated user, with `ProtectSystem=strict` and `NoNewPrivileges` — `scripts/bishop-memory.service` ([§3.4](plans/NETWORK-DEPLOYMENT-PLAN.md#34-other-hardening))
- [ ] Rate limiting and lockout after repeated 401s (planned with network phase 3) ([§3.4](plans/NETWORK-DEPLOYMENT-PLAN.md#34-other-hardening))
- [ ] Global request body cap on routes other than the push route (phase 3) ([§3.4](plans/NETWORK-DEPLOYMENT-PLAN.md#34-other-hardening))
- [ ] `POST /v1/documents/sync` accepts any absolute root except `/`; narrow or remove it once clients push (planned) ([ROADMAP](ROADMAP.md#service))
- [ ] Typed store helpers and the Markdown renderer are placeholders (deferred) — `internal/store/missions.go`, `internal/renderer/renderer.go` ([ROADMAP](ROADMAP.md#service))

## Docs/README

- [ ] `docs/` holds only the roadmap and plans; user docs are in the wiki — `docs/README.md`, CHANGELOG `[Unreleased]` (partial: PROGRESS.md sits in `docs/` and `docs/README.md` describes it as build material; both are on `main` via PR #7 (250202c). The item still names only the roadmap and plans, so it stays unticked until its wording is settled) ([docs index](README.md))
- [x] HUD plan §8: the merge and push are marked done (PR #5) — `docs/plans/MISSION-HUD-PLAN.md`, on main via PR #7 (250202c) ([§8](plans/MISSION-HUD-PLAN.md#8-outstanding))
- [x] Status blocks as of 2026-10-20 on the four plans (memory insight, HUD, network, PostgreSQL), on main via PR #7 (250202c) ([plans](plans/))
- [x] `docs/README.md` says phase 2 of the HUD is deferred; it has shipped. On main via PR #7 (250202c) ([docs index](README.md))
- [x] README table list is stale: it did not match the schema, which as of 2026-10-20 has 21 tables (`grep -c 'CREATE TABLE' db/schema.sql`). On main via PR #7 (250202c) ([README](../README.md))
- [x] README describes the HUD switch as three pages; Crew is the fourth. On main via PR #7 (250202c) ([README](../README.md))
- [x] README repository map omits `/crew` and `/performance`. On main via PR #7 (250202c) ([README](../README.md))
- [x] README configuration table omits `BISHOP_MEMORY_CA_FILE`. On main via PR #7 (250202c) ([README](../README.md))
- [x] README network section omits the push routes and `scripts/push-memory.py`. On main via PR #7 (250202c) ([README](../README.md))
- [ ] CHANGELOG has no entries for `mission_lessons`, `mission_deliverables`, `agent_mistakes`, review verdicts or brief prep, and no entry of its own for the Crew page (it is named only inside the HUD redesign entry) ([CHANGELOG](../CHANGELOG.md))

## Harness portability

Plan: [plans/HARNESS-PORTABILITY-PLAN.md](plans/HARNESS-PORTABILITY-PLAN.md).
Research and the coupling inventory are done. Phase 1 is on `main` through PR #8 (`8437f32`). The reconciler-bug part of phase 2 is on `main` through PR #9 (`1c4def9`). The layout profile and every later phase are not started. On 2026-10-21 the operator adopted the plan's H-numbered recommendations (§9) for Claude Code, OpenCode and pi, and deferred Kirsch.

- [x] Phase 1: compatibility guard, golden tests on `testdata/memory`, and a seam for the Bishop values — `testdata/golden/`, `internal/importer/golden_test.go`, `internal/profile/profile.go` (PR #8, `8437f32`; commits `260bb7c^..718cb7a`) ([§7](plans/HARNESS-PORTABILITY-PLAN.md#phase-1--compatibility-guard-and-seams-no-behaviour-change))
- [ ] Phase 2: memory layout profile, and the mission-ID and unknown-status fixes in the reconciler (partial: the mission-ID and unknown-status fixes are on `main` through PR #9, `1c4def9`; the layout profile is not started) ([§7](plans/HARNESS-PORTABILITY-PLAN.md#phase-2--layout-profile-and-the-reconciler-bugs))
- [ ] Phase 3: neutral integration contract, generic names and the conformance fixture (not started) ([§7](plans/HARNESS-PORTABILITY-PLAN.md#phase-3--the-integration-contract-and-generic-names))
- [ ] Phase 4: plain Claude Code adapter (not started) ([§7](plans/HARNESS-PORTABILITY-PLAN.md#phase-4--plain-claude-code-adapter))
- [ ] Phase 5: OpenCode adapter, including the `AGENTS.md` loading test (not started) ([§7](plans/HARNESS-PORTABILITY-PLAN.md#phase-5--opencode-adapter))
- [ ] Phase 6: pi adapter, with `exposure: "direct"` for the memory tools (not started) ([§7](plans/HARNESS-PORTABILITY-PLAN.md#phase-6--pi-adapter))
- [ ] Phase 7: Kirsch adapter (deferred by the operator on 2026-10-21; not started) ([§7](plans/HARNESS-PORTABILITY-PLAN.md#phase-7--kirsch-adapter), [§11](plans/HARNESS-PORTABILITY-PLAN.md#11-open-questions-for-the-operator))
- [ ] Phase 8: packaging, a Claude Code plugin and marketplace, a pi package, and `install.sh --harness` (not started) ([§7](plans/HARNESS-PORTABILITY-PLAN.md#phase-8--packaging), [§6](plans/HARNESS-PORTABILITY-PLAN.md#6-packaging-and-distribution))

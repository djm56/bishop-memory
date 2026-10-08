# Developer: Testing and CI

## The gate

Every change must pass, locally and in CI (`.github/workflows/ci.yml` on Linux):

```bash
go build ./...
go vet ./...
gofmt -l .            # must print nothing
go test ./... -count=1
go test -race ./... -count=1
```

`make test`, `make vet`, `make fmt` wrap the pieces. Python scripts have no test suite; they are verified by `--dry-run` against a copy of the database and the fixture tree (`testdata/memory`).

## What the suites prove

| Package | Suite | Proves |
|---|---|---|
| `internal/store` | `schema_test.go` | The SQL splitter handles comments, strings, trailing statements |
| `internal/store` | `migrate_test.go` | A database created before the triage tables boots through `ApplySchema` + `EnsureColumns`, gains the columns and the index, and keeps its rows |
| `internal/api` | `mission_steps_test.go` | Step upsert semantics, field preservation, audit notes from resulting state, the unique index migration and its duplicate detection |
| `internal/api` | `allocate_test.go` | Allocation is global across harnesses, resets per day, ignores non-sequence ids, widens past 99, is unique under concurrency |
| `internal/api` | `triage_test.go` | Decision transitions and their refusals, recommendation closure, classification upsert and slug validation, directive ratification allocating `DIR-NNN` and applying evidence, length budget, category rotation, the pending view, directive and harness upserts |
| `internal/api` | `search_test.go` | Punctuated words are matched literally instead of returning 500; embedded quotes are doubled, not concatenated; combining marks count as word runes; a schema fault returns 500 rather than being blamed on the caller; no input ever produces a 500 or echoes `q` |
| `internal/api` | `mission_links_test.go` | A live mission's steps get `started_at` and `ended_at`, a caller's value wins, a completed mission's replayed steps get neither; a `step-sync` row becomes the step summary and one for an unknown step changes nothing; a finding takes its harness's open mission, but not when it predates it or none is open; a sync with no root imports every registered harness tagged with its name, and a mission update imports that mission's new files; the HUD board's counts and filters, the mission detail's linked records, and `GET /v1/findings?mission_id=` |
| `internal/api` | `network_test.go` | `TestAPIKeyGuard`: on loopback with no keys `/v1` is open; on a network address with no keys it answers 401, and opens only with `BISHOP_ALLOW_NO_AUTH`; once a key exists both refuse no key and a wrong key and accept the right one, as `Bearer` or `X-API-Key`; `/healthz`, both pages and the icon need no key. `TestDocumentPush`: a pushed brief is filed under its mission and harness, an agent becomes crew, bad `rel_path`s and non-Markdown are refused with 400, hashes come back by `source_path`, and delete removes the document and its search row |
| `internal/auth` | `keys_test.go` | A missing keys file is an empty store; an added key looks generated (`bm_`), the file is mode 0600 and holds no key in clear, the key verifies by name and others do not; duplicate and malformed names are refused; a revoked key stops verifying without a restart; revoking a missing name fails; `BISHOP_API_KEY` makes the store non-empty and verifies as `env`; a malformed file is refused |
| `internal/api` | `errors_test.go`, `events_test.go` | The error contract never echoes request text; the filesystem root is refused for sync |
| `cmd/mcpd` | `main_test.go` | Dot-segment ids are refused before any request; every tool's method and path matches exactly one registered route |
| `cmd/memoryd` | `main_test.go` | SIGTERM shuts down gracefully (subprocess test under `t.TempDir()`) |
| `internal/importer` | `mission_test.go` | Mission documents get `mission_id`, every document its `harness`; an unchanged file is re-tagged and its stale kind corrected, and a later sync with no harness keeps the stored one; a root pointed at a repository skips its `.claude` and `.git` trees; `SyncMission` imports only the named mission's files and refuses an id that is not one path segment; `SyncAgents` makes one crew row per name, and a second harness defining the same agent updates it; `CrewName` normalises `@Hicks`, `hicks`, `harness:hicks` |
| `internal/ui` | `ui_test.go` | Both pages are served as HTML, link both icons, and carry the Triage / Missions switch |
| `internal/importer`, `internal/middleware`, `internal/config` | — | Import kinds and hashing, recovery and request id, loopback detection |

## Conventions

- **Boot the real schema.** `newTriageTestRouter` opens a temp database through `store.Open`, applies `db/schema.sql` and `EnsureColumns`, and returns the live router. Tests that hand-copy a `CREATE TABLE` (the older mission-steps suite) can drift from the shipped schema; prefer the real file.
- **Exercise the refusal.** Each handler test covers at least one 400 and one 404, not only the success path; the decision tests check that a reject without a note and an illegal transition are refused.
- **Assert the side effects.** Audit rows in `flight_recorder`, recommendation `state`, `last_processed_at`, not just the response code.
- **Keep tests hermetic.** No network, no `data/memory.db`, temp directories only; `t.Cleanup` closes databases.
- **Name the defect.** Where a test exists because of a shipped bug, its docblock says which (the migrate test names the index-in-schema.sql failure).

## Verifying scripts and agents

```bash
# Throwaway service on a copy of the live database
sqlite3 data/memory.db ".backup '/tmp/copy.db'"
PORT=8788 DB_PATH=/tmp/copy.db bin/memoryd &
export BISHOP_MEMORY_URL=http://127.0.0.1:8788

scripts/reconcile-memory.py --root /path/to/.claude/memory --harness x --dry-run
scripts/export-decisions.py --harness x --dry-run
scripts/backfill-mission-links.py --db /tmp/copy.db --verbose      # dry run against the copy
scripts/clean-scratch.py                                            # dry run against $BISHOP_MEMORY_URL; lists, moves nothing
scripts/triage-run.sh classify --dry-run
make triage-classify            # a real classifier run against the copy
```

Diff the copy of a harness tree before and after an export; the only changed lines should be status lines and appended directives.

## CI shape

One job, Ubuntu, Go from `go.mod`, module cache on, no secrets, no publishing. A failure in `gofmt -l .` prints the offending files.

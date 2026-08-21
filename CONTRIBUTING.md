# Contributing to bishop-memory

## Building and running locally

```bash
go build ./...          # or: make build   (produces bin/memoryd)
make run                # go run ./cmd/memoryd
make init-db             # create data/memory.db from db/schema.sql
make health              # curl the running service's /healthz
```

Copy `.env.example` to `.env` and edit locally if you need to override a
default (`HTTP_HOST`, `PORT`, `DB_PATH`, `APP_ENV`, `LOG_LEVEL`). `.env`
is git-ignored.

Cross-compiled Linux binaries (`bin/memoryd-linux-amd64`,
`bin/memoryd-linux-arm64`) are produced by `make dist-linux-amd64`,
`make dist-linux-arm64`, or `make dist-linux` for both — no C toolchain
required, since the SQLite driver (`modernc.org/sqlite`) is pure Go.

## Verification gate — every change must pass all of these

```bash
go build ./...
go vet ./...
gofmt -l .            # must print nothing
go test ./... -count=1
go test -race ./... -count=1
```

`gofmt -l .` prints the files that are *not* formatted correctly; a
clean pass prints nothing. Run `gofmt -w .` (or `make fmt`) to fix
formatting before committing. The GitHub Actions workflow at
`.github/workflows/ci.yml` runs this same gate on every push and pull
request — a change that fails any one of these locally will fail CI
too.

## Docblock convention

This codebase documents itself unusually thoroughly, and new code is
expected to match the existing style rather than introduce a lighter
one:

- **Every file** opens with a package-level comment in the form
  `// Package <name> — <what this file/package is responsible for>`,
  even in files that share a package with other files already
  carrying that comment (see `internal/api/*.go` or `internal/store/*.go`
  for examples — each file's package comment describes that specific
  file's slice of the package, not the whole package generically).
- **Every function** — exported or not — gets a doc comment starting
  with the function's name, describing what it does and, where the
  logic isn't obvious from the code alone, *why*: the edge case being
  guarded against, the invariant being preserved, or the review
  finding that shaped it. Grep the existing handlers
  (`internal/api/tasks.go`, `internal/api/events.go`) or
  `cmd/memoryd/main.go` for the pattern before writing a new one.
- Non-obvious logic gets an inline comment at the point of the logic,
  not only in the enclosing function's docblock.
- Where a change fixes a defect a code review found, or exists because
  of a documented convention (`CONVENTIONS.md`), the comment says so
  explicitly (e.g. "Step 4 review fix (Review B, C3)", "CONV-033") so
  a future reader can trace *why* the code looks the way it does
  without archaeology.

A pull request that is functionally correct but skips this level of
comment density will be asked to add it before merge.

## Deliberate stubs — do not "helpfully" implement these

Four files are intentionally incomplete. Each says so in its own
package comment, but it is worth stating plainly here too, because a
contributor skimming the tree can otherwise mistake "empty" for
"forgotten":

- `internal/store/tasks.go`
- `internal/store/events.go`
- `internal/store/documents.go`
- `internal/renderer/renderer.go`

These are Phase 2/Phase 3 groundwork placeholders (see each file's own
`// TODO: implement in Phase 2/3` comment and `README.md`'s roadmap
section). The current architecture deliberately keeps the SQL inline
in `internal/api/*.go` for Phase 2; moving it into typed store helpers,
and building the Markdown/JSONL renderer, are separately-scoped future
phases with their own design decisions still to be made. **Do not
implement these as part of an unrelated change** — if you have a real
need to move this work forward, open that as its own scoped
change/discussion first rather than folding it into a fix for
something else.

## Pull requests

- Keep changes scoped — a bug fix and a refactor in the same PR make
  both harder to review.
- Add or update tests for the code path you touch; `internal/api` and
  `internal/config`'s existing `*_test.go` files show the expected
  style (table-driven where there are several similar cases, real
  `httptest`/`gin.CreateTestContext` exercises rather than only
  unit-testing helper functions in isolation).
- Run the full verification gate above before opening the PR.

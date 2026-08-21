# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
This project has not yet made a tagged release, so every change to date
lives under `[Unreleased]` — no version numbers or dates are invented
here ahead of an actual release.

## [Unreleased]

### Security

- **HTTP listener now defaults to loopback-only (`127.0.0.1`).**
  `internal/config/config.go` previously composed the listen address as
  `":" + PORT`, which binds every interface on the host. bishop-memory
  has no authentication of any kind — every task/event endpoint is open
  read/write to any caller that can reach it, and
  `POST /v1/documents/sync` accepts a caller-supplied filesystem root —
  so binding all interfaces contradicted the "locally-bound" security
  model every doc in this repository already describes. A new
  `HTTP_HOST` variable (default `127.0.0.1`) lets an operator still
  choose a different bind explicitly; `cmd/memoryd/main.go` logs a
  loud startup warning naming exactly what becomes reachable whenever
  the configured bind is not loopback. See `.env.example` and
  `internal/config/config.go`'s `Config.HTTPHost` docblock.

### Fixed

- **`validationError`'s CONV-033 fix had over-corrected and collapsed
  every hand-authored error message into one generic string.** An
  earlier fix in this same task closed a real defect — a JSON
  type-mismatch error's `Error()` text could echo a raw
  request-supplied scalar back to the caller — but its fallback branch
  caught every other error too, discarding distinct, already-safe
  messages (`"q is required"`, `"status must be one of: ..."`,
  `"root must not be the filesystem root"`, the `event_type`/`summary`
  empty-value messages, and the wrapped FTS5 syntax-error text) across
  every handler in `internal/api`. `validationError` now distinguishes
  three cases explicitly: `validator.ValidationErrors` (structured
  field/tag list, unchanged), a genuine `*json.UnmarshalTypeError` /
  `*json.SyntaxError` (the one case CONV-033 actually applies to — kept
  as a static message), and everything else (passed through verbatim,
  since none of those messages are request-derived). `docs/api-contract.md`'s
  search-endpoint error example was also corrected — it had conflated
  the "`q` missing" and "malformed FTS5 syntax" cases under one shared
  example, when they return two distinct messages.

- **Linux installer overly-broad permission grant.** `scripts/install-daemon-linux.sh`
  previously ran `chmod -R o+rX "$BISHOP_ROOT"` on every invocation,
  recursively making the entire checkout world-readable — including any
  `.env` file the operator may have placed there (documented in
  `.gitignore` as holding secrets like PORT, DB_PATH, and future API
  keys). The revised version grants **only the dedicated service account**
  (`bishop-memory` group) **exactly the permissions it needs**: read/execute on
  `bin/memoryd`, read on `db/schema.sql`, and traversal on their parent
  directories. The recursive, "other"-class, and re-widening-on-every-run
  approaches are all removed.

- **Linux installer fails silently if the service crashes on startup.**
  `scripts/install-daemon-linux.sh` previously ran `systemctl restart`
  and returned success if systemd accepted the request, but for `Type=simple`
  units, systemd does not wait for the process to prove itself before
  returning — a unit that starts and immediately exits (hardening
  interaction, permission problem, bind failure) would silently report
  success. The revised version includes a post-start health check that
  polls `systemctl is-active` with a retry loop (up to 10 attempts), and
  on failure logs the unit's recent `journalctl` output before failing
  the entire install.

### Added

- **systemd unit + Linux installer** (`scripts/bishop-memory.service`,
  `scripts/install-daemon-linux.sh`) — the Linux/systemd counterpart to
  the existing macOS/launchd installer. Runs as a dedicated, installer-created
  non-root system account; binds loopback only; every hardening
  directive in the unit carries its own justification comment; the
  installer detects Linux, systemd (via `/run/systemd/system`), and CPU
  architecture rather than assuming any of them, and never elevates its
  own privilege — it prints the exact root-requiring commands rather
  than running `sudo` itself.
- **Cross-compiled Linux binaries.** `make dist-linux-amd64`,
  `make dist-linux-arm64`, and `make dist-linux` build static Linux
  binaries for both architectures from any host, with no C toolchain —
  `modernc.org/sqlite`, this project's only otherwise-cgo-shaped
  dependency, is pure Go, so `CGO_ENABLED=0` cross-compilation is
  clean.
- **Repository hygiene for the first commit**: `LICENSE` (MIT),
  `CONTRIBUTING.md` (build/test/verification-gate instructions and the
  project's docblock convention), and `.github/workflows/ci.yml`
  (`go build`, `go vet`, `gofmt -l`, `go test`, `go test -race` on every
  push/PR, matching the Go version pinned in `go.mod`).

### Changed

- `.gitignore` extended to cover the new cross-compiled Linux binaries
  and other build/test artefacts ahead of this project's first commit.

---

Earlier in this task (not previously changelogged): a three-part code
review (persistence, HTTP, MCP/scripts) found and fixed five CRITICAL
defects — an empty-body 500 from `gin.Recovery`, `createTaskHandler`
returning 409 for every insert failure instead of only real conflicts,
the `validationError` echo-scalar issue this file's own "Fixed" section
above continues the story of, `mcpd`'s HTTP adapter not guarding
dot-segment task IDs before building upstream URLs, and `memoryd` never
installing a signal handler so its deferred `db.Close()` was
unreachable on every real exit path. All five now have regression
tests; `cmd/memoryd/main.go` now shuts down gracefully on
`SIGINT`/`SIGTERM` via `signal.NotifyContext` + `http.Server.Shutdown`.

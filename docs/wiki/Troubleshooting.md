# Troubleshooting

Symptom first, then cause, then fix. Commands assume the bishop-memory checkout as the working directory.

## Service

**`make health` says connection refused.** The daemon is not running or listens elsewhere. `launchctl list | grep bishop-memory` (macOS) or `systemctl status bishop-memory` (Linux); `lsof -i :8787`. Logs: `~/Library/Logs/bishop-memory/memoryd.err.log` or `journalctl -u bishop-memory`.

**`healthz` returns `schema incomplete` with a list of tables.** The database predates the running binary's schema, or the daemon was not restarted after `git pull`. Restart it; tables and additive columns are created at boot. If it persists, the named tables tell you which `CREATE` failed — check the error log.

**Boot fails with `no such column`.** A schema change declared an index on an additive column in `db/schema.sql`. Indexes on additive columns belong in `internal/store/migrate.go` (`additiveIndexes`); see [Developer: Database](Developer-Database).

**`HTTP_HOST` warning at startup.** The service is bound to a non-loopback address and has no authentication. Unless that was deliberate, set `HTTP_HOST=127.0.0.1` and use an SSH tunnel.

**launchd job dies with exit 78 (EX_CONFIG).** A log path under `/Volumes`. launchd opens the log files before the process starts and the agent context is denied the volume. Re-run `install-daemon.sh --log-dir` with a path on the internal disk.

**launchd job "running" but never answers.** The binary is under `/Volumes`; dyld blocks waiting on a TCC grant that cannot be answered. The installer stages the binary under `~/.local/libexec` for this reason; use the default `--exec-dir`.

## MCP

**The crew sees no `bishop-memory` tools.** The `.mcp.json` server was not approved after the restart, or `bin/mcpd` does not exist at the path in `.mcp.json`. Restart Claude Code and approve; `make build-mcpd`.

**`mission_allocate` says `harness and title must be non-empty`.** The harness's `.mcp.json` does not set `BISHOP_HARNESS`, so `mcpd` had nothing to fill in. Re-run `.claude/connect-bishop-memory.sh` in the harness.

**Two harnesses got the same mission id.** They share a `BISHOP_HARNESS` value, or one is in standalone mode deriving ids locally. Each harness needs its own conf and name; central mode for all of them.

**`memory_search` returns nothing.** Documents were never imported: `sqlite3 data/memory.db 'SELECT COUNT(*) FROM documents'`. Run `documents_sync` with the memory root, or the reconciler. Hyphenated terms such as mission ids are matched as literal phrases automatically; a trailing `*` on a plain word is a prefix match; an unterminated `"` returns 400.

## Reconciler

**`[steps] … unchanged` but PROGRESS.md and the database disagree.** A step status outside `pending|in-progress|done|failed` in `PROGRESS.md` is dropped silently (known, recorded in `docs/ROADMAP.md`). Fix the status in the file.

**Journal reconcile skips a mission at the cap.** A mission with exactly 100 journal rows cannot be told from a truncated list, so `--include-journal` skips it and says so. The hook mirrors rows live; the skip is only a backstop gap.

**Status mirrored every run.** The file's `Status` disagrees with the service and the decision route keeps being called. That is correct behaviour while they differ; export the service's decision or fix the file.

## Triage

**`triage-run.sh` exits 2.** It printed why: service down, `claude` or `opencode` not on PATH (the PATH baked into the launchd plist is printed by `install-triage-schedule.sh`), `bin/mcpd` could not be built, a model id that does not fit the engine (`sonnet` under opencode, `opencode-go/...` under claude), or OpenCode did not resolve the agent to its `.opencode/agents/` twin.

**The JSON result says `Not logged in · Please run /login`, or the launchd job hangs before its first tool call.** No credentials. A terminal shell inside Claude Code carries an `ANTHROPIC_API_KEY`, so manual runs work; launchd has none. Put the key in `.env` (mode 0600) in the checkout. To bill your Claude plan instead of the API, put `CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token` there and remove the API key, which wins when both are set. The runner warns at start when neither `ANTHROPIC_API_KEY` nor `CLAUDE_CODE_OAUTH_TOKEN` is set. The opencode engine needs neither.

**The log says `agent stopped after TRIAGE_TIMEOUT_MIN=45 minutes`.** The run was stopped at the limit so the next night is not blocked behind it. Its run row stays `running`. Open the result file to see where it stalled; raise the limit in `.env` only if a large backlog genuinely needs longer.

**An opencode run fails with `Unexpected server error`.** OpenCode's provider refused the model. Check that the id appears in `opencode models` and runs by hand: `opencode run -m opencode-go/glm-5.2 "say ok" </dev/null`. Without `</dev/null`, `opencode run` waits for input on stdin.

**A run row stays `running` on the Runs tab.** The agent never reached `triage_run_finish`: it hit the turn or budget cap, or crashed. Open the JSON result in the log directory; `result` and `is_error` say what happened. The next run opens a fresh row; the stale one is harmless.

**`triage_classify` returns 400 naming an item.** The classifier used a slug that is not an active category, or a finding id that does not exist. The whole batch was refused. Re-seed the categories if you renamed one.

**A finding shows `harness: ?` on the page.** It matched no harness's `FINDINGS.md` during the backfill (edited after import, or from a harness not registered). Set it: `curl -X PUT http://127.0.0.1:8787/v1/findings/<id>/harness -d '{"harness":"kirsch"}'`.

**`export-decisions.py` exits 2.** The harness is not registered (run the reconciler once with `--harness`) or its memory root is not a directory (external volume unmounted).

**`export-decisions.py` exits 3 with CONFLICT lines.** The file already carries a non-proposed status that differs from the service's. The file wins. Reconcile to mirror it, then reopen and re-decide on the page if the file was wrong.

**The exporter changed the wrong entry.** It cannot: it matches by the natural key `(finding_date, target, suggestion)` computed with the reconciler's own parser, and a backup of the file is in `<memory_root>/workspace/triage-backups/`. If two entries share a key the second is a duplicate the reconciler would also have collapsed.

**Everything landed in `brief-writing` and `mission-planning`.** The classifier leans on those two when a finding is about a brief or a process. Tighten the neighbouring category descriptions in `db/finding-categories.json`, `make triage-seed`, then `make triage-classify RECLASSIFY=brief-writing`.

## Wiki

**`make wiki-publish` says the wiki repository does not exist.** GitHub creates `<repo>.wiki.git` only when the first page is made in the web UI. Open the repository's Wiki tab, create any page, run the target again.

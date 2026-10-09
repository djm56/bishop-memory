# Troubleshooting

Symptom first, then cause, then fix. Commands assume the bishop-memory checkout as the working directory.

## Service

**`make health` says connection refused.** The daemon is not running or listens elsewhere. `launchctl list | grep bishop-memory` (macOS) or `systemctl status bishop-memory` (Linux); `lsof -i :8787`. Logs: `~/Library/Logs/bishop-memory/memoryd.err.log` or `journalctl -u bishop-memory`.

**`healthz` returns `schema incomplete` with a list of tables.** The database predates the running binary's schema, or the daemon was not restarted after `git pull`. Restart it; tables and additive columns are created at boot. If it persists, the named tables tell you which `CREATE` failed — check the error log.

**Boot fails with `no such column`.** A schema change declared an index on an additive column in `db/schema.sql`. Indexes on additive columns belong in `internal/store/migrate.go` (`additiveIndexes`); see [Developer: Database](Developer-Database).

**`WARNING — serving plain HTTP on …` at startup.** The service listens on a network address without TLS, so API keys cross the network in clear unless the network itself is encrypted. Fine on Tailscale, WireGuard or an SSH tunnel; on a plain LAN set `TLS_CERT_FILE` and `TLS_KEY_FILE` or put a proxy in front. See [Server Install](Server-Install#1-choose-how-clients-reach-the-server).

**`memoryd` will not start: `HTTP_HOST=… is not loopback and no API key is configured`.** On a network address it refuses to run open. Create a key against the service's keys file (`memoryd keys add <name>` with the service's `DB_PATH`; the message names the file), then start it, or set `HTTP_HOST=127.0.0.1`. [Server Install](Server-Install#6-api-keys) has the commands per OS.

**Every `/v1` call returns 401 `{"error":"unauthorized",…}`.** A key exists on the server and the caller sent none, or one the server does not know. On the client, check `~/.config/bishop-memory/client.env` (`scripts/install.sh client` rewrites it and tests the key). A value already in the environment wins over that file, so an old `BISHOP_MEMORY_API_KEY` or `BISHOP_MEMORY_URL` exported in a shell, a harness's `.mcp.json` or a launchd plist hides the right one. On the server, `memoryd keys list` shows which names exist, and the access log names the key behind each accepted request (`key=<name>`). The pages prompt for a key on a 401; **Forget key** clears a wrong one.

**The harness hook's journal rows stop arriving after a key was added, while `mcpd` tools still work.** `mcpd` and the reconciler read the key from `client.env`; the hook's own `curl` calls must send it themselves. See [Server Install](Server-Install#8-changes-inside-each-harness).

**A client cannot connect to a server install.** Check, in order: the service is running; it listens on the address you expect; on Linux, the unit's `IPAddressAllow` admits the client's network (the shipped unit admits loopback only); the firewall; the client trusts the TLS certificate. [Server Install](Server-Install#troubleshooting) goes through each.

**launchd job dies with exit 78 (EX_CONFIG).** A log path under `/Volumes`. launchd opens the log files before the process starts and the agent context is denied the volume. Re-run `install-daemon.sh --log-dir` with a path on the internal disk.

**launchd job "running" but never answers.** The binary is under `/Volumes`; dyld blocks waiting on a TCC grant that cannot be answered. The installer stages the binary under `~/.local/libexec` for this reason; use the default `--exec-dir`.

## MCP

**The crew sees no `bishop-memory` tools.** The `.mcp.json` server was not approved after the restart, or `bin/mcpd` does not exist at the path in `.mcp.json`. Restart Claude Code and approve; `make build-mcpd`.

**`mission_allocate` says `harness and title must be non-empty`.** The harness's `.mcp.json` does not set `BISHOP_HARNESS`, so `mcpd` had nothing to fill in. Re-run `.claude/connect-bishop-memory.sh` in the harness.

**Two harnesses got the same mission id.** They share a `BISHOP_HARNESS` value, or one is in standalone mode deriving ids locally. Each harness needs its own conf and name; central mode for all of them.

**`memory_search` returns nothing.** Documents were never imported: `sqlite3 data/memory.db 'SELECT COUNT(*) FROM documents'`. Run `documents_sync` with the memory root, or the reconciler. Against a service on another machine, `documents_sync` cannot see the files; run the reconciler with `--harness`, or `scripts/push-memory.py`. Hyphenated terms such as mission ids are matched as literal phrases automatically; a trailing `*` on a plain word is a prefix match; an unterminated `"` returns 400.

## Reconciler

**`[steps] … unchanged` but PROGRESS.md and the database disagree.** A step status outside `pending|in-progress|done|failed` in `PROGRESS.md` is dropped silently (known, recorded in `docs/ROADMAP.md`). Fix the status in the file.

**Journal reconcile skips a mission at the cap.** A mission with exactly 100 journal rows cannot be told from a truncated list, so `--include-journal` skips it and says so. The hook mirrors rows live; the skip is only a backstop gap.

**Status mirrored every run.** The file's `Status` disagrees with the service and the decision route keeps being called. That is correct behaviour while they differ; export the service's decision or fix the file.

## Triage

**`triage-run.sh` exits 2.** It printed why: service down, `claude` or `opencode` not on PATH (the PATH baked into the launchd plist is printed by `install-triage-schedule.sh`), `bin/mcpd` could not be built, a model id that does not fit the engine (`sonnet` under opencode, `opencode-go/...` under claude), or OpenCode did not resolve the agent to its `.opencode/agents/` twin.

**The JSON result says `Not logged in · Please run /login`, or the launchd job hangs before its first tool call.** No credentials. A terminal shell inside Claude Code carries an `ANTHROPIC_API_KEY`, so manual runs work; launchd has none. Put the key in `.env` (mode 0600) in the checkout. To bill your Claude plan instead of the API, put `CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token` there and remove the API key, which wins when both are set. The runner warns at start when neither `ANTHROPIC_API_KEY` nor `CLAUDE_CODE_OAUTH_TOKEN` is set. The opencode engine needs neither.

**The log says `agent stopped after TRIAGE_TIMEOUT_MIN=45 minutes`.** The run was stopped at the limit so the next night is not blocked behind it. Its run row stays `running`. Open the result file to see where it stalled; raise the limit in `.env` only if a large backlog genuinely needs longer.

**An opencode run fails with `Unexpected server error`.** OpenCode's provider refused the model. Check that the id appears in `opencode models` and runs by hand: `opencode run -m opencode-go/glm-5.2 "say ok" </dev/null`. Without `</dev/null`, `opencode run` waits for input on stdin.

**The log line starts `ERROR run closed as failed`.** The agent gave up and said why in the run's notes on the Runs tab. The usual cause is a tool call that failed twice, which the doctrine says to stop on rather than work around.

**A run row stays `running` on the Runs tab.** The agent never reached `triage_run_finish`: it hit the turn or budget cap, or crashed. Open the JSON result in the log directory; `result` and `is_error` say what happened. The next run opens a fresh row; the stale one is harmless.

**`triage_classify` returns 400 naming an item.** The classifier used a slug that is not an active category, or a finding id that does not exist. The whole batch was refused. Re-seed the categories if you renamed one.

**A finding shows `harness: ?` on the page.** It matched no harness's `FINDINGS.md` during the backfill (edited after import, or from a harness not registered). Set it: `curl -X PUT http://127.0.0.1:8787/v1/findings/<id>/harness -d '{"harness":"kirsch"}'`.

**`export-decisions.py` exits 2.** The harness is not registered (run the reconciler once with `--harness`) or its memory root is not a directory (external volume unmounted).

**`export-decisions.py` exits 3 with CONFLICT lines.** The file already carries a non-proposed status that differs from the service's. The file wins. Reconcile to mirror it, then reopen and re-decide on the page if the file was wrong.

**The exporter changed the wrong entry.** It cannot: it matches by the natural key `(finding_date, target, suggestion)` computed with the reconciler's own parser, and a backup of the file is in `<memory_root>/workspace/triage-backups/`. If two entries share a key the second is a duplicate the reconciler would also have collapsed.

**Everything landed in `brief-writing` and `mission-planning`.** The classifier leans on those two when a finding is about a brief or a process. Tighten the neighbouring category descriptions in `db/finding-categories.json`, `make triage-seed`, then `make triage-classify RECLASSIFY=brief-writing`.

## Mission HUD

**A mission shows "No brief imported".** Its `BRIEF.md` is not in `documents` with its `mission_id`. Either the harness has no registered memory root (`curl -s http://127.0.0.1:8787/v1/harnesses | jq`), or no sync has run since the file was written or since the upgrade. Register it (`curl -X PUT http://127.0.0.1:8787/v1/harnesses/<name> -d '{"memory_root":"/abs/path/.claude/memory"}'`, or reconcile once with `--harness`), then `curl -X POST http://127.0.0.1:8787/v1/documents/sync`.

**A harness's missions are missing documents although the harness is registered.** A mission update re-imports a mission's files only when `missions.harness` names a registered harness. Missions created through `POST /v1/missions` (the reconciler) rather than `mission_allocate` have no harness, so only a full sync reaches them. Run `POST /v1/documents/sync`. If the harness was registered under a second name for the same memory root (`kirsch-opencode` and `kirschopencode`), merge them with `make mission-links RENAME="kirsch-opencode=kirschopencode" APPLY=1`.

**Document kinds such as `.claude` or `.opencode` appear in search.** Left by a sync once pointed at a repository instead of its memory root. The walker now skips hidden directories, so it cannot happen again; `make mission-links APPLY=1` deletes the rows that lie under no registered memory root.

**A mission has no findings, or steps with no times.** Rows recorded before the HUD carry no link. Run `make mission-links` to see what it would fill, then with `APPLY=1`. A finding that fits more than one mission is left unlinked and listed; set its mission by hand if it matters.

**The step list shows agents but the Crew section is empty.** The harness keeps no `agents/` directory beside its memory root, or no sync has run since the upgrade. The sync reads `<checkout>/.claude/agents/*.md` for a root at `<checkout>/.claude/memory`.

**Search returns old scratch files, or the database keeps growing.** Workspace scratch under `<memory root>/workspace/` is imported like any other file. `make scratch-clean` lists files older than 30 days; `make scratch-clean APPLY=1` moves them to the Trash and drops them from search. Run it on the machine that holds the harness checkouts; it skips roots that do not exist there.

## Wiki

**`make wiki-publish` says the wiki repository does not exist.** GitHub creates `<repo>.wiki.git` only when the first page is made in the web UI. Open the repository's Wiki tab, create any page, run the target again.

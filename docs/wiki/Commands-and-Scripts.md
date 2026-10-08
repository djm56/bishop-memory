# Commands and Scripts

Every `make` target and every script, with flags, environment, exit codes and when you would use it. Paths are relative to the bishop-memory checkout.

## Make targets

| Target | What it does | Notes |
|---|---|---|
| `make run` | Run `memoryd` in the foreground from source | Reads `.env`; default `127.0.0.1:8787`, `data/memory.db` |
| `make build` | Build `bin/memoryd` | |
| `make build-mcpd` | Build `bin/mcpd` | Every harness `.mcp.json` and the triage runner execute this path; rebuild after any change to `cmd/mcpd` |
| `make dist-linux-amd64` / `dist-linux-arm64` / `dist-linux` | Cross-compile static Linux binaries into `bin/` | No C toolchain; `CGO_ENABLED=0` |
| `make test` / `make vet` / `make fmt` / `make tidy` | The verification gate pieces | CI runs build, vet, gofmt, test, test -race |
| `make init-db` | Create the database from `db/schema.sql` with `sqlite3` | Lacks the unique steps index and additive columns, which `memoryd` adds at boot |
| `make reset-db` | Delete the database and its WAL files, then `init-db` | Destructive |
| `make health` | `curl /healthz` through `jq` | |
| `make triage-seed` | Load `db/finding-categories.json` | Idempotent upsert by slug |
| `make triage-backfill REGISTER="name=/abs/.claude/memory …"` | Register harnesses and set `findings.harness` on old rows | Run once per service |
| `make triage-classify [RECLASSIFY=slug]` | Run the classifier now | Exits 0 with nothing to do when everything is classified |
| `make triage-process [CATEGORY=slug] [LIMIT=n]` | Run the processor now | Default: next category in rotation, 30 items |
| `make triage-export` | Write decisions to every registered harness's Markdown | Exit 3 means a conflict was skipped |
| `make triage-review` | Open the review page | |
| `make triage-install` / `triage-uninstall` | Install or remove the nightly launchd jobs | macOS |
| `make mission-links [RENAME="old=new …"] [APPLY=1]` | One-off backfill of mission links: findings, step times and summaries, orphan documents, stray harness names | Dry run unless `APPLY=1`; run `POST /v1/documents/sync` first |
| `make scratch-clean [DAYS=30] [HARNESS=name] [APPLY=1]` | Move harness workspace files older than `DAYS` to the macOS Trash and drop them from search | Dry run unless `APPLY=1`; default every registered harness |
| `make wiki-publish` | Push `docs/wiki/` (pages and images) to the GitHub wiki | Needs the wiki created once in the web UI |
| `make screenshots` | Regenerate the review-page and mission HUD screenshots in `docs/wiki/images/` from demo data | Never touches your service or database |

All `triage-*` targets accept `BISHOP_MEMORY_URL=http://127.0.0.1:8788` to aim at another instance. `mission-links` and `scratch-clean` open the database file directly instead; both take `DB=` (default `data/memory.db`).

## Service scripts

### `scripts/install-daemon.sh` (macOS)

```
scripts/install-daemon.sh [--dry-run] [--memory-root <path>] [--log-dir <path>] [--exec-dir <path>]
```

Builds `memoryd`, stages it under `~/.local/libexec/bishop-memory` (the internal disk; launchd hangs on a binary under `/Volumes`), renders and installs `~/Library/LaunchAgents/com.bishop-memory.memoryd.plist`, boots the job out and back in, and polls `/healthz`. `--memory-root` is the default root for `documents_sync`. Idempotent; re-run to upgrade. Exit 64 for a bad flag, 66 for a missing directory, 1 for a failed bootstrap.

### `scripts/install-daemon-linux.sh` (Linux)

```
sudo scripts/install-daemon-linux.sh [--dry-run]
```

Creates the `bishop-memory` system user, installs `/etc/systemd/system/bishop-memory.service`, enables and starts it. Run without root it builds and prints the root-only commands instead.

## Reconciliation

### `scripts/reconcile-memory.py`

```
scripts/reconcile-memory.py --root <.claude/memory> [--harness NAME] [--url URL] [--dry-run]
                            [--include-journal] [--skip-steps] [--no-sync-documents]
```

Mirrors a harness's Markdown into the service with natural-key dedupe, so running it twice changes nothing. In order: registers the harness (`--harness`), syncs documents, missions, steps, journal (opt-in), findings (creating missing ones and mirroring a hand-set `Status` through the decision route), directives, patterns, service records. `--harness` has no environment fallback on purpose; pass it explicitly. Exit 0 success, 1 something failed, 66 bad root.

`scripts/backfill-memory.py` is a passthrough to the same script kept for old invocations.

## Findings triage

### `scripts/triage-run.sh`

```
scripts/triage-run.sh classify [--reclassify SLUG] [--engine claude|opencode] [--model M] [--dry-run]
scripts/triage-run.sh process  [--category SLUG] [--limit N] [--engine claude|opencode] [--model M] [--dry-run]
```

The single entry point for both agents; launchd and `make` call it. Sources `.env` (or `TRIAGE_ENV_FILE`) for credentials and defaults, checks `/healthz`, checks there is work, then runs the agent headless from the checkout with only the `bishop-triage` MCP server and the agent's tools, on Claude Code (`claude -p`, the default) or OpenCode (`opencode run`, using your own OpenCode config for the provider and key). `--dry-run` prints the exact command; on opencode it also checks the agent resolves. `make triage-classify` / `triage-process` take `ENGINE=` and `MODEL=` for one run. Logs to `$TRIAGE_LOG_DIR/triage.log` (default `~/Library/Logs/bishop-memory`) and keeps each run's result beside it (`.json` for claude, `.jsonl` events for opencode).

| Variable | Default | Purpose |
|---|---|---|
| `BISHOP_MEMORY_URL` | `http://127.0.0.1:8787` | Service URL |
| `TRIAGE_ENGINE` | `claude` | `claude` or `opencode`; `TRIAGE_CLASSIFY_ENGINE` / `TRIAGE_PROCESS_ENGINE` set one job |
| `TRIAGE_ITEMS_PER_RUN` | 30 | Findings per processed category |
| `TRIAGE_CATEGORIES_PER_RUN` | 1 | Categories per `process` call |
| `TRIAGE_CLASSIFY_MODEL` / `TRIAGE_PROCESS_MODEL` | `haiku` / `sonnet`; on opencode `opencode-go/glm-5.3-flash` / `opencode-go/glm-5.2` | Models |
| `TRIAGE_MAX_TURNS` | 60 / 120 | Turn cap (steps on opencode) |
| `TRIAGE_MAX_BUDGET_USD` | 2 / 5 | Spend cap per run; claude only |
| `TRIAGE_TIMEOUT_MIN` | 45 | Stop a run still going after this many minutes; `0` never |
| `TRIAGE_ADD_DIRS` | every registered harness checkout | Read-only directories for the processor |
| `TRIAGE_ENV_FILE` | `<checkout>/.env` | Credentials file |
| `CLAUDE_BIN` / `OPENCODE_BIN` | `claude` / `opencode` | CLI per engine |

Exit 0 ran or nothing to do, 1 the agent failed, 2 a precondition failed (service down, `claude` missing, `mcpd` unbuildable), 64 usage.

### `scripts/install-triage-schedule.sh`

```
scripts/install-triage-schedule.sh [--classify-at HH:MM] [--process-at HH:MM] [--url URL] [--log-dir DIR] [--dry-run]
scripts/install-triage-schedule.sh --uninstall
```

Renders `scripts/com.bishop-memory.triage.plist` twice and loads `com.bishop-memory.triage-classify` (21:00) and `com.bishop-memory.triage-process` (21:20). Loading never runs a job; `launchctl start <label>` does. Bakes in a PATH that includes wherever `claude` and `go` are now.

### `scripts/triage-seed-categories.py`

```
scripts/triage-seed-categories.py [--url URL] [--file db/finding-categories.json] [--deactivate-missing] [--dry-run]
```

Upserts every category; `--deactivate-missing` retires slugs no longer in the file (never deletes, classifications reference them).

### `scripts/triage-backfill-harness.py`

```
scripts/triage-backfill-harness.py [--url URL] [--register NAME=/abs/.claude/memory ...] [--dry-run]
```

Matches each finding with no harness against every registered harness's `FINDINGS.md` by natural key and sets `findings.harness`. Reports rows that match no harness or more than one. `--register` also upserts the harness, which the reconciler otherwise does on its next run.

### `scripts/export-decisions.py`

```
scripts/export-decisions.py --harness NAME [--url URL] [--dry-run]
scripts/export-decisions.py --all [--url URL] [--dry-run]
```

Writes decided findings and ratified directives into the harness's Markdown. Exit 0 done or nothing to do, 1 a write failed, 2 unregistered harness or missing tree, 3 at least one conflict skipped.

### `scripts/triage_common.py`

Not a command. The helper module the triage scripts share: it loads `reconcile-memory.py` as a module so every script computes a finding's natural key with the reconciler's own parser.

## Mission HUD

Both scripts work on the database file, not through the API, and are dry runs unless told to write. See [Mission HUD Guide](Mission-HUD-Guide) for when to run them.

### `scripts/backfill-mission-links.py`

```
scripts/backfill-mission-links.py [--db data/memory.db] [--rename-harness OLD=NEW ...] [--apply] [--verbose]
make mission-links [RENAME="old=new ..."] [APPLY=1]
```

Fills the mission links the database never recorded, in one transaction, in this order: moves rows in `missions`, `findings`, `directive_proposals` and `documents` from each `OLD` harness name to `NEW` and deletes the `OLD` harnesses row; deletes documents (and their search rows) under no registered harness memory root; fills empty `mission_steps.started_at`, `ended_at` and `summary` from the journal's `mission.step` and `step-sync` events; sets empty `findings.mission_id` from the `[YYYY-MM-DD] — target` lines in each debrief's **Findings and Patterns Linked** section, then from the one mission of the finding's harness that was open when the finding was recorded (up to two hours after it closed). It never overwrites a set column. `--verbose` (the make target always passes it) lists findings that fit several missions.

`--apply` first copies the database with SQLite's backup API to `data/memory.bak-<timestamp>.db` (safe while `memoryd` runs), and refuses with exit 1 while a triage run is `running`. Run it after deploying the matching `memoryd` and `POST /v1/documents/sync`, so debriefs carry their mission.

### `scripts/clean-scratch.py`

```
scripts/clean-scratch.py [--days 30] [--harness NAME] [--db data/memory.db] [--apply]
make scratch-clean [DAYS=30] [HARNESS=name] [APPLY=1]
```

For every registered harness (or `--harness`), lists the files under `<memory root>/workspace/` whose modification time is older than `--days`, with their size and search-document count. `--apply` moves them into `~/.Trash/bishop-scratch-<timestamp>/<harness>/`, deletes their documents and search rows, and removes directories left empty. `workspace/README.md` and hidden files (`.gitkeep` and the like) are always kept. Exit 1 for an unregistered `--harness`, 2 for `--days` below 1.

## Wiki

### `scripts/screenshots/run.sh`

```
scripts/screenshots/run.sh [--out DIR] [--port N] [--keep]
make screenshots
```

Builds `memoryd` into a temporary directory and starts it on port 8790 (`--port` to change) against a fresh database. Loads the taxonomy and the made-up findings and missions in `scripts/screenshots/demo.json` through the HTTP API (`seed-demo.py`, which refuses an instance that already holds findings). Captures each tab of `/triage` and a mission on `/missions` in light and dark, plus phone-width views, with headless Chrome (`capture.js`) into `docs/wiki/images/`. Then it stops the service, checks the port is free, and deletes the temporary directory. `--keep` leaves the demo service running so you can look around it.

Needs `go`, `python3`, `node` 18+ with `npm`, and Chrome or Chromium (`CHROME_PATH` if it is not in a standard place). The first run installs `playwright-core` into `scripts/screenshots/node_modules` (gitignored); no browser is downloaded. If `oxipng` or `optipng` is installed the images are compressed losslessly. To add a shot, add an entry to `SHOTS` in `capture.js`.

### `scripts/gen-mcp-reference.py`

```
scripts/gen-mcp-reference.py [--check]
```

Regenerates `docs/wiki/MCP-Tool-Reference.md` from the `mcpd` source so the tool names, descriptions and argument tables cannot drift. Examples live in the script. `--check` exits 1 when the page is stale.

### `scripts/publish-wiki.sh`

```
scripts/publish-wiki.sh [--dry-run] [--remote git@github.com:owner/repo.wiki.git]
```

Clones the wiki repository, replaces every page with `docs/wiki/*.md` and every image with `docs/wiki/images/`, commits and pushes. GitHub creates the wiki repository only when the first page is made in the web UI, so do that once; the script says so if it is missing.

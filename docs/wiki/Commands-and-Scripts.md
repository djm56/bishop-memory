# Commands and Scripts

Every `make` target and every script, with flags, environment, exit codes and when you would use it. Paths are relative to the bishop-memory checkout.

## Make targets

| Target | What it does | Notes |
|---|---|---|
| `make run` | Run `memoryd` in the foreground from source | Reads `.env`; default `127.0.0.1:8787`, `data/memory.db` |
| `make build` | Build `bin/memoryd` | |
| `make build-mcpd` | Build `bin/mcpd` | Every harness `.mcp.json` and the triage runner execute this path; rebuild after any change to `cmd/mcpd` |
| `make dist-linux-amd64` / `dist-linux-arm64` / `dist-linux` | Cross-compile static Linux binaries into `bin/` | No C toolchain; `CGO_ENABLED=0` |
| `make dist [VERSION=v]` | Release archives `dist/bishop-memory-<version>-<os>-<arch>.tar.gz` for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 | Each holds `bin/memoryd`, `bin/mcpd` (schema embedded), `scripts/`, `db/` and the README; unpack anywhere and run `scripts/install.sh`. `VERSION` defaults to `git describe --tags --always --dirty`. See [Server Install](Server-Install) |
| `make test` / `make vet` / `make fmt` / `make tidy` | The verification gate pieces | CI runs build, vet, gofmt, test, test -race |
| `make init-db` | Create the database from `db/schema.sql` with `sqlite3` | Lacks the unique steps index and additive columns, which `memoryd` adds at boot |
| `make reset-db` | Delete the database and its WAL files, then `init-db` | Destructive |
| `make health` | `curl /healthz` through `jq` | |
| `make triage-seed` | Load `db/finding-categories.json` | Idempotent upsert by slug |
| `make triage-backfill REGISTER="name=/abs/.claude/memory …"` | Register harnesses and set `findings.harness` on old rows | Run once per service |
| `make triage-classify [RECLASSIFY=slug]` | Run the classifier now | Exits 0 with nothing to do when everything is classified |
| `make triage-process [CATEGORY=slug] [LIMIT=n]` | Run the processor now | Default: next category in rotation, 30 items |
| `make triage-grade [LIMIT=n] [MISSIONS=id,…] [REGRADE=1]` | Grade finished missions now | Default: the next 10 ungraded; `REGRADE=1` needs `MISSIONS`. See [Mission Grading](Mission-Grading) |
| `make triage-export` | Write decisions to every registered harness's Markdown | Exit 3 means a conflict was skipped |
| `make triage-review` | Open the review page | |
| `make triage-install [ROOT=DIR] [ENV_FILE=FILE]` / `triage-uninstall` | Install or remove the nightly launchd jobs; `ROOT` runs them from another checkout, `ENV_FILE` names their credentials file | macOS. Only command-line values count; see [the installer](#scriptsinstall-triage-schedulesh) |
| `make triage-deploy [DEPLOY_DIR=/abs/path]` | Create or fast-forward the deploy worktree to `origin/main` and rebuild `bin/mcpd` in it. Default `$HOME/bishop-memory-deploy`. See [the deploy worktree](Developer-Triage-Agents#the-deploy-worktree-and-where-the-jobs-run-from) | Never between 21:00 and 21:40 local time |
| `make mission-links [RENAME="old=new …"] [APPLY=1]` | One-off backfill of mission links: findings, step times and summaries, orphan documents, stray harness names | Dry run unless `APPLY=1`; run `POST /v1/documents/sync` first |
| `make scratch-clean [DAYS=30] [HARNESS=name] [APPLY=1]` | Move harness workspace files older than `DAYS` to the Trash and drop them from search, through the API | Run on the machine that holds the harness checkouts. Dry run unless `APPLY=1`; default every registered harness |
| `make wiki-publish` | Push `docs/wiki/` (pages and images) to the GitHub wiki | Needs the wiki created once in the web UI |
| `make screenshots` | Regenerate the review-page and mission HUD screenshots in `docs/wiki/images/` from demo data | Never touches your service or database |

All `triage-*` targets and `scratch-clean` accept `BISHOP_MEMORY_URL=http://127.0.0.1:8788` to aim at another instance, and send the API key from `BISHOP_MEMORY_API_KEY` or `~/.config/bishop-memory/client.env`. `mission-links` opens the database file directly instead, so it runs on the server; it takes `DB=` (default `data/memory.db`).

## Service scripts

### `scripts/install.sh`

```
scripts/install.sh server [--host ADDR] [--port N] [--first-key NAME] [--tls-cert FILE --tls-key FILE] [--allow-from CIDR,…] [--dry-run] [-- installer flags]
scripts/install.sh client --url URL [--key KEY] [--ca FILE] [--dry-run]
```

The entry point for a server and its clients; the walkthrough is [Server Install](Server-Install).

`server` detects the OS. On Linux (run with `sudo`) it runs `install-daemon-linux.sh`, creates the first key as the `bishop-memory` user with `--first-key`, copies TLS files to `/etc/bishop-memory/tls/`, writes a systemd drop-in admitting clients when `--host` is not loopback (`--allow-from` narrows it; default any address), writes `/etc/bishop-memory/memoryd.env` (`HTTP_HOST`, `PORT`, `APP_ENV=production`, TLS paths), restarts the service and waits up to 20 seconds for `/healthz`. On macOS (run as yourself) it writes `~/Library/Application Support/bishop-memory/memoryd.env`, creates the first key in `<checkout>/data/api-keys`, then runs `install-daemon.sh --env-file` on that file. Any other OS is refused; run `memoryd` by hand. On Linux it refuses to run from under `/home` or `/root` (the unit has `ProtectHome=true`). `--host` defaults to `127.0.0.1`; a non-loopback host with no key existing stops the install. Re-running keeps the settings an earlier run wrote for every flag left out, and skips `--first-key` when that key exists. Flags after `--` go to the platform installer.

`client` writes `~/.config/bishop-memory/client.env` (mode 600) with `BISHOP_MEMORY_URL` and `BISHOP_MEMORY_API_KEY`, plus `BISHOP_MEMORY_CA_FILE` with `--ca` (the CA of a home-made server certificate, copied to `~/.config/bishop-memory/ca.pem`), prompting for the key without echo when `--key` is not given; builds `bin/mcpd` when Go and the source are present; and checks the key with `GET /v1/harnesses`. It exits non-zero on a 401 or when the server cannot be reached, and prints the harness-side settings to change.

### `memoryd keys` and `memoryd backup`

```
memoryd keys add <name>        create an API key and print it once
memoryd keys list              list key names
memoryd keys revoke <name>     remove a key
memoryd backup <file>          write a consistent copy of the database to <file>
memoryd help
```

Operator commands of the daemon binary; with no arguments it serves. They read the same configuration (`DB_PATH`, `BISHOP_API_KEYS_FILE`, default `api-keys` beside `DB_PATH`), so pass the service's `DB_PATH`: `sudo runuser -u bishop-memory -- env DB_PATH=/var/lib/bishop-memory/memory.db /opt/bishop-memory/bin/memoryd keys list` on Linux, `DB_PATH=data/memory.db bin/memoryd keys list` in a macOS checkout. Key changes take effect without a restart. `backup` uses SQLite's `VACUUM INTO`, which is safe while the service runs, and refuses a target that already exists. Exit 0 success, 1 failure, 2 usage.

### `scripts/install-daemon.sh` (macOS)

```
scripts/install-daemon.sh [--dry-run] [--memory-root <path>] [--log-dir <path>] [--exec-dir <path>] [--env-file <path>]
```

Builds `memoryd` (or uses the shipped `bin/memoryd` of a release archive when there is no Go), stages it under `~/.local/libexec/bishop-memory` (the internal disk; launchd hangs on a binary under `/Volumes`), renders and installs `~/Library/LaunchAgents/com.bishop-memory.memoryd.plist`, boots the job out and back in, and polls `/healthz`. `--memory-root` is the default root for `documents_sync`. `--env-file <file>` names the settings file the job reads through `MEMORYD_ENV_FILE` (default `~/Library/Application Support/bishop-memory/memoryd.env`; `HTTP_HOST`, `PORT`, TLS); `install.sh server` writes it. Idempotent; re-run to upgrade. Exit 64 for a bad flag, 66 for a missing directory, 1 for a failed bootstrap.

### `scripts/install-daemon-linux.sh` (Linux)

```
sudo scripts/install-daemon-linux.sh [--dry-run]
```

Builds `memoryd` when Go is on the PATH, otherwise uses the shipped `bin/memoryd` (a release archive) or `bin/memoryd-linux-<arch>`. Creates the `bishop-memory` system user, installs `/etc/systemd/system/bishop-memory.service`, enables and starts it. The unit reads `/etc/bishop-memory/memoryd.env` when it exists (`install.sh server` writes it) and keeps the database and keys in `/var/lib/bishop-memory`. Run without root it builds and prints the root-only commands instead.

## Reconciliation

### `scripts/reconcile-memory.py`

```
scripts/reconcile-memory.py --root <.claude/memory> [--harness NAME] [--url URL] [--dry-run]
                            [--include-journal] [--skip-steps] [--no-sync-documents]
```

Mirrors a harness's Markdown into the service with natural-key dedupe, so running it twice changes nothing. In order: registers the harness (`--harness`), brings documents up to date, missions, steps, journal (opt-in), findings (creating missing ones and mirroring a hand-set `Status` through the decision route), directives, patterns, service records. `--harness` has no environment fallback on purpose; pass it explicitly. With `--harness` the documents step pushes the tree by content (only files whose hash differs from the server's), so it works against a remote server; without it, or against a server too old to accept pushes, it asks the server to read `--root` itself (`POST /v1/documents/sync`), which works only when the server is on the same machine. `--no-sync-documents` skips the step. URL and key come from `--url` / `BISHOP_MEMORY_URL` and `BISHOP_MEMORY_API_KEY`, or `~/.config/bishop-memory/client.env`. Exit 0 success, 1 something failed (a failed write or a failed fetch, which outranks a journal cap skip), 3 journal events skipped at the cap with nothing else failed (`--include-journal` only), 2 usage error (an unknown or malformed argument), 66 bad root.

### `scripts/push-memory.py`

```
scripts/push-memory.py --root <.claude/memory> --harness NAME [--url URL] [--prune]
```

Registers the harness with this root, then pushes every Markdown file under it whose content differs from the server's copy (hidden files and directories skipped, as the importer does), plus the agent definitions in `<root>/../agents/`, in requests of at most 200 files. Files over 1 MiB are skipped with a message. `--prune` deletes the server's documents of this harness, under this root, whose files are gone. For a first upload to a server, or to push without reconciling; the reconciler does the same push on every run. Prints `pushed N, unchanged M`. Exits non-zero when the harness cannot be registered or the server has no push route (older than the client).

`scripts/backfill-memory.py` is a passthrough to the same script kept for old invocations.

## Findings triage

### `scripts/triage-run.sh`

```
scripts/triage-run.sh classify [--reclassify SLUG] [--engine claude|opencode] [--model M] [--dry-run]
scripts/triage-run.sh process  [--category SLUG] [--limit N] [--engine claude|opencode] [--model M] [--dry-run]
scripts/triage-run.sh grade    [--limit N] [--mission ID[,ID…]] [--regrade] [--engine claude|opencode] [--model M] [--dry-run]
```

On `grade --mission`, the run first asks the HUD list (`GET /v1/hud/missions?grade=excluded`) which named missions the operator has set aside as N/A, and logs a skip line for each. It never deletes or claims an excluded mission, so `--regrade` skips it too; when every named mission is excluded the run exits 0 with nothing run. The tools the lookup is guarded for are `curl`, `python3`, `mktemp`, `tail` and `sed`: if one is missing, or the lookup fails, the run exits 4 before any delete or claim. Other tools on the failure path (`tr`, `head` and `rm`) are not guarded. `--dry-run` reports the skips in the same way.

The single entry point for all three agents; launchd and `make` call it. Sources `.env` (or `TRIAGE_ENV_FILE`) for credentials and defaults, checks `/healthz`, checks there is work, then runs the agent headless from the checkout with only the `bishop-triage` MCP server and the agent's tools, on Claude Code (`claude -p`, the default) or OpenCode (`opencode run`, using your own OpenCode config for the provider and key). `--dry-run` prints the exact command; on opencode it also runs the agent guard, so a dry run exits 2 or 4 where a real run would not start. The opencode engine needs OpenCode v2; the runner was observed on 2.0.32, and only a live grade run on the v2 config shape (evidence: [Developer: Triage Agents](Developer-Triage-Agents#the-pieces)); see also [switching to OpenCode](Developer-Triage-Agents#switching-to-opencode). `--help` and `-h` print the header comment and exit 0 wherever they appear in the arguments, before any env file is read. `make triage-classify` / `triage-process` take `ENGINE=` and `MODEL=` for one run. Logs to `$TRIAGE_LOG_DIR/triage.log` (default `~/Library/Logs/bishop-memory`) and keeps each run's result beside it (`.json` for claude, `.jsonl` events for opencode).

| Variable | Default | Purpose |
|---|---|---|
| `BISHOP_MEMORY_URL` | `http://127.0.0.1:8787` | Service URL; also read from `client.env` when neither the environment nor `.env` sets it |
| `BISHOP_MEMORY_API_KEY` | — | Sent to the service as `Authorization: Bearer`, through a mode-600 header file rather than the `curl` command line; also read from `client.env` |
| `TRIAGE_ENGINE` | `claude` | `claude` or `opencode`; `TRIAGE_CLASSIFY_ENGINE` / `TRIAGE_PROCESS_ENGINE` / `TRIAGE_GRADE_ENGINE` set one job |
| `TRIAGE_GRADE_LIMIT` | 10 | Finished missions per grade run, 1–10 |
| `TRIAGE_ITEMS_PER_RUN` | 30 | Findings per processed category |
| `TRIAGE_CATEGORIES_PER_RUN` | 1 | Categories per `process` call |
| `TRIAGE_CLASSIFY_MODEL` / `TRIAGE_PROCESS_MODEL` / `TRIAGE_GRADE_MODEL` | `haiku` / `sonnet` / `sonnet`; on opencode `opencode-go/glm-5.3-flash` / `opencode-go/glm-5.2` / `opencode-go/glm-5.2` | Models |
| `TRIAGE_MAX_TURNS` | 60 / 120 / 20 | Turn cap (steps on opencode) |
| `TRIAGE_MAX_BUDGET_USD` | 2 / 5 / 2 | Spend cap per run; claude only |
| `TRIAGE_TIMEOUT_MIN` | 45 (20 for grade) | Stop a run still going after this many minutes; `0` never |
| `TRIAGE_GUARD_TIMEOUT_SEC` | 30 | Seconds the opencode agent listing may take before the guard exits 4; a whole number of at least 1; validated on the opencode engine only |
| `TRIAGE_ADD_DIRS` | every registered harness checkout | Read-only directories for the processor |
| `TRIAGE_ENV_FILE` | `<bishop-root>/.env`, where `<bishop-root>` is the checkout the runner runs from | Credentials file. The launchd jobs get it from the installer, which sets it in each plist to the installer checkout's `.env` unless `--env-file` is given; see the installer section below |
| `CLAUDE_BIN` / `OPENCODE_BIN` | `claude` / `opencode` | CLI per engine |

Exit 0 ran or nothing to do (on `grade --mission`, also when every named mission is excluded); 1 the agent run failed or timed out; 2 the guard refused the agent, or a precondition the run needs was not met; 4 the guard could not finish checking the agent, or on `grade --mission` the exclusion lookup or a tool it needs could not finish, so nothing ran; 64 usage error.

### `scripts/install-triage-schedule.sh`

```
scripts/install-triage-schedule.sh [--classify-at HH:MM] [--process-at HH:MM] [--grade-at HH:MM] [--url URL] [--log-dir DIR] [--root DIR] [--env-file FILE] [--dry-run]
scripts/install-triage-schedule.sh --uninstall
```

Renders `scripts/com.bishop-memory.triage.plist` three times and loads `com.bishop-memory.triage-classify` (21:00), `com.bishop-memory.triage-process` (21:20) and `com.bishop-memory.triage-grade` (21:40). Loading never runs a job; `launchctl start <label>` does. Bakes in a PATH that includes wherever `claude` and `go` are now.

- `--root DIR` (make: `ROOT=DIR`) runs the jobs from the checkout at `DIR`, such as the deploy worktree. The default is the checkout that holds the installer. `DIR` must hold an executable `scripts/triage-run.sh` and an executable `bin/mcpd`, or the installer exits 64 before it writes anything. A relative `DIR` is written as an absolute path.
- `--env-file FILE` (make: `ENV_FILE=FILE`) names the credentials file the jobs source, as `TRIAGE_ENV_FILE`. The default is `.env` in the checkout that holds the installer, even with `--root`, because `.env` is gitignored and a deploy worktree has none. A file that is missing or unreadable produces a WARNING and the install goes ahead. The path is refused, before anything is written, if it holds a character the plist render cannot carry; the installer's header lists them.
- Through make, `ROOT` and `ENV_FILE` count only when given on the command line; an exported value is ignored. An empty command-line value is refused with exit 64.
- `--uninstall` ignores `--root` and `--env-file`. The worked setup, with `make triage-deploy` first, is in [Developer: Triage Agents](Developer-Triage-Agents#the-deploy-worktree-and-where-the-jobs-run-from).

### `scripts/triage-deploy.sh`

```
scripts/triage-deploy.sh [--dir DIR]
```

Creates or fast-forwards the deploy worktree (default `$HOME/bishop-memory-deploy`, or `DEPLOY_DIR=` / `--dir`, absolute paths only) to `origin/main`, detached, and rebuilds `bin/mcpd` in it. Refuses a DIR in a state its header lists, leaving the worktree unchanged. It also refuses, and the header does not list, an existing path that is not a directory and a path inside another checkout; these refusals have no test, as the ROADMAP and PROGRESS record. The fetch of `origin/main` has already run by then. Exit codes and the details are in [Developer: Triage Agents](Developer-Triage-Agents#the-deploy-worktree-and-where-the-jobs-run-from).

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

Writes decided findings, ratified directives, amendments to existing `DIR-NNN` entries, and retirements into the harness's Markdown. Only touches entries whose Added line has `**Source:** bishop-memory triage` (service-owned); hand-authored entries are byte-preserved. Before every write it copies the file to `<memory_root>/workspace/triage-backups/`, writes atomically, and re-reads to confirm. Exit codes: 0 done or nothing to do, 1 a write failed, 2 unregistered harness or missing tree, 3 at least one conflict skipped (higher codes outrank 3). If the file already holds a status that differs from the service's decision, the entry is left alone and reported as a conflict; the file wins, and the next reconcile mirrors it into the service.

### `scripts/triage_common.py`

Not a command. The helper module the triage scripts share: it loads `reconcile-memory.py` as a module so every script computes a finding's natural key with the reconciler's own parser.

## Mission HUD

Both are dry runs unless told to write. `backfill-mission-links.py` works on the database file, so it runs on the server; `clean-scratch.py` works through the API and runs on the machine with the harness checkouts. See [Mission HUD Guide](Mission-HUD-Guide) for when to run them.

### `scripts/backfill-mission-links.py`

```
scripts/backfill-mission-links.py [--db data/memory.db] [--rename-harness OLD=NEW ...] [--only PART,...] [--apply] [--verbose]
make mission-links [RENAME="old=new ..."] [ONLY=part,...] [APPLY=1]
```

Fills the mission links the database never recorded, in one transaction, in this order: moves rows in `missions`, `findings`, `directive_proposals` and `documents` from each `OLD` harness name to `NEW` and deletes the `OLD` harnesses row; deletes documents (and their search rows) under no registered harness memory root; fills empty `mission_steps.started_at`, `ended_at` and `summary` from the journal's `mission.step` and `step-sync` events; sets empty `findings.mission_id` from the `[YYYY-MM-DD] — target` lines in each debrief's **Findings and Patterns Linked** section, then from the one mission of the finding's harness that was open when the finding was recorded (up to two hours after it closed); sets empty `missions.harness` from the harness of the mission's own brief, progress and debrief documents when they name exactly one. It never overwrites a set column. `--verbose` (the make target always passes it) lists findings that fit several missions and missions whose documents name no single harness. `--only` runs a subset of the parts, by name: `harness`, `orphans`, `steps`, `findings`, `mission-harness` (for example `--only mission-harness`).

`--apply` first copies the database with SQLite's backup API to `data/memory.bak-<timestamp>.db` (safe while `memoryd` runs), and refuses with exit 1 while a triage run is `running`. Run it after deploying the matching `memoryd` and `POST /v1/documents/sync`, so debriefs carry their mission.

### `scripts/rename-harness.py`

```
scripts/rename-harness.py --rename OLD=NEW [--rename OLD=NEW …] [--db data/memory.db] [--apply]
```

Renames a harness everywhere the service records it: the registry row (its memory root kept), the `harness` column of missions, findings, directive proposals and documents, and every journal, step and service-record agent written as `OLD:agent`. Journal notes that mention the old name in their text are left as written. Change `BISHOP_HARNESS` in the harness's `bishop-memory.conf` and MCP registration (`.mcp.json` or `opencode.json`) first, then run this; a session started before that change keeps writing under the old name until it restarts, so run it once more afterwards. Safe to repeat. Dry run unless `--apply`, which backs the database up first and writes in one transaction; refuses while a triage run started in the last two hours is still open. Opens the database file directly, so run it on the server.

### `scripts/clean-scratch.py`

```
scripts/clean-scratch.py [--days 30] [--harness NAME] [--url URL] [--apply]
make scratch-clean [DAYS=30] [HARNESS=name] [APPLY=1]
```

Runs on the machine that holds the harness checkouts, against a local or remote service. Takes the harnesses and their memory roots from `GET /v1/harnesses` (or only `--harness`) and skips any root that does not exist on this machine. For each, lists the files under `<memory root>/workspace/` whose modification time is older than `--days`, with their size and search-document count (from `GET /v1/documents/hashes`). `--apply` moves them into the Trash under `bishop-scratch-<timestamp>/<harness>/` (`~/.Trash` on macOS, `~/.local/share/Trash/files` elsewhere), deletes their documents and search rows through `POST /v1/documents/delete`, and removes directories left empty. If the delete fails after the move, it says so and names the `push-memory.py --prune` command that finishes the job. `workspace/README.md` and hidden files (`.gitkeep` and the like) are always kept. `--url` replaces the old `--db`; URL and key come from `--url` / `BISHOP_MEMORY_URL` and `BISHOP_MEMORY_API_KEY`, or `client.env`. Exit 1 for an unregistered `--harness` or a failed delete, 2 for `--days` below 1.

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

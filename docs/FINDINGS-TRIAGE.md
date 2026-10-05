# Findings Triage — Operator Guide

bishop-memory can classify the findings ledger, group findings that share a
rule, recommend a decision on each one, and draft binding directives — on a
schedule, with two small Claude Code agents — while leaving every decision to
you. This guide covers what runs, how to install it on the central server,
how to review, and how decisions get back into each harness's Markdown.

The design rationale and the decisions behind it are in
`docs/FINDINGS-TRIAGE-PLAN.md`.

## What runs

```
 nightly 21:00  classify   findings-classifier (Haiku)    unclassified findings → finding_triage
 nightly 21:20  process    findings-processor  (Sonnet)   one category per run  → finding_groups,
                                                                                 finding_recommendations,
                                                                                 directive_proposals
 any time       review     you, at http://127.0.0.1:8787/triage               → findings.status,
                                                                                 ratified directives
 after review   export     scripts/export-decisions.py                        → FINDINGS.md, DIRECTIVES.md
```

The two agents run on Claude Code by default. Either or both can run on
OpenCode instead, on any model your OpenCode config can reach (for example
an OpenCode Go plan); see [Running on OpenCode](#running-on-opencode).

Three rules hold throughout:

1. **Only the operator changes a finding's status.** The agents write
   classifications and recommendations into their own tables. The one route
   that writes `findings.status` is `POST /v1/findings/:id/decision`, and no
   MCP profile registers it.
2. **Markdown stays the source of truth.** Decisions are exported back into
   the owning harness's `FINDINGS.md` and `DIRECTIVES.md`; the reconciler
   mirrors a hand edit in the file into the service. A conflict (file and
   service disagree, both non-proposed) is reported, never overwritten.
3. **The agents never edit a file.** A recommendation's `proposed_change` is
   text for you to paste. Ratifying a directive writes the entry through the
   exporter, not through an agent.

## Install on the central server

Prerequisites beyond the service itself: `python3`, `curl`, `go` (to build
`bin/mcpd`), and one of the two engines:

- **Claude Code** (`claude`, the default) with Anthropic credentials. The
  scheduled jobs run headless as the user who installs them, with no terminal
  and no Claude login: a launchd or systemd job that lacks credentials stops
  at "Not logged in". The runner therefore sources `<bishop-root>/.env`
  (gitignored; the same file the service reads) before every run. Put either
  an API key (billed to the API) or a subscription token from
  `claude setup-token` (billed to your Claude plan) in it. If both are set,
  the API key wins.
- **OpenCode** (`opencode`) with a provider already configured in your own
  OpenCode config. Nothing goes in `.env`; see
  [Running on OpenCode](#running-on-opencode).

```bash
cd /path/to/bishop-memory
umask 077
printf 'ANTHROPIC_API_KEY=sk-ant-...\n' >> .env        # or CLAUDE_CODE_OAUTH_TOKEN=... from `claude setup-token`
chmod 600 .env
```

`TRIAGE_ENV_FILE` points the runner at a different file. A variable already in
the environment wins over the file, so `TRIAGE_ITEMS_PER_RUN=40 make
triage-process` still overrides a default written there. Spend per run is
capped by `TRIAGE_MAX_BUDGET_USD` (2 for classify, 5 for process); the first
full classification of 314 findings cost about $0.70 and a 15-finding
processor run about $0.30.

```bash
cd /path/to/bishop-memory
git pull

# 1. Rebuild and restart the service so the schema upgrade runs at boot.
#    Existing databases gain the triage tables and the two new findings
#    columns automatically (see internal/store/migrate.go).
scripts/install-daemon.sh --memory-root /path/to/a/harness/.claude/memory   # macOS
sudo scripts/install-daemon-linux.sh                                          # Linux
curl -s http://127.0.0.1:8787/healthz        # "schema":"ok" means every table is present

# 2. Build the MCP adapter (the runner and the harness .mcp.json execute bin/mcpd by path).
make build-mcpd

# 3. Load the category taxonomy.
make triage-seed

# 4. Register each harness and stamp existing findings with their harness.
#    Pass every harness that writes to this service, with the absolute path
#    of its .claude/memory tree.
make triage-backfill REGISTER="kirsch=/abs/path/kirsch/.claude/memory anomalous=/abs/path/bishop-harness/.claude/memory"

# 5. Reconcile each harness once so decisions already in its FINDINGS.md reach the service.
/path/to/bishop-memory/scripts/reconcile-memory.py --root /abs/path/kirsch/.claude/memory --harness kirsch

# 6. Prove the runner end to end before scheduling it.
scripts/triage-run.sh classify --dry-run        # prints the exact claude command
make triage-classify                            # classifies everything (a few minutes, Haiku)
make triage-process                             # recommends on the next category in rotation (Sonnet)

# 7. Schedule it (macOS; see below for Linux).
make triage-install                             # 21:00 classify, 21:20 process, daily
launchctl list | grep com.bishop-memory.triage
```

`scripts/install-triage-schedule.sh --classify-at 02:00 --process-at 02:30`
changes the slots. The evening default is deliberate: the machine is awake and
any external volume holding a harness checkout is mounted. launchd runs a
missed slot on wake.

On Linux, schedule the same two commands with a systemd user timer:

```ini
# ~/.config/systemd/user/bishop-triage-classify.service
[Service]
Type=oneshot
WorkingDirectory=/path/to/bishop-memory
Environment=BISHOP_MEMORY_URL=http://127.0.0.1:8787
ExecStart=/path/to/bishop-memory/scripts/triage-run.sh classify

# ~/.config/systemd/user/bishop-triage-classify.timer
[Timer]
OnCalendar=*-*-* 21:00:00
Persistent=true
[Install]
WantedBy=timers.target
```

Duplicate for `process` at 21:20, then `systemctl --user enable --now
bishop-triage-classify.timer bishop-triage-process.timer`.

## Running on OpenCode

The runner can start each agent with OpenCode (`opencode run`) instead of
Claude Code. The agents, the doctrine and everything they write stay the
same; only the CLI and the model change. Use it when a cheaper plan, such as
OpenCode Go, covers the models you want.

**Credentials come from your own OpenCode config.** OpenCode reads the same
files as when you start it by hand: `~/.config/opencode/opencode.json` for
providers and API keys, and `~/.local/share/opencode/auth.json` for logins. A
launchd job runs as you, so it reads them too. Nothing goes in `.env`, and
bishop-memory never sees the key. For each run, the runner layers a small
config on top through `OPENCODE_CONFIG_CONTENT`, so no file is written:

- the `bishop-triage` MCP server (mcpd in its triage profile);
- the step cap (`TRIAGE_MAX_TURNS`);
- read access to the harness checkouts, for the processor;
- sharing, snapshots, LSP servers, formatters and auto-update turned off;
- `small_model` pinned to the run's model, so no side call reaches another
  provider.

It also passes `--pure`, so your OpenCode plugins do not load during triage.

**The agents have OpenCode twins.** `.opencode/agents/findings-classifier.md`
and `.opencode/agents/findings-processor.md` mirror the two Claude agents.
Each denies every tool except reading files and its own `bishop-triage`
tools, and neither can read a `.env` file. OpenCode does not fail on an
unknown agent name: it falls back to its default agent, which can edit files
and run commands. So before every run, the runner asks OpenCode how it
resolves the agent. It refuses to start (exit 2) unless the result is the
twin, with no edit, write, shell or sub-agent tools.

**To switch over,** add these lines to `<bishop-root>/.env`:

```bash
TRIAGE_ENGINE=opencode                              # both jobs
TRIAGE_CLASSIFY_MODEL=opencode-go/glm-5.3-flash     # optional; these are the defaults
TRIAGE_PROCESS_MODEL=opencode-go/glm-5.2
```

- Models are `provider/model` ids, as `opencode models` lists them. The
  runner refuses a model id that does not fit the engine: `sonnet` under
  opencode, or `opencode-go/...` under claude.
- To move one job at a time, set `TRIAGE_CLASSIFY_ENGINE` or
  `TRIAGE_PROCESS_ENGINE` instead of `TRIAGE_ENGINE`.
- For a single run, use `make triage-process ENGINE=opencode
  MODEL=opencode-go/kimi-k3` or `scripts/triage-run.sh process --engine
  opencode`.

The launchd jobs need no reinstall: they read `.env` at every run. If
`opencode` is installed outside `/opt/homebrew/bin`, `/usr/local/bin` and
`~/.local/bin` (its own installer uses `~/.opencode/bin`), re-run
`make triage-install` so that its directory is on the jobs' PATH.

**To check it,** first run `scripts/triage-run.sh process --dry-run`. It
prints the exact `opencode run` command, and fails if the agent does not
resolve. Then run `launchctl start com.bishop-memory.triage-process`, which
takes the real scheduled path:

- `triage.log` shows `engine=opencode`;
- the Runs tab shows the model;
- the run's full event stream is kept beside the log as
  `triage-<kind>-<timestamp>.jsonl`.

**How it differs from the claude engine:**

- **No spend cap.** `TRIAGE_MAX_BUDGET_USD` applies only to Claude Code.
  Usage counts against your OpenCode plan's limits. The `cost_usd` in the log
  is OpenCode's list-price estimate, not a charge.
- **Two other limits stop a runaway run:** the step cap, and
  `TRIAGE_TIMEOUT_MIN` (45 minutes by default, for both engines).
- **Recommendation quality depends on the model.** Before relying on a model
  nightly, compare its accepted and declined counts on the Runs tab with
  earlier runs.

**To go back,** remove the `TRIAGE_*ENGINE` lines from `.env`, or set them
to `claude`.

## Reviewing

Open `http://127.0.0.1:8787/triage` (or `make triage-review`). Over SSH, the
same tunnel that reaches the API reaches the page.

- **Approver**: type your name once; it is stored in the browser and written
  as the `Approver` on every decision.
- **Pending**: one category at a time, groups first. Each finding shows the
  classifier's summary, the recommendation and its rationale, and the
  proposed change (copy button). `Approve`, `Reject` (reason required),
  `Defer`, `Supersede…`, `Retire`. **Accept all N recommendations** takes a
  whole group in one click: approves become `approved`, rejects become
  `rejected` with the model's rationale as the note, supersedes become
  `superseded`; defers are left alone.
- **Browse**: every finding by category and status, with the same buttons,
  for the ones the processor has not reached yet. `Reopen` undoes a decision.
- **Directives**: each pending draft as an editable form with the rendered
  length against the template's budget. **Ratify** allocates the next
  `DIR-NNN`, mirrors the directive into the `directives` table and marks every
  evidence finding `applied`. **Decline** needs a reason.
- **Runs**: every classifier and processor run with how many findings it
  read, how many rows it wrote, and how many of its recommendations you
  accepted or declined. A category whose acceptance rate drops is the one
  whose description in `db/finding-categories.json` needs tightening.
- Keys: `j`/`k` move between cards, `a` approve, `r` reject, `d` defer. They are ignored while Cmd, Ctrl or Alt is held.
- **Theme**: Auto, Light or Dark from the header. Auto follows the operating system, and the choice is saved in the browser. `?theme=light` or `?theme=dark` in the URL overrides it for one load.

## Exporting decisions to Markdown

```bash
make triage-export                       # every registered harness
scripts/export-decisions.py --harness kirsch --dry-run
```

The exporter rewrites only the three decision lines of a matching entry
(`**Status**`, `**Approver**`, `**Date approved**`) and appends one
`**Disposition (triage):**` line for a reject, retire or supersede. Ratified
directives are appended to `DIRECTIVES.md` in template form with an index
row. Before every write it copies the file to
`<memory_root>/workspace/triage-backups/`, writes atomically, and re-reads
to confirm. Exit code 3 means at least one conflict was skipped; the output
names the finding.

Run the export when no mission in that harness is mid-sync. The harness's
state-continuity hook fires only on Claude Code tool writes, so the exporter
cannot trigger a reconcile loop; the next reconcile sees matching status and
does nothing.

## How the pieces fit

| Piece | Where | Role |
|---|---|---|
| Schema | `db/schema.sql`, `internal/store/migrate.go` | `harnesses`, `finding_categories`, `triage_runs`, `finding_triage`, `finding_groups`, `finding_recommendations`, `directive_proposals`; additive `findings.harness`, `findings.decision_note` |
| Routes | `internal/api/findings.go`, `internal/api/triage.go`, `internal/api/directives.go` | Listed in `docs/api-contract.md` under Findings Triage |
| Review page | `internal/ui/triage.html` | Served at `/triage`, embedded in memoryd |
| MCP adapter | `cmd/mcpd/triage.go` | `MCPD_PROFILE=triage` registers the 13 triage tools; the default harness profile is unchanged apart from `finding_list` filters and `finding_append` carrying the harness |
| Agents | `.claude/agents/findings-classifier.md`, `.claude/agents/findings-processor.md` | Project-scope agents discovered when `claude -p` runs from this checkout |
| OpenCode agents | `.opencode/agents/findings-classifier.md`, `.opencode/agents/findings-processor.md` | The same two agents for `opencode run`; keep each pair in step |
| Doctrine | `.claude/skills/findings-triage/SKILL.md` | The rules both agents read first, on either engine |
| Runner | `scripts/triage-run.sh` | Health check, work check, category rotation, the headless `claude` or `opencode` call, logging |
| Schedule | `scripts/com.bishop-memory.triage.plist`, `scripts/install-triage-schedule.sh` | launchd user agents |
| Taxonomy | `db/finding-categories.json`, `scripts/triage-seed-categories.py` | 17 categories; edit, re-seed, re-classify |
| Backfill | `scripts/triage-backfill-harness.py` | Sets `findings.harness` by matching each harness's `FINDINGS.md` |
| Export | `scripts/export-decisions.py`, `scripts/triage_common.py` | Decisions and directives back into Markdown |
| Reconciler | `scripts/reconcile-memory.py` | Registers the harness, sends `harness` on findings, mirrors a hand-set status into the service, mirrors `DIRECTIVES.md` into `directives` |

### The category rotation

`GET /v1/triage/next-category` returns the active category with the most
proposed findings lacking a pending recommendation, least recently processed
first. A finished `process` run stamps `finding_categories.last_processed_at`.
With one category and 30 items a night the current backlog clears in about
two weeks; set `TRIAGE_CATEGORIES_PER_RUN=2 TRIAGE_ITEMS_PER_RUN=40` in the
plist (or the environment for a manual run) to go faster.

### Runner environment

| Variable | Default | Purpose |
|---|---|---|
| `BISHOP_MEMORY_URL` | `http://127.0.0.1:8787` | Service base URL |
| `TRIAGE_ENGINE` | `claude` | `claude` or `opencode`, for both jobs |
| `TRIAGE_CLASSIFY_ENGINE` / `TRIAGE_PROCESS_ENGINE` | — | Engine for one job; wins over `TRIAGE_ENGINE` |
| `TRIAGE_ITEMS_PER_RUN` | `30` | Findings the processor may recommend on per category |
| `TRIAGE_CATEGORIES_PER_RUN` | `1` | Categories per `process` invocation |
| `TRIAGE_CLASSIFY_MODEL` | `haiku` / `opencode-go/glm-5.3-flash` | Model for the classifier (claude / opencode) |
| `TRIAGE_PROCESS_MODEL` | `sonnet` / `opencode-go/glm-5.2` | Model for the processor (claude / opencode) |
| `TRIAGE_MAX_TURNS` | `60` / `120` | Turn cap per run (classify / process); OpenCode calls them steps |
| `TRIAGE_MAX_BUDGET_USD` | `2` / `5` | Spend cap per run; claude engine only |
| `TRIAGE_TIMEOUT_MIN` | `45` | Stop a run still going after this many minutes; `0` never stops it |
| `TRIAGE_LOG_DIR` | `~/Library/Logs/bishop-memory` | `triage.log` plus one result per run (`.json` for claude, `.jsonl` events for opencode) |
| `TRIAGE_ADD_DIRS` | every registered harness checkout | Directories the processor may read |
| `CLAUDE_BIN` / `OPENCODE_BIN` | `claude` / `opencode` | CLI to run for each engine |
| `TRIAGE_ENV_FILE` | `<bishop-root>/.env` | Credentials and defaults sourced before every run |
| `ANTHROPIC_API_KEY` or `CLAUDE_CODE_OAUTH_TOKEN` | — | Required for scheduled runs on the claude engine; set in `.env`. The API key wins if both are set |

### Changing the taxonomy

Edit `db/finding-categories.json`, then:

```bash
make triage-seed                                      # upsert descriptions
make triage-classify RECLASSIFY=brief-writing         # re-run one category
scripts/triage-seed-categories.py --deactivate-missing  # retire slugs you removed
```

A deactivated category keeps its classifications (they reference it) but is
skipped by the rotation and refused for new classifications.

## Troubleshooting

**`healthz` says `schema incomplete` after the upgrade.** The service was not
restarted after `git pull`; the tables are created at boot. Restart it.

**`triage-run.sh` exits 2.** It printed why: the service is down, `claude`
or `opencode` is not on the job's PATH, `bin/mcpd` could not be built, the
model id does not fit the engine, or OpenCode did not resolve the agent to
its `.opencode/agents/` twin. For a launchd job, the PATH baked into the
plist is printed by `scripts/install-triage-schedule.sh`; re-run it after
installing tools somewhere new.

**The log says `agent stopped after TRIAGE_TIMEOUT_MIN=45 minutes`.** The
run was still going at the limit and was stopped, so the next night is not
blocked behind it. Its run row stays `running` on the Runs tab. Open the
result file to see where it stalled. If a large backlog genuinely needs
longer, raise the limit in `.env`.

**An opencode run fails with `Unexpected server error`.** OpenCode's
provider refused the model. Check that the id appears in `opencode models`
and runs by hand, for example `opencode run -m opencode-go/glm-5.2 "say ok"
</dev/null`. Without the `</dev/null`, `opencode run` waits for input on
stdin.

**The job runs but the agent exits non-zero.** Open the JSON result in
`TRIAGE_LOG_DIR` (`triage-<kind>-<timestamp>.json`); `result` carries the
agent's last message and `is_error` the failure. A run row left `running` in
the Runs tab means the agent never reached `triage_run_finish`. On the
opencode engine the result is `triage-<kind>-<timestamp>.jsonl`. When the log
line starts `ERROR run closed as failed`, the agent gave up and said why in
the run's notes on the Runs tab. The usual cause is a tool call that failed
twice, which the doctrine says to stop on rather than work around.

**The result JSON says `Not logged in · Please run /login`, or the job hangs
before its first tool call.** The job has no credentials. A terminal shell
often carries an `ANTHROPIC_API_KEY` (Claude Code sets one for its own
subprocesses), which is why the same command works by hand; launchd does
not. Put the key in `<bishop-root>/.env` (mode 0600) as shown in the install
section. The runner warns at start when neither `ANTHROPIC_API_KEY` nor
`CLAUDE_CODE_OAUTH_TOKEN` is set.

**`export-decisions.py` exits 2 for a harness.** Its memory root is not a
directory (external volume unmounted) or the harness is not registered; run
the reconciler for that harness once.

**A finding shows `harness: ?` on the page.** It matched no harness's
`FINDINGS.md` during the backfill. Set it by hand:
`curl -X PUT :8787/v1/findings/<id>/harness -d '{"harness":"kirsch"}'`.

**The classifier put something in the wrong category.** Decide it anyway, or
re-classify the category after tightening the description. A single finding
can be moved with `PUT /v1/triage/classifications`.

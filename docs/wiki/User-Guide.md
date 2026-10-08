# User Guide

How to use bishop-memory from the operator's seat: install it, connect a harness, understand what the crew can do with it, review findings, follow missions, and keep the Markdown and the service in step. Reference detail lives in [MCP Tool Reference](MCP-Tool-Reference) and [Commands and Scripts](Commands-and-Scripts); this page is the narrative.

## 1. What you are running

Three processes and one file:

| Piece | What it is | Where it runs |
|---|---|---|
| `memoryd` | The HTTP service. Gin router, SQLite with FTS5, on `127.0.0.1:8787` by default | A launchd user agent (macOS) or a systemd service (Linux), on this machine or a server |
| `mcpd` | The MCP adapter. A stdio server that proxies the HTTP API as typed tools | Started by Claude Code from the harness's `.mcp.json`, one per session |
| Triage agents | Two headless agents (Claude Code, or OpenCode) that classify and pre-decide findings | Nightly launchd jobs, or `make triage-*` by hand |
| `data/memory.db` | The database. Missions, steps, audit journal, findings, patterns, service records, directives, documents, and the triage tables | Beside the checkout |

By default the service binds `127.0.0.1` with no API keys, so only local processes can reach it and nothing asks for a key. To run it on a server and reach it from other machines, every client sends an API key and the traffic goes over Tailscale, TLS, a reverse proxy or an SSH tunnel: [Server Install](Server-Install) covers all of it, including the changes each harness needs.

## 2. Install

### macOS

```bash
git clone git@github.com:djm56/bishop-memory.git
cd bishop-memory
scripts/install-daemon.sh --memory-root /abs/path/to/a/harness/.claude/memory
curl -s http://127.0.0.1:8787/healthz          # {"ok":true,"schema":"ok",...}
make build-mcpd                                 # bin/mcpd, referenced by every harness .mcp.json
```

The installer builds `memoryd`, stages the binary on the internal disk (launchd cannot run a binary from an external volume), writes the plist with `MEMORY_ROOT` baked in, and reloads the job. Re-running it is how you upgrade: it rebuilds, restages and restarts, and the schema upgrades itself at boot.

### Linux

```bash
make dist-linux-amd64        # or dist-linux-arm64; static binary, no C toolchain
sudo scripts/install-daemon-linux.sh
```

The Linux installer creates a `bishop-memory` system user and a systemd unit. Without root it builds and prints the commands that still need root.

### On a server

`scripts/install.sh server` installs `memoryd` on Ubuntu, any Linux with systemd, or a Mac, listening on a network address with API keys; `scripts/install.sh client` sets up each machine that talks to it. See [Server Install](Server-Install).

### Upgrading

`git pull`, then the same installer command. Existing databases gain new tables and columns at the next boot; `healthz` reports `schema incomplete` if a restart was missed.

## 3. Connect a harness

Everything about the connection lives in the harness, not here. In the harness checkout:

1. Copy `.claude/bishop-memory.conf.example` to `.claude/bishop-memory.conf` and set `BISHOP_MEMORY_MODE=central`, a unique `BISHOP_HARNESS` name, and `BISHOP_MEMORY_HOME` pointing at this checkout.
2. Run `.claude/connect-bishop-memory.sh`. It writes a project-scope `.mcp.json` that starts `bin/mcpd` with that harness's name.
3. Restart Claude Code and approve the `bishop-memory` MCP server when prompted.
4. Reconcile once so the harness's existing Markdown reaches the service:

```bash
/path/to/bishop-memory/scripts/reconcile-memory.py --root .claude/memory --harness <name>
```

With no conf file, or `BISHOP_MEMORY_MODE=standalone`, the harness never calls the service. [Developer: Harness Integration](Developer-Harness-Integration) explains why the switch is a file in the harness.

## 4. What the crew does with it during a mission

You do not call the tools yourself; Bishop and the crew do. Knowing which tool fires when makes the journal readable.

| Moment | Tool | Why |
|---|---|---|
| Mission start, central mode | `mission_allocate` | A mission id unique across every harness that day |
| Every step | the state-continuity hook, not a tool | Mirrors each new `FLIGHT-RECORDER.md` row and `PROGRESS.md` step live |
| An agent records an event directly | `flight_recorder_append`, `mission_step_record` | Actor stored as `<harness>:<agent>` |
| Before briefing an area | `finding_list` with `status=approved` and a category | The crew applies ratified findings; `DIRECTIVES.md` is read from the file |
| Looking something up | `memory_search`, `pattern_list`, `service_record_list` | FTS5 over imported Markdown; advisory patterns; per-agent calibration |
| Learning pass at close | `finding_append`, `pattern_append`, `service_record_append` | Usually written to Markdown by the doc writer instead; the hook and the step-H reconcile mirror them |
| Mission close, step H | the reconciler | Backstop that brings every structured table into line with the Markdown |

Two rules hold for the crew. A finding is always created `proposed`; no tool can advance it. Directives are read-only to every agent.

## 5. Findings triage: the daily loop

Findings accumulate at tens per mission. The triage loop keeps them moving without taking the decision away from you.

```
21:00  classifier (Haiku)   every unclassified finding gets a category, a confidence, a one-line summary
21:20  processor  (Sonnet)  one category: groups findings that share a rule, recommends approve / reject /
                            supersede / defer on each, drafts a DIRECTIVES.md entry where a rule has earned one
you    review page          http://127.0.0.1:8787/triage — decide, one click per finding or per group
you    make triage-export   decisions written into the owning harness's FINDINGS.md and DIRECTIVES.md
```

![The review page, Pending tab](images/pending-light.png)

First-time setup, after the service is installed:

```bash
printf 'ANTHROPIC_API_KEY=sk-ant-...\n' >> .env && chmod 600 .env     # scheduled jobs have no Claude login
make triage-seed
make triage-backfill REGISTER="kirsch=/abs/path/kirsch/.claude/memory"
make triage-classify
make triage-install                                                    # nightly launchd jobs
```

On OpenCode instead (cheaper models, your own OpenCode config and key): skip the `.env` key and add `TRIAGE_ENGINE=opencode` to `.env`. The defaults are `opencode-go/glm-5.3-flash` for the classifier and `opencode-go/glm-5.2` for the processor; [Developer: Triage Agents](Developer-Triage-Agents#switching-to-opencode) covers choosing models and switching one job at a time.

[Review Page Guide](Review-Page-Guide) walks through deciding. [Developer: Triage Agents](Developer-Triage-Agents) explains what the agents read and why their recommendations are checkable.

## 6. Following a mission

The mission HUD at `http://127.0.0.1:8787/missions` (also where `/` lands) shows one mission at a time: the brief with its acceptance criteria ticked from the debrief, a step timeline with each agent's crew role, start time, duration and summary, the linked findings with their triage state, the debrief, and the patterns, directives, crew and service records that came out of it. A **Triage / Missions** switch in the header moves between it and the review page.

![The mission HUD](images/missions-light.png)

The brief, progress and debrief reach it as documents. Once the harness is registered (a reconcile with `--harness` does that), sync them:

```bash
curl -X POST http://127.0.0.1:8787/v1/documents/sync     # every registered harness, plus its agent definitions
make mission-links APPLY=1                                # once, to link older findings and steps
```

After that each mission update re-imports its own three files. With the service on another machine, the sync above cannot see the harness files; the reconciler pushes them instead (see [Server Install](Server-Install#9-first-upload-of-each-memory-tree)). [Mission HUD Guide](Mission-HUD-Guide) covers the page, the backfill and `make scratch-clean` for old workspace scratch.

## 7. Keeping Markdown and the service in step

Markdown under `.claude/memory/` is the record. The service is a derived copy, kept current three ways:

- **Live**: the harness's `state-continuity.sh` hook mirrors journal rows and steps as they are written, and triggers a reconcile after memory-tree writes.
- **At close**: step H of the closing checklist runs the reconciler.
- **By hand**: `scripts/reconcile-memory.py --root … --harness …` any time, idempotent. `--dry-run` shows what would change.

One flow runs the other way: your decisions on the review page go back into the Markdown through `make triage-export`. The exporter changes only the three status lines of an entry and appends ratified directives, backs the file up first, and refuses when the file already disagrees. A status you set by hand in `FINDINGS.md` is mirrored into the service by the next reconcile.

## 8. Day-to-day checks

```bash
make health                                  # liveness and schema
curl -s 'http://127.0.0.1:8787/v1/findings?status=proposed' | jq '.findings | length'
curl -s http://127.0.0.1:8787/v1/triage/runs | jq '.runs[0]'
tail -5 ~/Library/Logs/bishop-memory/triage.log
sqlite3 data/memory.db 'SELECT status, COUNT(*) FROM findings GROUP BY status'
```

When something looks wrong, start at [Troubleshooting](Troubleshooting).

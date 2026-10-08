# bishop-memory

A memory service for the Bishop agent harness. One Go binary, one SQLite file, an MCP adapter the crew talks to, a nightly findings-triage loop with a review page for the operator, and a mission HUD that shows each mission's history in one place. It runs beside the harness on one machine with nothing to configure, or on a server (Ubuntu, any Linux, or a Mac) that several machines reach across the network with API keys.

**Source of these pages:** `docs/wiki/` in the repository. Edit there and run `make wiki-publish`; edits made here on github.com are overwritten.

## For end users

Read these in order if you run a harness against bishop-memory or review its findings.

| Page | What it covers |
|---|---|
| [User Guide](User-Guide) | Installing the service, connecting a harness, what the crew can do, the daily workflow |
| [Server Install](Server-Install) | Running `memoryd` on a server and reaching it across the network: transport, API keys, client set-up, harness changes, moving the database, backups |
| [MCP Tool Reference](MCP-Tool-Reference) | Every MCP tool in both profiles: arguments, examples, what comes back, what can go wrong |
| [Review Page Guide](Review-Page-Guide) | Deciding findings and ratifying directives at `/triage`, and getting decisions back into Markdown |
| [Mission HUD Guide](Mission-HUD-Guide) | Reading a mission's brief, steps, findings and debrief at `/missions`, and getting its documents and links into the database |
| [Commands and Scripts](Commands-and-Scripts) | Every `make` target and script with its flags, exit codes and when to use it |
| [Troubleshooting](Troubleshooting) | Symptoms, causes, fixes |

## For developers

Separate documents, one per subsystem.

| Page | What it covers |
|---|---|
| [Developer Overview](Developer-Overview) | Repository layout, layering, the design rules that keep the service small |
| [Developer: HTTP API](Developer-HTTP-API) | Route catalogue, authentication, error contract, how to add a route |
| [Developer: Database](Developer-Database) | Every table, the migration path, how to add a column or table safely |
| [Developer: MCP Adapter](Developer-MCP-Adapter) | How `mcpd` proxies the API, the two profiles, how to add a tool |
| [Developer: Scripts and Reconciler](Developer-Scripts-and-Reconciler) | The reconciler, the exporter, the backfills, scratch cleanup, natural keys, the write-back contract |
| [Developer: Triage Agents](Developer-Triage-Agents) | The classifier and processor, the skill, the runner, scheduling, tuning |
| [Developer: Testing and CI](Developer-Testing-and-CI) | The verification gate, test conventions, what each suite proves |
| [Developer: Harness Integration](Developer-Harness-Integration) | Standalone versus central mode, the conf file, the hook, what crosses the boundary |

## The one-paragraph version

![The review page, Pending tab](images/pending-light.png)

The harness writes its memory as Markdown under `.claude/memory/`. bishop-memory keeps a derived copy in SQLite so several harnesses can share mission IDs, an audit journal, a findings ledger and a full-text search index, all reachable through MCP tools. Markdown stays the source of truth; the reconciler mirrors it in. Findings pile up faster than anyone reads them, so a nightly classifier sorts them into categories and a nightly processor groups each category and recommends a decision on every finding; the operator decides on the review page, and the exporter writes those decisions back into the harness's Markdown. No agent ever changes a finding's status. The mission HUD beside the review page reads the same database by mission: brief, step timeline, findings, debrief and what came out of it.

# bishop-memory

A locally bound memory service for the Bishop agent harness. One Go binary, one SQLite file, an MCP adapter the crew talks to, and a nightly findings-triage loop with a review page for the operator.

**Source of these pages:** `docs/wiki/` in the repository. Edit there and run `make wiki-publish`; edits made here on github.com are overwritten.

## For end users

Read these in order if you run a harness against bishop-memory or review its findings.

| Page | What it covers |
|---|---|
| [User Guide](User-Guide) | Installing the service, connecting a harness, what the crew can do, the daily workflow |
| [MCP Tool Reference](MCP-Tool-Reference) | Every MCP tool in both profiles: arguments, examples, what comes back, what can go wrong |
| [Review Page Guide](Review-Page-Guide) | Deciding findings and ratifying directives at `/triage`, and getting decisions back into Markdown |
| [Commands and Scripts](Commands-and-Scripts) | Every `make` target and script with its flags, exit codes and when to use it |
| [Troubleshooting](Troubleshooting) | Symptoms, causes, fixes |

## For developers

Separate documents, one per subsystem.

| Page | What it covers |
|---|---|
| [Developer Overview](Developer-Overview) | Repository layout, layering, the design rules that keep the service small |
| [Developer: HTTP API](Developer-HTTP-API) | Route catalogue, error contract, how to add a route |
| [Developer: Database](Developer-Database) | Every table, the migration path, how to add a column or table safely |
| [Developer: MCP Adapter](Developer-MCP-Adapter) | How `mcpd` proxies the API, the two profiles, how to add a tool |
| [Developer: Scripts and Reconciler](Developer-Scripts-and-Reconciler) | The reconciler, the exporter, the backfill, natural keys, the write-back contract |
| [Developer: Triage Agents](Developer-Triage-Agents) | The classifier and processor, the skill, the runner, scheduling, tuning |
| [Developer: Testing and CI](Developer-Testing-and-CI) | The verification gate, test conventions, what each suite proves |
| [Developer: Harness Integration](Developer-Harness-Integration) | Standalone versus central mode, the conf file, the hook, what crosses the boundary |

## The one-paragraph version

The harness writes its memory as Markdown under `.claude/memory/`. bishop-memory keeps a derived copy in SQLite so several harnesses can share mission IDs, an audit journal, a findings ledger and a full-text search index, all reachable through MCP tools. Markdown stays the source of truth; the reconciler mirrors it in. Findings pile up faster than anyone reads them, so a nightly classifier sorts them into categories and a nightly processor groups each category and recommends a decision on every finding; the operator decides on the review page, and the exporter writes those decisions back into the harness's Markdown. No agent ever changes a finding's status.

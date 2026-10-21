# Harness Portability — Plan

**Status as of 2026-10-20: phase 1 shipped on `main` through PR #8 (`8437f32`,
commits `260bb7c^..718cb7a`); the reconciler-bug part of phase 2 shipped on
`main` through PR #9 (`1c4def9`); the layout profile and later phases not
started.**
Phase 1 adds golden tests for the importer and the reconciler's dry-run output,
the `internal/profile` seam for the Go values, the `BISHOP_LAYOUT` and
`APPROVER_DEFAULT` seam in the reconciler, and a CI step that runs the Python
script tests. This document records the coupling inventory from the code, what
the vendor documentation says about each target harness, the target
architecture, and a phased plan with exit criteria. The Bishop harness must keep
working unchanged through every phase (§8).

Phase 1 does not pin `AgentsDir`, `DefaultHarness`, `ReviewerRole` or request
payloads, and the fixture contains no nested mission folders, so a golden cannot
see behaviour that differs there. The gaps are listed in `testdata/README.md`,
"What the goldens cannot see". These are inputs for phase 2.

Date of research: 2026-10-20. Vendor pages were read on that date; where a page
shows its own date it is given in §3. Anything not confirmed from a primary
source is marked **uncertain**.

## 1. Goal

Today bishop-memory's code and conventions are built around Bishop, the
crew-based development workflow. Its memory tree, its Markdown tables, its agent
names and its environment variables are written into the service, the reconciler and the
MCP adapter.

The goal is a general memory service that can be installed into:

- **plain Claude Code** (no Bishop doctrine, no crew);
- **OpenCode**;
- **pi** (the coding agent now maintained by Earendil; see §3.3 for the
  identity correction);
- **Kirsch**, the operator's own custom agent harness, which is treated as a
  target with an integration contract it implements. Its specifics are the
  operator's to supply (§11).

Bishop remains the first and reference harness. Nothing it does today may stop
working.

## 2. Coupling inventory

The inventory was taken from the code on `docs/roadmap-progress` at `8a4714b`.
Line numbers are for that commit. Each item is classed as:

- **core-generic** — works for any harness; no change needed.
- **Bishop-specific-but-configurable** — Bishop's convention, but a value,
  path or name could come from configuration instead.
- **hard-coupled** — a Bishop name or convention is written into logic, so it
  must be changed in code, not configuration.

### 2.1 Summary

The service core (HTTP API, SQLite schema, FTS5 search, API keys, TLS, the push
route, the harness registry) is generic. Coupling sits at these edges:

1. **The importer and reconciler**, which know the memory tree and the Markdown
   tables Bishop writes.
2. **The MCP adapter** (`mcpd`), which names Bishop's harness and sub-agent
   identity, and whose tool descriptions refer to Bishop's config file.
3. **The triage and grading agents**, which are Bishop-named in tool names,
   agent definitions and the `bishop-triage` MCP server.
4. **The installers and the harness-side hooks**, which write Bishop's config
   file and use `BISHOP_*` names.

### 2.2 Memory tree layout and file formats

| Area | Where | What it assumes | Class |
|---|---|---|---|
| Kind mapping by path | `internal/importer/importer.go:14-28`, `kindFromPath` at `:694-745` | `state/`, `missions/<id>/{BRIEF,PROGRESS,DEBRIEF}.md`, `findings/FINDINGS.md`, `findings/PATTERNS.md`, `findings/service-records/`, `reference/DIRECTIVES.md`, `workspace/`. Any other top-level directory becomes a kind named after it. | Bishop-specific-but-configurable |
| Reconciler layout | `scripts/reconcile-memory.py:13-19` (layout comment); `parse_missions` `:416`, `parse_steps` `:507`, `parse_journal` `:528`, `parse_findings` `:551`, `parse_directives` `:587`, `parse_service_records` `:635` | The same paths, hard-wired into each parser | Bishop-specific-but-configurable |
| Reconciler `--root` help | `scripts/reconcile-memory.py:1188` | "Path to the `.claude/memory` tree" | Bishop-specific-but-configurable (help text only) |
| Agent definitions beside the root | `internal/importer/agents.go:15-16` (`AgentsDirFor` returns `<checkout>/.claude/agents`); `scripts/reconcile-memory.py:224-233` (`agent_files`) | The agent folder is a sibling of `memory/` under `.claude/` | Bishop-specific-but-configurable |
| CURRENT-MISSION fields | `internal/importer/importer.go:750-756` (`currentMissionKnownFields`: Mission ID, Status, Owner, Next Action, Last Updated, Blockers) | Bullet-list format from Bishop's template | Bishop-specific-but-configurable |
| PROGRESS, FLIGHT-RECORDER, MISSION-ARCHIVE table columns | `scripts/reconcile-memory.py:324` (`table_rows` with expected column counts), `:507-522` (PROGRESS columns), `:421` (archive columns), `:528` (journal) | Column count and order from Bishop's templates | Bishop-specific-but-configurable |
| Step status vocabulary | `scripts/reconcile-memory.py:522` (`pending`, `in-progress`, `done`, `failed`); `internal/api/missions.go:30` (`not-started`, `in-progress`, `blocked`, `complete`) | Bishop's lifecycle words. Unknown step statuses are silently dropped (ROADMAP, Reconciler) | Core-generic in the service, Bishop-specific in the reconciler |
| Flight-recorder pipe escaping | `internal/importer/importer.go:810-816`, `prependFlightRecorderFields` `:828` | Markdown table escaping `\|` | Core-generic format rule, Bishop-specific file |
| DEBRIEF section names | `internal/debrief/parser.go:12,27,35` ("Sub-Agent Mistakes and Corrections", "Deliverables Changed"); `internal/importer/lessons.go:70` ("Wrong Assumptions"); `internal/importer/criteria.go:19-20` ("Acceptance Criteria", "Acceptance Criteria Outcome"); `internal/api/grades.go:229-236` ("Tracker And Reality", "QA Verdict", and the others) | Bishop's DEBRIEF and BRIEF template headings | Bishop-specific-but-configurable (the headings move into a profile, §4.4) |
| Review verdict line | `internal/review/verdict.go:38` — the fixed `Verdict: APPROVED \| NOT APPROVED; critical=N; warnings=N; reviews_step=S` line | Bishop's reviewer convention | Bishop-specific-but-configurable (parser accepts text fallback, `:19-21`) |
| Service-record source enum | `internal/model/service_record.go:30` (`self-reported`, `bishop-observed`) | The word "bishop-observed" is a Bishop term in a generic enum | Hard-coupled (a rename is a schema and API change) |
| Finding ownership line | `scripts/reconcile-memory.py:559-561` (`**Mission**:` field, matched by `MISSION_ID`) | Bishop's FINDINGS entry format | Bishop-specific-but-configurable |

### 2.3 Mission ID scheme

| Area | Where | What it assumes | Class |
|---|---|---|---|
| Allocator pattern | `internal/api/allocate.go:56` — `^mission-(\d{8})-(\d+)$`, with the comment at `:48-55` | UTC date and a daily counter | Core-generic in the service (a configurable prefix is cheap) |
| Reconciler pattern | `scripts/reconcile-memory.py:110` — `mission-\d{8}-\d{2}`, used at `:561` | Two digits, with **no end anchor**, so it matches the prefix of any longer ID. The allocator is anchored and its `(\d+)` takes one or more digits (the comment at `allocate.go:49-50` says two or more, growing past 99) | Hard-coupled, and **already inconsistent** (see §2.7) |
| Stale rule | `internal/api/hud.go:40` (`staleAfter = "-48 hours"`) | Bishop's 48-hour stale convention | Core-generic policy, configurable value |

### 2.4 Crew, agent names and identity

| Area | Where | What it assumes | Class |
|---|---|---|---|
| Review role is `apone` | `internal/api/crew.go:205` — `importer.CrewName(agentStr) == "apone"` | The reviewer agent is named `apone` in Bishop's crew | Hard-coupled |
| Crew name normalising | `internal/importer/agents.go:77-84` (`CrewName` strips `@`, lower-cases and strips a `harness:` prefix); example at `:78` ("bishopharnessopencode:hicks") | `@agent` and `harness:agent` forms | Core-generic idea, Bishop-flavoured example |
| Role from description | `internal/importer/agents.go:117-126` (`roleFrom`) | Bishop's description pattern "Code reviewer (apone). …" | Bishop-specific-but-configurable (degrades to the whole description) |
| Default harness | at `250202c`, `cmd/mcpd/main.go:63-69` (`defaultHarness = "claude-code"`); on `main`, `internal/profile/profile.go:61` (`DefaultHarness`), read at `cmd/mcpd/main.go:98` | A default identity of `claude-code` when unset | Core-generic value, but a silent default that hides a missing registration |
| Identity composition | `cmd/mcpd/main.go:1460` (`composeAgent`, `<harness>:<agent>`); `:928-947` and `:987-1000` (`flight_recorder_append`, `mission_step_record` require `agent`) | Actor is `<harness>:<agent>` | Core-generic (the pattern is good for any harness) |
| Sub-agent argument descriptions | `cmd/mcpd/main.go:327` and `:351` ("e.g. \"bishop\", \"hicks\"") | Examples name Bishop crew | Bishop-specific-but-configurable (text only) |
| Step agent value | `internal/model/mission_step.go:18` (comment: `@bishop`, `@hicks`); `internal/debrief/parser.go:13,19` | `@name` in the Agent cell | Bishop-specific-but-configurable |
| Service-record subject | `cmd/mcpd/main.go:437-438` (the subject, not composed), `:442` (the `agent` parameter) | Subject name, not actor | Core-generic |
| Crew page | `internal/ui/crew.html`, `GET /v1/crew` (`internal/api/crew.go`) | Reads crew from the `crew` table filled from agent files | Core-generic once the agent folder is configurable |

### 2.5 Environment names, config file and connect script

| Area | Where | What it assumes | Class |
|---|---|---|---|
| `BISHOP_HARNESS` identity | `cmd/mcpd/main.go:30, 66, 101` (read); `cmd/memoryd` none; `scripts/reconcile-memory.py:1191-1192` (read as harness fallback) | One name for the harness identity, shared by mcpd and the reconciler | Bishop-specific name, core-generic role |
| `BISHOP_MEMORY_URL`, `BISHOP_MEMORY_API_KEY`, `BISHOP_MEMORY_CA_FILE`, `BISHOP_MEMORY_CLIENT_ENV` | `cmd/mcpd/main.go:100`; `cmd/mcpd/clientenv.go:16-18, 70-96`; `scripts/reconcile-memory.py:72-101, 1189`; `scripts/push-memory.py:33`; `scripts/clean-scratch.py:17, 75`; `scripts/export-decisions.py:655`; `scripts/triage-run.sh:34` | Client variables under the Bishop prefix | Bishop-specific-but-configurable (rename with aliases) |
| Server variables | `internal/config/config.go:79-101` (`BISHOP_API_KEYS_FILE`, `BISHOP_API_KEY`, `BISHOP_ALLOW_NO_AUTH`) | Server-side names | Bishop-specific-but-configurable |
| Config file | `cmd/mcpd/main.go:215, 750-770` (error text and tool description name `.claude/bishop-memory.conf`); `scripts/install.sh:305` (writes it); wiki `Developer-Harness-Integration.md` | `.claude/bishop-memory.conf` is the switch | Bishop-specific-but-configurable (path and key names) |
| Install output | `scripts/install.sh:31, 37, 305-308` (names `.mcp.json / opencode.json`, `.claude/bishop-memory.conf`) | Writes Bishop's paths; mentions OpenCode in text only, no generator | Bishop-specific-but-configurable |
| Connect script and state-continuity hook | Live in the **bishop-harness** repository (`.claude/connect-bishop-memory.sh`, `state-continuity.sh`, `reconcile-bishop-memory.sh`), not here. Described in `docs/wiki/Developer-Harness-Integration.md` and ROADMAP "Harness side" | Bishop's hook writes the journal mirror and runs the reconciler | Hard-coupled to Bishop; out of this repository, so each replacement lives in the target's own adapter |
| Status words in HUD | `internal/ui/missions.html:392`, `crew.html:218`, `performance.html:433`, `triage.html:369` (`localStorage` key `bishop.apiKey`) | A browser key name | Cosmetic; rename with the others |

### 2.6 Triage and grading agents

| Area | Where | What it assumes | Class |
|---|---|---|---|
| Engine switch | `scripts/triage-run.sh:35-36, 190-193` (`TRIAGE_ENGINE=claude\|opencode`) | The engine is already chosen at run time by `TRIAGE_ENGINE` | Core-generic in shape |
| Agent folders | `scripts/triage-run.sh:21-23` (`.claude/agents/`, `.opencode/agents/`), `:253` (`AGENT="findings-classifier"`) | Bishop's repository layout and agent names | Hard-coupled to this repository's files |
| Tool allow-list | `scripts/triage-run.sh:256` (`mcp__bishop-triage__*`); `.claude/agents/*.md` `tools:` lines; `.opencode/agents/*.md` `bishop-triage_*` permissions | The MCP server is named `bishop-triage` | Hard-coupled (the server name is in `findings-processor`, `findings-classifier` and `mission-grader` under both `.claude/agents/` and `.opencode/agents/`, and in the script) |
| Triage MCP profile | `cmd/mcpd/triage.go`; `MCPD_PROFILE=triage` (ROADMAP, Shipped) | A separate tool set under the same server name | Core-generic profile idea |
| Doctrine paths | `.claude/skills/findings-triage/SKILL.md`, `.claude/skills/mission-grading/SKILL.md` (named in `triage-run.sh:24-25`) | The agents read Bishop-style skill files | Bishop-specific-but-configurable |
| Grader packet | `internal/api/grades.go:229-236, 356, 400` | Reads Bishop's DEBRIEF sections | Bishop-specific-but-configurable (see §2.2) |

### 2.7 Bugs found during the inventory

These are not portability work, but the inventory turned them up and they
belong in phase 2.

- **The mission-ID rules disagree.** The allocator is anchored at both ends
  and takes a numeric counter, matched by `(\d+)`, which is one or more digits
  (`allocate.go:56`). The comment at `:49-50` describes the id as two digits or
  more, which is the convention, not what the regex enforces. The comment at
  `:48-55` says that a harness-qualified id, whose trailing number
  is not a global sequence number, must not fold into the sequence. The
  reconciler matched `mission-\d{8}-\d{2}` with no end anchor at `250202c`
  (`reconcile-memory.py:110`, used at `:561`). A finding that names
  `mission-20261009-100` is linked to `mission-20261009-10`, and a finding that
  names a harness-qualified id such as `mission-20260907-anom-01` is not linked
  at all. The first is a wrong link. The second is a missed link. At `250202c`
  the reconciler reported no warning for these. Fixed on `main` by PR #9
  (`1c4def9`): see §7 phase 2.
- **The reconciler drops unknown step statuses silently**, as ROADMAP already
  records (`reconcile-memory.py:522` at `250202c`). Fixed on `main` by PR #9
  (`1c4def9`): see §7 phase 2.
- **`defaultHarness` silently substitutes `claude-code`** (`cmd/mcpd/main.go:69` at `250202c`; on `main`, `DefaultHarness` in `internal/profile/profile.go:61`).
  A harness that forgets to set `BISHOP_HARNESS` writes its audit rows under the
  wrong identity. For any harness other than Claude Code this is a trap.

## 3. Research: the target harnesses

The findings below come from the vendor documentation as read on 2026-10-20.
Each claim names where it came from. Where a source was secondary, or a page
gave no date, it is labelled.

### 3.1 Claude Code (plain, without Bishop)

Sources: [MCP](https://code.claude.com/docs/en/mcp),
[Hooks](https://code.claude.com/docs/en/hooks),
[Memory (CLAUDE.md and AGENTS.md)](https://code.claude.com/docs/en/memory),
[Plugins overview](https://code.claude.com/docs/en/plugins).

- **MCP registration.** `claude mcp add --transport stdio <name> -- <command> [args]`
  adds a stdio server, with `-e KEY=value` for environment. Scopes are
  **local** (default; stored in `~/.claude.json` for the project path),
  **project** (`--scope project`; written to `.mcp.json` at the project root and
  committed), and **user** (all projects, `~/.claude.json`). `.mcp.json` supports
  `${VAR}` and `${VAR:-default}` expansion in `command`, `args`, `env`, `url`
  and `headers`. `${CLAUDE_PROJECT_DIR:-.}` is the documented way to refer to the
  project root in a project file.
- **Approval.** In an interactive session Claude Code asks before using a
  project's `.mcp.json` servers. In `claude -p`, the Agent SDK and cloud
  sessions it does not ask, so a headless run loads them silently. This matters
  for the triage runner, which uses `claude -p`.
- **Scope and identity.** A user-scope registration gives every project the
  same environment. That is the reason the Bishop harness uses project scope
  with a per-harness `BISHOP_HARNESS` (see `docs/wiki/Developer-Harness-Integration.md`).
  The same reasoning applies to a plain Claude Code project.
- **Hooks.** Events include `SessionStart`, `UserPromptSubmit`, `PreToolUse`,
  `PostToolUse`, `PostToolUseFailure`, `SubagentStart`, `SubagentStop`, `Stop`,
  `PreCompact`, `PostCompact`, `Notification`, `InstructionsLoaded`, `CwdChanged`,
  `FileChanged`, and `SessionEnd`, among others. `PostToolUse` cannot block
  (the tool already ran); `Stop` and `SubagentStop` can keep the agent working.
  Hooks are configured in `~/.claude/settings.json`, `.claude/settings.json`,
  `.claude/settings.local.json`, plugin `hooks/hooks.json`, or a skill or agent's
  frontmatter. Handlers are `command`, `http`, `mcp_tool`, `prompt` or `agent`.
- **Hook input.** Every hook gets `session_id`, `transcript_path`, `cwd`,
  `hook_event_name`, and — inside a subagent — `agent_id` and `agent_type`.
  Those fields give a mirror hook the **agent identity** without reading a
  Markdown file. MCP tools do not receive that context, so the `agent` argument
  stays necessary on the identity-bearing tools (`flight_recorder_append`,
  `mission_step_record`).
- **Instruction files.** `CLAUDE.md` at the project root or `.claude/CLAUDE.md`,
  and `~/.claude/CLAUDE.md` for the user; `CLAUDE.local.md` for personal use;
  `@path/to/file` imports. Claude Code also reads a repository's `AGENTS.md` when
  no `CLAUDE.md` is present (requires v2.1.277 or later, per the page). Caveats
  from the same page:
  - A `CLAUDE.md`, `CLAUDE.local.md` or `.claude/CLAUDE.md` in the working
    directory or any directory above it stops `AGENTS.md` from loading by default.
  - Before v2.1.281, some sessions, such as those on Amazon Bedrock or with
    telemetry disabled, read `CLAUDE.md` files only.
  Source: [Memory (CLAUDE.md and AGENTS.md)](https://code.claude.com/docs/en/memory),
  section "AGENTS.md", read 2026-10-20.
- **Skills and agents.** `SKILL.md` folders and subagent Markdown with
  frontmatter, under `.claude/` or inside a plugin.
- **Plugins.** A plugin is a directory with `.claude-plugin/plugin.json`. It may
  ship skills, agents, `hooks/hooks.json`, `.mcp.json` and commands.
  Marketplaces are catalogues in `.claude-plugin/marketplace.json`, added with
  `/plugin marketplace add`. An enabled plugin is active in every session it is
  enabled for, so its hooks fire and its servers run. It runs with the user's
  privileges. Plugin hooks can use `CLAUDE_PLUGIN_ROOT` and `CLAUDE_PLUGIN_DATA`.
  The plugin does not install the `mcpd` binary; that stays a separate install.

Date note: the MCP page and the plugin overview are undated on the page. The
hook page lists versions up to v2.1.295 and was read on 2026-10-20.

### 3.2 OpenCode

Sources: [MCP servers](https://opencode.ai/docs/mcp-servers/) (page shows
"Last updated: Oct 19, 2026"), [Config](https://opencode.ai/docs/config/)
(Oct 19, 2026), [Plugins](https://opencode.ai/docs/plugins/) (Oct 19, 2026).

- **Config location.** Project `opencode.json` or `opencode.jsonc` at the
  project root (OpenCode checks the current directory and walks up to the
  nearest Git directory); global `~/.config/opencode/opencode.json`; a custom
  file named by `OPENCODE_CONFIG`; inline `OPENCODE_CONFIG_CONTENT`. Configs are
  **merged**, with later sources winning on conflicting keys.
- **MCP registration.** Under a top-level `"mcp"` key, each server gets a name.
  `"type": "local"` takes a `"command"` array plus optional `"environment"`,
  `"cwd"`, `"enabled"` and `"timeout"` (milliseconds; default 5000 per the page).
  `"type": "remote"` takes a `"url"` with optional `"headers"`, `"oauth"`,
  `"enabled"` and `"timeout"`. `"oauth": false` turns off OAuth detection for
  key-based servers. Tools are prefixed with the server name, which is exactly
  what the repository's OpenCode triage agents already rely on
  (`bishop-triage_*`).
- **Tool permissions.** A top-level `"tools"` object can switch MCP tools off,
  with glob patterns. Per-agent access sits under `"agent"` → `"tools"`.
- **Agents.** Set under the `"agent"` key in config (fields such as
  `description`, `model`, `prompt`, `tools`), or as Markdown files in
  `.opencode/agents/` or `~/.config/opencode/agents/`. The repository's
  `.opencode/agents/` files already use the Markdown form. `"default_agent"`
  chooses the primary agent.
- **Plugins.** Plugins go in `.opencode/plugins/` (project) or
  `~/.config/opencode/plugins/` (global). The folder name is taken from the
  page and is **uncertain**; check it against the installed version. npm
  plugins are listed in the `"plugin"` array of `opencode.json`. A plugin exports
  an async function that receives `{ project, client, $, directory, worktree }`
  and returns hooks. Events named on the page include `session.created`,
  `session.idle`, `session.compacted`, `session.error`, `file.edited`,
  `tool.execute.before` and `tool.execute.after`. `session.idle` is the nearest
  equivalent to Claude Code's `Stop`.
- **Instructions.** The `"instructions"` key takes file paths and globs. The
  config page does not mention `AGENTS.md`. The agents.md site lists OpenCode as
  a reader of it (§3.5). Whether OpenCode loads a root `AGENTS.md` by default is
  **uncertain** here and needs a test.
- **Identity.** The harness name goes in the server's `"environment"` map, so
  that it is per project. The sub-agent name is the OpenCode agent name.

Repository status: `scripts/install.sh:307` names `opencode.json` in its output
but nothing generates it, and `.opencode/` holds the triage agents only.

### 3.3 pi (Earendil; identity corrected)

An earlier description gave pi as the coding agent by Mario Zechner
(pi-mono). Research shows the project has moved, and its MCP stance has reversed.

Sources: [pi.dev](https://pi.dev/), [GitHub repository](https://github.com/badlogic/pi-mono)
(which now resolves to `earendil-works/pi`), [pi docs: MCP](https://pi.dev/docs/latest/mcp),
[pi docs: extensions](https://pi.dev/docs/latest/extensions). Secondary
coverage of the 1.0 release and the stance change is linked in §12. Those
pages are undated, so the dates below come from secondary sources and are
**uncertain** until checked against the release notes.

- **Identity.** Maintained by **Earendil** (`Earendil Inc. & Contributors`, MIT
  licence on pi.dev). npm packages are under `@earendil-works/`, with the CLI at
  `@earendil-works/pi-coding-agent`. The repository is `earendil-works/pi`.
  One secondary source reports that Earendil acquired the project from Zechner
  "on April 8", with no year given; the year is **uncertain**.
- **MCP stance has reversed.** An earlier position, quoted in secondary sources
  as Zechner's November 2025 "pi does not and will not support MCP", is
  superseded. The current pi.dev page says "MCP is built in", through a
  Codemode sandbox, and that "MCP extensions are still supported too". Secondary
  sources date Pi 1.0 to 1 October 2026. Treat as **uncertain** until read in
  the release notes.
- **MCP registration.** User file `~/.pi/agent/mcp.json`; project file
  `.pi/mcp.json`, read only **after project trust is granted**. Both use an
  `mcpServers` object. Stdio takes `command` (one executable, not a shell
  string), plus `args`, `env` and `cwd`. HTTP takes `url` plus `headers` and
  `oauth`. The legacy SSE transport is rejected. CLI form:
  `pi mcp add <name> -- <command> [args]`, with `-l` for the project file.
- **Exposure, which matters.** Each server has an `exposure` setting. The
  default is **`codemode`**: tools are callable only from Codemode scripts and
  are **not declared to the model**. Other values are `deferred` (loaded by
  `tool_search` on a match), `direct` (declared like a built-in tool) and
  `hidden`. Memory tools that the model should call directly need
  `"exposure": "direct"`, or they will not be visible to the model. Tools are
  named `mcp__<server>__<tool>`.
- **Extensions.** TypeScript (or JavaScript) modules, loaded from
  `~/.pi/agent/extensions/` (the guide's example) or a project extensions
  directory; the project location is not stated on the page. `pi --extension
  ./file.ts` is the development route, and `/reload` reloads. Events include
  `session_start`, `session_shutdown`, `agent_start`, `agent_end`, `turn_start`,
  `turn_end`, `tool_call`, `tool_result`, `tool_execution_end` and
  `mcp_servers_change`. The docs warn that `agent_end` is **not** a final-idle
  signal, because a queued message can start another run. Extensions can also
  call `pi.registerMcpServer(name, config)`; those registrations are not saved
  and must be repeated on each load.
- **Packages.** Extensions, skills, prompt templates and themes can ship as a
  "Pi package" over npm or git: `pi install npm:@scope/name` or
  `pi install git:github.com/...`.
- **Instructions.** `AGENTS.md` is loaded at startup from `~/.pi/agent/`, from
  parent directories and from the current directory. `SYSTEM.md` can replace or
  extend the system prompt.
- **Trust.** The `project_trust` event runs before project extensions load.
  Project MCP config is read only after trust.

### 3.4 Kirsch (operator's harness)

No external research applies. Kirsch is the operator's own custom agent
harness, the same class as pi and OpenCode. This plan treats it as a target
that implements the **integration contract** in §4.2. Its specifics are the
operator's to supply (§11, the K-questions).

One fact is already in the repository: `kirsch` appears as a live harness name
in the database and in the documentation examples (ROADMAP, the "263 kirsch
findings" line; `docs/wiki/Developer-Harness-Integration.md`, the
`BISHOP_HARNESS=kirsch` example; `scripts/gen-mcp-reference.py`). So Kirsch is
already a registered harness. It is not yet known whether it writes the Bishop
memory layout or something else. The plan does not assume either.

### 3.5 Other harnesses (quick notes, not researched in depth)

- **Codex CLI.** Sources: [learn.chatgpt.com, MCP](https://learn.chatgpt.com/docs/extend/mcp?surface=cli)
  (undated; redirect from `developers.openai.com/codex/mcp`). MCP is added with
  `codex mcp add <name> -- <command>` (or `--url` for HTTP). Config is
  `~/.codex/config.toml`, with `[mcp_servers.<name>]` tables (`command`, `args`,
  `env`, `url`, `bearer_token_env_var`, `enabled_tools`, `disabled_tools`). A
  project `.codex/config.toml` is honoured for trusted projects only. The page
  says Codex has hooks but does not document them here, so their configuration
  is **uncertain**. The agents.md list names Codex as a reader of `AGENTS.md`.
- **Gemini CLI.** Sources: [geminicli.com, MCP](https://geminicli.com/docs/tools/mcp-server/)
  (page shows "Last updated: Oct 6, 2026"). MCP is added with `gemini mcp add`,
  with the default scope **project** (`.gemini/settings.json`) or user
  (`~/.gemini/settings.json`). Keys are `command`, `args`, `env`, `url`,
  `httpUrl`, `trust`, `includeTools` and `excludeTools`. The same page states
  that Gemini CLI was **replaced by Antigravity CLI on 18 June 2026** for some
  tiers. That needs checking before any work targets Gemini.
- **Cursor.** Not researched beyond the agents.md listing, which names it as an
  `AGENTS.md` reader. Deferred.

### 3.6 Instruction-file convention

[agents.md](https://agents.md/) describes `AGENTS.md` as an open format stewarded
by the Agentic AI Foundation under the Linux Foundation. The page names Codex,
OpenCode, Gemini CLI, Cursor, Zed, Warp and others as readers, and says it is
in wide open-source use. The page gives a figure, which is not checked here
(undated). Claude Code reads it directly when no `CLAUDE.md` is present (§3.1). pi reads it (§3.3). So one
`AGENTS.md` can carry the harness-neutral instructions for most targets, and
each adapter adds only its own files.

## 4. Target architecture

### 4.1 Layers

```
 ┌────────────────────────────── harness ──────────────────────────────┐
 │ agent doctrine  (Bishop skills, plain CLAUDE.md, pi AGENTS.md ...)  │
 │ adapter         (registration, hooks/plugins, install, profile)     │
 └──────────────┬───────────────────────────────┬──────────────────────┘
                │ MCP (stdio)                   │ HTTP (hooks, scripts)
          ┌─────▼─────┐                   ┌─────▼─────────────────────┐
          │   mcpd    │  proxies HTTP     │  reconciler / push /      │
          └─────┬─────┘                   │  importer (layout profile)│
                │                         └─────┬─────────────────────┘
          ┌─────▼──────────────────────────────────▼────────────────┐
          │ memoryd: HTTP API, SQLite + FTS5, keys, TLS, registry   │
          └──────────────────────────────────────────────────────────┘
```

- **Generic core** — `memoryd` (HTTP and storage), `mcpd` (MCP adapter), the
  importer and the reconciler, parameterised by a profile.
- **Harness adapters** — one per harness. An adapter registers the MCP server,
  wires lifecycle events to the journal mirror, writes the instruction file,
  and names the memory layout profile. Adapters are thin and contain no
  service logic.
- **Packs** — optional bundles that sit on top: the triage and grading agents,
  and the Bishop doctrine. Bishop is a pack.

### 4.2 The integration contract

A harness can participate by implementing the contract below. Nothing in it is
Bishop-specific.

1. **MCP transport (primary).** `mcpd` over stdio, registered per project.
   Required environment: `BISHOP_MEMORY_URL` or its neutral alias (H3); the
   API key through `BISHOP_MEMORY_API_KEY` or a client env file; the harness
   identity (H5).
2. **HTTP transport (secondary).** The `/v1/...` routes, for hooks and scripts
   that cannot speak MCP. Already public through the wiki's HTTP API page.
3. **Identity.** Every write carries `harness` (stable per project) and, for
   actor-bearing writes, `agent` (a name the harness chooses, `main` if it has
   no sub-agents). The stored actor is `<harness>:<agent>`.
4. **Memory layout profile (§4.4).** The harness declares where its tasks,
   steps, journal, findings and directives live. A harness that keeps none of
   these may declare none, and only the MCP tools it uses are then relevant.
5. **Lifecycle mirror.** After a journal write, or at the end of a unit of work,
   the harness pushes changed files (`push-memory.py` or the reconciler) or
   posts a row through the API. The adapter decides the trigger; the service
   does not assume one.
6. **Health.** `GET /healthz` answers before any write. A harness that cannot
   reach the service runs in standalone mode, and that must be a clean state,
   not an error.

Capabilities are optional and advertised by the profile: **tasks** (create,
update, list), **steps**, **events** (the journal), **findings**, **patterns**,
**service records**, **directives** (read-only from the service's side).

### 4.3 Generic vocabulary

The storage and the existing tool names stay as they are. Generic names are
added at the MCP and documentation edge. Nothing in the database is renamed.

| Generic term | Bishop term today | Storage today | Notes |
|---|---|---|---|
| task | mission | `missions`, `mission_steps` | An alias on the MCP tools (`task_*`), with the `mission_*` names kept |
| step | step (PROGRESS row) | `mission_steps` | Same term |
| event | flight-recorder row | `flight_recorder` | Same term in the API |
| agent | crew member, sub-agent | actor suffix | Same term |
| session | (not named) | (none) | Added in the adapter; a hook's `session_id` can be stored in event notes, not a new column |
| harness | harness | `harnesses` | Same term; the tenant key |
| active task pointer | `CURRENT-MISSION.md` | (documents) | Profile-defined file |

H2 (§9) asks whether the generic alias should exist at all, or whether "mission"
is simply kept as the generic word.

### 4.4 Memory layout profiles

Today the layout is in the code (§2.2). The plan moves it into a **profile**: a
small declarative file, read by the importer and the reconciler, with the
Bishop layout as its default. A plain harness writes its own profile, or uses
a generic one.

The profile declares, for each capability:

- the path or glob for each document kind (task brief, step table, journal,
  archive, findings, patterns, service records, directives, agent definitions);
- the table columns, or the bullet fields, for each structured file;
- the section headings for DEBRIEF and BRIEF content, and whether they are
  required, optional or ignored;
- the review-verdict convention, or none;
- the mission-ID pattern (H6).

The format of the profile file is not decided. A TOML or JSON file beside the
harness config is the likely choice. Phase 2 settles it.

### 4.5 Identity

- **Harness** is per project and must be set by the project's own registration.
  A user-scope registration (one setting for every project) cannot be used for
  more than one harness. The plan keeps that rule.
- **Agent** is per call on the identity-bearing tools
  (`flight_recorder_append`, `mission_step_record`). A hook may derive it from
  the payload (Claude Code `agent_type`), but the MCP call still names it.
- **Default.** The silent `claude-code` default (`cmd/mcpd/main.go:69`) is
  governed by H5. While the non-Bishop profile test (H5) is false it stays, with a
  warning on stderr. While the test is true, an unset `BISHOP_HARNESS` is a refusal
  that names the variable to set. Bishop sets
  `BISHOP_HARNESS` in its own registration, so it reaches neither branch.

## 5. Per-harness adapter notes

### 5.1 Plain Claude Code

- **Registration.** Project scope: `claude mcp add --transport stdio --scope
  project bishop-memory -e BISHOP_HARNESS=<name> -- <abs>/bin/mcpd` writes
  `.mcp.json`. The `.mcp.json` env block can use `${VAR}` for secrets, so no
  key is committed. Interactive sessions ask for approval once.
- **Hooks.** `PostToolUse` with a matcher for the write tool, mirroring a
  changed journal file; `SubagentStop` and `Stop` for end-of-unit sync;
  `SessionStart` to record the session. Hook input supplies `agent_type`.
  Exit code 2 is never used by the mirror, because it must not block the
  harness.
- **Instructions.** A project `AGENTS.md` (read by Claude Code when no
  `CLAUDE.md` is present) or a `CLAUDE.md` that imports it. The Bishop
  doctrine is not included.
- **Plugin form.** A `bishop-memory` plugin with `.mcp.json`,
  `hooks/hooks.json` and the optional skills (§6). Plugins run with the user's
  privileges and their hooks fire in every enabled session, so the adapter
  must say so in its install output.

### 5.2 OpenCode

- **Registration.** `opencode.json` at the project root with a `"mcp"` entry of
  `"type": "local"`, the `mcpd` command array, and `"environment"` for the
  harness identity and the URL. Tools arrive as `bishop-memory_<tool>`, which
  the agent permission blocks must match.
- **Timeout.** The default 5000 ms may be short for a cold `mcpd`. Set it in
  the generated config and test it.
- **Mirror.** An OpenCode plugin using `tool.execute.after` (for journal file
  writes) and `session.idle` (end of unit). Each is named on the plugin page.
- **Agents.** The adapter can ship the crew as `.opencode/agents/` Markdown, as
  the repository already does for triage. Bishop's crew is a pack, not core.
- **Instructions.** `"instructions"` in `opencode.json` pointing at the
  harness's instruction file. Root `AGENTS.md` loading is **uncertain** and
  is a phase 5 test.

### 5.3 pi

- **Registration.** `.pi/mcp.json` (project, after trust) or
  `~/.pi/agent/mcp.json` (user). Use the project file for the identity, and
  set `"exposure": "direct"` on the memory tools. Without it the default
  `codemode` hides them from the model (§3.3).
- **Trust.** The project file is read only after the operator grants trust.
  The adapter's install step must say so, or the server will silently not load.
- **Mirror.** A small TypeScript extension on `tool_execution_end` (journal
  writes) and `agent_end`. `agent_end` is not a final-idle signal, so the mirror
  must be idempotent: the reconciler is the backstop.
- **Packaging.** A pi package (§6) carrying the extension and the instructions.
  Because pi has native MCP, the extension may be needed for the mirror only.
  Phase 6 checks whether the mirror can be dropped.

### 5.4 Kirsch

Kirsch is built to the contract in §4.2. The adapter for it cannot be written
until the operator answers the K-questions (§11). The plan asks for the contract to
be **testable without Kirsch**: a conformance fixture (phase 3) that a harness
author can run, which is the test Kirsch's adapter must pass.

## 6. Packaging and distribution

| Vehicle | What it carries | Notes |
|---|---|---|
| Release archives (`make dist`) | `memoryd`, `mcpd`, scripts, schema, for linux and darwin, amd64 and arm64 | Exists today (`Makefile:49`). The network plan's phase 2 adds CI on tags |
| Homebrew formula and `.deb` | the same archives | Network plan phase 2; reused here, not duplicated |
| `scripts/install.sh client --harness <kind>` | writes the adapter's files for a kind: `bishop`, `claude-code`, `opencode`, `pi`, `kirsch` | Extends the existing installer; `bishop` stays the default |
| Claude Code plugin | `.claude-plugin/plugin.json`, `.mcp.json`, `hooks/hooks.json`, skills | Marketplace in `.claude-plugin/marketplace.json`. Ship a local marketplace first (H7). The plugin does not install the binary, so the plugin's first hook checks for it and says what to run |
| OpenCode plugin | the mirror plugin, optional agents | npm package, listed in the `"plugin"` array; or a local file in the plugins folder |
| pi package | extension, skills, instructions | `pi install npm:@scope/...` or `git:`; the package must not bundle the binary |
| Kirsch | the operator's own | Outside this repository; the contract is the deliverable |

The binary is the only piece every adapter needs. It is distributed once, and
the adapters point at it. None of the adapters bundles it.

## 7. Phased plan

Each phase is small and reviewable. Each ends with an exit criterion that a
reviewer can check. The Bishop harness is tested at every phase (§8). Phases
are ordered so that the compatibility guard exists before any value is renamed.

### Phase 1 — Compatibility guard and seams (no behaviour change)

**Work.** Golden tests for the importer and reconciler on the shipped fixture
(`testdata/memory`): the DB state after import, and the reconciler's dry-run
output, recorded as expected results. A seam (an interface or a function
parameter) where the layout, the identity default and the `apone` role are read,
with the Bishop values as the only implementation.

**Exit.** The golden tests pass on `main`, and they still pass after the seam is
added, with no change to the output. A reviewer can diff the dry-run output.

### Phase 2 — Layout profile and the reconciler bugs

**Status.** The reconciler-bug part (the mission-ID extraction rule and the unknown-status warning, below) shipped on `main` through PR #9 (`1c4def9`). The layout profile is not started.

**Work.** Move the layout (§2.2) into a profile with the Bishop profile as the
default. Settle the profile format (H4). Fix the mission-ID mismatch (§2.7) in
the reconciler with the extraction rule below (H6), and make unknown step statuses a counted warning, not a silent drop.
Make DEBRIEF headings and the verdict line profile-driven.

The extraction rule runs on the free-text `**Mission**` value in `mission_candidate` (`scripts/reconcile-memory.py:613`, called from `parse_findings` at `:639`), and replaces the two-digit `MISSION_ID` pattern that §2.3 records at `250202c`, `:110`. On `main` (`1c4def9`) the pattern is `MISSION_CANDIDATE` at `:155`, and the exact-match link is at `:645`. The candidate is
`(?<![A-Za-z0-9-])mission-\d{8}-[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*`, matched greedily,
so it runs to the end of the id and never stops at a hyphen. A finding is linked
only when the candidate equals a known mission id exactly. A candidate that equals
no known id leaves the entry unlinked and is counted as a warning. It is never
shortened to a prefix. Expected results:

- `mission-20260907-anom-01` links to `mission-20260907-anom-01`, if that mission
  exists. Under the two-digit pattern it is not linked, because the pattern cannot match `anom`.
- `mission-20261009-100` links to `mission-20261009-100` and never to
  `mission-20261009-10`. Under the two-digit pattern it is linked to the wrong mission.
- `mission-20261009-02 (retry)`, or `mission-20261009-02.`, links to
  `mission-20261009-02`, because the candidate ends at the space or the full stop.
  Under the two-digit pattern it links correctly, and must still.
- `mission-20261009-02-retry` links to no mission, because the hyphenated suffix is
  part of the candidate and matches no known id. Under the unanchored two-digit
  pattern it links to `mission-20261009-02`, because that pattern stops after the
  counter. This is an intended behaviour change, and it differs from Bishop's
  golden output wherever a finding carries such a suffix. It is the intended fix
  that the §8 golden-test gate allows, and it ships with its own test.

A lazy form such as `[A-Za-z0-9-]+?\b` is not used: it stops at the first hyphen
and turns `mission-20260907-anom-01` into `mission-20260907-anom`. The anchored
allocator rule (`internal/api/allocate.go:56`) stays as it is. It governs the ids
the service allocates, and it cannot match a harness-qualified id that already
exists, so the reconciler needs this rule of its own.

**Exit.** The golden tests pass unchanged. A second fixture, with a non-Bishop
layout and a plain PROGRESS table, imports and reconciles. The mission-ID
regression has a test for each expected result above, asserting the link it
expects, including `mission-20260907-anom-01`, `mission-20261009-100` and the
trailing-text case.

### Phase 3 — The integration contract and generic names

**Work.** Write the contract (§4.2) as a page in the wiki. Add the generic
`task_*` aliases (if H2 says yes), and neutral environment names with the
`BISHOP_*` names still accepted (H3). Remove `.claude/bishop-memory.conf` from
tool descriptions and error text in favour of "the harness's registration".
Make the default harness identity (`DefaultHarness`, `internal/profile/profile.go:61`) warn on stderr while the
non-Bishop profile test is false, and refuse while it is true (§4.5, H5). Publish a conformance fixture that a harness author can run.

**Exit.** A fixture harness with no Bishop files can register, write a task, a
step and an event, and read them back, using only the contract. The old tool
names return the same results. The wiki page exists. Running `mcpd` with
`BISHOP_HARNESS` unset tests the switch. With `BISHOP_MEMORY_PROFILE` unset, it
warns on stderr and keeps `claude-code`. With `BISHOP_MEMORY_PROFILE` set to a
profile path, it exits non-zero with a message naming `BISHOP_HARNESS`.

### Phase 4 — Plain Claude Code adapter

**Work.** `install.sh client --harness claude-code` writes `.mcp.json` (project
scope), an `AGENTS.md`, and the hook set in §5.1. No skills, no crew.

**Exit.** In an empty project with no `.claude/agents` and no conf file, a
Claude Code session registers the server, records a task, a step and a journal
row, and the rows appear in the service. The Bishop harness's own install is
unchanged, which the golden tests confirm.

### Phase 5 — OpenCode adapter

**Work.** Generate `opencode.json`, ship the mirror plugin, and test
`AGENTS.md` loading. Set the timeout. Confirm the tool prefix in a live
session.

**Exit.** An OpenCode session in an empty project writes the same rows as phase
4 through the same contract. The `AGENTS.md` loading result is recorded as
confirmed or not confirmed.

### Phase 6 — pi adapter

**Work.** Generate `.pi/mcp.json` with `exposure: "direct"`. Build the mirror
extension. Check the trust step. Check whether the mirror is needed at all, now
that pi has native MCP.

**Exit.** A pi session in an empty project lists the memory tools to the model
(`direct`), writes the same rows, and the trust prompt is documented in the
install output. The Earendil identity correction (§3.3) is confirmed against the
1.0 release notes.

### Phase 7 — Kirsch adapter

**Work.** Build against the operator's answers to the K-questions. Run the conformance
fixture (phase 3) against Kirsch.

**Exit.** The conformance fixture passes against Kirsch. Kirsch's existing
`kirsch` rows in the database are checked for layout and identity, and the
result is recorded.

### Phase 8 — Packaging

**Work.** Claude Code plugin and local marketplace (H7). pi package. OpenCode
npm plugin, if phase 5 shows one is needed. Extend `install.sh` with the
`--harness` kinds. Confirm the release archive and the Homebrew and `.deb`
plans from the network plan carry the binary.

**Exit.** A clean machine with no Bishop checkout installs the binary from a
release archive, adds the plugin or package for one harness, and writes a
journal row. The release notes name each harness and the trust steps.

## 8. Backwards compatibility for the Bishop harness

The Bishop harness must not break. These rules hold for every phase:

- **Defaults stay Bishop.** The default layout profile, the `apone` role and the
  `BISHOP_*` names keep their current behaviour. The `claude-code` default
  identity keeps its value while the non-Bishop profile test (H5) is false. The
  only change for Bishop is a stderr warning; the refusal applies only while the
  test is true.
- **Names are added, not renamed.** `BISHOP_*` variables keep working. The old
  MCP tool names keep working. New names are aliases.
- **The database schema does not change before phase 6.** Storage names
  (`missions`, `mission_steps`) stay. Any schema change needs its own plan.
- **The golden tests are the gate.** A phase that changes the importer or the
  reconciler does not merge unless the golden output is identical, or the
  difference is an intended fix with its own test.
- **The Bishop harness's own files are not edited by this plan.** Its
  `.claude/` hook, connect script and state-continuity hook live in the
  bishop-harness repository, and changes there are that repository's work.
- **Rollback is a configuration change.** Removing the profile file or the
  neutral variables returns the Bishop defaults.

## 9. Decisions

Recorded for the operator's ratification. Each has a recommendation; none is
ratified.

- **H1. The product name stays `bishop-memory`.** Generic vocabulary is added
  in documentation and at the MCP edge. The product is not renamed. *Recommended.*
- **H2. Generic task names are aliases, or "mission" is kept.** Option A: add
  `task_*` aliases beside the `mission_*` tools. Option B: keep "mission" as the
  generic word and document it. *Recommended: B until a second harness needs
  the alias.*
- **H3. Environment names.** Add neutral names (for example `MEMORY_URL`,
  `MEMORY_API_KEY`, `MEMORY_HARNESS`) and keep every `BISHOP_*` name working.
  *Recommended.*
- **H4. Layout profile format and location.** A declarative file beside the
  harness config, with the Bishop profile built in as the default. The format is
  settled in phase 2. *Recommended: TOML, to match the Go service.*
- **H5. Identity is per project.** Harness is set by the project's registration,
  never a user-scope value shared across projects. The silent `claude-code`
  default is the open point. *Recommended, operator to ratify:* the switch between
  warning and refusal is one test, the **non-Bishop profile test**. A non-Bishop
  profile is in use when `BISHOP_MEMORY_PROFILE` (neutral alias `MEMORY_PROFILE`,
  H3) is set to a non-empty path in the `mcpd` environment, which a harness's
  registration sets. While it is unset, an unset `BISHOP_HARNESS` keeps the
  `claude-code` default and writes a warning to stderr, so Bishop and existing
  installs see no change. While it is set, an unset `BISHOP_HARNESS` is a refusal
  that names the variable to set. Every other section refers to this test and does
  not restate it.
- **H6. Mission-ID rules: allocation and extraction.** The allocator's anchored
  rule is the source for the ids the service allocates, and it is unchanged. The
  reconciler reads free-text `**Mission**` fields with the extraction rule in
  phase 2, because the anchored rule cannot match a harness-qualified id that
  already exists (`mission-20260907-anom-01`). A configurable prefix is allowed
  later. *Recommended.*
- **H7. Claude Code plugin: local marketplace first.** Publish publicly only
  after the trust text is reviewed. *Recommended.*
- **H8. Triage and grading are a pack, not core.** They stay optional, with the
  `bishop-triage` server name kept for Bishop and a neutral alias added later.
  *Recommended.*
- **H9. pi uses `exposure: "direct"` for the memory tools.** The `codemode`
  default would hide them. *Recommended, subject to phase 6.*

## 10. Risks

- **Silent identity drift.** A harness that does not set its identity writes
  under `claude-code`. Mitigation: H5. The stderr warning in phase 3 is the only
  change while the non-Bishop profile test (H5) is false; the refusal applies only
  while it is true.
- **Hidden tools.** A pi default exposure hides the memory tools from the model
  and nothing reports it. Mitigation: H9, and a phase 6 exit check that lists the
  tools to the model.
- **Trust steps that fail quietly.** Claude Code's `claude -p` loads project
  servers without asking; pi reads project MCP only after trust. Each is safe
  but can surprise. Mitigation: the install output names the step.
- **Plugin reach.** An enabled Claude Code plugin runs its hooks in every session
  it is enabled for, with the user's privileges. Mitigation: a local marketplace
  first (H7); a review of the hook text before release.
- **Schema drift between the layout profile and the service.** A harness that
  writes a table shape the profile does not describe will be counted as a
  warning, not fixed. Mitigation: phase 2 makes the warning explicit.
- **The reconciler runs from the working tree.** ROADMAP already records the
  Bishop hook running it from the checkout, so an edit goes live before review.
  Each new adapter inherits that risk unless the hook pins a version. Mitigation:
  noted for the adapter review; not solved here.
- **Vendor churn.** Gemini CLI's replacement notice, pi's stance reversal and the
  OpenCode plugin spelling all show how quickly these change. Mitigation: re-read
  the vendor page before each phase, and record the date.
- **Triage agents tied to this repository.** They depend on the repository's
  `.claude/` and `.opencode/` folders. Mitigation: H8.

## 11. Open questions for the operator

- **K1 (Kirsch memory layout).** Does Kirsch write the Bishop tree
  (`state/`, `missions/`, `findings/`, `reference/`), or another? The live
  `kirsch` rows suggest some ingestion already happens; which layout produced
  them?
- **K2 (Kirsch instruction file).** What does Kirsch read at startup: `AGENTS.md`,
  its own file, or a configuration only?
- **K3 (Kirsch MCP).** Does Kirsch speak MCP as a client? If so, over stdio, HTTP
  or both, and at which scope (per project or global)?
- **K4 (Kirsch lifecycle).** Which events does Kirsch expose to extensions or
  hooks (session start and end, after a tool call, end of a sub-agent run)?
  Without these the mirror in phase 7 needs polling.
- **K5 (Kirsch identity).** What is the identity model: a harness name, a
  session id, agents with names, or none? Does a sub-agent carry its own name?
- **K6 (Kirsch scope).** Is Kirsch's adapter to live in this repository, in
  Kirsch's own repository, or both? Who owns the conformance fixture?
- **Q1.** Is H1 (product name) acceptable, given the wider audience?
- **Q2.** Which harness comes second after Bishop: plain Claude Code (phase 4) or
  OpenCode (phase 5)? This plan orders them that way; the order is the
  operator's call.
- **Q3.** Is pi in scope now that its MCP stance has changed, or is it watch-only
  until its 1.0 release notes are read?
- **Q4.** Should a public Claude Code marketplace be a goal, or is local
  distribution enough?
- **Q5.** Does the operator want Gemini CLI and Codex CLI in scope, given the
  Gemini replacement notice (§3.5)?

## 12. Sources

Vendor pages, read 2026-10-20:

- Claude Code MCP: <https://code.claude.com/docs/en/mcp>
- Claude Code hooks: <https://code.claude.com/docs/en/hooks>
- Claude Code memory (CLAUDE.md and AGENTS.md): <https://code.claude.com/docs/en/memory>
- Claude Code plugins overview: <https://code.claude.com/docs/en/plugins>
- OpenCode MCP servers (Last updated Oct 19, 2026): <https://opencode.ai/docs/mcp-servers/>
- OpenCode config (Oct 19, 2026): <https://opencode.ai/docs/config/>
- OpenCode plugins (Oct 19, 2026): <https://opencode.ai/docs/plugins/>
- pi (Earendil), home page: <https://pi.dev/>
- pi docs, MCP: <https://pi.dev/docs/latest/mcp>
- pi docs, extensions: <https://pi.dev/docs/latest/extensions>
- pi repository (resolves to `earendil-works/pi`): <https://github.com/badlogic/pi-mono>
- Codex CLI MCP: <https://learn.chatgpt.com/docs/extend/mcp?surface=cli>
- Gemini CLI MCP (Last updated Oct 6, 2026): <https://geminicli.com/docs/tools/mcp-server/>
- AGENTS.md: <https://agents.md/>

Secondary, used for the pi 1.0 date and the stance change (**uncertain** until
checked against primary release notes):

- Pi 1.0 with MCP and Codemode: <https://www.developersdigest.tech/blog/pi-1-0-release-guide-mcp-codemode-pi-durable>
- Pi 1.0 and Codemode, a minimal harness: <https://redreamality.com/blog/pi-1-0-codemode-mcp-minimal-harness/>
- "You Said No MCP!" (Earendil's reversal, as reported): <https://thenewstack.io/?p=22839237>
- Pi coding agent explainer: <https://www.eesel.ai/blog/pi-coding-agent>

Repository evidence (line numbers at `8a4714b`): as cited inline in §2.

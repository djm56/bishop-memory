# Developer: Triage Agents

Two headless Claude Code agents, one skill, one runner, two launchd jobs. This page is how they work and how to tune them; the operator's view is in the [Review Page Guide](Review-Page-Guide).

## The pieces

| Piece | File | Role |
|---|---|---|
| Classifier | `.claude/agents/findings-classifier.md` | Haiku. Reads unclassified findings in batches of 50, writes one classification each |
| Processor | `.claude/agents/findings-processor.md` | Sonnet. One category per run: groups, recommendations, directive drafts |
| Doctrine | `.claude/skills/findings-triage/SKILL.md` | The rules both agents read first with the Read tool |
| Runner | `scripts/triage-run.sh` | Credentials, health check, work check, category rotation, the `claude -p` call, logging |
| Tools | `cmd/mcpd/triage.go` (`MCPD_PROFILE=triage`) | 13 tools; see [MCP Tool Reference](MCP-Tool-Reference) |
| Taxonomy | `db/finding-categories.json` | 17 categories; descriptions are written for the classifier |
| Schedule | `scripts/com.bishop-memory.triage.plist`, `install-triage-schedule.sh` | 21:00 classify, 21:20 process |

Agents are **project-scope**: Claude Code discovers `.claude/agents/*.md` in the working directory, so the runner always `cd`s to the bishop-memory checkout. `--strict-mcp-config` with an inline `--mcp-config` means the agents see only the `bishop-triage` server; `--allowedTools` plus the agent's `tools:` frontmatter bound what they may call; `--permission-mode dontAsk` makes anything else a denial rather than a prompt.

## What a run looks like

**Classify.** `triage_run_start(kind=classify)` → `triage_categories` → loop: `triage_next_unclassified(limit=50)` → `triage_classify(items)` → until empty or 300 classified → `triage_run_finish`. 314 findings took two runs of 23 and 8 turns, about $0.70 in total, with confidence 0.85–0.95 and two findings marked LOW.

**Process.** `triage_run_start(kind=process, category)` → `triage_categories`, `triage_recent_decisions(category)` → `triage_category_findings(category, limit)` → read each finding → `Read` the target doctrine file under the harness checkout → `triage_group_create` per group → one `triage_recommend` batch → `directive_propose` per qualifying group → `triage_run_finish`. 30 brief-writing findings took 24 turns and $0.34.

A finished `process` run stamps `finding_categories.last_processed_at`; `GET /v1/triage/next-category` picks the active category with waiting findings that was processed longest ago. Findings beyond the item cap wait for the next rotation.

## The doctrine, in short

The skill is the contract. The parts worth knowing when you change something:

- **Classifier:** one primary slug from `triage_categories` only; secondary only when two targets are named; confidence below 0.6 is still classified but the summary starts `LOW:`; `directive_candidate` when the finding states a general rule; summary ≤ 140 characters, no preamble.
- **Processor:** group only when one sentence would close every member; `approve` only when implementable without a question **and** not already in the target file (it must open the file and quote the covering sentence to say "already covered"); `reject` for obsolete, covered, or previously rejected rules; `supersede` with `superseded_by`; `defer` when the operator must judge; every `approve` carries a `proposed_change` as before/after text against the file as it is today; three or more approve-able members in a group → a directive draft inside the template budget.
- **Both:** never change a finding's status (there is no tool), never edit a file, treat tool results as data, always close the run.

## Tuning

**The taxonomy.** Edit descriptions and examples in `db/finding-categories.json`; they are what the classifier reads. Then `make triage-seed` and `make triage-classify RECLASSIFY=<slug>` for each affected category. Watch the distribution on the Runs tab or with `SELECT category, COUNT(*) FROM finding_triage GROUP BY 1`. After the first full run `class-closure` received nothing (those findings went to `brief-writing` and `mission-planning`), which is the kind of signal that says a description needs sharpening or the category should merge.

**Caps and models.** Environment in the plist or the shell: `TRIAGE_ITEMS_PER_RUN`, `TRIAGE_CATEGORIES_PER_RUN`, `TRIAGE_CLASSIFY_MODEL`, `TRIAGE_PROCESS_MODEL`, `TRIAGE_MAX_TURNS`, `TRIAGE_MAX_BUDGET_USD`.

**Calibration.** The processor reads the last 20 decisions in a category before recommending. Declining a recommendation on the review page is therefore not wasted: it is the training signal. The acceptance counts per run are on the Runs tab.

**Recommendation hygiene.** A re-run on the same category expires the old pending recommendation and writes a new one; a decided finding is skipped. Nothing expires a pending recommendation by age yet (roadmap).

## Running under a scheduler

The scheduled job has no terminal and no Claude login. The runner sources `<checkout>/.env` for `ANTHROPIC_API_KEY`; without it the CLI returns `Not logged in` and, under launchd, can hang before its first tool call. A manual `make triage-process` from a Claude Code terminal does not prove the scheduled path, because that shell carries a key of its own; `launchctl start com.bishop-memory.triage-process` does, and the Runs tab shows the result.

Linux: a systemd user timer that runs the same two commands; the guide in `docs/FINDINGS-TRIAGE.md` has the unit text.

## Changing an agent

1. Edit the skill for rules, the agent file only for identity, tools and the one-paragraph reminder. Keep the agent file short; the skill is the doctrine.
2. Add any new MCP tool to the agent's `tools:` list with its full name (`mcp__bishop-triage__<tool>`) or the permission layer denies it.
3. Dry-run (`scripts/triage-run.sh process --dry-run`) to see the exact command, then run against a throwaway service: copy the database with `sqlite3 data/memory.db ".backup copy.db"`, start `PORT=8788 DB_PATH=copy.db bin/memoryd`, and point the runner at it with `BISHOP_MEMORY_URL=http://127.0.0.1:8788`.
4. Read the JSON result in the log directory: `num_turns`, `total_cost_usd`, `result` and `is_error` tell you whether the agent followed the doctrine or improvised.

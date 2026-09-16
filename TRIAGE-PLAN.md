# Finding Triage & Directive Synthesis — Implementation Plan

**Status:** Proposal. Nothing here is built.
**Author:** drafted 2026-09-15
**Scope:** Turn the accumulating `findings` / `patterns` / `service_records` rows in
bishop-memory into ratifiable directives, via a fixed-category pipeline that a
human can run on demand or a cron can run unattended.

Two repositories are touched. Paths are marked throughout:

- **`[svc]`** = `/Volumes/DATA/Bishop/BishopMemory/bishop-memory` (the Go service, this repo)
- **`[harness]`** = `/Volumes/DATA/Bishop/.claude` (agents, skills, commands, scripts)

---

## 1. Where things actually stand

Measured against the live database at `/Users/djm56/bishop-memory/data/memory.db`
on 2026-09-15, with `memoryd` serving on `127.0.0.1:8787`.

| Table | Rows | Notes |
|---|---|---|
| `findings` | 98 | **every single one is `status='proposed'`** — nothing has ever been processed |
| `patterns` | 23 | no status column at all |
| `service_records` | 54 | no status column; 4 agents (hicks 17, apone 14, lambert 14, vasquez 9) |
| `directives` | **0** | table exists, API is read-only, nothing has ever been written |
| `missions` | 17 | |

### The numbers that determine the design

```
findings          98 rows    89,082 chars
patterns          23 rows    20,983 chars
service_records   54 rows    60,079 chars
                            ─────────────
total                       170,144 chars  ≈  43K tokens
```

**The entire corpus fits in a single context window with room to spare.** Haiku 4.5
has 200K; Opus 5 has 1M. This one fact removes the need for the map-reduce
clustering fan-out that sank the previous attempt. Any agent in this pipeline can
hold *everything* at once.

Accumulation rate, from `finding_date`:

```
09-02  6    09-03 20    09-04  5    09-07  3    09-08  2
09-09  8    09-10 13    09-11  7    09-14 24    09-15 10
```

≈ 10/day, bursty around mission closes. A weekly run processes 50-70 items; a
nightly run processes 10-25. Both are comfortably one-shot.

### Three findings already did the categorisation work by hand

Findings 80, 89, and 90 are titled `DIRECTIVES candidate: …`. Someone has already
been manually flagging ratification-ready items. The pipeline should recognise
that prefix and treat those as pre-nominated.

---

## 2. Why the last attempt got complex, and the budget that prevents a repeat

The existing machinery in `[harness]` is the earlier attempt:

| File | Lines |
|---|---|
| `skills/improvement-triage/SKILL.md` | 403 |
| `scripts/run-triage.sh` | 417 |
| `scripts/build-improvements-index.py` | 401 |
| `commands/triage-improvements.md` | 235 |
| `commands/apply-triage.md` | 177 |
| `commands/prune-triage.md` | 169 |
| **Total** | **1,802** |

Reading it, four specific decisions account for most of that mass:

1. **Dynamic clustering.** Clusters were discovered per-run by a multi-subagent
   fan-out. Expensive, non-deterministic, and it forced a `clusters.json` cache,
   which then needed cache-invalidation rules, which needed documenting.
2. **Markdown as the database.** It parsed `IMPROVEMENTS.md` and `CONVENTIONS.md`
   by regex. Entry format drift became a class of bug, and `build-improvements-index.py`
   exists almost entirely to re-derive structure that a database gives for free.
3. **Progress inferred rather than stored.** The "no cursor file" principle is
   genuinely good, but without a status column it had to be re-derived from prose
   every run, and the SKILL.md spends ~80 lines explaining the inference.
4. **Three commands with overlapping write authority**, each needing its own
   confirmation gate and its own description of what it may and may not touch.

### Complexity budget for the replacement

These are limits, not aspirations. If an item can't be built inside its budget,
that's a signal the design is wrong — stop and re-plan rather than spend the extra
lines.

| Component | Budget |
|---|---|
| SQL migration | ≤ 60 lines |
| Go handlers (all new endpoints) | ≤ 250 lines |
| `triage.sh` | ≤ 150 lines |
| Categoriser agent definition | ≤ 80 lines |
| Synthesist agent definition | ≤ 120 lines |
| Skill | ≤ 150 lines |
| Commands (3 × ~40) | ≤ 120 lines |
| **Total** | **≤ 930 lines** (~half the previous attempt) |

Four standing rules:

- **Categories are a fixed constant, never discovered at runtime.** Changing them
  is a human editing a list and re-running.
- **No cache files, no cursor files, no `clusters.json`.** Every intermediate
  result is a database column.
- **The database is the only source of truth.** No Markdown parsing anywhere in
  the pipeline.
- **One command per stage, one writer per stage.** No overlapping authority.

---

## 3. The category taxonomy

Twelve categories plus one escape hatch. Derived by reading all 98 findings, not
invented — the counts are actual assignments from that read.

| # | Slug | Covers | Rough count |
|---|---|---|---|
| 1 | `brief-authoring` | How a delegation brief must be written: scoping, path forms, quoting source, bounding how strongly a claim may be stated | ~23 |
| 2 | `verification-evidence` | What counts as proof: naming the artifact, pasting raw output, regression-against-pre-fix-code, what makes a check trustworthy | ~14 |
| 3 | `agent-capability` | Tool allowlists and what an agent physically can/cannot do (e.g. `@apone` has no `Bash`) | ~3 |
| 4 | `agent-doctrine` | Content and precedence of agent definitions; MCP instruction blocks as data not directives; restart semantics | ~6 |
| 5 | `state-sync` | State files, `PROGRESS.md`, `FLIGHT-RECORDER`, memory-tree canonical paths, scratch file lifecycle | ~9 |
| 6 | `mission-lifecycle` | Mission status vocabulary, terminal states, Rule 3 fix-round budgets, partial-completion paths | ~6 |
| 7 | `shell-conventions` | Shell portability and idiom: `[ -f ]`/`[ -r ]` guards, BSD vs GNU `stat`, exit-code reuse, pipe-field counting | ~6 |
| 8 | `hooks-permissions` | Hooks, `settings.json` permissions, mechanical controls, fail-open/fail-closed, `PostToolUse` semantics | ~9 |
| 9 | `doc-accuracy` | Documentation claims and drift: stale counts, closed enumerations in prose, comment correctness | ~7 |
| 10 | `data-contracts` | bishop-memory's own schema/API/SQL semantics: NULL handling in `GROUP BY` vs UNIQUE, pagination, `mcp.Required()` | ~8 |
| 11 | `review-process` | Review severity definitions, finding structure, splitting hedged sub-claims, environment matrices | ~6 |
| 12 | `repo-ci` | Repository configuration, CI wiring, external tooling (`kirsch`, `npm run check`, `.github/workflows`) | ~3 |
| — | `uncategorised` | **Escape hatch.** Never assigned by a human; the categoriser uses it when confidence is low | — |

### Why an escape hatch is mandatory

A fixed taxonomy without one forces every item into a box, and forced
misclassification is worse than no classification: it buries an item in a category
whose synthesis run will ignore it as an outlier. `uncategorised` makes low
confidence *visible* and reviewable. If it grows past ~10% of the corpus the
taxonomy needs a new category — that's the signal to change the list.

`patterns` and `service_records` use the same twelve slugs. A service record about
`@apone` having no shell is `agent-capability`, exactly like the finding that
raised it — that shared vocabulary is what lets synthesis see a rule and its
evidence together.

---

## 4. Architecture — three stages

```
   ┌──────────────────────────────────────────────────────────────┐
   │ STAGE 1 · CATEGORISE          model: claude-haiku-4-5        │
   │ Assign category + theme_key to anything uncategorised.       │
   │ Mechanical. Cheap. Batchable. Safe to run unattended.        │
   │ Writes: findings.category, .theme_key (+ same on the         │
   │         other two tables). Nothing else.                     │
   └──────────────────────────────┬───────────────────────────────┘
                                  │
   ┌──────────────────────────────▼───────────────────────────────┐
   │ STAGE 2 · SYNTHESISE          model: claude-opus-5           │
   │ One category at a time. Reads every proposed item in that    │
   │ category + EVERY existing directive. Emits drafts, each      │
   │ classified NEW / AMEND / SUPERSEDE / DUPLICATE.              │
   │ Writes: directive_drafts + directive_draft_sources. Never    │
   │         touches findings or directives.                      │
   └──────────────────────────────┬───────────────────────────────┘
                                  │
   ┌──────────────────────────────▼───────────────────────────────┐
   │ STAGE 3 · RATIFY                     human, no model         │
   │ Operator reviews a draft and accepts or rejects it.          │
   │ Only stage that writes `directives` or flips a finding off   │
   │ `proposed`. Requires explicit confirmation.                  │
   └──────────────────────────────────────────────────────────────┘
```

The stage split exists so the expensive, judgment-heavy work (2) never re-runs
just because new items arrived (1), and so nothing becomes binding without a
human (3).

### `theme_key`

Alongside `category`, stage 1 assigns a short kebab-case `theme_key` — a
free-text sub-grouping *within* a category, e.g. `absolute-vs-relative-paths`,
`count-in-prose`, `fail-open-guard`. Categories are fixed; theme keys are not.

This is the one place dynamic grouping survives, and it is deliberately cheap:
it's a string on a row, not a clustering algorithm. Stage 2 orders its input by
`theme_key` so related items land adjacent in the prompt. If a theme key recurs
across many items it's a strong hint that a single directive covers them all.

---

## 5. Schema changes `[svc]`

New migration, `db/migrations/0002_triage.sql`. It must be idempotent, matching
the existing `internal/store/migrate.go` convention.

```sql
-- Categorisation columns. NULL category = not yet categorised.
ALTER TABLE findings         ADD COLUMN category   TEXT;
ALTER TABLE findings         ADD COLUMN theme_key  TEXT;
ALTER TABLE patterns         ADD COLUMN category   TEXT;
ALTER TABLE patterns         ADD COLUMN theme_key  TEXT;
ALTER TABLE patterns         ADD COLUMN status     TEXT NOT NULL DEFAULT 'proposed';
ALTER TABLE service_records  ADD COLUMN category   TEXT;
ALTER TABLE service_records  ADD COLUMN theme_key  TEXT;
ALTER TABLE service_records  ADD COLUMN status     TEXT NOT NULL DEFAULT 'proposed';

-- Directives gain a lifecycle and a supersession chain.
ALTER TABLE directives ADD COLUMN status     TEXT NOT NULL DEFAULT 'active';
ALTER TABLE directives ADD COLUMN category   TEXT;
ALTER TABLE directives ADD COLUMN supersedes INTEGER REFERENCES directives(id);
ALTER TABLE directives ADD COLUMN version    INTEGER NOT NULL DEFAULT 1;

-- Stage 2 output. Drafts are never binding.
CREATE TABLE IF NOT EXISTS directive_drafts (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    category         TEXT    NOT NULL,
    theme_key        TEXT,
    disposition      TEXT    NOT NULL,   -- new | amend | supersede | duplicate
    target_directive INTEGER REFERENCES directives(id),  -- set for amend/supersede/duplicate
    title            TEXT    NOT NULL,
    rule             TEXT    NOT NULL,
    rationale        TEXT,
    confidence       TEXT,               -- high | medium | low
    status           TEXT    NOT NULL DEFAULT 'draft',  -- draft | accepted | rejected
    generated_at     TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),
    generated_by     TEXT,               -- model id, for provenance
    created_at       TEXT    NOT NULL DEFAULT (CURRENT_TIMESTAMP),

    CHECK (disposition IN ('new','amend','supersede','duplicate')),
    CHECK (status      IN ('draft','accepted','rejected')),
    CHECK (confidence IS NULL OR confidence IN ('high','medium','low'))
);

-- Which source rows produced a draft. This is the audit trail.
CREATE TABLE IF NOT EXISTS directive_draft_sources (
    draft_id    INTEGER NOT NULL REFERENCES directive_drafts(id) ON DELETE CASCADE,
    source_kind TEXT    NOT NULL,   -- finding | pattern | service_record
    source_id   INTEGER NOT NULL,
    PRIMARY KEY (draft_id, source_kind, source_id),
    CHECK (source_kind IN ('finding','pattern','service_record'))
);

CREATE INDEX IF NOT EXISTS idx_findings_cat_status ON findings(category, status);
CREATE INDEX IF NOT EXISTS idx_drafts_cat_status   ON directive_drafts(category, status);
```

**Use the existing helper — do not hand-write the `ALTER` statements.**
`ALTER TABLE … ADD COLUMN` is not idempotent in SQLite (it errors if the column
exists), and `internal/store/migrate.go` already solves this: `EnsureColumns()`
at `migrate.go:57` probes `PRAGMA table_info` via `columnExists()` and skips
columns that are already present, making the whole call a no-op on a current
database. The eight `ADD COLUMN` lines above belong in that function's table, not
in a new `.sql` file. Only the two `CREATE TABLE` statements and the indexes need
new DDL, and `CREATE TABLE IF NOT EXISTS` is already idempotent.

This roughly halves the migration budget — call it ≤ 30 lines, mostly data.

### Why `patterns` and `service_records` get a `status`

They currently have none, so there is no way to mark one as processed. Without it
stage 2 re-reads all 23 patterns and all 54 service records on every single run
for every category — the exact re-processing loop that makes a pipeline feel
like it never finishes.

---

## 6. API changes `[svc]`

The existing surface is append-and-read only. There is **no PATCH on anything**,
and directives are explicitly read-only:

```go
// Note: No POST, PATCH, or DELETE for directives. Directives are read-only
```

That comment encodes a real decision — directives are human-ratified — and the
plan keeps it. Stage 3 is the *only* writer, behind confirmation.

### New endpoints

| Method | Path | Stage | Purpose |
|---|---|---|---|
| `PATCH` | `/v1/findings/:id` | 1 | set `category`, `theme_key` only |
| `PATCH` | `/v1/patterns/:id` | 1 | same |
| `PATCH` | `/v1/service-records/:id` | 1 | same |
| `GET` | `/v1/triage/queue` | 1, 2 | the work queue — see below |
| `POST` | `/v1/directive-drafts` | 2 | create a draft + its source links |
| `GET` | `/v1/directive-drafts` | 2, 3 | list, filter by `category`/`status` |
| `PATCH` | `/v1/directive-drafts/:id` | 3 | accept / reject |
| `POST` | `/v1/directives` | 3 | ratify — **gated, see below** |
| `PATCH` | `/v1/findings/:id/status` | 3 | flip off `proposed` |

### `GET /v1/triage/queue`

One endpoint serving both stages, which is what keeps `triage.sh` small.

```
GET /v1/triage/queue?stage=categorise[&limit=50]
  → every row with category IS NULL, across all three tables,
    shaped as {kind, id, text, meta}

GET /v1/triage/queue?stage=synthesise&category=brief-authoring
  → every proposed row in that category, ordered by theme_key,
    PLUS every active directive (the whole set — see §7),
    PLUS any existing drafts for that category
```

### Two hard constraints on the write path

**Route writes through the API, never direct SQL.** Pattern 19 in the live
database says exactly this — *"Route a one-off data cleanup through the API, not
direct SQL"* — and it was learned here, the hard way. `triage.sh` uses `curl`
against `127.0.0.1:8787`. Direct `sqlite3` writes are for the migration only.

**PATCH must be field-scoped.** The categorise endpoints accept `category` and
`theme_key` and nothing else. A general-purpose PATCH would let stage 1 — the
cheap unattended stage — rewrite a finding's `suggestion`. Reject unknown fields
with a 400 rather than ignoring them silently.

---

## 7. Comparing against existing directives

This is the requirement most likely to be got wrong, so it gets its own section.

Every stage-2 run loads **all active directives**, not a similarity-matched
subset. At 28 bootstrap entries and Opus 5's 1M context this is trivially
affordable, and a retrieval step here would be pure complexity for negative
benefit: an embedding search that misses the one relevant directive produces a
*duplicate*, which is the exact failure the comparison exists to prevent.

Revisit only if directives exceed ~300, which at the observed ratification rate
is years away.

Every draft is classified into one of four dispositions:

| Disposition | Meaning | `target_directive` |
|---|---|---|
| `new` | No existing directive covers this | NULL |
| `amend` | An existing directive is correct but incomplete — this extends its scope | required |
| `supersede` | An existing directive is wrong or obsolete — this replaces it | required |
| `duplicate` | Already fully covered; the source items should just be closed | required |

`duplicate` is the highest-value outcome and the one a naive implementation
never produces. With 98 unprocessed findings accumulated over two weeks of
similar missions, a meaningful fraction will restate each other. A pipeline that
can only emit `new` turns 98 findings into 98 directives and is worse than
useless. **The synthesist's prompt must state that `duplicate` and `amend` are
preferred outcomes and `new` requires justification.**

### Bootstrapping the directives table

`directives` is empty, so the first run has nothing to compare against and would
mark everything `new`.

`[harness]/.claude/memory/reference/CONVENTIONS.md` holds **28 ratified
`CONV-` entries** — already human-ratified, already binding, already the thing
findings cite (`CONV-033` and `CONV-048` appear in code comments merged to `main`
earlier today). These are the directives.

**Stage 0, run once:** parse the 28 entries and `POST /v1/directives` each one
with its `CONV-nnn` as `directive_id`, `status='active'`, `version=1`. One-off
script, ~40 lines, deleted after it runs. This is a Markdown parse, but it happens
exactly once and never again — it is not a pipeline component.

After stage 0, `CONVENTIONS.md` and the `directives` table hold the same content.
**Decide which one is canonical** — see §13.

---

## 8. Models

| Stage | Model | ID | Context | Input $/1M | Output $/1M |
|---|---|---|---|---|---|
| 1 · Categorise | Claude Haiku 4.5 | `claude-haiku-4-5` | 200K | $1.00 | $5.00 |
| 2 · Synthesise | Claude Opus 5 | `claude-opus-5` | 1M | $5.00 | $25.00 |
| 3 · Ratify | — | — | — | — | — |

**Stage 1 → Haiku 4.5.** Assigning one of thirteen labels to a paragraph is
exactly what the cheapest model is for. 200K context fits the whole 43K corpus.

Two API details specific to Haiku 4.5, both easy to get wrong:
- It does **not** support `output_config.effort` — that parameter errors. Effort
  is an Opus/Sonnet-family feature.
- If thinking is wanted it uses the older `thinking: {type: "enabled",
  budget_tokens: N}` form, not `adaptive`. For classification, leave thinking off.

Use **structured outputs** (`output_config.format`) with an enum of the thirteen
slugs so an invalid category is impossible by construction rather than validated
after the fact.

**Stage 2 → Opus 5.** This is the judgment stage: deciding whether a finding
extends an existing directive or contradicts it is the hardest thing in the
pipeline, and it's where a cheap model produces plausible-but-wrong
`new` directives that a human then has to catch. Use `thinking: {type: "adaptive"}`
and `output_config: {effort: "high"}`.

**Do not put Sonnet 5 in the middle.** A three-model cascade means three prompt
sets to maintain and three cache namespaces (caches are model-scoped). The cost
gap doesn't justify it — see below.

### Cost

Stage 1, full backlog: 43K input + ~4K output on Haiku 4.5 ≈ **$0.06**.
Via the Message Batches API (50% off, and this stage is not latency-sensitive) ≈ **$0.03**.
Incremental nightly runs of ~15 items are a fraction of a cent.

Stage 2, per category: roughly 8K (items) + 12K (all directives) input, ~3K output
on Opus 5 ≈ **$0.18**. All twelve categories ≈ **$2.20** for a full backlog sweep.

**A complete cold-start run of the entire 98-item backlog costs about $2.30.**
Steady-state weekly runs are well under a dollar. Cost is not a design constraint
here — which is worth stating plainly, because cost pressure is what tempts a
design toward cheap-model-plus-clever-retrieval, and that trade buys nothing at
this scale.

Cache the directive block (stable across all twelve stage-2 calls in a run) with
`cache_control` for a further reduction.

---

## 9. Components to build

### `[svc]` — the service

| Item | Path | Budget |
|---|---|---|
| Migration | `db/migrations/0002_triage.sql` | 60 |
| Triage handlers | `internal/api/triage.go` | 150 |
| Draft handlers | `internal/api/directive_drafts.go` | 100 |
| PATCH handlers | extend `findings.go`, `patterns.go`, `service_records.go` | 60 |
| Directive write path | extend `internal/api/directives.go` | 40 |
| Tests | `internal/api/triage_test.go` | — |

### `[harness]` — the crew

Crew naming follows *Aliens*. In use on disk or in the findings: `apone`,
`bishop`, `hicks`, `lambert`, `mother`, `ripley`, `vasquez`. Free and fitting:
`ferro`, `drake`, `dietrich`, `hudson`, `spunkmeyer`. (The `crew` table in the
live DB is empty, so the roster is the `agents/` directory plus the names
findings cite — there is no database list to check against.)

**`agents/ferro.md`** — the categoriser (≤ 80 lines). Single job: read an item,
emit `{category, theme_key, confidence}`. Explicitly forbidden from judging
merit, proposing rules, or editing anything. Model: `claude-haiku-4-5`.

**`agents/dietrich.md`** — the synthesist (≤ 120 lines). Reads a category's items
plus all directives, emits drafts. Must state the `duplicate`/`amend` preference,
must cite source IDs for every draft, must never write `directives` or mutate a
finding. Model: `claude-opus-5`.

**`skills/directive-synthesis/SKILL.md`** (≤ 150 lines) — the shared contract:
the thirteen slugs, the four dispositions, the evidence rules, the draft output
shape. Both agents read it. This is the single definition both stages agree on;
it is what stops the taxonomy drifting between them.

**`scripts/triage.sh`** (≤ 150 lines) — the automation spine:

```
triage.sh categorise [--limit N] [--dry-run]
triage.sh synthesise <category> | --all
triage.sh status
```

Health-checks `127.0.0.1:8787` first and exits non-zero with a clear message if
`memoryd` is down. Everything through `curl`. No `sqlite3`.

**Commands** (≤ 40 each): `/triage-categorise`, `/triage-synthesise <category>`,
`/triage-review [category]`. The third is the stage-3 operator surface: shows
drafts with their sources, accepts or rejects one at a time, and is the only
path that writes a directive.

### Deliberately not built

- No `/apply-triage` equivalent that writes many directives at once. Ratification
  is one at a time, deliberately.
- No pruning or archiving command. Nothing accumulates that needs pruning — that
  was a consequence of the old `logs/` directory.
- No cache or cursor files of any kind.

---

## 10. Running it

**Manual, the normal path.**

```bash
.claude/scripts/triage.sh categorise          # ~30s, pennies
.claude/scripts/triage.sh status              # what landed where
.claude/scripts/triage.sh synthesise brief-authoring
/triage-review brief-authoring                # operator ratifies
```

**Unattended — stage 1 only.**

```
0 3 * * *  /Volumes/DATA/Bishop/.claude/scripts/triage.sh categorise >> ~/.bishop/triage.log 2>&1
```

Stage 1 is safe to automate: it writes two nullable columns and nothing else, it's
idempotent (only touches `category IS NULL`), and a wrong label is corrected by
clearing the column and re-running.

**Stage 2 should not be on a cron initially.** It's the expensive stage and its
output is drafts a human must read — generating them faster than they're reviewed
just builds a second backlog behind the first. Automate it only once stage 3 is
demonstrably keeping up.

**Stage 3 is never automated.** That is the whole point of it.

---

## 11. Build order

Each phase ends in a working, useful system. Stop after any of them.

| Phase | Deliverable | Value on its own |
|---|---|---|
| **A** | Migration + `GET /v1/triage/queue` + PATCH endpoints, with tests | Schema ready; queue inspectable by hand |
| **B** | Stage 0 bootstrap: 28 `CONV-` entries → `directives` | The comparison corpus exists |
| **C** | Stage 1: quinn + `triage.sh categorise` + `/triage-categorise` | **98 findings sorted into 12 buckets.** Real value, cheap, low risk |
| **D** | Stage 2: synthesist + drafts API + `triage.sh synthesise` | Drafts appear for review |
| **E** | Stage 3: `/triage-review` + gated directive write path | Loop closes |
| **F** | Cron for stage 1; tune taxonomy against `uncategorised` rate | Runs itself |

**Phase C is the natural first checkpoint.** After it, the question "what is
actually in these 98 findings" has an answer, and the shape of the taxonomy can
be judged against real assignments before any expensive synthesis is built on top
of it. If the categories turn out wrong, C is cheap to redo and D-E haven't been
written yet.

---

## 12. Failure modes, and the guard for each

| Risk | Guard |
|---|---|
| Miscategorisation buries an item | `uncategorised` escape hatch; `confidence` recorded; `triage.sh status` surfaces low-confidence counts |
| Synthesist emits 98 `new` directives | Prompt states `duplicate`/`amend` preferred, `new` needs justification; §7 disposition table is in the skill both agents read |
| Draft cites a finding that doesn't support it | `directive_draft_sources` makes every claim traceable; `/triage-review` shows source text beside the draft |
| A run half-completes | Every write is a single-row API call; re-running skips what's already set. No multi-step transaction to leave torn |
| Taxonomy drifts between the two agents | Both read one `SKILL.md`; slugs are a CHECK-constrained enum in the DB |
| Stage 1 corrupts a finding | PATCH is field-scoped to `category`/`theme_key`; unknown fields → 400 |
| `memoryd` down mid-run | `triage.sh` health-checks first, exits non-zero with a clear message |
| Directives and `CONVENTIONS.md` diverge | Unresolved — see §13, decision 1 |

---

## 13. Decisions needed before Phase A

These are yours, not mine. Each changes what gets built.

**1. After bootstrap, which is canonical — `CONVENTIONS.md` or the `directives`
table?** They'd hold the same 28 entries. Three options: DB canonical and the
file generated from it; file canonical and the DB a read-only mirror; or both
maintained by hand, which will drift. This is the single most consequential
choice here, because CLAUDE.md currently points every agent at the *file* as
binding, and a stage-3 ratification writes the *table*. My recommendation: DB
canonical, file regenerated on ratify, since the pipeline's whole value is having
these queryable.

**2. Should `patterns` participate in synthesis, or only inform it?** Patterns are
advisory by CLAUDE.md's own definition ("non-binding; CONVENTIONS.md wins"), so
promoting one to a directive changes its status. Cleanest: patterns are *context*
for stage 2 but never a primary source for a draft.

**3. Are `service_records` in scope at all?** They're per-agent performance
observations, and the natural output is an edit to an agent definition — not a
directive. They may want their own stage 2 variant producing agent-definition
proposals rather than directives. Including them in the main pipeline risks
generating directives out of what is really feedback about one agent.

**4. Twelve categories, or fewer?** Categories 3 (`agent-capability`, ~3 items)
and 12 (`repo-ci`, ~3 items) are thin. They could fold into 4 (`agent-doctrine`)
and 10 (`data-contracts`), giving ten. Thin categories aren't harmful — they just
cost one near-empty stage-2 call each.

**5. Do the three `DIRECTIVES candidate:` findings (80, 89, 90) skip stage 2?**
They're already written as ratifiable rules. A `--fast-track` flag could route
them straight to a draft. Small addition; asymmetry to maintain.

---

## 14. What this deliberately does not do

- **No auto-application.** Nothing edits an agent, a skill, or `CONVENTIONS.md`.
  The pipeline produces ratifiable *text*; a human ratifies; applying it is a
  separate mission through the normal lifecycle.
- **No recurrence detection.** CLAUDE.md notes that a finding logged 3+ times
  means an agent-definition deficiency. That's a genuinely different output
  (a definition proposal, not a directive) and folding it in now is how a second
  system's worth of complexity gets in. `theme_key` frequency gives the raw
  signal; act on it manually first, and only build something if the manual
  version proves worth automating.
- **No retirement of existing triage machinery.** The 1,802 lines stay until this
  is proven. Removal is its own decision, after phase E.

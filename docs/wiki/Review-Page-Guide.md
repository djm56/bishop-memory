# Review Page Guide

The review page at `http://127.0.0.1:8787/triage` is where findings get decided and directives get ratified. It is one embedded HTML page served by `memoryd`; nothing to install, reachable through the same SSH tunnel as the API. Open it with `make triage-review`.

## Before you start

Type your name in the **Approver** box once. It is stored in the browser and written as `Approver` on every decision, so the exported `FINDINGS.md` entry reads `**Approver**: Donovan Maidens` exactly as a hand-approved one does.

## The four tabs

### Pending

What the processor has recommended and you have not decided. The left rail lists categories with their pending counts; click one to narrow, or stay on **All categories**.

Within a category, **groups** come first. A group is a set of findings the processor judged to be instances of one rule; its header shows that rule, the doctrine file the fix belongs in, and **Accept all N recommendations**. Below the groups come **ungrouped** findings.

Each finding card shows:

- the id, date, owning harness, category, confidence and one-line summary;
- the target and the classifier's summary, with the full suggestion and rationale one click away;
- the recommendation badge (`approve`, `reject`, `supersede by #N`, `defer`) and its rationale, written to be checkable: a quoted sentence, a file, a finding id;
- for an approve, the **proposed change**: before/after text against the target file as it is today, with a copy button. You paste it; the agent never edits a file.

Buttons on a proposed finding:

| Button | Effect | Needs |
|---|---|---|
| Approve | `status = approved`, approver and date recorded | nothing |
| Reject | `status = rejected` | a reason, written as `Disposition (triage)` in the Markdown |
| Defer | nothing is written; the card stays pending for later | nothing |
| Supersede… | `status = superseded`, with the surviving finding's id | the id, and a reason is composed for you |
| Retire | `status = retired` | a reason |

**Accept all N recommendations** on a group takes each member's recommendation as the decision: approves become approved, rejects become rejected with the model's rationale as the reason, supersedes become superseded. Defers are left alone. Use it when a group reads right; use the per-card buttons when one member does not.

When you decide against the recommendation, that is recorded: the recommendation closes as `declined`, and the Runs tab counts it. The processor reads your recent decisions in a category before it recommends again, so disagreeing teaches it.

### Browse

Every finding, by category and status, with the same buttons. This is where you reach findings the processor has not got to yet, and where **Reopen** undoes a decision (it sets the finding back to `proposed` and clears the approver, date and note).

### Directives

Each pending draft is an editable form: title, applies-when, rule, rationale, reviewer check, example, with the evidence finding ids and the rendered length against the template budget (1,300 typical, 1,510 absolute; the service refuses more).

**Ratify** allocates the next `DIR-NNN`, stores your edited text, mirrors the directive into the `directives` table, marks every evidence finding `applied` with the note `Ratified as DIR-NNN`, and closes their pending recommendations. **Decline** needs a reason.

The entry does not reach `DIRECTIVES.md` until you export (below).

### Runs

Every classifier and processor run: category, model, how many findings it read, how many rows it wrote, and how many of its recommendations you accepted or declined. A category whose acceptance rate falls is the one whose description in `db/finding-categories.json` wants tightening; see [Developer: Triage Agents](Developer-Triage-Agents).

## Keyboard

`j` / `k` move the focus between cards. `a` approves, `r` rejects (prompts for the reason), `d` defers.

## Getting decisions into the Markdown

The header counts decided findings in the service. Decisions reach the harness files only when you run:

```bash
make triage-export                              # every registered harness
scripts/export-decisions.py --harness kirsch --dry-run   # preview one
```

For each decided finding the exporter finds the entry in that harness's `FINDINGS.md` by its natural key and rewrites exactly three lines — `**Status**`, `**Approver**`, `**Date approved**` — plus one `**Disposition (triage):**` line for a reject, retire or supersede. Ratified directives are appended to `DIRECTIVES.md` in template form with an index row. Before every write it copies the file to `<memory_root>/workspace/triage-backups/`, writes atomically, and re-reads to confirm.

Run it when no mission in that harness is mid-sync. It cannot start a reconcile loop: the harness hook only fires on Claude Code tool writes.

If the file already says something other than `proposed` and it differs from your decision, the exporter reports a **conflict**, leaves the entry alone and exits 3. The file wins; the next reconcile mirrors the file's status into the service. Reopen and re-decide on the page if the file was wrong.

## What the page cannot do, on purpose

- It cannot edit a skill, agent or finding text. Proposed changes are text to paste.
- It cannot run the agents. `make triage-classify` and `make triage-process` do that; the nightly jobs do it unattended.
- It cannot write Markdown. `make triage-export` does, so that a file write is always a deliberate step you ran.

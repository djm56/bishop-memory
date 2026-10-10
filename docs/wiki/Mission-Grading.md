# Mission Grading

Every finished mission gets a grade from **A** to **F**, a short summary of why, and a few suggestions for the next mission. The grades appear on the [mission HUD](Mission-HUD-Guide) and roll up into a per-harness view at `/performance`, so you can see how each connected harness is doing over time.

Grading is the third triage job, after classify and process. It runs nightly at 21:40 and can be run by hand. It works from the database alone: the mission's brief, debrief, steps, findings and agent notes as bishop-memory already holds them. It reads no files and makes no changes to a harness.

![Harness performance, light theme](images/performance-light.png)

## What the grades mean

| Grade | Means |
|---|---|
| **A** | Clean from start to finish: every acceptance criterion met, no rework, no escalation, no real sub-agent mistakes. Rare. |
| **B** | Every criterion met (or all but one minor one), at most one fix round, mistakes caught and corrected inside the mission. A good, normal mission. |
| **C** | Delivered, with noticeable rework: two or more fix rounds, an escalation, or several execution mistakes. Also normal. |
| **D** | Completed with significant problems: a criterion unmet together with repeated rework, or many recorded mistakes. |
| **E** | Completed only nominally: most criteria unmet or deferred. |
| **F** | Failed: outcome `failed`, or no criterion met. |

Most missions should land on **B** or **C**. Findings are not counted against a mission in themselves: a finding that proposes an improvement is the harness learning. Only findings and debrief entries that describe something that went wrong *in this mission* weigh on its grade.

A mission whose record is too thin to judge (no debrief and no steps) gets **not enough evidence** instead of a letter.

## Where grades appear

**Mission HUD.** Each graded mission shows its letter in the mission list and in its header, and a **Grade** section under the tiles: the letter, **Why** (the summary), **Suggestions**, and the counts the grade rests on. The list has a grade filter, including **Ungraded**.

![A mission's grade on the HUD, dark theme](images/missions-dark.png)

**Performance page** (`/performance`, the third link in the page switch):

- **Tiles** — the average grade over every harness, how many missions are graded, how many are waiting, and how many were set aside (not enough evidence, or skipped).
- **One card per harness** — its average letter and score out of 5, an A–F distribution bar, its latest ten grades newest first (each opens the mission), and **improving** or **slipping** when its five newest grades differ from the five before them by a third of a grade or more.
- **Grades** — every verdict, newest first, filterable by harness and grade, with the suggestions one click away.

The average counts A as 5 down to F as 0 and turns the result back into the nearest letter.

**Triage Runs tab.** Each grading run is listed with kind `grade`, its model, and how many missions it read and graded.

## What the grader sees

For each mission, bishop-memory assembles a compact **grading packet**:

- the mission row (id, title, harness, outcome, dates);
- **signals** the service counts itself: acceptance criteria met, partial, unmet and unclear (read from the result word after each criterion in the debrief, such as `— pass` or `— partial`), steps by status, injected steps such as `2a`/`2b` (usually fix rounds), escalation steps, QA steps, blocked events, and findings by status;
- the brief's goal and acceptance criteria;
- the debrief's judgement sections: Mission Summary, Acceptance Criteria Outcome, Tracker And Reality, Wrong Assumptions, Sub-Agent Mistakes, QA Verdict;
- one line per step, per finding and per agent note.

Every text is capped, so ten missions make one small read. The signals are stored with each grade and shown under it, so any letter can be checked against the facts it rests on.

## Running it

The schedule runs it nightly. To run it now:

```sh
make triage-grade                       # the next 10 ungraded finished missions
make triage-grade LIMIT=5               # fewer
make triage-grade MISSIONS=mission-20261009-03,mission-20261009-04
make triage-grade MISSIONS=mission-20261009-03 REGRADE=1   # replace an existing grade
```

A run with nothing waiting exits at once without starting an agent. Each run makes one pass: the grader claims at most ten missions, grades them, writes every verdict in one call and stops.

## No loops

- A grade, or a "not enough evidence" verdict, is final. A graded mission is never picked up again on its own.
- A mission a run claimed but did not grade (the run failed or timed out) is retried by **one** later run. If that one fails too, the mission is marked **skipped** and left alone.
- The only way to grade a mission again is `REGRADE=1` with its id, or `DELETE /v1/mission-grades/<id>`. No agent can do either.

## Settings

| Variable | Default | Purpose |
|---|---|---|
| `TRIAGE_GRADE_ENGINE` | `TRIAGE_ENGINE` | `claude` or `opencode` for grading only |
| `TRIAGE_GRADE_MODEL` | `opencode-go/glm-5.2` on opencode, `sonnet` on claude | Model |
| `TRIAGE_GRADE_LIMIT` | 10 | Missions per run, 1–10 |
| `TRIAGE_TIMEOUT_MIN` | 20 for a grade run | Stop a run still going after this many minutes |

`scripts/install-triage-schedule.sh --grade-at HH:MM` moves the nightly slot.

## About these screenshots

The images come from made-up demo data (`make screenshots`); the harnesses `api-service` and `docs-site` and their grades are invented.

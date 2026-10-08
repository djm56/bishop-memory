#!/usr/bin/env python3
"""
scripts/gen-mcp-reference.py — regenerate docs/wiki/MCP-Tool-Reference.md
from the mcpd source.

Tool names, descriptions and argument schemas are parsed out of
cmd/mcpd/main.go (harness profile) and cmd/mcpd/triage.go (triage profile),
so the page cannot drift from what the adapter registers. The worked
examples and notes live in EXAMPLES below; add an entry when you add a tool.

Usage: scripts/gen-mcp-reference.py [--check]
  --check   exit 1 if the page on disk differs from what would be generated
"""
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "docs", "wiki", "MCP-Tool-Reference.md")
SOURCES = (("harness", "cmd/mcpd/main.go"), ("triage", "cmd/mcpd/triage.go"))

ROUTE = {
    "memory_search": "GET /v1/memory/search", "mission_list": "GET /v1/missions", "mission_get": "GET /v1/missions/:id",
    "mission_allocate": "POST /v1/missions/allocate", "mission_create": "POST /v1/missions", "mission_update": "PATCH /v1/missions/:id",
    "flight_recorder_append": "POST /v1/flight-recorder", "mission_step_record": "POST /v1/missions/:id/steps",
    "documents_sync": "POST /v1/documents/sync", "mission_steps_list": "GET /v1/missions/:id/steps", "finding_list": "GET /v1/findings",
    "pattern_list": "GET /v1/patterns", "service_record_list": "GET /v1/service-records", "finding_append": "POST /v1/findings",
    "pattern_append": "POST /v1/patterns", "service_record_append": "POST /v1/service-records",
    "triage_categories": "GET /v1/finding-categories", "triage_next_unclassified": "GET /v1/findings?unclassified=1&order=asc",
    "triage_category_findings": "GET /v1/findings?category=&status=proposed&no_pending=1", "triage_recent_decisions": "GET /v1/triage/decisions",
    "triage_run_start": "POST /v1/triage/runs", "triage_run_finish": "PATCH /v1/triage/runs/:id", "triage_classify": "PUT /v1/triage/classifications",
    "triage_group_create": "POST /v1/finding-groups", "triage_recommend": "POST /v1/finding-recommendations", "directive_propose": "POST /v1/directive-proposals",
}

# tool -> (example call, example result, note)
EXAMPLES = {
    "memory_search": ({"q": "state-sync \"positive control\"", "limit": 5},
        {"query": "state-sync \"positive control\"", "results": [{"id": 41, "title": "mission-20260916-02 — DEBRIEF", "kind": "debrief", "source_path": "/…/.claude/memory/missions/mission-20260916-02/DEBRIEF.md", "snippet": "…paired with a <b>positive control</b> proven to return…"}]},
        "Multi-word is implicit AND; wrap phrases in double quotes; a single trailing `*` on a plain word is a prefix match (`verif*`). Any word containing punctuation — a hyphenated mission id, `step-sync`, `FLIGHT-RECORDER` — is matched as a literal phrase automatically, so it never needs quoting. An unterminated `\"` returns a 400, and no error ever echoes your query. An empty index returns an empty list, not an error. Full rules: `[Developer: HTTP API](Developer-HTTP-API#search-query-handling)`, *Query handling*."),
    "mission_list": ({}, {"missions": [{"id": "mission-20261002-02", "title": "Close Milestone 2", "status": "in-progress", "outcome": None, "harness": "kirsch", "priority": "normal", "next_action": "", "updated_at": "2026-10-02 08:25 UTC"}]},
        "Newest-updated first. There is no status filter on the tool; filter the list yourself."),
    "mission_get": ({"id": "mission-20261002-02"}, {"id": "mission-20261002-02", "title": "Close Milestone 2", "status": "in-progress", "harness": "kirsch"},
        "`.` and `..` are refused before any request is made: they would collapse the URL onto the list route."),
    "mission_allocate": ({"title": "Fix Linux-only CI test failures", "harness": "kirsch", "owner": "bishop"}, {"id": "mission-20261002-03", "harness": "kirsch", "date": "20261002", "seq": 3, "created": True, "attempts": 1},
        "Use this, not `mission_create`, in central mode. The sequence is global across every harness for that UTC day, so two harnesses cannot collide. Pass `harness` from the calling harness's `.claude/bishop-memory.conf`; omit it only when one harness uses the service."),
    "mission_create": ({"id": "mission-20261002-03", "title": "Fix Linux-only CI test failures", "status": "in-progress"}, {"id": "mission-20261002-03", "created": True},
        "Standalone mode only, where the harness derives the id itself. A duplicate id is a 409."),
    "mission_update": ({"id": "mission-20261002-03", "status": "complete", "outcome": "done", "next_action": ""}, {"id": "mission-20261002-03", "updated": True},
        "Omitted fields are unchanged. Setting `status` to `complete` stamps `closed_at`. The canonical value for an absent `next_action` is the empty string."),
    "flight_recorder_append": ({"mission_id": "mission-20261002-03", "step": "4a", "event": "step-sync", "note": "Step 4a done; tests green", "agent": "lambert", "occurred_at": "2026-10-02 09:31 UTC"}, {"appended": True, "id": 3275},
        "The stored actor is `<BISHOP_HARNESS>:<agent>`, e.g. `kirsch:lambert`. `mission_id` must exist (404 otherwise); omit it for a system-scoped event. `event` is a dotted or hyphenated discriminator the API does not validate against a list."),
    "mission_step_record": ({"id": "mission-20261002-03", "step": "4a", "phase": "Fix", "agent": "hicks", "status": "done", "summary": "Lint clean"}, {"recorded": True, "mission_id": "mission-20261002-03", "created": False},
        "Upsert keyed on `(mission_id, step)`: the first call creates (201, `created: true`), later calls update and preserve omitted fields. An empty string behaves like an omitted field, so a value cannot be cleared through this tool."),
    "documents_sync": ({"root": "/abs/path/to/harness/.claude/memory"}, {"root": "/abs/path/to/harness/.claude/memory", "synced": True},
        "Incremental: unchanged files are skipped by SHA-256. The filesystem root `/` is refused. This is the one tool with a five-minute timeout; everything else has thirty seconds."),
    "mission_steps_list": ({"id": "mission-20261002-03"}, {"steps": [{"id": 912, "mission_id": "mission-20261002-03", "step": "4a", "phase": "Fix", "agent": "kirsch:hicks", "status": "done", "summary": "Lint clean"}]}, ""),
    "finding_list": ({"status": "approved", "category": "brief-writing"},
        {"findings": [{"id": 70, "finding_date": "2026-09-14", "target": "mission-lifecycle", "suggestion": "State the precondition for renumbering PROGRESS.md rows…", "status": "approved", "approver": "Donovan Maidens", "date_approved": "02 October 2026", "harness": "kirsch", "triage": {"category": "state-files-and-journal", "confidence": 0.95, "summary": "Renumber only rows with no FLIGHT-RECORDER history"}}]},
        "The crew's way to read ratified findings for an area before briefing it: `status=approved` plus the category. Each row carries its triage classification and, when one is pending, its recommendation. `category=uncategorised` lists findings with no classification."),
    "pattern_list": ({}, {"patterns": [{"id": 45, "name": "Set file modes in tests with Chmod", "context": "A test asserts on a file's permission bits.", "solution": "…", "discovered_mission": "mission-20261002-01"}]}, "Advisory. Directives win on any conflict."),
    "service_record_list": ({"agent": "lambert"}, {"service_records": [{"id": 83, "agent": "lambert", "record_date": "2026-10-02", "title": "Exact-text edits accurate; trailers still intermittent", "source": "bishop-observed"}]}, "`agent` is the subject of the record, not the caller."),
    "finding_append": ({"suggestion": "Every review brief pastes the reviewed step's own diff hunks.", "finding_date": "2026-10-02", "target": "@bishop (review briefs)", "rationale": "The reviewer has no shell…", "mission_id": "mission-20261002-01"}, {"id": 315, "created": True},
        "Always created `proposed`; `status`, `approver` and `date_approved` are not accepted. The owning harness is taken from `BISHOP_HARNESS`, never from the caller. The reconciler creates the same row from `FINDINGS.md` using the natural key `(finding_date, target, suggestion)`, so a finding written both ways is stored once."),
    "pattern_append": ({"name": "Pair a filtered item with an unfiltered control", "context": "Testing that something is removed or stripped.", "solution": "Assert the control survives in the same test.", "discovered_at": "2026-09-30", "discovered_mission": "mission-20260930-01"}, {"id": 46, "created": True}, ""),
    "service_record_append": ({"agent": "hicks", "record_date": "2026-10-02", "title": "Every code fix landed correct", "note": "…", "adjustment": "Keep the clause …", "source": "bishop-observed"}, {"id": 84, "created": True}, "`source` must be `self-reported` or `bishop-observed` (or omitted)."),
    "triage_categories": ({}, {"categories": [{"slug": "brief-writing", "name": "Brief writing", "description": "How Bishop writes a brief…", "examples": "…", "proposed_count": 70, "pending_count": 30, "last_processed_at": "2026-10-02 10:52:06"}], "uncategorised_count": 0}, "The slugs returned here are the only legal values for `triage_classify`."),
    "triage_next_unclassified": ({"limit": 50}, {"findings": [{"id": 12, "finding_date": "2026-09-03", "target": "mission-lifecycle skill", "suggestion": "…", "rationale": "…", "status": "proposed"}]}, "Oldest first. An empty list means classification is complete."),
    "triage_category_findings": ({"category": "brief-writing", "limit": 30}, {"findings": [{"id": 3, "status": "proposed", "triage": {"category": "brief-writing", "summary": "Bound how strongly a deliverable may state a claim"}}]}, "Only proposed findings without a pending recommendation, unless `include_pending` is true."),
    "triage_recent_decisions": ({"category": "brief-writing", "limit": 20}, {"findings": [{"id": 71, "status": "rejected", "decision_note": "Tool roster varies per build", "recommendation": {"recommendation": "approve", "state": "declined"}}]}, "What the operator decided and whether they took the earlier recommendation. Read before recommending."),
    "triage_run_start": ({"kind": "process", "category": "brief-writing", "model": "claude-sonnet-5-5"}, {"id": 4, "created": True}, "Pass the returned id as `run_id` on every write."),
    "triage_run_finish": ({"id": 4, "status": "done", "considered": 30, "written": 30, "notes": "4 groups, 26 approve, 1 supersede, 1 reject, 2 defer, 2 directive drafts."}, {"id": 4, "status": "done", "updated": True}, "A finished `process` run stamps the category's `last_processed_at`, which moves the nightly rotation on."),
    "triage_classify": ({"classified_by": "claude-haiku-4-5-20251001", "run_id": 1, "items": [{"finding_id": 12, "category": "mission-planning", "confidence": 0.9, "directive_candidate": False, "summary": "Define a resume path for a partially completed step"}]}, {"classified": 1}, "Whole batch refused with a 400 naming the item if a slug is not active or an id is unknown."),
    "triage_group_create": ({"category": "brief-writing", "title": "Quote literal text in a brief", "summary": "Where a brief depends on exact text, it quotes it verbatim.", "target": ".claude/agents/bishop.md", "run_id": 4}, {"id": 3, "created": True}, ""),
    "triage_recommend": ({"run_id": 4, "items": [{"finding_id": 36, "group_id": 3, "recommendation": "approve", "rationale": "Not covered in bishop.md.", "proposed_change": "File: .claude/agents/bishop.md, under \"Writing briefs\"\nADD bullet: …"}, {"finding_id": 33, "recommendation": "supersede", "superseded_by": 49, "rationale": "#49 states the same rule with more detail."}]}, {"written": 2, "skipped": []}, "A finding that is no longer `proposed` is skipped and listed. An existing pending recommendation is expired and replaced."),
    "directive_propose": ({"title": "Briefs quote text and records literally", "applies_when": "A change that writes a brief which depends on exact code, comment or state-record text", "rule": "A brief MUST quote such text verbatim and MUST NOT paraphrase it.", "rationale": "A paraphrase that does not exist in the tree gets transcribed as fact.", "reviewer_check": "Does every quoted string in the brief match the file byte for byte?", "evidence": [36, 69, 73], "group_id": 3, "harness": "kirsch", "run_id": 4}, {"id": 6, "created": True, "rendered_length": 742}, "Creates a proposal only; a human ratifies it on the review page. Refused when the rendered entry would exceed 1,510 characters."),
}

ERRORS = """| Message shape | Meaning | What to do |
|---|---|---|
| `<tool>: \\`x\\` is required and must not be empty` | The adapter refused the call before any request was made | Supply the argument |
| `<tool>: "id" must not be "." or ".."` | A dot segment would collapse the URL onto another route | Use a real id |
| `<tool>: HTTP 400: {"error":"invalid request","details":…}` | The service rejected the body; `details` names the field and tag, or carries a static message | Fix the named field |
| `<tool>: HTTP 404: …` | Unknown mission, finding, run or proposal | Check the id |
| `<tool>: HTTP 409: …` | `mission_create` with an id that exists | Use `mission_allocate` in central mode |
| `<tool>: HTTP 503: …` | `mission_allocate` lost ten id races in a row | Retry after a second |
| `<tool>: HTTP transport error: …` | The service is not running or the URL is wrong | `curl http://127.0.0.1:8787/healthz`; check `BISHOP_MEMORY_URL` in `.mcp.json` |
| `<tool>: server returned a 2xx response with a malformed JSON body` | A service-side bug | Report it with the tool name |
"""


def capture_call(src, start):
    """Return the text inside the parenthesis opening at src[start] and the index of its close."""
    depth = 0
    i = start
    in_string = False
    while i < len(src):
        c = src[i]
        if in_string:
            if c == "\\":
                i += 2
                continue
            if c == '"':
                in_string = False
        else:
            if c == '"':
                in_string = True
            elif c == "(":
                depth += 1
            elif c == ")":
                depth -= 1
                if depth == 0:
                    return src[start + 1:i], i
        i += 1
    return None, i


def string_literal(s):
    m = re.search(r'"((?:[^"\\]|\\.)*)"', s)
    return m.group(1).replace('\\"', '"') if m else ""


def parse_tools():
    tools = []
    for profile, path in SOURCES:
        src = open(os.path.join(ROOT, path), encoding="utf-8").read()
        for m in re.finditer(r"mcp\.NewTool\(", src):
            body, _ = capture_call(src, m.end() - 1)
            name = string_literal(body)
            desc = ""
            dm = re.search(r"mcp\.WithDescription\(", body)
            if dm:
                d, _ = capture_call(body, dm.end() - 1)
                desc = string_literal(d)
            params = []
            for pm in re.finditer(r"mcp\.With(String|Integer|Boolean|Array)\(", body):
                pb, _ = capture_call(body, pm.end() - 1)
                pdesc = ""
                dd = re.search(r"mcp\.Description\(", pb)
                if dd:
                    d, _ = capture_call(pb, dd.end() - 1)
                    pdesc = string_literal(d)
                params.append({"name": string_literal(pb), "type": pm.group(1).lower(),
                               "required": "mcp.Required()" in pb, "description": pdesc})
            tools.append({"profile": profile, "name": name, "description": desc, "params": params})
    return tools


def table(params):
    if not params:
        return "_No arguments._\n"
    rows = ["| Argument | Type | Required | Meaning |", "|---|---|---|---|"]
    for p in params:
        rows.append("| `%s` | %s | %s | %s |" % (p["name"], p["type"], "yes" if p["required"] else "no",
                                                 p["description"].replace("|", "\\|")))
    return "\n".join(rows) + "\n"


def render(tools):
    out = ["# MCP Tool Reference\n",
           "Every tool `mcpd` exposes, in both profiles, generated from the adapter source (`cmd/mcpd/main.go`, `cmd/mcpd/triage.go`) by `scripts/gen-mcp-reference.py`, with worked examples. An asterisk marks a required argument. Every tool returns the HTTP route's JSON body as text; a non-2xx status or transport failure comes back as an MCP error result naming the tool, never as a thrown error.\n",
           "## Profiles\n",
           "`mcpd` registers one of two tool sets, chosen by the `MCPD_PROFILE` environment variable in its registration:\n",
           "- **harness** (default) — the 16 tools a Bishop crew uses during a mission. Registered by the harness's generated `.mcp.json` with `BISHOP_HARNESS` set to that harness's name.\n- **triage** — the 13 tools the two scheduled findings-triage agents use. Registered only by `scripts/triage-run.sh`.\n",
           "The two profiles share no write tool. Neither profile can change a finding's status, decide a directive proposal, or write a directive: those routes exist on HTTP for the operator and are never registered as tools.\n",
           "## Agent identity\n",
           "Two harness tools record who acted: `flight_recorder_append` and `mission_step_record`. Each takes an `agent` argument naming the sub-agent (`bishop`, `hicks`, …) and `mcpd` stores `<BISHOP_HARNESS>:<agent>`, for example `kirsch:hicks`. `finding_append` records the harness only. Nothing else carries identity.\n"]
    shared = ("memory_search", "pattern_list", "finding_list")
    for profile, title in (("harness", "Harness profile (16 tools)"), ("triage", "Triage profile (13 tools)")):
        out.append("\n## %s\n" % title)
        if profile == "triage":
            out.append("`memory_search`, `pattern_list` and `finding_list` are the same tools as in the harness profile and are not repeated here. The rest are below.\n")
        for t in tools:
            if t["profile"] != profile or (profile == "triage" and t["name"] in shared):
                continue
            out.append("\n### `%s`\n" % t["name"])
            out.append("**Route:** `%s`\n" % ROUTE.get(t["name"], ""))
            out.append(t["description"] + "\n")
            out.append(table(t["params"]))
            if t["name"] in EXAMPLES:
                call, resp, note = EXAMPLES[t["name"]]
                out.append("\n**Example call**\n```json\n" + json.dumps(call, indent=2, ensure_ascii=False) + "\n```\n")
                out.append("**Example result**\n```json\n" + json.dumps(resp, indent=2, ensure_ascii=False) + "\n```\n")
                if note:
                    out.append(note + "\n")
    out.append("\n## Errors you will see\n")
    out.append(ERRORS)
    out.append("\n## Regenerating this page\n")
    out.append("```bash\nscripts/gen-mcp-reference.py          # rewrite docs/wiki/MCP-Tool-Reference.md from the source\nscripts/gen-mcp-reference.py --check  # exit 1 if the page is stale\n```\n")
    return "\n".join(out)


def main():
    text = render(parse_tools())
    if "--check" in sys.argv:
        current = open(OUT, encoding="utf-8").read() if os.path.exists(OUT) else ""
        if current != text:
            print("MCP-Tool-Reference.md is stale; run scripts/gen-mcp-reference.py", file=sys.stderr)
            return 1
        print("MCP-Tool-Reference.md is current")
        return 0
    with open(OUT, "w", encoding="utf-8") as fh:
        fh.write(text)
    print("wrote %s" % os.path.relpath(OUT, ROOT))
    return 0


if __name__ == "__main__":
    sys.exit(main())

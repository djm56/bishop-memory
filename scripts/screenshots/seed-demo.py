#!/usr/bin/env python3
"""
scripts/screenshots/seed-demo.py — load the made-up demo dataset
(scripts/screenshots/demo.json) into a bishop-memory instance through the
HTTP API, so the documentation screenshots show a realistic review page
without any real operator data.

It drives the same routes the triage agents and the review page use —
findings, classifications, runs, groups, recommendations, directive
proposals, decisions — so a schema or contract change that breaks those
routes breaks the screenshots run too, instead of silently drifting.

Demo missions are written as a harness memory tree under --memory-root
(missions/<id>/BRIEF.md, PROGRESS.md, DEBRIEF.md, and agents/ beside it),
registered as the demo harness, and recorded through the mission, step,
flight-recorder, pattern and service-record routes, so /missions has
something to show. --db, the throwaway database file, lets the seeder
backdate the missions and their journal to the dates in demo.json; the API
stamps both with the current time.

Refuses to seed a database that already holds findings: this is for a
throwaway instance only (scripts/screenshots/run.sh starts one).

Usage: seed-demo.py --url http://127.0.0.1:8790 [--file demo.json]
                    [--memory-root DIR --db FILE]
"""
import argparse
import json
import os
import sqlite3
import sys
import urllib.error

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(HERE))
import triage_common as tc  # noqa: E402


def call(url, method, path, body=None):
    try:
        return tc.api(method, url + path, body)[1]
    except urllib.error.HTTPError as exc:
        tc.die(f"[seed-demo] {method} {path} -> HTTP {exc.code}: {exc.read().decode(errors='replace')[:300]}")


BRIEF = """# Brief — {id}

## Goal
{goal}

## Acceptance Criteria
{criteria}

## Key Files
{files}
"""

PROGRESS = """# Progress — {id}

| Step | Phase | Agent | Status | Notes |
|------|-------|-------|--------|-------|
{rows}
"""

DEBRIEF = """# Debrief — {id}

## Mission Summary
- Goal: {goal}
- Outcome: {outcome}
- Completed: {closed} UTC

## Acceptance Criteria Outcome
{outcomes}

## Logical Step Recap
| Step | Phase | Agent | Status | Notes |
|------|-------|-------|--------|-------|
{recap}

## Deliverables Changed
{files}

## Wrong Assumptions (Mandatory)
{wrong}

## Sub-Agent Mistakes and Corrections (Mandatory)
{mistakes}

## Findings and Patterns Linked
- Findings entry refs: {refs}
- Pattern entry refs: {patterns}
"""


def write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)


def seed_missions(url, demo, memory_root, db_path):
    """Record the demo missions and write their files. Returns {finding key: mission id}."""
    harness = demo["harness"]
    for a in demo.get("agents", []):
        write(os.path.join(os.path.dirname(memory_root), "agents", a["name"] + ".md"),
              f"---\nname: {a['name']}\ndescription: \"{a['description']}\"\n---\n\n# {a['name'].title()}\n")
    call(url, "PUT", f"/v1/harnesses/{harness}", {"memory_root": memory_root})

    findings_by_key = {f["key"]: f for f in demo["findings"]}
    mission_of = {}
    for m in demo.get("missions", []):
        mid = call(url, "POST", "/v1/missions/allocate", {"harness": harness, "title": m["title"], "owner": m["owner"]})["id"]
        demo.setdefault("_mission_ids", {})[m["key"]] = mid
        bullets = lambda items: "\n".join("- " + i for i in items)
        rows = lambda: "\n".join(f"| {s['step']} | {s['phase'] or '—'} | {s['agent']} | {s['status']} | {s['summary'] or s['notes']} |" for s in m["steps"])
        folder = os.path.join(memory_root, "missions", mid)
        write(os.path.join(folder, "BRIEF.md"), BRIEF.format(id=mid, goal=m["goal"],
              criteria=bullets(c for c, _ in m["criteria"]), files=bullets(m["files"])))
        write(os.path.join(folder, "PROGRESS.md"), PROGRESS.format(id=mid, rows=rows()))
        if m["status"] == "complete":
            refs = "; ".join(f"[{findings_by_key[k]['date']}] — {findings_by_key[k]['target']}" for k in m["findings"]) or "none"
            write(os.path.join(folder, "DEBRIEF.md"), DEBRIEF.format(
                id=mid, goal=m["goal"], outcome=m["outcome"], closed=m["closed"],
                outcomes="\n".join(f"- [x] {c} — {e}" for c, e in m["criteria"]),
                recap=rows(), files=bullets(m["files"]), wrong=m["wrong"], mistakes=m["mistakes"],
                refs=refs, patterns="; ".join(p["name"] for p in m["patterns"]) or "none"))

        call(url, "PATCH", f"/v1/missions/{mid}", {"status": "in-progress"})
        for s in m["steps"]:
            body = {k: s[k] for k in ("step", "agent", "status", "notes")}
            if s["phase"]:
                body["phase"] = s["phase"]
            if s["start"]:
                body["started_at"] = s["start"]
            if s["end"]:
                body["ended_at"] = s["end"]
            call(url, "POST", f"/v1/missions/{mid}/steps", body)
            if s["summary"]:
                call(url, "POST", "/v1/flight-recorder", {"mission_id": mid, "step": s["step"], "agent": s["agent"],
                                                          "event": "step-sync", "note": s["summary"]})
        for p in m["patterns"]:
            call(url, "POST", "/v1/patterns", dict(p, discovered_at=m["opened"][:10], discovered_mission=mid))
        for r in m["service_records"]:
            call(url, "POST", "/v1/service-records", dict(r, record_date=m["opened"][:10]))
        if m["status"] == "complete":
            call(url, "POST", "/v1/flight-recorder", {"mission_id": mid, "agent": "@bishop", "event": "complete", "note": m["title"]})
            call(url, "PATCH", f"/v1/missions/{mid}", {"status": "complete", "outcome": m["outcome"]})
        else:
            call(url, "PATCH", f"/v1/missions/{mid}", {"next_action": m["next_action"]})
        for k in m["findings"]:
            mission_of[k] = mid

        # The API stamps the mission and its journal with the current time;
        # move them to the demo's dates, spreading the journal across them.
        if db_path:
            db = sqlite3.connect(db_path)
            end = m["closed"] or m["steps"][-1]["start"]
            db.execute("UPDATE missions SET opened_at = ?, closed_at = ?, created_at = ?, updated_at = ? WHERE id = ?",
                       (m["opened"], m["closed"], m["opened"], end, mid))
            ids = [r[0] for r in db.execute("SELECT id FROM flight_recorder WHERE mission_id = ? ORDER BY id", (mid,))]
            for i, rid in enumerate(ids):
                db.execute("""UPDATE flight_recorder SET created_at = datetime(?, '+' ||
                              CAST((julianday(?) - julianday(?)) * 86400 * ? / ? AS INTEGER) || ' seconds') WHERE id = ?""",
                           (m["opened"], end, m["opened"], i, max(len(ids) - 1, 1), rid))
            db.commit()
            db.close()

    call(url, "POST", "/v1/documents/sync", {"harness": harness})
    return mission_of


def seed_grades(url, demo, db_path):
    """Grade the demo missions, and a short made-up history on other harnesses,
    through the mission grader's own routes: a grade run, claim, write."""
    spec = demo.get("grades")
    if not spec:
        return 0
    verdicts = {}
    for key, v in spec.get("missions", {}).items():
        verdicts[demo["_mission_ids"][key]] = v
    for h in spec.get("history", []):
        mid = call(url, "POST", "/v1/missions/allocate", {"harness": h["harness"], "title": h["title"], "owner": "@bishop"})["id"]
        call(url, "PATCH", f"/v1/missions/{mid}", {"status": "complete", "outcome": "done"})
        if db_path:
            db = sqlite3.connect(db_path)
            db.execute("UPDATE missions SET opened_at = datetime(?, '-3 hours'), closed_at = ?, updated_at = ? WHERE id = ?",
                       (h["closed"], h["closed"], h["closed"], mid))
            db.commit()
            db.close()
        verdicts[mid] = h

    run_id = call(url, "POST", "/v1/triage/runs", {"kind": "grade", "model": spec["model"]})["id"]
    ids = list(verdicts)
    written = 0
    for i in range(0, len(ids), 10):
        batch = ids[i:i + 10]
        call(url, "POST", "/v1/mission-grades/claim", {"run_id": run_id, "mission_ids": batch})
        items = [{"mission_id": mid, "grade": verdicts[mid]["grade"], "summary": verdicts[mid]["summary"],
                  "suggestions": verdicts[mid]["suggestions"]} for mid in batch]
        written += call(url, "POST", "/v1/mission-grades", {"graded_by": spec["model"], "run_id": run_id, "items": items})["written"]
    call(url, "PATCH", f"/v1/triage/runs/{run_id}",
         {"status": "done", "considered": len(ids), "written": written,
          "notes": f"Graded {written} finished missions."})
    return written


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--url", required=True)
    parser.add_argument("--file", default=os.path.join(HERE, "demo.json"))
    parser.add_argument("--memory-root", help="where to write the demo harness's mission files (enables demo missions)")
    parser.add_argument("--db", help="the throwaway database file, to backdate the demo missions")
    args = parser.parse_args()
    url = args.url.rstrip("/")
    demo = json.load(open(args.file, encoding="utf-8"))

    if call(url, "GET", "/v1/findings?limit=1")["findings"]:
        tc.die("[seed-demo] this instance already holds findings; refusing to mix demo data into it", 2)

    harness = demo["harness"]
    approver = demo["approver"]

    mission_of = seed_missions(url, demo, os.path.abspath(args.memory_root), args.db) if args.memory_root else {}

    # Findings, created the way finding_append and the reconciler create them.
    ids = {}
    for f in demo["findings"]:
        body = {"finding_date": f["date"], "target": f["target"],
                "suggestion": f["suggestion"], "rationale": f["rationale"], "harness": harness}
        if f["key"] in mission_of:
            body["mission_id"] = mission_of[f["key"]]
        out = call(url, "POST", "/v1/findings", body)
        ids[f["key"]] = out["id"]

    group_ids = {}
    proposal_ids = {}
    for run in demo["runs"]:
        run_id = call(url, "POST", "/v1/triage/runs",
                      {"kind": run["kind"], "category": run.get("category", ""), "model": run["model"]})["id"]
        written = 0
        if run["kind"] == "classify":
            items = []
            for f in demo["findings"]:
                item = {"finding_id": ids[f["key"]], "category": f["category"],
                        "confidence": f["confidence"], "summary": f["summary"],
                        "directive_candidate": f.get("directive_candidate", False)}
                if f.get("secondary"):
                    item["secondary_category"] = f["secondary"]
                items.append(item)
            call(url, "PUT", "/v1/triage/classifications",
                 {"classified_by": run["model"], "run_id": run_id, "items": items})
            considered = written = len(items)
        else:
            for g in run["groups"]:
                group_ids[g["key"]] = call(url, "POST", "/v1/finding-groups", {
                    "category": run["category"], "title": g["title"], "summary": g["summary"],
                    "target": g["target"], "run_id": run_id})["id"]
            items = []
            for r in run["recommendations"]:
                item = {"finding_id": ids[r["finding"]], "recommendation": r["recommendation"],
                        "rationale": r["rationale"]}
                if r.get("group"):
                    item["group_id"] = group_ids[r["group"]]
                if r.get("superseded_by"):
                    item["superseded_by"] = ids[r["superseded_by"]]
                if r.get("proposed_change"):
                    item["proposed_change"] = r["proposed_change"]
                items.append(item)
            written = call(url, "POST", "/v1/finding-recommendations", {"run_id": run_id, "items": items})["written"]
            for p in run.get("proposals", []):
                body = {k: p[k] for k in ("title", "applies_when", "rule", "rationale", "reviewer_check")}
                body.update({"evidence": [ids[k] for k in p["evidence"]], "group_id": group_ids[p["group"]],
                             "run_id": run_id})
                if p.get("example"):
                    body["example"] = p["example"]
                proposal_ids[p["group"]] = call(url, "POST", "/v1/directive-proposals", body)["id"]
                written += 1
            considered = len(run["recommendations"])
        call(url, "PATCH", f"/v1/triage/runs/{run_id}",
             {"status": "done", "considered": considered, "written": written, "notes": run["notes"]})

    # The operator's decisions, through the same route the review page uses.
    for d in demo["decisions"]:
        body = {"status": d["status"], "approver": approver}
        if d.get("note"):
            body["note"] = d["note"]
        call(url, "POST", f"/v1/findings/{ids[d['finding']]}/decision", body)
    for group_key in demo.get("ratify", []):
        call(url, "POST", f"/v1/directive-proposals/{proposal_ids[group_key]}/decision",
             {"state": "accepted", "decided_by": approver})

    graded = seed_grades(url, demo, args.db) if args.memory_root else 0

    pending = call(url, "GET", "/v1/triage/pending")
    missions = len(demo.get("missions", [])) if args.memory_root else 0
    print(f"[seed-demo] {missions} missions, {len(ids)} findings, {len(demo['runs'])} runs, "
          f"{pending['pending_findings']} pending recommendations, "
          f"{len(pending['directive_proposals'])} pending directive drafts, {graded} mission grades")


if __name__ == "__main__":
    main()

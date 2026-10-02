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

Refuses to seed a database that already holds findings: this is for a
throwaway instance only (scripts/screenshots/run.sh starts one).

Usage: seed-demo.py --url http://127.0.0.1:8790 [--file demo.json]
"""
import argparse
import json
import os
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


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--url", required=True)
    parser.add_argument("--file", default=os.path.join(HERE, "demo.json"))
    args = parser.parse_args()
    url = args.url.rstrip("/")
    demo = json.load(open(args.file, encoding="utf-8"))

    if call(url, "GET", "/v1/findings?limit=1")["findings"]:
        tc.die("[seed-demo] this instance already holds findings; refusing to mix demo data into it", 2)

    harness = demo["harness"]
    approver = demo["approver"]

    # Findings, created the way finding_append and the reconciler create them.
    ids = {}
    for f in demo["findings"]:
        out = call(url, "POST", "/v1/findings", {
            "finding_date": f["date"], "target": f["target"],
            "suggestion": f["suggestion"], "rationale": f["rationale"], "harness": harness,
        })
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

    pending = call(url, "GET", "/v1/triage/pending")
    print(f"[seed-demo] {len(ids)} findings, {len(demo['runs'])} runs, "
          f"{pending['pending_findings']} pending recommendations, "
          f"{len(pending['directive_proposals'])} pending directive drafts")


if __name__ == "__main__":
    main()

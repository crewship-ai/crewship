#!/usr/bin/env python3
"""
Nightly CI probe — deterministic, token-zero. Runs as a WAKE GATE: only when
it returns wake=true does the expensive agent triage routine wake up.

It watches two different failures:

  RED    — a scheduled workflow finished and failed (failure / timed_out /
           cancelled / startup_failure / action_required / stale). Visible.
  STALE  — a scheduled workflow has not run AT ALL for longer than the
           threshold. Invisible, and the worse of the two: a cron dropped in a
           YAML refactor, broken syntax, or GitHub disabling the workflow after
           60 days of repository inactivity. No green appears, no red either —
           and nobody notices.

Data source:
    --repo owner/name                 -> GitHub API (needs GH_TOKEN in the env)
    --workflows-file / --runs-file    -> injected data (tests, dry runs)

Output: JSON on stdout, always carrying a `wake` field and a `panel` object
with ready-made status.v1 states so a routine can publish it to a Page with
plain `.field` transforms.

The exit code is ALWAYS 0, even when there is something to report. That is
deliberate: in a routine `script` step a non-zero exit means the step failed,
so "I found a red workflow" would abort the run instead of waking it. The wake
gate reads `wake` from the JSON. For shell/CI use pass `--exit-code` so the
state is visible in the exit status too.
"""
import argparse
import json
import os
import sys
import urllib.error
import urllib.request
from datetime import datetime, timedelta, timezone

# Conclusions we treat as a failure. `success` is fine, `skipped`/`neutral`
# too (the workflow legitimately skipped itself), None = still running.
FAILURE_CONCLUSIONS = {"failure", "timed_out", "cancelled", "startup_failure",
                       "action_required", "stale"}

# Hours without a run after which a daily workflow is suspicious. 48 h leaves
# room for one missed run (runner outage); two missed runs are a signal.
DEFAULT_MAX_STALE_HOURS = 48

API = "https://api.github.com"


def ts(s):
    """GitHub ISO8601 ('2026-08-06T03:50:00Z') -> aware datetime."""
    if not s:
        return None
    return datetime.fromisoformat(s.replace("Z", "+00:00"))


def gh_get(path, token):
    req = urllib.request.Request(
        f"{API}{path}",
        headers={
            "Accept": "application/vnd.github+json",
            "Authorization": f"Bearer {token}",
            "X-GitHub-Api-Version": "2022-11-28",
            "User-Agent": "crewship-ci-probe",
        },
    )
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.load(r)


def load_from_api(repo, token, branch):
    """Returns (workflows, runs) in the same shape the tests inject."""
    wf_data = gh_get(f"/repos/{repo}/actions/workflows?per_page=100", token)
    workflows, runs = [], []
    for w in wf_data.get("workflows", []):
        # Only workflows with a scheduled trigger: the others run on push and
        # the PR gate watches their health, not the night watch.
        if w.get("state") != "active":
            continue
        wid = w["id"]
        r = gh_get(
            f"/repos/{repo}/actions/workflows/{wid}/runs"
            f"?event=schedule&branch={branch}&per_page=5", token)
        wf_runs = r.get("workflow_runs", [])
        if not wf_runs:
            continue  # no scheduled run in history = no cron, not watched
        workflows.append({"name": w["name"], "cron": ""})
        for b in wf_runs:
            runs.append({
                "workflow": w["name"],
                "conclusion": b.get("conclusion"),
                "created_at": b.get("created_at"),
                "id": b.get("id"),
                "html_url": b.get("html_url"),
            })
    return workflows, runs


def assess(workflows, runs, now, max_stale):
    by_wf = {}
    for r in runs:
        by_wf.setdefault(r["workflow"], []).append(r)

    detail = []
    for w in workflows:
        name = w["name"]
        wf_runs = sorted(
            (b for b in by_wf.get(name, []) if b.get("created_at")),
            key=lambda b: ts(b["created_at"]),
        )
        latest = wf_runs[-1] if wf_runs else None

        if latest is None:
            detail.append({
                "workflow": name, "status": "STALE",
                "last_run": None, "run_id": None, "url": None,
                "reason": "no run in history — does this workflow have a cron at all?",
            })
            continue

        when = ts(latest["created_at"])
        age = now - when
        conclusion = latest.get("conclusion")
        row = {
            "workflow": name,
            "last_run": latest["created_at"],
            "run_id": latest.get("id"),
            "url": latest.get("html_url"),
            "hours_ago": round(age.total_seconds() / 3600, 1),
        }

        # Order is deliberate: a failed run is reported RED even when it is
        # old. Otherwise an old red would hide under the milder "stale".
        if conclusion in FAILURE_CONCLUSIONS:
            row.update({"status": "RED", "conclusion": conclusion,
                        "reason": f"last run ended `{conclusion}`"})
        elif conclusion is None:
            row.update({"status": "RUNNING", "conclusion": None,
                        "reason": "run has not finished yet"})
        elif age > max_stale:
            row.update({
                "status": "STALE", "conclusion": conclusion,
                "reason": f"{row['hours_ago']} h without a run "
                          f"(threshold {int(max_stale.total_seconds() // 3600)} h) — "
                          f"the cron is probably not firing",
            })
        else:
            row.update({"status": "OK", "conclusion": conclusion, "reason": None})
        detail.append(row)

    order = {"RED": 0, "STALE": 1, "RUNNING": 2, "OK": 3}
    detail.sort(key=lambda z: (order[z["status"]], z["workflow"]))

    red = sum(1 for z in detail if z["status"] == "RED")
    stale = sum(1 for z in detail if z["status"] == "STALE")
    out = {
        "checked_at": now.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "checked": len(workflows),
        "red": red,
        "stale": stale,
        "running": sum(1 for z in detail if z["status"] == "RUNNING"),
        "ok": sum(1 for z in detail if z["status"] == "OK"),
        "wake": red + stale > 0,
        "detail": detail,
    }
    out["panel"] = panel_for(out)
    return out


def panel_for(out):
    """status.v1-shaped summary: one state + label per watched failure class.

    Emitted here, not assembled in the routine, because a transform step can
    only project `.field` — it cannot compute "critical if red > 0". The probe
    is the one place that knows the rule, so it publishes the verdict."""
    def names(status):
        return ", ".join(z["workflow"] for z in out["detail"] if z["status"] == status)

    if out.get("error"):
        red_state, red_label = "critical", f"probe failed: {out['error']}"
        stale_state, stale_label = "critical", "not checked — probe failed"
    else:
        red_state = "critical" if out["red"] else "ok"
        red_label = (f"{out['red']} failed: {names('RED')}" if out["red"]
                     else "every scheduled workflow passed")
        stale_state = "warning" if out["stale"] else "ok"
        stale_label = (f"{out['stale']} not running: {names('STALE')}" if out["stale"]
                       else "every scheduled workflow ran on time")
    return {
        "red_state": red_state,
        "red_label": red_label,
        "stale_state": stale_state,
        "stale_label": stale_label,
        "checked_label": f"{out.get('checked', 0)} scheduled workflows checked at {out.get('checked_at', '?')}",
    }


def error_result(message, now):
    # The probe is a wake gate — when it fails itself it MUST NOT quietly
    # report "calm". wake=true makes the failure land in the inbox.
    out = {
        "checked_at": now.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "checked": 0, "red": 0, "stale": 0, "running": 0, "ok": 0,
        "wake": True, "error": message, "detail": [],
    }
    out["panel"] = panel_for(out)
    return out


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--repo", help="owner/name (reads GH_TOKEN from the environment)")
    ap.add_argument("--branch", default="main")
    ap.add_argument("--workflows-file", help="instead of the API: workflow list")
    ap.add_argument("--runs-file", help="instead of the API: run list")
    ap.add_argument("--now", help="ISO8601, tests only (default: now)")
    ap.add_argument("--max-stale-hours", type=int, default=DEFAULT_MAX_STALE_HOURS)
    ap.add_argument("--exit-code", action="store_true",
                    help="exit 1 when wake=true (shell/CI; NEVER in a routine step)")
    args = ap.parse_args()

    now = ts(args.now) if args.now else datetime.now(timezone.utc)

    if args.workflows_file and args.runs_file:
        with open(args.workflows_file, encoding="utf-8") as f:
            workflows = json.load(f)
        with open(args.runs_file, encoding="utf-8") as f:
            runs = json.load(f)
    elif args.repo:
        token = os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN")
        if not token:
            sys.exit("GH_TOKEN is missing from the environment")
        try:
            workflows, runs = load_from_api(args.repo, token, args.branch)
        except urllib.error.HTTPError as e:
            print(json.dumps(error_result(f"GitHub API {e.code}: {e.reason}", now),
                             ensure_ascii=False, indent=2))
            sys.exit(1 if args.exit_code else 0)
        except (urllib.error.URLError, OSError) as e:
            print(json.dumps(error_result(f"GitHub API unreachable: {e}", now),
                             ensure_ascii=False, indent=2))
            sys.exit(1 if args.exit_code else 0)
    else:
        sys.exit("pass --repo, or --workflows-file + --runs-file")

    out = assess(workflows, runs, now, timedelta(hours=args.max_stale_hours))
    out["repo"] = args.repo or "injected"
    print(json.dumps(out, ensure_ascii=False, indent=2))
    sys.exit(1 if (args.exit_code and out["wake"]) else 0)


if __name__ == "__main__":
    main()

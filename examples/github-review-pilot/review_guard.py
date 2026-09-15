#!/usr/bin/env python3
"""Bounded pilot checks. No network, secrets, shell, or execution of PR code."""
import json
import re
import sys


def require(condition, message):
    if not condition:
        raise ValueError(message)


def config_check(config):
    require(re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", config["repo"]), "invalid repository")
    for field in ("repository_id", "pr_number"):
        require(type(config[field]) is int and config[field] > 0, "invalid " + field)
    require(bool(config["base_ref"]), "missing base branch")
    require(re.fullmatch(r"[A-Za-z0-9-]+(?:\[bot\])?", config["reviewer_login"]), "invalid reviewer")


def snapshot(config, pr):
    config_check(config)
    require(pr["number"] == config["pr_number"] and pr["state"] == "open", "PR is not the permitted open PR")
    base = pr["base"]
    require(base["repo"]["id"] == config["repository_id"], "repository ID mismatch")
    require(base["repo"]["full_name"] == config["repo"], "repository name mismatch")
    require(base["ref"] == config["base_ref"], "base branch mismatch")
    # This pilot intentionally excludes fork PRs.
    require(pr["head"]["repo"]["id"] == config["repository_id"], "fork PR is outside this pilot")
    head, base_sha = pr["head"]["sha"], base["sha"]
    require(all(re.fullmatch(r"[0-9a-f]{40}", sha) for sha in (head, base_sha)), "invalid revision")
    return {"head": head, "base": base_sha, "number": config["pr_number"]}


def check_event(config, event):
    require(event["action"] in ("opened", "reopened", "synchronize"), "unsupported event action")
    require(event["repository"]["id"] == config["repository_id"], "event repository mismatch")
    require(event["repository"]["full_name"] == config["repo"], "event repository name mismatch")
    require(event["number"] == config["pr_number"], "event PR number mismatch")
    return snapshot(config, event["pull_request"])


def marker(config, snap):
    return "<!-- crewship-review-pilot:v1:%d:%d:%s -->" % (config["repository_id"], config["pr_number"], snap["head"])


def already_reviewed(config, snap, reviews):
    require(isinstance(reviews, list) and len(reviews) < 100, "review list needs pagination; pilot refuses partial history")
    return any(
        review.get("user", {}).get("login") == config["reviewer_login"]
        and review.get("commit_id") == snap["head"]
        and marker(config, snap) in (review.get("body") or "")
        for review in reviews
    )


def preflight(config, event, current, reviews):
    snap = check_event(config, event)
    require(snapshot(config, current) == snap, "stale webhook revision")
    return {**snap, "proceed": not already_reviewed(config, snap, reviews)}


def packet(config, snap, comparison):
    require(comparison["base_commit"]["sha"] == snap["base"], "comparison base mismatch")
    commits = comparison["commits"]
    require(bool(commits) and commits[-1]["sha"] == snap["head"], "comparison does not reach reviewed head")
    require(comparison["total_commits"] == len(commits), "comparison commit history is truncated")
    files = comparison["files"]
    require(0 < len(files) <= 20, "pilot accepts 1–20 text files only")
    selected = []
    for file in files:
        require(isinstance(file.get("patch"), str) and bool(file["patch"]), "missing text patch; cannot review binary/omitted diff")
        require(file["status"] in ("added", "modified", "removed", "renamed"), "unsupported file status")
        lines = file["patch"].splitlines()
        require(sum(line.startswith("+") for line in lines) == file["additions"]
                and sum(line.startswith("-") for line in lines) == file["deletions"],
                "patch is truncated or inconsistent with file change counts")
        selected.append({"path": file["filename"], "status": file["status"], "patch": file["patch"]})
    result = {"repository": config["repo"], "pr": snap["number"], "commit": snap["head"], "files": selected}
    require(len(json.dumps(result).encode()) <= 30000, "diff exceeds pilot review limit")
    return result


def publication(config, snap, current, reviews, review_text):
    require(snapshot(config, current) == {key: snap[key] for key in ("head", "base", "number")}, "PR changed during review")
    if already_reviewed(config, snap, reviews):
        return {"proceed": False}
    require(isinstance(review_text, str) and 1 <= len(review_text.strip()) <= 12000, "invalid review length")
    # Do not let model output generate user notifications or HTML comments.
    text = review_text.strip().replace("@", "＠").replace("<", "&lt;")
    body = (marker(config, snap) + "\n## Crewship pilot review\n\n"
            + "Reviewed commit: `" + snap["head"] + "`\n"
            + "Diff-only analysis; PR code was not executed. This is not a merge approval.\n\n" + text)
    return {"proceed": True, "request": {"commit_id": snap["head"], "event": "COMMENT", "body": body}}


def complete(config, snap, decision_raw, response_raw):
    if not snap["proceed"] or not json.loads(decision_raw)["proceed"]:
        outcome, summary = "NO_CHANGE", "Matching review already exists; nothing published"
    else:
        response = json.loads(response_raw)
        require(type(response.get("id")) is int and response["id"] > 0, "missing published review ID")
        require(response.get("commit_id") == snap["head"], "published review commit mismatch")
        require(response.get("state") == "COMMENTED", "published review is not COMMENTED")
        require(response.get("user", {}).get("login") == config["reviewer_login"], "publisher identity mismatch")
        require(marker(config, snap) in response.get("body", ""), "published review marker missing")
        outcome = "SUCCEEDED"
        summary = "Published https://github.com/%s/pull/%d#pullrequestreview-%d" % (config["repo"], config["pr_number"], response["id"])
    return "%s\n\n---CHECKPOINT---\ndone: %s\nnext_step: none\nconfidence: high\noutcome: %s\n---END CHECKPOINT---" % (summary, summary, outcome)


def main():
    config = json.loads(sys.argv[2])
    mode = sys.argv[1]
    if mode == "event":
        result = check_event(config, json.loads(sys.argv[3]))
    elif mode == "preflight":
        result = preflight(config, *(json.loads(arg) for arg in sys.argv[3:6]))
    elif mode == "packet":
        result = packet(config, *(json.loads(arg) for arg in sys.argv[3:5]))
    elif mode == "publication":
        result = publication(config, *(json.loads(arg) for arg in sys.argv[3:6]), sys.argv[6])
    elif mode == "complete":
        result = complete(config, json.loads(sys.argv[3]), sys.argv[4], sys.argv[5])
    else:
        raise ValueError("unknown guard mode")
    print(result if isinstance(result, str) else json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, IndexError):
        # Payloads are untrusted: never echo them, credentials, or a traceback.
        print("Review pilot refused invalid, stale, incomplete, or out-of-scope input", file=sys.stderr)
        sys.exit(1)

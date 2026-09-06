#!/usr/bin/env bash
# Wrapper for docs_drift.py: checks out the repository and runs every pair
# from config/docs_map.json. The output is one JSON document on stdout.
#
# It runs as a routine `script` step, so it MUST exit zero even when it finds
# drift — a non-zero exit would mean the step failed. The state is in
# `total_candidates`.
#
# EVERY exit path leaves through the SAME emitter: the shell guards below set
# FAIL_REASON and call `audit`, the scan itself calls `fail()`, and both end
# in `emit()` with a panel built by `panel_for()`. One place knows the panel
# shape, because the routine (routines_packs.go) projects .panel.state,
# .panel.label AND .panel.sha_label out of this document with `transform`
# steps — and a transform whose field is missing does not degrade, it fails
# the run and takes the agent review and both page panels with it.
#
# The repository is kept in /crew/shared/work (a persistent mount), so the
# second run fetches instead of cloning.
set -uo pipefail

SHARED="${SHARED_ROOT:-/crew/shared}"
REPO="${REPO:-crewship-ai/crewship}"
BRANCH="${BRANCH:-main}"
WORK="$SHARED/work/$(basename "$REPO")"
MAP="${MAP:-$SHARED/config/docs_map.json}"
DRIFT="$SHARED/scripts/docs_drift.py"
SHA=""
FAIL_REASON=""

# audit runs the scan — or, with FAIL_REASON set, emits the failure document
# through the same code path. The panel is built once, in python, for both.
audit() {
  MAP="$MAP" WORK="$WORK" DRIFT="$DRIFT" SHA="$SHA" REPO="$REPO" \
  FAIL_REASON="$FAIL_REASON" MAX_CANDIDATES="${MAX_CANDIDATES:-25}" python3 <<'PY'
import json, os, subprocess, sys

REPO = os.environ.get("REPO", "")
SHA = os.environ.get("SHA", "")


def panel_for(out):
    """status.v1-shaped panel: ONE function, every exit path.

    Emitted here rather than assembled in the routine because a transform
    step can only project `.field` — it cannot compute "critical when the
    scan died". The script is the one place that knows the rule, and this
    is the one place in the script that knows the panel's SHAPE: a failure
    panel missing a field the routine projects is not a graceful degrade,
    it is a hard failure of the whole audit."""
    if out.get("error"):
        state = "critical"
        label = "scan failed: {}".format(out["error"])
    else:
        results = out.get("results") or []
        broken = sum(1 for v in results if v.get("error"))
        if broken:
            state = "critical"
            label = f"{broken} of {len(results)} pairs could not be scanned"
        elif out.get("phantoms"):
            state = "warning"
            label = f"{out['phantoms']} phantom(s) and {out['gaps']} gap(s) across {len(results)} pairs"
        elif out.get("gaps"):
            state = "ok"
            label = f"no phantoms; {out['gaps']} gap candidate(s) across {len(results)} pairs"
        else:
            state = "ok"
            label = f"no drift candidate across {len(results)} pairs"
    # sha_label names what was actually read. Never an empty string (the page
    # would render a blank row) and never an invented SHA: a run that has no
    # commit says so.
    repo = out.get("repo") or "repository unknown"
    sha = out.get("sha") or ""
    if not sha or sha == "unknown":
        sha_label = f"{repo} @ not checked out" if out.get("error") else f"{repo} @ commit unknown"
    else:
        sha_label = f"{repo} @ {sha[:12]}"
    return {"state": state, "label": label, "sha_label": sha_label}


def emit(out):
    out["panel"] = panel_for(out)
    print(json.dumps(out, ensure_ascii=False, indent=2))
    sys.exit(0)


def fail(message):
    # A scan that cannot run says so in JSON and exits ZERO — a non-zero exit
    # would fail the routine step and leave the status panel silently stale.
    # The shell guards above reach this through FAIL_REASON, so there is only
    # one failure document in this file, not one per language.
    emit({"repo": REPO, "sha": SHA, "error": message, "pairs": 0,
          "total_candidates": 0, "phantoms": 0, "gaps": 0, "results": []})


reason = os.environ.get("FAIL_REASON", "")
if reason:
    fail(reason)

try:
    with open(os.environ["MAP"], encoding="utf-8") as f:
        doc_map = json.load(f)
except (OSError, ValueError) as e:
    fail(f"docs map is not valid JSON: {e}")
if not isinstance(doc_map, dict) or not isinstance(doc_map.get("pairs"), list):
    fail("docs map has no \"pairs\" list")
work, drift = os.environ["WORK"], os.environ["DRIFT"]
results = []

for i, pair in enumerate(doc_map["pairs"]):
    if not isinstance(pair, dict) or not pair.get("doc") or not isinstance(pair.get("pkg"), list) or not pair["pkg"]:
        results.append({"doc": str((pair or {}).get("doc", f"pairs[{i}]")) if isinstance(pair, dict) else f"pairs[{i}]",
                        "error": "map entry needs a \"doc\" and a non-empty \"pkg\" list",
                        "phantoms": [], "gaps": [], "drift": 0})
        continue
    doc = os.path.join(work, pair["doc"])
    if not os.path.exists(doc):
        results.append({"doc": pair["doc"], "error": "page does not exist in the repository",
                        "phantoms": [], "gaps": [], "drift": 0})
        continue
    cmd = [sys.executable, drift, "--doc", doc]
    missing_pkg = []
    for p in pair["pkg"]:
        full = os.path.join(work, p)
        if os.path.exists(full):
            cmd += ["--pkg", full]
        else:
            missing_pkg.append(p)
    if missing_pkg and len(missing_pkg) == len(pair["pkg"]):
        results.append({"doc": pair["doc"], "error": f"code does not exist: {missing_pkg}",
                        "phantoms": [], "gaps": [], "drift": 0})
        continue
    r = subprocess.run(cmd, capture_output=True, text=True)
    try:
        out = json.loads(r.stdout)
    except json.JSONDecodeError:
        results.append({"doc": pair["doc"], "error": (r.stderr or "")[:300],
                        "phantoms": [], "gaps": [], "drift": 0})
        continue
    # Shorten paths back to repo-relative so the report links are clickable.
    for g in out.get("gaps", []):
        g["source"] = g["source"].replace(work + "/", "")
    # Truncate so the report stays readable — but NEVER silently. How much was
    # dropped is in the output, so "we went through everything" cannot become
    # an accidental claim.
    cap = int(pair.get("max_candidates", os.environ.get("MAX_CANDIDATES", 25)))
    dropped = 0
    for field in ("phantoms", "gaps"):
        if len(out.get(field, [])) > cap:
            dropped += len(out[field]) - cap
            out[field] = out[field][:cap]
    if dropped:
        out["truncated"] = dropped
        out["truncated_note"] = (f"showing the first {cap} in each category, "
                                 f"{dropped} more not listed — narrow `pkg` in the map")
    out["doc"] = pair["doc"]
    out["packages"] = pair["pkg"]
    out["why"] = pair.get("why", "")
    if missing_pkg:
        out["note"] = f"part of the code does not exist: {missing_pkg}"
    results.append(out)

results.sort(key=lambda v: -v.get("drift", 0))
emit({
    "repo": REPO,
    "sha": SHA,
    "pairs": len(results),
    "total_candidates": sum(v.get("drift", 0) for v in results),
    "phantoms": sum(len(v.get("phantoms", [])) for v in results),
    "gaps": sum(len(v.get("gaps", [])) for v in results),
    "results": results,
})
PY
}

fail() {
  FAIL_REASON="$1"
  audit
  exit 0
}

# LOCAL_REPO skips git entirely — for a dry run against a checkout you have.
if [ -n "${LOCAL_REPO:-}" ]; then WORK="$LOCAL_REPO"; fi

[ -n "${LOCAL_REPO:-}" ] || [ -n "${GH_TOKEN:-}" ] || fail "GH_TOKEN is missing"
[ -f "$MAP" ]  || fail "docs map is missing: $MAP"
[ -f "$DRIFT" ] || fail "docs_drift.py is missing: $DRIFT"

# Clone / update. The token travels as a per-invocation HTTP header, NOT in
# the remote URL: a URL-embedded token is written into .git/config on the
# crew's shared volume, where every agent of the crew can read it (and quote
# it into a report). The header is never persisted.
AUTH_HEADER="AUTHORIZATION: basic $(printf 'x-access-token:%s' "${GH_TOKEN:-}" | base64 | tr -d '\n')"
GIT_AUTH=(-c "http.extraheader=${AUTH_HEADER}")
if [ -n "${LOCAL_REPO:-}" ]; then
  :
elif [ -d "$WORK/.git" ]; then
  git -C "$WORK" remote set-url origin "https://github.com/${REPO}.git" >/dev/null 2>&1
  git "${GIT_AUTH[@]}" -C "$WORK" fetch --quiet --depth 1 origin "$BRANCH" 2>/dev/null || fail "git fetch failed"
  git -C "$WORK" checkout --quiet FETCH_HEAD 2>/dev/null || fail "git checkout failed"
else
  mkdir -p "$(dirname "$WORK")"
  git "${GIT_AUTH[@]}" clone --quiet --depth 1 --branch "$BRANCH" \
    "https://github.com/${REPO}.git" "$WORK" 2>/dev/null \
    || fail "git clone failed"
fi
unset AUTH_HEADER

SHA="$(git -C "$WORK" rev-parse HEAD 2>/dev/null || echo unknown)"

audit

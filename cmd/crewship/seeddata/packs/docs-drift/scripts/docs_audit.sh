#!/usr/bin/env bash
# Wrapper for docs_drift.py: checks out the repository and runs every pair
# from config/docs_map.json. The output is one JSON document on stdout.
#
# It runs as a routine `script` step, so it MUST exit zero even when it finds
# drift — a non-zero exit would mean the step failed. The state is in
# `total_candidates`.
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

fail() {
  printf '{"error":%s,"pairs":0,"total_candidates":0,"results":[],"panel":{"state":"critical","label":%s}}\n' \
    "$(printf '%s' "$1" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')" \
    "$(printf 'scan failed: %s' "$1" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')"
  exit 0
}

# LOCAL_REPO skips git entirely — for a dry run against a checkout you have.
if [ -n "${LOCAL_REPO:-}" ]; then WORK="$LOCAL_REPO"; fi

[ -n "${LOCAL_REPO:-}" ] || [ -n "${GH_TOKEN:-}" ] || fail "GH_TOKEN is missing"
[ -f "$MAP" ]  || fail "docs map is missing: $MAP"
[ -f "$DRIFT" ] || fail "docs_drift.py is missing: $DRIFT"

# Clone / update. The token goes into git via the URL only; it never reaches
# the output.
if [ -n "${LOCAL_REPO:-}" ]; then
  :
elif [ -d "$WORK/.git" ]; then
  git -C "$WORK" remote set-url origin "https://x-access-token:${GH_TOKEN}@github.com/${REPO}.git" >/dev/null 2>&1
  git -C "$WORK" fetch --quiet --depth 1 origin "$BRANCH" 2>/dev/null || fail "git fetch failed"
  git -C "$WORK" checkout --quiet FETCH_HEAD 2>/dev/null || fail "git checkout failed"
else
  mkdir -p "$(dirname "$WORK")"
  git clone --quiet --depth 1 --branch "$BRANCH" \
    "https://x-access-token:${GH_TOKEN}@github.com/${REPO}.git" "$WORK" 2>/dev/null \
    || fail "git clone failed"
fi

SHA="$(git -C "$WORK" rev-parse HEAD 2>/dev/null || echo unknown)"

MAP="$MAP" WORK="$WORK" DRIFT="$DRIFT" SHA="$SHA" REPO="$REPO" \
MAX_CANDIDATES="${MAX_CANDIDATES:-25}" python3 <<'PY'
import json, os, subprocess, sys

doc_map = json.load(open(os.environ["MAP"], encoding="utf-8"))
work, drift = os.environ["WORK"], os.environ["DRIFT"]
results = []

for pair in doc_map.get("pairs", []):
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
total = sum(v.get("drift", 0) for v in results)
phantoms = sum(len(v.get("phantoms", [])) for v in results)
gaps = sum(len(v.get("gaps", [])) for v in results)
errors = sum(1 for v in results if v.get("error"))
if errors:
    state, label = "critical", f"{errors} of {len(results)} pairs could not be scanned"
elif phantoms:
    state, label = "warning", f"{phantoms} phantom(s) and {gaps} gap(s) across {len(results)} pairs"
elif gaps:
    state, label = "ok", f"no phantoms; {gaps} gap candidate(s) across {len(results)} pairs"
else:
    state, label = "ok", f"no drift candidate across {len(results)} pairs"
print(json.dumps({
    "repo": os.environ["REPO"], "sha": os.environ["SHA"],
    "pairs": len(results),
    "total_candidates": total,
    "phantoms": phantoms,
    "gaps": gaps,
    "results": results,
    "panel": {"state": state, "label": label,
              "sha_label": f"{os.environ['REPO']} @ {os.environ['SHA'][:12]}"},
}, ensure_ascii=False, indent=2))
PY

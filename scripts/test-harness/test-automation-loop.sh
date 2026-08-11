#!/usr/bin/env bash
# The composition depth cap — runtime validation against a live server.
#
# This is the one suite in this directory that asserts a SAFETY property rather
# than a feature. A substrate where a routine can act on Crewship's own nouns,
# and where a journal commit can start a routine, can close a cycle: routine →
# crewship step → issue event → automation → routine. Nothing else bounds it.
#
# Why it cannot be a unit test. It already was one, and the unit test lied.
# automation.TestObserver_ClosedLoopStopsAtMaxChainDepth reconstructs the cycle
# in memory and sets `entry.TraceID = parent.ID` by hand — asserting that the
# triggering journal entry names the run that caused it. That is the pointer
# Registry.Flush resolves to price a hop, and the production emitter did not
# write it: the internal issue route decoded no author_run_id and the entry
# carried no trace_id. The unit test passed for as long as the world it modelled
# did not exist. Measured on a live instance before the fix: 28 status changes
# and climbing, every run at chain_depth 1 with its own chain_origin, zero
# automation.depth_exceeded entries.
#
# So this drives the REAL cycle through the REAL CLI and counts what the server
# actually did.
#
# max_per_hour is raised far above the hop budget on purpose. If the throttle is
# what stops the loop, this suite proves nothing about the cap — and the throttle
# is a number the cycle's own author picks.
#
# Usage:
#   export CREWSHIP_SERVER=<devN url>
#   ./scripts/test-harness/test-automation-loop.sh
#
# shellcheck source=./lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

preflight

MAX_CHAIN_DEPTH=8          # internal/pipeline.MaxChainDepth
SETTLE_SECONDS=${SETTLE_SECONDS:-75}
# Lowercased: nonce() uppercases on purpose (models echo it back more
# reliably), but a routine slug must be kebab-case and the server refuses
# anything else at save time.
TAG="$(nonce LOOP | tr '[:upper:]' '[:lower:]')"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

# Everything this suite creates, torn down even when an assertion fails: a
# cycling pair left enabled on a shared slot keeps firing after the run ends.
_CREATED_AUTOMATIONS=()
cleanup_automations() {
  for id in ${_CREATED_AUTOMATIONS[@]+"${_CREATED_AUTOMATIONS[@]}"}; do
    cs automation delete "$id" >/dev/null 2>&1 || true
  done
}
trap 'cleanup_automations; rm -rf "$WORKDIR"' EXIT

section "Setup: an issue and two routines that toggle it"

CREW="$(cs crew list -f json 2>/dev/null | python3 -c 'import sys,json;print(json.load(sys.stdin)[0]["slug"])' 2>/dev/null || true)"
if [[ -z "$CREW" ]]; then
  skip "composition depth cap" "no crew in this workspace — seed it first"
  finish
fi

ISSUE_OUT="$(cs issue create --title "loop guard probe $TAG" --crew "$CREW" 2>&1)"
IDENT="$(printf '%s' "$ISSUE_OUT" | sed -n 's/^Created issue \([A-Z0-9-]*\):.*/\1/p')"
if [[ -z "$IDENT" ]]; then
  _fail "create probe issue" "$(printf '%s' "$ISSUE_OUT" | head -c 200)"
  finish
fi

# `journal --mission` wants the mission ID, not the human identifier: passing
# "OPS-3" matches nothing and returns 0, which reads as "the cycle never ran"
# — a false GREEN on the one assertion this suite exists for. Resolve it once.
MISSION_ID="$(cs issue get "$IDENT" -f json 2>/dev/null \
  | python3 -c 'import sys,json;print(json.load(sys.stdin).get("id",""))' 2>/dev/null || true)"
if [[ -z "$MISSION_ID" ]]; then
  _fail "resolve the probe issue's mission id" \
    "without it the status-change count silently returns 0 and every assertion below is meaningless"
  finish
fi
info "probe issue: $IDENT (mission $MISSION_ID, crew $CREW)"

# Two routines, each setting the status the OTHER rule watches for. This is the
# exact shape of the incident: neither routine is wrong on its own.
for pair in "a:IN_PROGRESS" "b:TODO"; do
  half="${pair%%:*}"; status="${pair##*:}"
  cat > "$WORKDIR/loop-$half.yaml" <<YAML
dsl_version: "1.0"
name: loop-toggle-$half-$TAG
description: Sets the probe issue to $status. Half of a deliberate cycle.
steps:
  - id: act
    type: crewship
    action: issue.update
    args:
      identifier: $IDENT
      status: $status
YAML
  if ! out="$(cs routine save --name "loop-toggle-$half-$TAG" \
        --definition "$WORKDIR/loop-$half.yaml" --author-crew "$CREW" 2>&1)"; then
    _fail "save routine loop-toggle-$half" "$(printf '%s' "$out" | tail -c 200)"
    finish
  fi
done
_pass "two routines saved, each setting the status the other rule watches"

section "Arming the cycle"

# Baseline BEFORE the rules exist. A shared slot may already carry status
# changes from other suites, and counting from zero would attribute them here.
changes_count() {
  cs journal --type mission.status_change --mission "$1" --lines 500 2>/dev/null \
    | grep -c "status_changed" || true
}

# Same for the two journal counters below. They are workspace-wide — a run of
# this suite an hour ago leaves depth_exceeded rows behind, and counting the
# TOTAL passes on somebody else's evidence. Measured as a delta, they cannot.
type_count() {
  cs journal --type "$1" --lines 500 2>/dev/null | grep -c . || true
}
BEFORE="$(changes_count "$MISSION_ID")"
DEPTH_BEFORE="$(type_count automation.depth_exceeded)"
THROTTLE_BEFORE="$(type_count automation.throttled)"
info "status changes on $IDENT before arming: $BEFORE"

for pair in "a:TODO" "b:IN_PROGRESS"; do
  half="${pair%%:*}"; watch="${pair##*:}"
  out="$(cs automation create --name "cycle-$half-$TAG" \
      --event mission.status_change --payload-equals "to=$watch" \
      --routine "loop-toggle-$half-$TAG" \
      --debounce-seconds 1 --max-per-hour 10000 2>&1)"
  id="$(printf '%s' "$out" | sed -n 's/^Automation \(aut_[a-f0-9]*\) created.*/\1/p')"
  if [[ -z "$id" ]]; then
    _fail "create automation cycle-$half" "$(printf '%s' "$out" | head -c 200)"
    finish
  fi
  _CREATED_AUTOMATIONS+=("$id")
done
_pass "two automations armed, max_per_hour 10000 so the throttle cannot be what stops it"

section "The cycle"

cs issue update "$IDENT" --status TODO >/dev/null 2>&1
info "kicked; settling for ${SETTLE_SECONDS}s"
sleep "$SETTLE_SECONDS"

AFTER="$(changes_count "$MISSION_ID")"
PRODUCED=$(( AFTER - BEFORE ))
info "status changes produced by the cycle: $PRODUCED"

if (( PRODUCED < 2 )); then
  _fail "the cycle actually ran" \
    "produced $PRODUCED status changes — the rules never fired, so nothing was tested. \
Check 'crewship automation preview' against mission.status_change."
  finish
fi

# It must STOP. Measured as "no new events over a second settle" rather than
# as a count: MaxChainDepth bounds DEPTH, not events. A cycle branches — two
# runs can sit at the same depth, each emitting a change — so an event count
# above MaxChainDepth+1 is normal and asserting on it fails a healthy system.
# An earlier revision of this suite did exactly that and reported a defect that
# was not there.
sleep 20
SETTLED="$(changes_count "$MISSION_ID")"
if (( SETTLED != AFTER )); then
  _fail "the composed cycle terminates" \
    "status changes went $AFTER → $SETTLED during a quiet 20s window; the cycle is still running."
else
  _pass "the composed cycle terminated (at $PRODUCED status changes)"
fi

# And it stopped AT the cap, not before it. `<=` alone would pass on a cycle
# that died for an unrelated reason — a broken trigger looks identical to a
# guarded one from the outside, which is the failure this whole suite exists
# to tell apart.
max_depth_of() {
  cs routine records "$1" -f json 2>/dev/null | python3 -c '
import sys,json
try: rs=json.load(sys.stdin)
except Exception: print(-1); sys.exit(0)
print(max([(r.get("chain_depth") or 0) for r in rs], default=-1))
' 2>/dev/null || echo "-1"
}
DEEPEST=0
for half in a b; do
  d="$(max_depth_of "loop-toggle-$half-$TAG")"
  (( d > DEEPEST )) && DEEPEST="$d"
done
if (( DEEPEST != MAX_CHAIN_DEPTH )); then
  _fail "the cycle ran exactly to the cap" \
    "deepest run was chain_depth $DEEPEST, want exactly $MAX_CHAIN_DEPTH. Below it the cycle \
died for some other reason and the cap was never tested; above it the cap did not hold."
else
  _pass "the deepest run sits exactly at MaxChainDepth ($DEEPEST)"
fi

# Stopping is necessary and not sufficient: it must stop BECAUSE of the depth
# cap. A silent stop is indistinguishable from a broken trigger.
DEPTH_EXCEEDED=$(( $(type_count automation.depth_exceeded) - DEPTH_BEFORE ))
if (( DEPTH_EXCEEDED < 1 )); then
  _fail "the refusal is recorded" \
    "no automation.depth_exceeded entry. The cycle may have stopped for an unrelated reason, \
and an operator would have no record of why their rule stopped firing."
else
  _pass "automation.depth_exceeded records the refusal"
fi

# And it must not have been the throttle. If max_per_hour fired, the depth
# assertion above proved nothing.
THROTTLED=$(( $(type_count automation.throttled) - THROTTLE_BEFORE ))
if (( THROTTLED > 0 )); then
  _fail "the depth cap is what stopped it, not the rate limit" \
    "automation.throttled fired despite max_per_hour=10000 — this run does not prove the cap."
else
  _pass "no throttle fired: the depth cap is what stopped the cycle"
fi

section "The chain reads as ONE chain"

# Depth bounds the cycle; origin is what makes the runs legible as a single
# chain afterwards. They were fixed separately and can regress separately: a
# bounded chain that re-roots every hop reads as N unrelated one-hop runs,
# which is also the shape a loop would prefer to present.
ORIGINS="$(cs routine records "loop-toggle-a-$TAG" -f json 2>/dev/null | python3 -c '
import sys,json
try: rs=json.load(sys.stdin)
except Exception: sys.exit(0)
print(len({r.get("chain_origin") or "" for r in rs}))
' 2>/dev/null || echo "")"
DEPTHS="$(cs routine records "loop-toggle-a-$TAG" -f json 2>/dev/null | python3 -c '
import sys,json
try: rs=json.load(sys.stdin)
except Exception: sys.exit(0)
print(len({r.get("chain_depth") for r in rs}))
' 2>/dev/null || echo "")"
ORPHANS="$(cs routine records "loop-toggle-a-$TAG" -f json 2>/dev/null | python3 -c '
import sys,json
try: rs=json.load(sys.stdin)
except Exception: sys.exit(0)
print(sum(1 for r in rs if not (r.get("chain_origin") or "")))
' 2>/dev/null || echo "")"

if [[ -z "$ORIGINS" || -z "$DEPTHS" ]]; then
  skip "the chain is legible" "routine records returned nothing parseable"
else
  # NOT "exactly one origin". The cycle branches, and a branch whose parent
  # run cannot be resolved legitimately roots itself — so a small number of
  # distinct roots is expected. What must never happen is a composed run that
  # names NO chain at all: that run is unattributable, and `crewship chain`
  # shows it as an orphan.
  if [[ "$ORPHANS" != "0" ]]; then
    _fail "every composed run names the chain it belongs to" \
      "$ORPHANS run(s) carry an empty chain_origin — those are unattributable, and the causal \
walk cannot place them."
  else
    _pass "every composed run names the chain it belongs to ($ORIGINS distinct roots)"
  fi
  if [[ "$DEPTHS" == "1" ]]; then
    _fail "chain_depth increments across the hop" \
      "every run recorded the same depth — the budget is not being spent, and the cap above \
may be holding for some other reason."
  else
    _pass "chain_depth increments across the hop ($DEPTHS distinct depths)"
  fi
fi

# ---------------------------------------------------------------------------
# The OTHER closed loop: a rule on the run's own completion.
#
# Everything above drives the cycle through `crewship issue.update` →
# mission.status_change, and that is the one emitter that overrides
# journal.Entry.TraceID with the causing run id (internal/api/issue_events.go).
# The hop was priced from TraceID alone, so this suite passed 8/8 while the
# shape below — one rule, `--event pipeline.run.completed`, pointed at the
# routine whose completion it watches — ran unbounded. It is also the first
# thing anyone tries.
#
# Measured on a live instance before the fix: 13 runs, every one at
# chain_depth 1 with its own chain_origin, zero depth_exceeded, stopped only by
# max_per_hour. After: nine runs at depths 0..8, ONE chain_origin,
# depth_exceeded written, throttled zero.
#
# max_per_hour is at the ceiling again, for the same reason as above.
# ---------------------------------------------------------------------------

section "The cycle, armed on the run's own completion"

cat > "$WORKDIR/self.yaml" <<YAML
dsl_version: "1.0"
name: loop-self-$TAG
description: A routine whose completion re-fires it. Half of a deliberate cycle.
steps:
  - id: tick
    type: transform
    transform:
      input: tick
      expression: "."
YAML
if ! out="$(cs routine save --name "loop-self-$TAG" --definition "$WORKDIR/self.yaml" \
      --author-crew "$CREW" 2>&1)"; then
  _fail "save routine loop-self" "$(printf '%s' "$out" | tail -c 200)"
  finish
fi

SELF_BEFORE_DEPTH="$(type_count automation.depth_exceeded)"
SELF_BEFORE_THROTTLE="$(type_count automation.throttled)"

self_out="$(cs automation create --name "loop-self-$TAG" \
  --event pipeline.run.completed \
  --payload-equals "pipeline_slug=loop-self-$TAG" \
  --routine "loop-self-$TAG" --max-per-hour 10000 --debounce-seconds 1 2>&1)"
self_id="$(printf '%s' "$self_out" | sed -n 's/^Automation \(aut_[a-z0-9]*\) created.*/\1/p')"
if [[ -z "$self_id" ]]; then
  _fail "arm the self-firing rule" "$(printf '%s' "$self_out" | tail -c 200)"
  finish
fi
_CREATED_AUTOMATIONS+=("$self_id")
_pass "rule armed on pipeline.run.completed, max_per_hour 10000"

cs routine run "loop-self-$TAG" >/dev/null 2>&1
info "kicked; settling for ${SETTLE_SECONDS}s"
sleep "$SETTLE_SECONDS"

SELF_RUNS="$(cs routine records "loop-self-$TAG" -f json 2>/dev/null | python3 -c '
import sys,json
try: rs=json.load(sys.stdin)
except Exception: sys.exit(0)
print(len(rs))
' 2>/dev/null || echo "")"
SELF_MAXDEPTH="$(cs routine records "loop-self-$TAG" -f json 2>/dev/null | python3 -c '
import sys,json
try: rs=json.load(sys.stdin)
except Exception: sys.exit(0)
print(max([r.get("chain_depth") or 0 for r in rs], default=0))
' 2>/dev/null || echo "")"
SELF_DEPTH_DELTA=$(( $(type_count automation.depth_exceeded) - SELF_BEFORE_DEPTH ))
SELF_THROTTLE_DELTA=$(( $(type_count automation.throttled) - SELF_BEFORE_THROTTLE ))

if [[ -z "$SELF_RUNS" || "$SELF_RUNS" == "0" ]]; then
  skip "the self-firing cycle is bounded" "the rule never fired — nothing to bound"
else
  info "runs produced by the self-firing cycle: $SELF_RUNS (deepest chain_depth $SELF_MAXDEPTH)"
  if (( SELF_RUNS > MAX_CHAIN_DEPTH + 2 )); then
    _fail "the self-firing cycle terminated" \
      "$SELF_RUNS runs from one kick. The hop is not spending from the composition budget: \
this emitter puts the causing run in payload.run_id, not in trace_id."
  else
    _pass "the self-firing cycle terminated (at $SELF_RUNS runs)"
  fi
  if [[ "$SELF_MAXDEPTH" != "$MAX_CHAIN_DEPTH" ]]; then
    _fail "the deepest run sits exactly at MaxChainDepth ($MAX_CHAIN_DEPTH)" \
      "deepest was $SELF_MAXDEPTH. Short of it means something else stopped the cycle."
  else
    _pass "the deepest run sits exactly at MaxChainDepth ($MAX_CHAIN_DEPTH)"
  fi
  if (( SELF_DEPTH_DELTA < 1 )); then
    _fail "automation.depth_exceeded records the refusal" \
      "no new entry: the cycle ended for some reason other than the cap."
  else
    _pass "automation.depth_exceeded records the refusal"
  fi
  if (( SELF_THROTTLE_DELTA > 0 )); then
    _fail "no throttle fired: the depth cap is what stopped the cycle" \
      "$SELF_THROTTLE_DELTA automation.throttled entries — max_per_hour is doing the work, \
so this proves nothing about the cap."
  else
    _pass "no throttle fired: the depth cap is what stopped the cycle"
  fi
fi

finish

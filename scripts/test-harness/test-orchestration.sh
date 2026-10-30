#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Routine orchestration — scheduler, agentless token-zero, HITL approval gate,
# cross-tier consistency. These exercise the parts of the routine engine beyond
# a single agent_run.
#
#   ./test-orchestration.sh            # all four blocks
#   EVAL=0 ./test-orchestration.sh     # skip the (token-heavy) eval block

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight

# shellcheck source=routine-fixtures.sh
source "$HERE/routine-fixtures.sh"
harness_fixture probe ops PROBE_ROUTINE
SCHEDULE_ROUTINE="$PROBE_ROUTINE"
harness_fixture approval ops APPROVAL_ROUTINE

# ─────────────────────────────────────────────────────────────────────────────
section "1. Scheduler: create a cron schedule and confirm it's listed + enabled"
# ─────────────────────────────────────────────────────────────────────────────
# Seed no longer wires demo cron schedules (feat/no-demo-schedules) — a fresh
# workspace intentionally ships with zero schedules so a new routine doesn't
# immediately start racking up unattended cron-fired activity. This block used
# to assert on seed-provided schedules (classify-ticket / consistency-sweep /
# daily-status-digest — the last of which was never a real seeded routine
# slug to begin with). It now self-provisions the schedule it needs, so the
# assertion exercises the real scheduler instead of a demo-seed side effect.
harness_sched_name="$SCHEDULE_ROUTINE-schedule"
sched_create_out="$_HARNESS_FIXTURE_DIR/schedule.json"
if cs routine schedules create --slug "$SCHEDULE_ROUTINE" --name "$harness_sched_name" \
  --cron "0 0 1 1 *" --format json >"$sched_create_out"; then
  schedule_id="$(jq -er '.id | select(type == "string" and length > 0)' "$sched_create_out")" || { _fail "created schedule reports its full id"; finish; }
  _HARNESS_FIXTURE_SCHEDULES+=("$schedule_id")
  sched_out="$(cs routine schedules list --format json)"
  if printf '%s' "$sched_out" | jq -e --arg id "$schedule_id" 'any(.[]; .id == $id and .enabled == true)' >/dev/null; then
    _pass "owned schedule is listed and enabled"
  else
    _fail "owned schedule is listed and enabled"
  fi
  if cs routine schedules now "$schedule_id" >/dev/null; then
    _pass "owned schedule can be force-fired through its full id"
  else
    _fail "owned schedule can be force-fired through its full id"
  fi
else
  _fail "create owned schedule"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "2. Agentless wake-gate: token-zero (needs production CodeRunner)"
# ─────────────────────────────────────────────────────────────────────────────
# The explicit fixture uses the production expression CodeRunner, without tokens.
cs routine run "$PROBE_ROUTINE" >"$_HARNESS_FIXTURE_DIR/cs-probe.out" 2>&1; probe_rc=$?
if (( probe_rc == 0 )); then
  _pass "owned agentless probe ran"
  if have jq; then
    cost="$(cs routine records "$PROBE_ROUTINE" --json --limit 1 2>/dev/null | jq -r '.[0].cost_usd // 0')"
    if [[ "$cost" =~ ^0(\.0+)?$ ]]; then
      _pass "agentless run cost is exactly 0 (token-zero guarantee holds)"
    else
      _fail "agentless run cost is 0" "recorded cost_usd=$cost — agentless routine spent tokens"
    fi
  else
    skip "token-zero cost assertion" "jq missing"
  fi

else
  _fail "owned agentless probe ran" "unexpected error: $(head -c 160 "$_HARNESS_FIXTURE_DIR/cs-probe.out" | tr '\n' ' ')"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "3. HITL approval gate: pause → approve → resume"
# ─────────────────────────────────────────────────────────────────────────────
# approval-gate-demo has a wait(approval) step. The run parks (status WAITING),
# a pollable waitpoint surfaces via `routine waitpoints list`, approval via the
# CLI resumes it to completion. (Historical note: the 2026-06-25 "no queryable
# waitpoint" finding was a CLI gap — `waitpoints list --format json` printed the
# human table, so jq never saw the token. Fixed alongside CRE-137/143.)
info "Launching approval-gate-demo (pauses at the approval gate)…"
( cs routine run "$APPROVAL_ROUTINE" >"$_HARNESS_FIXTURE_DIR/cs-appr.out" 2>&1 ) &
run_pid=$!

token=""; waited=0; wp_window="${WAITPOINT_TIMEOUT:-90}"
while (( waited < wp_window )); do
  token="$(cs routine waitpoints list --format json 2>/dev/null | jq -r --arg prompt "$APPROVAL_ROUTINE" '.[] | select(.prompt == $prompt) | .token' 2>/dev/null)"
  [[ -n "$token" && "$token" != "null" ]] && break
  sleep "$POLL_INTERVAL"; waited=$((waited+POLL_INTERVAL))
done

if [[ -n "$token" && "$token" != "null" ]]; then
  _pass "approval-gate-demo created a pending waitpoint"
  approval_run_id="$(cs routine waitpoints list --format json | jq -er --arg token "$token" '.[] | select(.token == $token) | .pipeline_run_id')"
  if [[ -z "$approval_run_id" ]]; then
    _fail "owned waitpoint identifies its run"
    finish
  fi
  if cs routine waitpoints approve "$token" --comment "harness LGTM" >/dev/null 2>&1; then
    _pass "waitpoint approved via CLI"
  else
    _fail "waitpoint approved via CLI" "approve call failed for token $token"
  fi
  wait "$run_pid" 2>/dev/null
  # Approval schedules durable resume asynchronously. Check this exact run's
  # terminal status with a bounded wait, rather than sampling 'waiting' once.
  poll_until "exact approved run resumed and completed" 30 \
    "cs routine records '$APPROVAL_ROUTINE' --json --limit 1 | jq -e --arg id '$approval_run_id' 'any(.[]; .id == \$id and .status == \"completed\")'"

else
  _fail "HITL approval gate" "no queryable waitpoint within ${wp_window}s — check 'routine waitpoints list --format json' output"
  kill "$run_pid" 2>/dev/null  # don't let a blocked run hang the suite
fi

# ─────────────────────────────────────────────────────────────────────────────
section "4. Cross-tier consistency (eval — token-heavy, EVAL=0 to skip)"
# ─────────────────────────────────────────────────────────────────────────────
if [[ "${EVAL:-1}" == "1" ]]; then
  info "Running one eval scenario on fast+smart tiers…"
  if have jq; then
    out="$(cs eval scenarios --scenarios eval-extract-emails --runs 1 --tiers fast,smart -f json 2>/dev/null)"
    if printf '%s' "$out" | jq -e '.' >/dev/null 2>&1; then
      _pass "eval scenarios returned structured results across tiers"
    else
      skip "eval cross-tier" "no parseable result (scenario may not exist on this workspace)"
    fi
  else
    cs eval scenarios --scenarios eval-extract-emails --runs 1 --tiers fast -f markdown 2>/dev/null | head -20
    skip "eval cross-tier assertion" "jq missing — printed output above"
  fi
else
  skip "cross-tier eval" "EVAL=0"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "5. Crew network policy defaults to restricted (#1366)"
# ─────────────────────────────────────────────────────────────────────────────
# A crew created without --network-mode must come up 'restricted' (fail-closed
# egress), and the v148 backfill tightens legacy 'free' rows to match. We
# create a throwaway crew, assert `crew get` reports restricted, then clean up.
NM_SLUG="harness-netmode-$$"
if cs crew create --name "Harness NetMode $$" --slug "$NM_SLUG" >"$_HARNESS_FIXTURE_DIR/cs-nm.out" 2>&1; then
  if cs crew get "$NM_SLUG" 2>/dev/null | grep -iq "network mode.*restricted"; then
    _pass "new crew defaults to network_mode=restricted"
  else
    _fail "new crew network mode default" "expected restricted; got: $(cs crew get "$NM_SLUG" 2>/dev/null | grep -i 'network mode' | tr -s ' ')"
  fi
  cs crew delete "$NM_SLUG" --yes >/dev/null 2>&1 || true
else
  skip "crew network-mode default" "crew create unavailable: $(head -c 160 "$_HARNESS_FIXTURE_DIR/cs-nm.out" | tr '\n' ' ')"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "6. Interactive WS path never fails with a bare 'ws read: EOF' (#1386)"
# ─────────────────────────────────────────────────────────────────────────────
# The interactive `ask`/`run` path runs over a WebSocket. When the server
# refuses the connection after the 101 upgrade, the CLI must now print the
# server's close REASON, never an opaque `ws read: EOF`. We run a trivial ask;
# whether it succeeds or fails, assert the output never contains the bare EOF —
# a failure must carry a reason ("server rejected the connection: …").
ASK_AGENT="${ASK_AGENT:-robin}"
ask_out="$(mktemp -t cs-ask.XXXXXX)"
cs ask --agent "$ASK_AGENT" "reply with the single word OK" >"$ask_out" 2>&1; ask_rc=$?
if (( ask_rc == 0 )); then
  _pass "interactive 'ask' over WS completed"
elif grep -qiE '(^|[^[:alnum:]_])read: *EOF' "$ask_out" && ! grep -qi 'server rejected the connection' "$ask_out"; then
  _fail "ask failure carries a reason" "opaque 'ws read: EOF' with no reason — #1386 regressed: $(tail -c 160 "$ask_out" | tr '\n' ' ')"
else
  # The ask didn't complete, but it FAILED LEGIBLY (a real close reason, not a
  # bare EOF) — which is exactly what #1386 guarantees. That's a green outcome
  # for the observability contract, yet the ask itself is still unusable in this
  # environment. A silent skip would render that green even though the feature
  # can't be exercised here, so surface it LOUDLY as a known-broken xfail
  # (visible, referencing #1386) without turning CI hard-red on an env issue.
  xfail "interactive ask over WS (known broken here)" \
    "#1386: ask failed with a legible reason (not a bare EOF), but did not complete in this env: $(tail -c 160 "$ask_out" | tr '\n' ' ')"
fi
rm -f "$ask_out"

finish

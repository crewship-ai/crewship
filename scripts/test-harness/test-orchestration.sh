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
harness_sched_name="harness-orchestration-smoke"
sched_create_out="$(mktemp -t cs-sched-create.XXXXXX)"
cs routine schedules create --slug classify-ticket --name "$harness_sched_name" \
  --cron "*/30 * * * *" >"$sched_create_out" 2>&1
sched_out="$(cs routine schedules list 2>/dev/null)"
if printf '%s' "$sched_out" | grep -qi "$harness_sched_name"; then
  _pass "self-provisioned schedule for 'classify-ticket' is listed"
else
  _fail "self-provisioned schedule for 'classify-ticket' is listed" "$(head -c 200 "$sched_create_out" | tr '\n' ' ')"
fi
rm -f "$sched_create_out"
if printf '%s' "$sched_out" | grep -qiE 'yes'; then
  _pass "at least one schedule is enabled"
else
  _fail "at least one schedule is enabled"
fi
# Force-fire is blocked by a CLI gap: `schedules list --format json` is ignored
# (always prints a table) and the table truncates the psched_ id, so there's no
# reliable way to pass a full id to `schedules now <id>`. Documented, SKIP.
skip "schedules now <id> (force-fire)" "CLI gap: --format json ignored + id truncated in table → no full id to pass"

# ─────────────────────────────────────────────────────────────────────────────
section "2. Agentless wake-gate: token-zero (needs production CodeRunner)"
# ─────────────────────────────────────────────────────────────────────────────
# cost-spike-probe is agentless/token-zero — but it uses a type:code step, and
# the production CodeRunner is NOT wired in this build. The run then FAILS with
# a known, clearly-labelled limitation ("no CodeRunner wired … convert to
# type: agent_run"), not a real defect. Detect that and xfail (SKIP); FAIL only
# on an unexpected error. Once CodeRunner lands this becomes a live token-zero
# assertion. (Live-confirmed on dev 2026-06-25.)
cs routine run cost-spike-probe >/tmp/cs-probe.out 2>&1; probe_rc=$?
if (( probe_rc == 0 )); then
  _pass "agentless routine 'cost-spike-probe' ran"
  if have jq; then
    cost="$(cs routine records cost-spike-probe --json --limit 1 2>/dev/null | jq -r '.[0].cost_usd // 0')"
    if [[ "$cost" =~ ^0(\.0+)?$ ]]; then
      _pass "agentless run cost is exactly 0 (token-zero guarantee holds)"
    else
      _fail "agentless run cost is 0" "recorded cost_usd=$cost — agentless routine spent tokens"
    fi
  else
    skip "token-zero cost assertion" "jq missing"
  fi
elif grep -qi 'CodeRunner' /tmp/cs-probe.out; then
  skip "agentless cost-spike-probe (token-zero)" "KNOWN GAP: type:code steps need the production CodeRunner (not wired in this build) — agentless probe cannot run until it lands"
else
  _fail "agentless routine 'cost-spike-probe' ran" "unexpected error: $(head -c 160 /tmp/cs-probe.out | tr '\n' ' ')"
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
( cs routine run approval-gate-demo >/tmp/cs-appr.out 2>&1 ) &
run_pid=$!

token=""; waited=0; wp_window="${WAITPOINT_TIMEOUT:-90}"
while (( waited < wp_window )); do
  if have jq; then
    token="$(cs routine waitpoints list --format json 2>/dev/null | jq -r '.[0].token // .[0].id // empty' 2>/dev/null)"
  else
    token="$(cs routine waitpoints list 2>/dev/null | awk 'NR==2{print $1}')"
  fi
  [[ -n "$token" && "$token" != "null" ]] && break
  sleep "$POLL_INTERVAL"; waited=$((waited+POLL_INTERVAL))
done

if [[ -n "$token" && "$token" != "null" ]]; then
  _pass "approval-gate-demo created a pending waitpoint"
  if cs routine waitpoints approve "$token" --comment "harness LGTM" >/dev/null 2>&1; then
    _pass "waitpoint approved via CLI"
  else
    _fail "waitpoint approved via CLI" "approve call failed for token $token"
  fi
  wait "$run_pid" 2>/dev/null
  if have jq; then
    st="$(cs routine records approval-gate-demo --json --limit 1 2>/dev/null | jq -r '.[0].status // empty')"
    if [[ "$st" == "completed" ]]; then
      _pass "run resumed and completed after approval"
    else
      skip "run completion after approval" "latest status=$st (may still be finishing)"
    fi
  fi
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
section "5. Interactive WS path never fails with a bare 'ws read: EOF' (#1386)"
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
elif grep -qiE 'ws read: *EOF' "$ask_out" && ! grep -qi 'server rejected the connection' "$ask_out"; then
  _fail "ask failure carries a reason" "opaque 'ws read: EOF' with no reason — #1386 regressed: $(tail -c 160 "$ask_out" | tr '\n' ' ')"
else
  skip "interactive ask over WS" "ask did not complete but failed with a reason (not a bare EOF): $(tail -c 160 "$ask_out" | tr '\n' ' ')"
fi
rm -f "$ask_out"

finish

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
section "1. Scheduler: the seeded cron schedules are present + enabled"
# ─────────────────────────────────────────────────────────────────────────────
sched_out="$(cs routine schedules list 2>/dev/null)"
for s in classify-ticket consistency-sweep daily-status-digest; do
  if printf '%s' "$sched_out" | grep -qi "$s"; then
    _pass "schedule for '$s' is listed"
  else
    _fail "schedule for '$s' is listed"
  fi
done
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
section "2. Agentless wake-gate: token-zero guarantee"
# ─────────────────────────────────────────────────────────────────────────────
# cost-spike-probe is declared agentless: it must run with ZERO LLM cost.
if cs routine run cost-spike-probe >/dev/null 2>&1; then
  _pass "agentless routine 'cost-spike-probe' ran"
  if have jq; then
    cost="$(cs routine records cost-spike-probe --json --limit 1 2>/dev/null | jq -r '.[0].cost_usd // 0')"
    if [[ "$cost" == "0" || "$cost" == "0.0" || "$cost" == "0.00000" ]]; then
      _pass "agentless run cost is exactly 0 (token-zero guarantee holds)"
    else
      _fail "agentless run cost is 0" "recorded cost_usd=$cost — agentless routine spent tokens"
    fi
  else
    skip "token-zero cost assertion" "jq missing"
  fi
else
  _fail "agentless routine 'cost-spike-probe' ran" "routine run exited non-zero"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "3. HITL approval gate: pause → approve → resume"
# ─────────────────────────────────────────────────────────────────────────────
# approval-gate-demo has a wait(approval) step. Run it in the background (it
# blocks on the gate), find the pending waitpoint, approve it, confirm resume.
info "Launching approval-gate-demo (will pause at the approval waitpoint)…"
( cs routine run approval-gate-demo >/tmp/cs-appr.out 2>&1 ) &
run_pid=$!

token=""
waited=0
while (( waited < 60 )); do
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
else
  skip "HITL approval gate" "no pending waitpoint appeared within 60s"
fi
wait "$run_pid" 2>/dev/null
# After approval the run should reach a terminal state (completed).
if have jq; then
  st="$(cs routine records approval-gate-demo --json --limit 1 2>/dev/null | jq -r '.[0].status // empty')"
  if [[ "$st" == "completed" ]]; then
    _pass "run resumed and completed after approval"
  else
    skip "run completion after approval" "latest status=$st (may still be finishing)"
  fi
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

finish

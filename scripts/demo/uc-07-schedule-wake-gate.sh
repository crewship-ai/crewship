#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# uc: schedule-wake-gate
# title: A schedule with a token-zero wake gate holds on a real tick
# needs: none
# minutes: 2
#
# The shape every pack has: a cheap probe decides whether the expensive
# routine runs at all. A schedule for workspace-digest is gated by
# cost-spike-probe (an `expr` step: spend > threshold) with inputs that make
# the gate HOLD, on a cron that fires within the minute. The scheduler's own
# tick runs the probe and records HELD; the digest does not fire. The
# schedule is deleted on exit.
#
# Not `schedules now`: force-fire runs the TARGET directly and skips the
# gate (internal/api RunSchedule), so it cannot show this.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight
have jq || demo_skip_all "jq is required to read the schedule id"

NAME="demo-wake-$(nonce S | tr 'A-Z' 'a-z')"
SCHED_ID=""
digest_runs() { cs routine runs workspace-digest --limit 100 --format json 2>/dev/null | jq '[.[]? | select(.entry_type | test("run\\.started"))] | length' 2>/dev/null || echo 0; }
sched_json() { cs routine schedules list --slug workspace-digest --format json 2>/dev/null | jq -c --arg id "$SCHED_ID" 'first(.[]? | select(.id == $id)) // {}' 2>/dev/null; }

demo_step "Create an every-minute schedule gated by the probe"
demo_say "Probe inputs spend 1 < threshold 5: the gate must hold, and the digest must not fire."
out="$(cs routine schedules create --slug workspace-digest --name "$NAME" --cron '* * * * *' \
        --wake-slug cost-spike-probe --wake-inputs '{"spend_usd":1,"threshold_usd":5}' --format json 2>&1)"; rc=$?
SCHED_ID="$(printf '%s' "$out" | jq -r '.id // empty' 2>/dev/null)"
[[ -z "$SCHED_ID" ]] && SCHED_ID="$(cs routine schedules list --slug workspace-digest --format json 2>/dev/null | jq -r --arg n "$NAME" 'first(.[]? | select(.name == $n)) | .id // empty')"
if (( rc == 0 )) && [[ -n "$SCHED_ID" ]]; then
  _pass "schedule $NAME created ($SCHED_ID)"
  demo_cleanup "cs routine schedules delete '$SCHED_ID' --yes"
else
  printf '%s\n' "$out" | sed 's/^/     /'
  _fail "schedule created" "exit $rc, id='${SCHED_ID}'"
fi
demo_show "schedules list" cs routine schedules list --slug workspace-digest
demo_ui "Schedules" "/routines/workspace-digest"

demo_step "Wait for the scheduler's tick: the probe runs, the gate holds"
digest_before="$(digest_runs)"
if [[ -n "$SCHED_ID" ]]; then
  demo_say "The scheduler ticks every 30 s and fires at the next minute boundary; the probe is keyed on that occurrence."
  poll_until "the wake check ran (wake_check_count ≥ 1)" 150 \
    "[ \"\$(\"$CREWSHIP\" --server \"$SERVER\" ${_CS_ARGS[*]+${_CS_ARGS[*]}} routine schedules list --slug workspace-digest --format json | jq -r --arg id \"$SCHED_ID\" 'first(.[]? | select(.id == \$id)) | .wake_check_count // 0')\" -ge 1 ]"
  s="$(sched_json)"
  printf '     %s\n' "$(printf '%s' "$s" | jq -c '{wake_check_count, wake_fire_count, last_wake_status, last_wake_at}')"
  assert_eq "last wake status is SKIPPED (the probe said no)" "SKIPPED" "$(printf '%s' "$s" | jq -r '.last_wake_status // ""')"
  assert_eq "wake fire count stays 0" "0" "$(printf '%s' "$s" | jq -r '.wake_fire_count // 0')"
  digest_after="$(digest_runs)"
  if (( digest_after == digest_before )); then
    _pass "the gate held: workspace-digest did not fire ($digest_before run(s) before and after)"
  else
    _fail "the gate held" "workspace-digest fired anyway ($digest_before → $digest_after)"
  fi
  demo_show "the probe's run, triggered by the wake check" cs routine runs cost-spike-probe --limit 2
else
  skip "wake check" "no schedule id"
fi

demo_step "Clean up"
if [[ -n "$SCHED_ID" ]]; then
  demo_show "schedules delete $SCHED_ID" cs routine schedules delete "$SCHED_ID" --yes
  _DEMO_CLEANUPS=()
fi
demo_say "Flip the inputs (spend 9 > threshold 5) and the same tick WAKES the digest. For the real thing: crewship digest enable"

finish

#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# uc: approval-gate
# title: A routine drafts a change, parks on a human approval, then resumes
# needs: model
# minutes: 4
#
# The human-gated change workflow: `approval-gate-demo` drafts a reversible
# production change (agent step), enters a durable wait(approval), and only
# after `crewship routine waitpoints approve` runs its last step. The run
# sits in WAITING for as long as the human takes (24 h here). The receipt
# of `routine run` names the waitpoint and both commands.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight
reason="$(demo_need model)" || demo_skip_all "$reason"

NONCE="$(nonce GATE)"
ACTION="Pause the release while GitHub reports an incident ($NONCE)"

demo_step "Start the routine — it drafts, then stops at the gate"
demo_say "Without --wait: the receipt comes back as soon as the run parks (--wait would sit with it until the human decides)."
run_receipt approval-gate-demo --inputs "{\"action\":\"$ACTION\"}"
assert_nonempty "run id in the receipt" "$RUN_ID"
assert_eq "the run is WAITING" "WAITING" "$RUN_STATUS"
token="$(printf '%s\n' "$DEMO_OUT" | sed -nE 's/.*waitpoints approve ([a-f0-9]{16,}).*/\1/p' | head -1)"
assert_nonempty "the receipt names the waitpoint and the approve command" "$token"

demo_step "The gate is visible everywhere a human looks"
demo_show "routine waitpoints list" cs routine waitpoints list
[[ -n "$token" ]] && demo_show "routine waitpoints show $token" cs routine waitpoints show "$token"
assert_contains "the prompt carries morgan's draft for this run ($NONCE)" "$DEMO_OUT" "$NONCE"
demo_show "the same gate as an inbox waitpoint, titled with the action" cs inbox list --kind waitpoint --state all --limit 5
assert_contains "the inbox row names the action" "$DEMO_OUT" "Pause the release while GitHub"
demo_ui "Inbox" "/inbox"

demo_step "A human approves; the run resumes and finishes"
if [[ -z "$token" ]]; then
  skip "approve" "no waitpoint token"
else
  demo_show "routine waitpoints approve $token" cs routine waitpoints approve "$token" --comment "LGTM (demo)"
  if have jq; then
    poll_until "run $RUN_ID reaches completed" 600 "[ \"\$(run_status approval-gate-demo \"$RUN_ID\")\" = completed ]"
  else
    skip "run completion" "jq missing"
  fi
  demo_show "run history" cs routine runs approval-gate-demo --limit 4
fi
demo_ui "Routine runs" "/routines/approval-gate-demo"

finish

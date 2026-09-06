#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# uc: ephemeral-hire
# title: Hire a short-lived specialist; a guided crew stages it for approval
# needs: none
# minutes: 1
#
# `crewship hire` spawns a contractor agent from a crew template with a TTL.
# What happens next is the crew's autonomy level: guided → PENDING_REVIEW
# with an approval in the queue; trusted/full → live at once. Both branches
# are shown; the guided one is then approved from the CLI.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight

TMPL="${TMPL:-devops-sre}"
CREW="${CREW:-ops}"

demo_step "Hire from template '$TMPL' into $CREW, TTL 15 minutes"
demo_say "The reason is written to the audit trail; the agent ghosts by itself when the TTL elapses."
hire_out="$(cs hire --crew "$CREW" --template "$TMPL" --reason "demo: prototype a rate-limiter" --ttl 15 --yes 2>&1)"; rc=$?
printf '%s\n' "$hire_out" | sed 's/^/     /'
agent_id="$(printf '%s\n' "$hire_out" | grep -oE '\b(agent_|cm)[a-z0-9]{8,}\b' | head -1)"
if (( rc == 0 )); then
  _pass "hire accepted${agent_id:+ (agent $agent_id)}"
else
  _fail "hire accepted" "exit $rc"
fi

demo_step "Where did it land?"
demo_show "roster" cs agent list
if printf '%s' "$DEMO_OUT" | grep -qiE 'PENDING_REVIEW'; then
  _pass "guided autonomy: the hire is staged as PENDING_REVIEW"
  demo_show "the approval waiting for a human" cs approvals list --status pending
  demo_show "…and the blocking inbox waitpoint" cs inbox list --kind waitpoint --state all
  demo_ui "Inbox" "/inbox"

  demo_step "A human approves it from the CLI"
  pending_id="$(cs agent list --format json 2>/dev/null | jq -r '[.[]? | select((.status // "") == "PENDING_REVIEW")] | first | .id // .slug // empty' 2>/dev/null)"
  target="${agent_id:-$pending_id}"
  if [[ -n "$target" ]]; then
    demo_show "hire approve $target" cs hire approve "$target"
    poll_until "the hired agent is IDLE (serving)" 60 \
      "\"$CREWSHIP\" --server \"$SERVER\" ${_CS_ARGS[*]+${_CS_ARGS[*]}} agent list --format json | jq -e '[.[]? | select(.status == \"IDLE\" and ((.id == \"$target\") or (.slug == \"$target\")))] | length > 0'"
  else
    skip "approve the staged hire" "could not read the staged agent's id from the hire receipt or the roster"
  fi
elif printf '%s' "$hire_out" | grep -qiE 'rejected|strict'; then
  skip "hire outcome" "crew $CREW has strict autonomy — the hire is rejected by policy (that is the feature)"
else
  _pass "trusted/full autonomy: the hire is live without a gate"
fi
demo_say "The contractor ghosts automatically at TTL; 'crewship rehire <id>' would extend it."
demo_ui "Agents" "/agents"

finish

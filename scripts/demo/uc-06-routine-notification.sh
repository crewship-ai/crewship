#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# uc: routine-notification
# title: A token-zero routine runs to completion and lands in the inbox
# needs: none
# minutes: 1
#
# `workspace-digest` is query → transform → notify: no model, no egress. It
# is the cheapest proof that a routine's outcome is observable afterwards —
# a run row with a terminal status and a notification carrying the run id.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight

demo_step "Run workspace-digest and wait for it"
run_routine workspace-digest
assert_eq "run completed" "COMPLETED" "$RUN_STATUS"
assert_nonempty "run id" "$RUN_ID"
assert_contains "the digest says how many runs it counted" "$DEMO_OUT" "routine run(s)"

demo_step "The run is on record"
demo_show "routine runs workspace-digest" cs routine runs workspace-digest --limit 4
if have jq; then
  # The journal is written asynchronously; give it a moment.
  poll_until "journal has this run as completed" 30 "[ \"\$(run_status workspace-digest \"$RUN_ID\")\" = completed ]"
else
  skip "journal has this run" "jq missing"
fi

demo_step "The notification is in the inbox, tied to that run"
poll_until "inbox item for $RUN_ID" 30 "inbox_has \"$RUN_ID\""
demo_show "inbox list --kind message" cs inbox list --kind message --state all --limit 5
demo_ui "Inbox" "/inbox"
demo_say "Channel preferences fan the same item out to Slack or email: crewship notifications --help"

finish

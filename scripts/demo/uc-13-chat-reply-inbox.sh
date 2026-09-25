#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# uc: chat-reply-inbox
# title: An agent's chat reply arrives in the unified inbox
# needs: model
# minutes: 3
#
# A one-shot CLI ask does not subscribe the human to the chat's live WebSocket
# session. The saved reply therefore exercises the real chat → inbox path.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight
reason="$(demo_need model)" || demo_skip_all "$reason"

MARKER="$(nonce INBOX)"
demo_step "Ask Morgan in a fresh chat, then look for the saved reply"
reply="$(ask_agent morgan "For the fictional incident DEMO-42, give one safe first triage step. Include this exact marker in your reply: $MARKER. Do not contact an external service.")"
assert_nonempty "Morgan returned a saved reply" "$reply"
assert_contains "Morgan's reply carries this run's marker" "$reply" "$MARKER"

demo_step "The agent reply is projected into Inbox → Agent replies"
if [[ -n "$reply" ]]; then
  poll_until "chat reply $MARKER in Inbox" 30 \
    "cs inbox list --kind message --state all --limit 100 --format json 2>/dev/null | grep -qF '$MARKER'"
else
  skip "chat reply in Inbox" "the agent run returned no reply, so there is no chat event to project"
fi
demo_show "recent Inbox messages" cs inbox list --kind message --state all --limit 5
demo_ui "Inbox" "/inbox"

finish

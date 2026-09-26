#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# uc: chat-reply-inbox
# title: An agent's chat reply arrives in the unified inbox
# needs: model
# minutes: 3
#
# `crewship ask` subscribes to the session while it waits, so it correctly
# suppresses the bell. This case sends a message without that subscription.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight
reason="$(demo_need model)" || demo_skip_all "$reason"
have jq || demo_skip_all "jq is required to correlate the chat with its Inbox item"
have go || demo_skip_all "Go is required for the send-and-leave WebSocket helper"

MARKER="$(nonce INBOX)"
demo_step "Send Morgan a message, then leave the chat before the reply"
prompt="For the fictional incident DEMO-42, give one safe first triage step. Include this exact marker in your reply: $MARKER. Do not contact an external service."
chat_id="$(cd "$HERE/../.." && go run ./scripts/demo/ws-send-and-leave --server "$SERVER" --agent morgan --prompt "$prompt" 2>/dev/null)"
send_rc=$?
if [[ -n "$chat_id" ]]; then
  # chat delete also removes the chat's projected Inbox message, so this one
  # cleanup covers both; it runs on exit, after the assertions below.
  demo_cleanup "cs chat delete '$chat_id' --yes"
fi
# No set -e here, and the assignment above swallows the helper's exit status:
# ws-send-and-leave prints the id as soon as the chat exists, so a FAILED send
# still leaves a real id in $chat_id, and polling it below would burn
# ASK_TIMEOUT on a chat whose message never went out instead of reporting the
# failure. Gate the polls on the send actually succeeding; the exit cleanup
# above still deletes the chat either way.
send_ok=false
if (( send_rc != 0 )); then
  _fail "the message was sent" "ws-send-and-leave exited ${send_rc}"
elif [[ -n "$chat_id" ]]; then
  send_ok=true
fi
assert_nonempty "direct chat was created" "$chat_id"
if [[ "$send_ok" == true ]]; then
  poll_until "Morgan's saved reply carries $MARKER" "$ASK_TIMEOUT" \
    "cs chat '$chat_id' --format json 2>/dev/null | jq -e --arg marker '$MARKER' 'any(.[]?; .role == \"assistant\" and ((.content // \"\") | contains(\$marker)))' >/dev/null"
fi

demo_step "The agent reply is projected into Inbox → Agent replies"
if [[ "$send_ok" == true ]]; then
  poll_until "agent reply for chat $chat_id in Inbox" 30 \
    "cs inbox list --kind message --state all --limit 100 --format json 2>/dev/null | jq -e --arg id '$chat_id' 'any(.[]?; .sender_type == \"agent\" and .payload.chat_id == \$id)' >/dev/null"
else
  skip "chat reply in Inbox" "the message could not be sent"
fi
demo_show "recent Inbox messages" cs inbox list --kind message --state all --limit 5
demo_ui "Inbox" "/inbox"

finish

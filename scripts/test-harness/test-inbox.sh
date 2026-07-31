#!/usr/bin/env bash
# Inbox — runtime validation against a live server.
#
# The inbox is a human-attention queue whose whole value is that a click does
# what it says: an approval reaches the waitpoint, a dismissal reaches the row,
# and neither is undone by the next poll. None of that is provable in a unit
# test, because every branch is a contract with an endpoint and the endpoints
# only exist at runtime.
#
# So this drives the REAL CLI against a running server: it makes an item the
# hard way (fire a routine that parks on an approval), acts on it, and then
# re-reads the server to prove the state actually moved. It also checks the
# facts the UI derives from — kinds, targeting, categories — because a UI that
# renders a field the server never sends is a UI that lies convincingly.
#
# Usage:
#   export CREWSHIP_SERVER=<devN url>
#   ./scripts/test-harness/test-inbox.sh
#
# shellcheck source=./lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

preflight

# ── Helpers ────────────────────────────────────────────────────────────────

# inbox_json <state> — the inbox list as JSON, or "[]" when the CLI cannot.
#
# --limit 500 because the CLI defaults to 50 and the server caps at 500: a
# count taken off the default page would be a count of the first fifty rows
# wearing the name of the whole state.
inbox_json() {
  local out rc
  out="$(cs inbox list --state "${1:-all}" --limit 500 -f json 2>&1)"; rc=$?
  if (( rc != 0 )); then
    # Do NOT fall back to "[]". An auth or transport failure would then read as
    # "the workspace is empty", every assertion below would skip, and the run
    # would go green while proving nothing.
    _fail "inbox list (${1:-all})" "CLI exited $rc: $(printf '%s' "$out" | head -c 200)"
    printf '[]'
    return 1
  fi
  printf '%s' "$out"
}

# item_field <id> <jq-path> — one field off one row, empty when absent.
item_field() {
  local id="$1" path="$2"
  if have jq; then
    inbox_json all | jq -r --arg id "$id" \
      ".[]? | select(.id==\$id) | ${path} // empty" 2>/dev/null
  fi
}

# count_state <state> — how many rows the server reports in that state.
count_state() {
  if have jq; then
    inbox_json "$1" | jq -r 'length' 2>/dev/null || printf '0'
  else
    printf '0'
  fi
}

section "Preconditions"

if ! have jq; then
  skip "inbox assertions need jq for JSON field access"
  finish
fi

WHO="$(cs whoami 2>&1)"
assert_contains "CLI is authenticated against $SERVER" "$WHO" "Workspace:"

# The list endpoint has to answer before anything else means anything.
LIST_RAW="$(inbox_json all)"
assert_nonempty "inbox list returns a payload" "$LIST_RAW"

section "A real decision: fire a routine that parks on an approval"

# approval-gate-demo is the seeded routine whose middle step is a `wait`. It is
# the only way to mint a genuine waitpoint — one with a token the approve
# endpoint will accept — without hand-writing a row.
RUN_OUT="$(cs routine run approval-gate-demo --inputs '{"action":"harness probe"}' 2>&1)"

if ! grep -qi "waiting\|approve" <<<"$RUN_OUT"; then
  skip "approval-gate-demo did not park on an approval — is it seeded? ($(head -1 <<<"$RUN_OUT"))"
else
  _pass "routine parked on its approval step"

  # The waitpoint token the CLI prints is the same source_id the inbox row
  # carries; that identity is what lets the UI's Approve reach the right gate.
  TOKEN="$(grep -oE 'waitpoints approve [a-f0-9]{16,}' <<<"$RUN_OUT" | awk '{print $3}' | head -1)"
  assert_nonempty "run reports an approval token" "$TOKEN"

  # Give the write-through a moment: the row is inserted by the waitpoint
  # creator, not by the run's response.
  poll_until "inbox shows the new waitpoint" 30 \
    "[ \"\$(cs inbox list --state active --limit 500 -f json 2>/dev/null | jq -r 'map(select(.source_id==\"$TOKEN\")) | length')\" = 1 ]"

  ROW_ID="$(inbox_json active | jq -r --arg t "$TOKEN" '.[]? | select(.source_id==$t) | .id')"
  assert_nonempty "the waitpoint has an inbox row" "$ROW_ID"

  # ── The fields the redesign renders. Each one is a claim about the server.
  KIND="$(item_field "$ROW_ID" '.kind')"
  assert_eq "row kind is waitpoint" "waitpoint" "$KIND"

  TARGET="$(item_field "$ROW_ID" '.target_role')"
  assert_eq "waitpoint is addressed to MANAGER (approve is roleCreate)" "MANAGER" "$TARGET"

  BLOCKING="$(item_field "$ROW_ID" '.blocking')"
  assert_eq "waitpoint is marked blocking" "true" "$BLOCKING"

  # timeout_at is the field the shipped UI dropped and the redesign counts down.
  TIMEOUT="$(item_field "$ROW_ID" '.payload.timeout_at')"
  assert_nonempty "payload carries timeout_at — the countdown has a source" "$TIMEOUT"

  RUN_ID="$(item_field "$ROW_ID" '.payload.pipeline_run_id')"
  assert_nonempty "payload carries pipeline_run_id — run progress has a source" "$RUN_ID"

  # ── Approving through the source endpoint must cascade the inbox row.
  APPROVE_OUT="$(cs routine waitpoints approve "$TOKEN" --comment "harness" 2>&1)"
  assert_not_contains "approve is accepted" "$APPROVE_OUT" "error"

  poll_until "approving the waitpoint resolves its inbox row" 45 \
    "[ \"\$(cs inbox list --state active --limit 500 -f json 2>/dev/null | jq -r 'map(select(.id==\"$ROW_ID\")) | length')\" = 0 ]"

  STATE_AFTER="$(item_field "$ROW_ID" '.state')"
  assert_eq "the row is resolved server-side, not just hidden" "resolved" "$STATE_AFTER"

  # This is the contract KindActions is built on: the SOURCE closes the row.
  # If the UI also patched it, the patch would 409 — and if the source stopped
  # cascading, every approval would leave a ghost in the queue.
  info "source-managed cascade confirmed (inbox.ResolveBySource)"
fi

section "Dismissal reaches the server, not just the screen"

# A plain message is the one kind the inbox may close itself — but picking an
# arbitrary one would close a real notification belonging to whoever is using
# this instance. Mint one instead, by running a routine whose notify step
# addresses the caller, and dismiss only that.
BEFORE_IDS="$(inbox_json active | jq -r '.[]? | select(.kind=="message") | .id' | sort)"
cs routine run "${NOTIFY_ROUTINE:-demo-fetch-and-report}" >/dev/null 2>&1
sleep "$POLL_INTERVAL"
MSG_ID="$(comm -13 <(printf '%s\n' "$BEFORE_IDS") \
  <(inbox_json active | jq -r '.[]? | select(.kind=="message") | .id' | sort) | head -1)"

if [[ -z "$MSG_ID" ]]; then
  skip "could not mint a message to dismiss (is ${NOTIFY_ROUTINE:-demo-fetch-and-report} seeded?)"
else
  info "dismissing only the row this run created: $MSG_ID"
  cs inbox resolve "$MSG_ID" --action dismissed >/dev/null 2>&1

  poll_until "dismissing a message removes it from the active list" 20 \
    "[ \"\$(cs inbox list --state active --limit 500 -f json 2>/dev/null | jq -r 'map(select(.id==\"$MSG_ID\")) | length')\" = 0 ]"

  ACTION="$(item_field "$MSG_ID" '.resolved_action')"
  assert_eq "resolved_action records HOW it was closed" "dismissed" "$ACTION"

  RESOLVER="$(item_field "$MSG_ID" '.resolved_by_user_id')"
  assert_nonempty "resolved_by_user_id records WHO closed it" "$RESOLVER"
fi

section "Every kind the UI can draw is a kind the server admits"

# The redesign renders seven. The DB CHECK is the thing that decides whether a
# producer's row survives the insert, and a kind missing from it fails silently
# — the alert simply never arrives. Nothing here mints them; this asserts the
# vocabulary the UI is coded against is the vocabulary in the data.
KINDS_SEEN="$(inbox_json all | jq -r '.[]? | .kind' | sort -u | tr '\n' ' ')"
info "kinds present in this workspace: ${KINDS_SEEN:-none}"

for k in waitpoint escalation message; do
  if grep -qw "$k" <<<"$KINDS_SEEN"; then
    _pass "server produces kind=$k"
  else
    skip "no kind=$k in this workspace right now"
  fi
done

section "Targeting matches who can decide"

# The mismatch this release fixed: a row addressed to MANAGER whose approve
# endpoint is OWNER/ADMIN handed its reader a 403. Assert it at runtime, where
# the row and the route meet.
SKILL_TARGET="$(inbox_json all | jq -r '.[]? | select(.payload.kind=="skill_proposal") | .target_role' | head -1)"
if [[ -z "$SKILL_TARGET" ]]; then
  skip "no skill proposal in this workspace to check targeting"
else
  assert_eq "skill proposals are addressed to ADMIN (approve is roleManage)" "ADMIN" "$SKILL_TARGET"
fi

section "Counts the UI shows come from the server"

ACTIVE_N="$(count_state active)"
UNREAD_N="$(count_state unread)"
RESOLVED_N="$(count_state resolved)"
info "active=$ACTIVE_N unread=$UNREAD_N resolved=$RESOLVED_N"

# Unread is a subset of active; if it is not, the badge and the list disagree
# and one of them is lying to the operator.
if (( UNREAD_N > ACTIVE_N )); then
  _fail "unread ($UNREAD_N) exceeds active ($ACTIVE_N) — badge and list disagree"
else
  _pass "unread is a subset of active"
fi

# The known ceiling: the list endpoint is LIMIT 100 with no cursor, so a
# workspace past that silently truncates. Say so loudly rather than let a green
# run imply the archive is complete.
# The request asks for 500, which is the server's own maximum; hitting it means
# the page IS truncated. (The endpoint's default is 100 — that is what the UI
# gets, and why its archive is a window either way.)
if (( RESOLVED_N >= 500 )); then
  info "NOTE: resolved list returned $RESOLVED_N — that is the 500-row ceiling and there is no cursor, so this is a window, not the archive"
fi

finish

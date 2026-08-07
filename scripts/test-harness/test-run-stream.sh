#!/usr/bin/env bash
# Agent-run NDJSON stream (#1818) — runtime validation against a live server.
#
# The whole point of `GET /api/v1/chats/{chatId}/stream` is that an agent in a
# shell can watch a run WITHOUT a WebSocket client. That claim is only provable
# by driving the real CLI against a real server: a unit test can assert the
# handler writes frames, but it cannot show that the shipped binary, the
# shipped route, the auth chain and the frame contract line up end to end.
#
# Two tiers, and the suite says which one it ran:
#
#   Tier A — control plane, NO provider credential required.
#     CLI parity, the 404-for-a-chat-you-cannot-see rule, and the frame
#     contract against a real chat with nothing running (stream.open →
#     stream.end/no_active_run, exit 0).
#
#   Tier B — a live run, requires a provider credential.
#     Start a run, attach to it from a second process the way an operator or a
#     second agent would, and prove the run's output actually arrives. SKIPs
#     with the reason when no agent replies (the `LLMRunner: provider: no
#     active … credential` shape every runtime suite hits without a key).
#
# Tier B deliberately drives `cs run`, and that is a real coverage limit worth
# stating: today the WebSocket send_message path is the ONLY producer of
# session-channel frames (internal/ws/client.go streams.begin/record). A run
# started by a routine, webhook or pipeline emits nothing there, so this suite
# CANNOT catch a regression on those paths — there is no signal to regress.
# #1823 tracks extending emission to them; when it lands, add a case here that
# starts the run from a routine instead of the CLI.
#
# Usage:
#   export CREWSHIP_SERVER=<devN url>
#   ./scripts/test-harness/test-run-stream.sh
#
# shellcheck source=./lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

preflight

# How long Tier B waits for a run to start producing before giving up. A cold
# container plus a first token is comfortably inside this on a dev slot.
RUN_ATTACH_TIMEOUT="${RUN_ATTACH_TIMEOUT:-90}"

# ── Tier A: control plane ──────────────────────────────────────────────────

section "Tier A · CLI parity"

# Rule #3 in CLAUDE.md: an API endpoint with no CLI command is an API no agent
# can use safely. Read the binary's own manifest rather than `--help` text so
# a renamed command fails here instead of silently passing a grep.
MANIFEST="$(cs commands --format json 2>/dev/null)"
if [[ -z "$MANIFEST" ]]; then
  _fail "command manifest" "crewship commands --format json produced nothing"
elif have jq; then
  HAS_STREAM="$(printf '%s' "$MANIFEST" | jq -r '[.. | objects | select(.path? == "chat stream")] | length' 2>/dev/null || echo 0)"
  assert_eq "chat stream is in the command manifest" "1" "$HAS_STREAM"
else
  assert_contains "chat stream is in the command manifest" "$MANIFEST" '"chat stream"'
fi

assert_ok "chat stream --help" bash -c "$CREWSHIP --no-color chat stream --help >/dev/null 2>&1"

section "Discovery"

# Any agent with a chat will do; the contract under test is the transport, not
# the conversation. Prefer an agent that already has one so the suite does not
# have to create state. Discovered up front because the tenancy section below
# needs a chat that genuinely EXISTS.
CHAT_ID=""
AGENT_SLUGS="$(cs agent list --format json 2>/dev/null | { have jq && jq -r '.[]?.slug // empty' || cat; } 2>/dev/null | head -10)"
for slug in $AGENT_SLUGS; do
  if have jq; then
    CHAT_ID="$(cs chat list "$slug" --format json 2>/dev/null | jq -r '.[0]?.id // empty' 2>/dev/null)"
  fi
  [[ -n "$CHAT_ID" ]] && break
done
info "chat for transport tests: ${CHAT_ID:-<none>}"

section "Tier A - a chat that does not exist is 404"

# The CLI surfaces the status verbatim and must NOT retry a permanent error.
# NOTE: this covers only the "no such row" arm of the authorizer. On its own it
# cannot tell a correct implementation from one that leaks existence - see the
# cross-tenant section below, which is the assertion with teeth.
BOGUS_OUT="$(cs chat stream cnotarealchatid00000 --idle 5 2>&1)"; BOGUS_RC=$?
if (( BOGUS_RC == 0 )); then
  _fail "unknown chat exits non-zero" "exit 0 for a chat that does not exist"
else
  _pass "unknown chat exits non-zero"
fi
assert_contains "unknown chat reports 404" "$BOGUS_OUT" "404"
assert_not_contains "unknown chat does not leak a 403" "$BOGUS_OUT" "403"

section "Tier A - an EXISTING chat you may not see is ALSO 404, not 403"

# The security property this endpoint stands on: a caller who is not a member
# of the chat's workspace must not be able to tell "no such chat" from "not
# yours". A 403 here confirms the id is real in somebody else's workspace, and
# an attacker enumerates ids and reads the status code as an oracle.
#
# Proving that needs a chat that EXISTS and a caller who may not see it. This
# builds the second identity out of the seeded users (`crewship seed
# --with-users`, known passwords) and then removes that user from the
# workspace, which is the real-world trigger: membership withdrawn while the
# chat stays exactly where it was.
#
# The membership is restored immediately after the request is captured - before
# any assertion runs, so a FAILING assertion cannot leave the workspace altered
# - and again from an EXIT trap in case the suite dies unexpectedly.
T2_EMAIL="${RUN_STREAM_T2_EMAIL:-viewer1@crewship.local}"
T2_PASSWORD="${RUN_STREAM_T2_PASSWORD:-viewerpass12}"
T2_PROFILE="harness-run-stream-t2"
T2_USER_ID=""
T2_ROLE=""

restore_t2_membership() {
  [[ -z "$T2_USER_ID" || -z "$T2_ROLE" ]] && return 0
  # Idempotent: re-adding an existing member is not an error worth reporting
  # from a trap, and the whole point is that the workspace ends as it started.
  cs workspace member add "$T2_USER_ID" --role "$T2_ROLE" >/dev/null 2>&1 || true
}
trap restore_t2_membership EXIT

if ! have jq; then
  skip "cross-tenant 404" "needs jq to resolve the second user"
elif [[ -z "$CHAT_ID" ]]; then
  skip "cross-tenant 404" "no existing chat to test against"
else
  T2_ROW="$(cs workspace member list --format json 2>/dev/null \
    | jq -r --arg e "$T2_EMAIL" '.[]? | select(.email==$e) | "\(.user_id // .id)\t\(.role)"' 2>/dev/null | head -1)"
  T2_USER_ID="$(printf '%s' "$T2_ROW" | cut -f1)"
  T2_ROLE="$(printf '%s' "$T2_ROW" | cut -f2)"

  if [[ -z "$T2_USER_ID" || -z "$T2_ROLE" ]]; then
    skip "cross-tenant 404" "seeded user $T2_EMAIL is not a member of this workspace (re-seed with 'crewship seed --with-users')"
  elif ! "$CREWSHIP" server add "$T2_PROFILE" --server "$SERVER" >/dev/null 2>&1; then
    skip "cross-tenant 404" "could not create the $T2_PROFILE server profile"
  elif ! printf '%s' "$T2_PASSWORD" | "$CREWSHIP" login --profile "$T2_PROFILE" --server "$SERVER" \
        --email "$T2_EMAIL" --password-stdin >/dev/null 2>&1; then
    skip "cross-tenant 404" "could not log in as $T2_EMAIL (seeded password changed?)"
  else
    info "second identity $T2_EMAIL ($T2_USER_ID, $T2_ROLE) - removing from the workspace"
    if ! cs workspace member remove "$T2_USER_ID" --yes >/dev/null 2>&1; then
      skip "cross-tenant 404" "could not remove $T2_EMAIL from the workspace"
    else
      # The chat still exists and is unchanged; only this caller's membership
      # is gone. `chat stream` needs no workspace, so nothing else is in play.
      XT_OUT="$("$CREWSHIP" --profile "$T2_PROFILE" --server "$SERVER" --no-color \
        chat stream "$CHAT_ID" --idle 5 2>&1)"; XT_RC=$?

      # Restore BEFORE asserting, so a failing assertion cannot leave the
      # workspace altered.
      restore_t2_membership
      T2_USER_ID=""; T2_ROLE=""   # the trap now has nothing left to do

      if (( XT_RC == 0 )); then
        _fail "cross-tenant stream exits non-zero" "exit 0 - a non-member was served chat $CHAT_ID"
      else
        _pass "cross-tenant stream exits non-zero"
      fi
      assert_contains "existing chat, non-member caller reports 404" "$XT_OUT" "404"
      # THE assertion. A 403 is the existence oracle: it says "this chat is
      # real, you just cannot have it".
      assert_not_contains "existing chat, non-member caller does NOT leak a 403" "$XT_OUT" "403"
      # Same wire shape as the nonexistent case - the two must be
      # indistinguishable, not merely both non-zero. The ids are normalised out
      # so only the shape of the answer is compared.
      if [[ "${BOGUS_OUT//cnotarealchatid00000/X}" == "${XT_OUT//$CHAT_ID/X}" ]]; then
        _pass "cross-tenant and nonexistent answers are indistinguishable"
      else
        _fail "cross-tenant and nonexistent answers are indistinguishable" \
          "missing=<$(printf '%s' "$BOGUS_OUT" | head -c 100)> cross-tenant=<$(printf '%s' "$XT_OUT" | head -c 100)>"
      fi
    fi
  fi
fi
"$CREWSHIP" server remove "$T2_PROFILE" >/dev/null 2>&1 || true

section "Tier A - frame contract on an idle chat"

if [[ -z "$CHAT_ID" ]]; then
  skip "idle-chat frame contract" "no existing chat in this workspace (seed with --with-memory, or run Tier B first)"
else
  info "streaming chat $CHAT_ID with nothing running"
  # --idle is a belt: with no run active the server closes immediately with
  # no_active_run, so this must return in well under a second.
  IDLE_OUT="$(cs chat stream "$CHAT_ID" --format ndjson --idle 10 --quiet 2>&1)"; IDLE_RC=$?
  assert_eq "idle chat exits 0" "0" "$IDLE_RC"
  assert_contains "first frame is stream.open" "$(printf '%s' "$IDLE_OUT" | head -1)" '"type":"stream.open"'
  assert_contains "stream.open names the chat" "$(printf '%s' "$IDLE_OUT" | head -1)" "$CHAT_ID"
  assert_contains "last frame is stream.end" "$(printf '%s' "$IDLE_OUT" | tail -1)" '"type":"stream.end"'
  assert_contains "idle chat ends with no_active_run" "$IDLE_OUT" '"reason":"no_active_run"'
  # Every line must be a standalone JSON object — that is the entire contract
  # a `jq` pipeline depends on.
  if have jq; then
    if printf '%s\n' "$IDLE_OUT" | grep -v '^$' | jq -e . >/dev/null 2>&1; then
      _pass "every line parses as JSON"
    else
      _fail "every line parses as JSON" "$(printf '%s' "$IDLE_OUT" | head -c 200)"
    fi
    # stream.end must carry the resume watermark, or a reconnect has nothing
    # to resume from.
    LAST_SEQ="$(printf '%s\n' "$IDLE_OUT" | grep '"stream.end"' | jq -r '.last_seq // empty' 2>/dev/null)"
    assert_nonempty "stream.end carries last_seq" "${LAST_SEQ:-}"
  fi

  # A resume request against the same idle chat must still be a clean exit —
  # not a hang and not an error. This is what a reconnect loop does.
  assert_ok "resume with --last-seq is accepted" \
    bash -c "$CREWSHIP --server '$SERVER' chat stream '$CHAT_ID' --last-seq 1 --idle 10 --quiet >/dev/null 2>&1"
fi

# ── Tier B: a live run ─────────────────────────────────────────────────────

section "Tier B · watching a run somebody else started"

RUN_AGENT="$(printf '%s\n' "$AGENT_SLUGS" | head -1)"
if [[ -z "$RUN_AGENT" ]]; then
  skip "live run stream" "no agents in this workspace"
  finish
fi

# Start the run in the background and attach from THIS process — that is the
# real shape of the feature (the run's owner is somebody else) and it is the
# only way to be sure the stream is not just replaying a finished run.
RUN_LOG="$(mktemp -t cs-run-stream.XXXXXX)"
BEFORE_IDS="$(cs chat list "$RUN_AGENT" --format json 2>/dev/null | { have jq && jq -r '.[]?.id' || true; } 2>/dev/null | sort)"
cs run "$RUN_AGENT" "Reply with the single word ACKNOWLEDGED and nothing else." \
  --quiet --timeout "$ASK_TIMEOUT" >"$RUN_LOG" 2>&1 &
RUN_PID=$!

# Poll for the chat the run created, then attach to it while it is still live.
NEW_CHAT=""
waited=0
while (( waited < RUN_ATTACH_TIMEOUT )); do
  if have jq; then
    AFTER_IDS="$(cs chat list "$RUN_AGENT" --format json 2>/dev/null | jq -r '.[]?.id' 2>/dev/null | sort)"
    NEW_CHAT="$(comm -13 <(printf '%s\n' "$BEFORE_IDS") <(printf '%s\n' "$AFTER_IDS") | head -1)"
  fi
  [[ -n "$NEW_CHAT" ]] && break
  # The run process dying before a chat appeared means it never got off the
  # ground — almost always a missing provider credential.
  kill -0 "$RUN_PID" 2>/dev/null || break
  sleep 2; waited=$((waited+2))
done

if [[ -z "$NEW_CHAT" ]]; then
  wait "$RUN_PID" 2>/dev/null
  REASON="no chat appeared within ${RUN_ATTACH_TIMEOUT}s"
  if grep -qi "no active .* credential\|provider:" "$RUN_LOG" 2>/dev/null; then
    REASON="no provider credential in this workspace — $(head -c 120 "$RUN_LOG" | tr '\n' ' ')"
  fi
  skip "live run stream" "$REASON"
  rm -f "$RUN_LOG"
  finish
fi

info "attaching to live chat $NEW_CHAT"
STREAM_OUT="$(cs chat stream "$NEW_CHAT" --format ndjson --idle 60 --quiet 2>&1)"; STREAM_RC=$?
wait "$RUN_PID" 2>/dev/null
rm -f "$RUN_LOG"

assert_eq "live stream exits 0" "0" "$STREAM_RC"
assert_contains "live stream opened" "$STREAM_OUT" '"type":"stream.open"'

# The run may have finished in the window between the chat row appearing and
# the attach. That is a legitimate race, not a defect — but it proves nothing
# about live delivery, so say so rather than pass on it.
if printf '%s' "$STREAM_OUT" | grep -q '"reason":"no_active_run"'; then
  skip "live run output arrives" "the run completed before the stream attached (race, not a failure)"
else
  assert_contains "live stream saw the run finish" "$STREAM_OUT" '"reason":"run_complete"'
  assert_contains "live stream delivered a terminal done" "$STREAM_OUT" '"type":"done"'
  if have jq; then
    REPLY="$(printf '%s\n' "$STREAM_OUT" | jq -r 'select(.type=="text") | .content' 2>/dev/null | tr -d '\n')"
    assert_nonempty "live stream delivered the agent's text" "$REPLY"
    # Sequence numbers must be strictly increasing — that is what makes the
    # resume watermark meaningful.
    SEQS="$(printf '%s\n' "$STREAM_OUT" | jq -r 'select(.seq != null) | .seq' 2>/dev/null)"
    if [[ -z "$SEQS" ]]; then
      _fail "run frames carry seq" "no frame carried a sequence number"
    elif [[ "$SEQS" == "$(printf '%s' "$SEQS" | sort -n -u)" ]]; then
      _pass "run frames carry strictly increasing seq"
    else
      _fail "run frames carry strictly increasing seq" "seqs: $(printf '%s' "$SEQS" | tr '\n' ' ')"
    fi
  fi
fi

finish

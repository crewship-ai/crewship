#!/usr/bin/env bash
# Agent-run NDJSON stream (#1818) — runtime validation against a live server.
#
# The whole point of `GET /api/v1/chats/{chatId}/stream` is that an agent in a
# shell can watch a run WITHOUT a WebSocket client. That claim is only provable
# by driving the real CLI against a real server: a unit test can assert the
# handler writes frames, but it cannot show that the shipped binary, the
# shipped route, the auth chain and the frame contract line up end to end.
#
# Three tiers, and the suite says which ones it ran:
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
# Tier B drives `cs run`, which goes over the WebSocket — the path that has
# always worked.
#
#   Tier C — a ROUTINE-triggered run, requires a provider credential.
#     The case #1823 exists for. A routine's agent_run step never touches the
#     WebSocket: it calls orch.RunAgent from the pipeline runner. Before #1823
#     that produced nothing on `session:{chatId}`, so attaching answered
#     stream.open{active:false} → stream.end/no_active_run with zero output for
#     a run that was demonstrably executing. This tier fails on exactly that
#     answer, which is why it could not have passed before the fix.
#
# Tier C is the regression signal for every non-WebSocket dispatch path
# (scheduler, webhook, pipeline step, agent-start IPC): they all publish through
# the one chokepoint the routine path exercises here.
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

# ── Shared between Tier B and Tier C ───────────────────────────────────────

# await_new_chat <agent-slug> <before-ids> <pid> <timeout-secs>
#
# Echo the id of the first chat that appears for <agent-slug> which was not in
# <before-ids>, or nothing if none does. Both tiers need it: nothing that starts
# a run hands back the chat id, so the only way to attach to a run somebody else
# started is to watch the agent's chat list. Gives up when the launching process
# is gone — a run that died before creating a chat never got off the ground.
await_new_chat() {
  local agent="$1" before="$2" pid="$3" budget="$4"
  local after found waited=0
  while (( waited < budget )); do
    after="$(cs chat list "$agent" --format json 2>/dev/null | jq -r '.[]?.id' 2>/dev/null | sort)"
    found="$(comm -13 <(printf '%s\n' "$before") <(printf '%s\n' "$after") | head -1)"
    if [[ -n "$found" ]]; then
      printf '%s' "$found"
      return 0
    fi
    kill -0 "$pid" 2>/dev/null || return 0
    sleep 2; waited=$((waited+2))
  done
}

# assert_increasing_seq <label> <ndjson>
#
# Sequence numbers must be strictly increasing across one stream's frames — that
# is the entire basis of `--last-seq` resume, and the property a second,
# divergent implementation of the recorder would silently break. Asserted on
# both tiers because they exercise two different producers of those numbers.
assert_increasing_seq() {
  local label="$1" out="$2" seqs
  seqs="$(printf '%s\n' "$out" | jq -r 'select(.seq != null) | .seq' 2>/dev/null)"
  if [[ -z "$seqs" ]]; then
    _fail "$label" "no frame carried a sequence number"
  elif [[ "$seqs" == "$(printf '%s' "$seqs" | sort -n -u)" ]]; then
    _pass "$label"
  else
    _fail "$label" "seqs: $(printf '%s' "$seqs" | tr '\n' ' ')"
  fi
}

# ── Tier B: a live run ─────────────────────────────────────────────────────

RUN_AGENT="$(printf '%s\n' "$AGENT_SLUGS" | head -1)"

# Tier B is a function so its early exits are `return`, not `finish`: Tier C
# below is the one that covers #1823 and must run even when Tier B has nothing
# to work with.
tier_b() {
section "Tier B · watching a run somebody else started"

if [[ -z "$RUN_AGENT" ]]; then
  skip "live run stream" "no agents in this workspace"
  return 0
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
# Without jq there is no way to read an id out of the list, so the tier has
# nothing to attach to and falls through to its SKIP below.
NEW_CHAT=""
if have jq; then
  NEW_CHAT="$(await_new_chat "$RUN_AGENT" "$BEFORE_IDS" "$RUN_PID" "$RUN_ATTACH_TIMEOUT")"
fi

if [[ -z "$NEW_CHAT" ]]; then
  wait "$RUN_PID" 2>/dev/null
  REASON="no chat appeared within ${RUN_ATTACH_TIMEOUT}s"
  if grep -qi "no active .* credential\|provider:" "$RUN_LOG" 2>/dev/null; then
    REASON="no provider credential in this workspace — $(head -c 120 "$RUN_LOG" | tr '\n' ' ')"
  fi
  skip "live run stream" "$REASON"
  rm -f "$RUN_LOG"
  return 0
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
    assert_increasing_seq "run frames carry strictly increasing seq" "$STREAM_OUT"
  fi
fi
}

tier_b

# ── Tier C: a run NOBODY sent over the WebSocket ────────────────────────────
#
# The assertion this whole issue turns on. A routine's `agent_run` step reaches
# the agent through internal/pipeline/runner_orchestrator.go → orch.RunAgent —
# no socket, no send_message, no `cs run`. Before #1823 that published nothing
# on `session:{chatId}`: every attach answered no_active_run and exited 0 with
# no output, so the docs had to say so and this suite had no signal at all.
#
# Note what is NOT special-cased here: the scheduler, the webhook trigger and
# the agent-start IPC take the same chokepoint, so a regression on any of them
# reddens this tier too.

section "Tier C · watching a ROUTINE-triggered run (#1823)"

# How long to keep re-attaching while the routine works its way to the agent
# step. Cold container + first token lives inside this on a dev slot.
ROUTINE_STREAM_TIMEOUT="${ROUTINE_STREAM_TIMEOUT:-180}"

tier_c() {
if ! have jq; then
  skip "routine run stream" "needs jq to discover the step's chat"
  return 0
fi
if [[ -z "$RUN_AGENT" ]]; then
  skip "routine run stream" "no agents in this workspace"
  return 0
fi

# The routine is owned by a crew, and its agent_slug resolves against THAT crew
# (the cross-crew reuse contract in `routine save`). Read the crew off the agent
# rather than guessing a name.
RUN_CREW="$(cs agent get "$RUN_AGENT" --format json 2>/dev/null \
  | jq -r '.crew_slug // .crew.slug // .crew // empty' 2>/dev/null)"
if [[ -z "$RUN_CREW" || "$RUN_CREW" == "null" ]]; then
  skip "routine run stream" "could not resolve the crew that owns agent $RUN_AGENT"
  return 0
fi

# Lowercase kebab-case is a server-side constraint on the DSL's `name`
# (422 otherwise); nonce() emits uppercase for model-echo reliability, which is
# not what this needs.
R_SLUG="hs-stream-$(nonce p | tr '[:upper:]' '[:lower:]')"
R_DSL="$(mktemp -t cs-stream-routine.XXXXXX)"
cat > "$R_DSL" <<JSON
{ "name":"$R_SLUG","description":"#1823 — routine-triggered run must be watchable","dsl_version":"1.0",
  "steps":[{"id":"say","type":"agent_run","agent_slug":"$RUN_AGENT",
            "prompt":"Reply with the single word ACKNOWLEDGED and nothing else."}] }
JSON

if ! R_SAVE="$(cs routine save --name "$R_SLUG" --description "1823 stream probe" \
      --definition "$R_DSL" --author-crew "$RUN_CREW" 2>&1)"; then
  rm -f "$R_DSL"
  # Report what the server said. A silenced save turns every environment
  # problem (slug rules, a crew with no such agent, a missing tier config) into
  # the same blank SKIP, which is how a suite quietly stops testing anything.
  skip "routine run stream" "could not save the probe routine (crew=$RUN_CREW agent=$RUN_AGENT): $(printf '%s' "$R_SAVE" | tr '\n' ' ' | head -c 200)"
  return 0
fi
rm -f "$R_DSL"
info "routine $R_SLUG saved (crew $RUN_CREW, agent $RUN_AGENT)"

# The probe routine is disposable — soft-delete it on every exit from here on
# so a suite run does not leave a new `hs-stream-*` in the registry each time.
_tier_c_cleanup() { cs routine delete "$R_SLUG" --yes >/dev/null 2>&1 || true; }

R_LOG="$(mktemp -t cs-stream-routine-run.XXXXXX)"
R_BEFORE="$(cs chat list "$RUN_AGENT" --format json 2>/dev/null | jq -r '.[]?.id' 2>/dev/null | sort)"
cs routine run "$R_SLUG" >"$R_LOG" 2>&1 &
R_PID=$!

# The step creates its chat row BEFORE it starts the agent, so the id is
# discoverable while the run is still in front of us.
R_CHAT="$(await_new_chat "$RUN_AGENT" "$R_BEFORE" "$R_PID" "$ROUTINE_STREAM_TIMEOUT")"

if [[ -z "$R_CHAT" ]]; then
  wait "$R_PID" 2>/dev/null
  REASON="no chat appeared within ${ROUTINE_STREAM_TIMEOUT}s"
  if grep -qi "no active .* credential\|provider:" "$R_LOG" 2>/dev/null; then
    REASON="no provider credential in this workspace — $(head -c 120 "$R_LOG" | tr '\n' ' ')"
  fi
  skip "routine run stream" "$REASON — $(head -c 160 "$R_LOG" | tr '\n' ' ')"
  rm -f "$R_LOG"
  _tier_c_cleanup
  return 0
fi
info "routine step chat: $R_CHAT"

# Attach with --follow, and retry while the routine is still working.
#
# --follow is what removes the race, and the race is real: the chat row exists
# before the agent starts, so a plain attach can land during container start,
# get the documented `no_active_run` answer for an idle chat, and return before
# the run produces anything. With --follow the connection instead WAITS for the
# run — so a step that takes three seconds end to end is watched rather than
# missed. The cost is that the stream deliberately outlives the run and closes
# on `idle_timeout` rather than `run_complete`; the terminal `done` frame is the
# run-finished signal asserted below.
#
# The retry loop covers the other direction: a cold container start longer than
# one idle window. It stops when frames arrive, or when the routine process is
# gone and nothing ever did.
#
# Before #1823 no attach ever delivered a frame, so this loop ran out against a
# routine that was demonstrably executing and the assertion below failed on an
# empty transcript. That is the red this tier exists to produce.
R_IDLE="${ROUTINE_STREAM_IDLE:-25}"
R_OUT=""; R_RC=0; attached=0; waited=0
while (( waited < ROUTINE_STREAM_TIMEOUT )); do
  R_OUT="$(cs chat stream "$R_CHAT" --format ndjson --follow --idle "$R_IDLE" --quiet 2>&1)"; R_RC=$?
  if printf '%s\n' "$R_OUT" | grep -qE '"type":"(run_begin|text|thinking|tool_call|done)"'; then
    attached=1
    break
  fi
  kill -0 "$R_PID" 2>/dev/null || break
  waited=$((waited+R_IDLE))
done
wait "$R_PID" 2>/dev/null

if (( ! attached )); then
  _fail "routine-triggered run publishes on the session channel" \
    "every attach to $R_CHAT answered with no run frames while the routine was executing — this is #1823's failure mode. last stream: $(printf '%s' "$R_OUT" | head -c 200) | routine: $(head -c 200 "$R_LOG" | tr '\n' ' ')"
  rm -f "$R_LOG"
  _tier_c_cleanup
  return 0
fi
_pass "routine-triggered run publishes on the session channel"
rm -f "$R_LOG"
_tier_c_cleanup

assert_contains "routine stream opened" "$R_OUT" '"type":"stream.open"'
assert_contains "routine stream delivered a terminal done" "$R_OUT" '"type":"done"'

# Exit status is a contract of its own: 0 for a run that finished, non-zero
# when the stream carried an agent error. Asserting a flat 0 would call a
# correctly-reported failure a bug.
if printf '%s\n' "$R_OUT" | grep -q '"type":"error"'; then
  if (( R_RC != 0 )); then
    _pass "routine stream exits non-zero when the run errored"
  else
    _fail "routine stream exits non-zero when the run errored" "exit 0 despite an error frame"
  fi
else
  assert_eq "routine stream exits 0" "0" "$R_RC"
fi

# Sequenced the same way a chat-initiated run's frames are — same assertion,
# different producer, which is the point of running it on both tiers.
assert_increasing_seq "routine run frames carry strictly increasing seq" "$R_OUT"

# The agent's own words. A run that failed (no credential, container refused)
# still produces run_begin + error + done — real frames, and enough to prove the
# channel works — so report that case rather than failing on it.
R_TEXT="$(printf '%s\n' "$R_OUT" | jq -r 'select(.type=="text") | .content' 2>/dev/null | tr -d '\n')"
if [[ -n "$R_TEXT" ]]; then
  _pass "routine stream delivered the agent's text"
elif printf '%s\n' "$R_OUT" | grep -q '"type":"error"'; then
  skip "routine stream delivered the agent's text" \
    "the routine's run failed and the stream reported it: $(printf '%s\n' "$R_OUT" | jq -r 'select(.type=="error") | .content' 2>/dev/null | head -c 160)"
else
  _fail "routine stream delivered the agent's text" "no text and no error frame: $(printf '%s' "$R_OUT" | head -c 200)"
fi

# Every line still has to be a standalone JSON object on this path too.
if printf '%s\n' "$R_OUT" | grep -v '^$' | jq -e . >/dev/null 2>&1; then
  _pass "every routine-stream line parses as JSON"
else
  _fail "every routine-stream line parses as JSON" "$(printf '%s' "$R_OUT" | head -c 200)"
fi
}

tier_c

finish

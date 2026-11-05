#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Crew links — does a link actually let one crew hand work to another?
#
# This is the test that would have caught what an audit found on 2026-07-29:
# the link existed, was active, and was enforced server-side — and cross-crew
# delegation still could not happen. The sidecar rejected any target outside
# its own crew and then sent its OWN crew id anyway, so the "are these crews
# linked" branch was unreachable; and the lead's prompt never named the crews
# it could reach, so the model reported a linked crew as unreachable.
#
# Unit tests cannot see that: every piece passed its own test. Only driving the
# real CLI against a real server, with a real agent in a real container, shows
# whether the feature exists.
#
# Engineering crew: alex(lead) · sam · robin. Ops crew: morgan(lead) · riley.
#
# If section 3 fails while section 4 passes, suspect a STALE SIDECAR before
# suspecting the code: the sidecar is bind-mounted into the crew container and
# its process starts with the container, so a container that has been up since
# before the deploy is still running the old binary no matter how many times
# dev.sh rebuilt it. Drop it and re-run:
#
#   crewship crew restart-agents engineering

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"
# shellcheck source=crew-links-evidence.sh
source "$HERE/crew-links-evidence.sh"
keep_failure_evidence  # #4874: keep transcripts of a failing run before the gauntlet reseed

preflight

# ─────────────────────────────────────────────────────────────────────────────
section "1. The link graph is real data, not decoration"
# ─────────────────────────────────────────────────────────────────────────────

# Start from a known state: engineering ↔ ops linked both ways.
cs crew connect engineering ops --direction bidirectional >/dev/null 2>&1 || true
links="$(cs crew connections 2>/dev/null)"
assert_contains "crew connections lists the engineering↔ops link" "$links" "engineering"

# Every row must name two crews that still exist. Soft-deleted crews keep
# their id with a _deleted_ suffix, and those rows used to outnumber the live
# ones 24:3 on a reseeded instance.
assert_not_contains "no links to deleted crews are listed" "$links" "_deleted_"

# ─────────────────────────────────────────────────────────────────────────────
section "2. A lead knows which crews it can reach"
# ─────────────────────────────────────────────────────────────────────────────

info "Asking alex (engineering lead) what its sidecar reports as linked…"
seen="$(ask_agent alex "Run exactly: curl -s http://localhost:9119/connections — then reply with ONLY the crew slugs you found, comma separated, nothing else.")"
assert_contains "alex sees ops among its linked crews" "$seen" "ops"

# ─────────────────────────────────────────────────────────────────────────────
section "3. Delegation ACROSS a live link reaches the other crew"
# ─────────────────────────────────────────────────────────────────────────────

TAG="$(nonce XLINK)"
RECORD_CODE="$(nonce VERIFIED)"

# WHAT COUNTS AS EVIDENCE THAT THE LINK CARRIED WORK.
#
# This used to be "did morgan's chat count go up", which is a proxy two
# layers away from the thing under test. `/assign` writes a durable
# assignment row (internal/api/assignments_run.go), and `crewship activity`
# is the cross-crew feed that surfaces it — assignment.created/running/
# completed/failed plus peer.conversation, resolved to participant slugs.
# That row is the artifact of a delegation actually crossing the link, so
# assert on it and stop inferring from chat bookkeeping.
#
# Any NEW feed entry addressed to morgan counts: an assignment row is the
# expected shape, but a peer.conversation is also work crossing the link,
# and this suite is about the link, not about which mechanism carried it.
# CORRELATED TO THIS DELEGATION, not merely addressed to morgan.
#
# "any new row with to_slug == morgan" was the first cut and it is unsound:
# the harness runs suites in sequence against a shared slot, and anything
# else that assigns to morgan inside the window would mark this suite green
# without a single request having crossed the link. On a promotion gate a
# false green is the expensive direction, so the nonce that already
# identifies this delegation does the correlating.
#
# TAG rides in the task text alex passes to /assign, which the journal entry
# carries as payload.task and echoes into summary (assignments_run.go builds
# it as "assigned <slug> → <name>: <task preview>"). Entry types are pinned
# to the documented cross-crew set so an unrelated type cannot satisfy it.
#
# The trade, stated rather than hidden: if the model delegates but drops the
# tag from the task, this reads as no evidence. That is the correct bias —
# it fails loudly instead of passing on somebody else's assignment.
#
# A tag in alex's REPLY is not evidence (#4855): the prompt hands alex the
# tag, so a refusal that quotes it ("I could not get Morgan to say
# XLINK-…-OK") reads exactly like a delivered answer. On stage ff4c164 that
# certified a crossing the server had refused with 403. Only the
# tag-correlated artifact decides, in both halves of the suite.
# Non-zero when the feed cannot be read: an empty answer must never stand in
# for "no activity".
xlink_to_morgan_ids() { # [tag] — defaults to this delegation's TAG
  local feed
  feed="$(cs activity --since 30m --lines 200 --export ndjson 2>/dev/null)" || return 1
  printf '%s\n' "$feed" |
    jq -r --arg tag "${1:-$TAG}" '
      select(.to_slug == "morgan")
      | select(.entry_type | test("^(assignment\\.|peer\\.conversation)"))
      | select(((.summary // "") + " " + (.payload.task // "")) | contains($tag))
      | .id' 2>/dev/null | sort -u
}

XLINK_HAVE_JQ=0
have jq && XLINK_HAVE_JQ=1
before_ids="$(mktemp)"; after_ids="$(mktemp)"
xlink_removed=0
# Invoked indirectly by the EXIT trap below, including early exits.
# shellcheck disable=SC2317
restore_xlink() {
  if (( xlink_removed == 1 )); then
    timeout --signal=TERM --kill-after=1s 3s "$CREWSHIP" --server "$SERVER" \
      ${_CS_ARGS[@]+"${_CS_ARGS[@]}"} crew connect engineering ops --direction bidirectional >/dev/null 2>&1 || true
  fi
}
# One composed EXIT handler: capture before restoring and retain signal status.
# The parent run-all waits for this registered child before the scan can reseed.
# shellcheck disable=SC2317
crew_links_exit() {
  local original=$?
  trap - EXIT
  trap '' TERM INT
  if (( original == 143 || original == 130 )); then preserve_interrupted_issues "$original"; fi
  restore_xlink
  rm -f "$before_ids" "$after_ids"
  _state_cleanup  # Existing $$ owner guard: never another suite's state.
  return "$original"
}
trap crew_links_exit EXIT
trap 'exit 143' TERM
trap 'exit 130' INT
feed_ok=1
if (( XLINK_HAVE_JQ == 1 )); then xlink_to_morgan_ids > "$before_ids" || feed_ok=0; fi

# GROUNDED WORK (#4873). A bare "delegate this, have Morgan say <tag>" prompt
# is declined by the seeded lead as an unverified relay before /assign is ever
# called (stage 70a74af: three refusals, no request reached the server), which
# says nothing about the link. Spec (docs/guides/orchestration.mdx, "Cross-Crew
# Work"): "A lead assigns across a link by naming the crew", and its result
# comes back from /results/{assignment_id}. So the request is an engineering
# issue delegated to alex whose work IS that hand-off; the tag rides in the
# task so the assignment row can be correlated. Success is unchanged: a new
# tag-correlated cross-crew entry addressed to morgan — and now also its
# completion, so the hand-off is proven to have produced a result.
# This Ops record is real, readable work for Morgan: report a code from an
# operator-created issue, without certifying a release that never happened.
ops_record="$(cs issue create --crew ops --status BACKLOG --title "Cross-crew test record $TAG" \
  --description "This operator-created record exercises a linked crew hand-off. Verification code: $RECORD_CODE. It makes no claim about a production release or on-call readiness." --format json 2>/dev/null)" || ops_record=""
ops_record_id="$(printf '%s' "$ops_record" | jq -r '.id // empty' 2>/dev/null)"
ops_record_ident="$(printf '%s' "$ops_record" | jq -r '.identifier // .id // empty' 2>/dev/null)"
if [[ -z "$ops_record_id" || -z "$ops_record_ident" ]]; then
  _fail "a real Ops record grounds the delegation" "the Ops issue could not be created"
  finish
fi
_state_add issues "$ops_record_ident"
info "Opening an engineering issue that alex (lead) hands to Ops/morgan (tag $TAG)…"
echoed=0
landed=0
xlink_work="$(issue_work engineering alex "Read the Ops verification record ($TAG)" \
  "Ops issue $ops_record_ident (id $ops_record_id) is an actual operator-created record for this test. Ops is linked to our crew. As engineering lead, assign this to morgan on ops through /assign with crew \"ops\". Include $TAG and issue id $ops_record_id in the task. Ask Morgan to read GET /mission/$ops_record_id, report the verification code in the issue description, and identify the record read. Wait for Morgan's result and report it here. This is record verification, not release certification.")" || true
info "issue run: ${xlink_work:-none}"
if (( XLINK_HAVE_JQ == 1 )); then
  xlink_to_morgan_ids > "$after_ids" || feed_ok=0
  [ -n "$(comm -13 "$before_ids" "$after_ids")" ] && landed=1
fi
xlink_ident="${xlink_work%% *}"
if [[ -n "$xlink_ident" ]] && cs issue runs "$xlink_ident" --format json 2>/dev/null \
    | jq -r '.[] | (.result_summary // "") + " " + (.error_message // "")' 2>/dev/null | grep -qiF -- "$TAG"; then
  echoed=1
fi

if (( XLINK_HAVE_JQ == 1 )) && (( landed == 0 )); then
  xlink_to_morgan_ids > "$after_ids" || feed_ok=0
  [ -n "$(comm -13 "$before_ids" "$after_ids")" ] && landed=1
fi

xlink_positive_receipt=""
xlink_projection_error=0
xlink_completed() {
  xlink_assignment_completed "$TAG" || return 1
  local rc
  xlink_positive_receipt="$(xlink_result_evidence "$xlink_ident" "$RECORD_CODE" "$TAG")"
  rc=$?
  # A projection error ends polling immediately; no extra paid action/retry.
  if (( rc > 1 )); then xlink_projection_error=1; return 0; fi
  return "$rc"
}
if (( landed == 1 )); then
  _pass "delegation crossed the link — new cross-crew activity addressed to morgan"
  waited=0
  until xlink_completed || (( waited >= ASK_TIMEOUT )); do sleep "$POLL_INTERVAL"; waited=$((waited+POLL_INTERVAL)); done
  if xlink_completed; then
    _pass "the cross-crew assignment completed with a result"
  else
    _fail "the cross-crew assignment completed with a result" "no completion joined to the tagged assignment plus Morgan result reporting the real record code within ${ASK_TIMEOUT}s"
  fi
elif (( XLINK_HAVE_JQ == 0 )); then
  # No jq means no way to read the feed, so there is no evidence either way.
  # That is a missing tool, not a failing product — say which.
  skip "cross-crew delegation over a live link" \
    "jq is not installed, so the activity feed could not be read — install jq to make this assertion"
elif (( feed_ok == 0 )); then
  _fail "cross-crew delegation over a live link" \
    "\`crewship activity\` could not be read, so there is no evidence either way — the CLI feed this suite depends on failed"
elif (( echoed == 1 )); then
  _fail "cross-crew delegation over a live link" \
    "alex's run result contains $TAG, but no cross-crew activity addressed to morgan carries it. The issue itself contains the tag, so an echo (for example a refusal quoting it) is not evidence that work crossed the link"
else
  # FAIL, and deliberately so.
  #
  # This was briefly a `skip`, matching test-delegation.sh, after the suite
  # flaked hard: it PASSED at 14:01 and FAILED at 18:25 on 2026-08-07
  # against the identical sha, fail/fail/pass/pass/pass/fail across the
  # week. But skipping answered the wrong question. stage exists to decide
  # whether a build is promotable as a REAL application, and "a lead agent
  # can hand work to a linked crew" is the feature this suite is named for.
  # A suite that shrugs when that does not happen certifies nothing.
  #
  # What was actually wrong was the EVIDENCE, not the verdict: success was
  # inferred from morgan's chat count, a proxy that misses a delegation
  # landing as an assignment row. Now it reads the artifact `/assign`
  # actually writes, so reaching this branch means three explicit attempts
  # produced no assignment, no peer conversation, and no echoed tag.
  #
  # If that still flakes, it is not the test being unfair — it is
  # cross-crew delegation being unreliable, and that is exactly the kind of
  # thing a promotion gate is supposed to refuse to certify.
  _fail "cross-crew delegation over a live link" \
    "the issue run (${xlink_work:-not started}) produced no echoed tag AND no new cross-crew activity addressed to morgan (checked \`crewship activity\` for assignment/peer entries). The link is live — sections 1 and 2 pass — so work is not crossing it"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "4. …and is refused once the link is gone"
# ─────────────────────────────────────────────────────────────────────────────

conn_id="$(cs crew connections 2>/dev/null | awk '/engineering/ && /ops/ && !/_deleted_/ {print $2; exit}')"
if [[ -z "$conn_id" ]]; then
  skip "cross-crew refusal without a link" "could not resolve the engineering↔ops connection id"
else
  info "Removing the link ($conn_id) for the negative half…"
  if ! cs crew disconnect "$conn_id" >/dev/null 2>&1; then
    _fail "the link is removed for the negative scenario" "disconnect failed, so no unlinked call can be tested"
    finish
  fi
  xlink_removed=1

  NEG="$(nonce XLINKNEG)"
  neg_before="$(mktemp)"; neg_after="$(mktemp)"
  neg_feed_ok=1
  if (( XLINK_HAVE_JQ == 1 )); then xlink_to_morgan_ids "$NEG" > "$neg_before" || neg_feed_ok=0; fi

  # Require a server-authored receipt for an actual /assign attempt. A model
  # refusing the task itself or merely quoting a 403 does not exercise the gate.
  source_crew_id="$(cs crew get engineering --format json 2>/dev/null | jq -r '.id // empty')"
  target_crew_id="$(cs crew get ops --format json 2>/dev/null | jq -r '.id // empty')"
  wait_agent_free alex
  neg_ran=1
  neg_work="$(issue_work engineering alex "Ops confirmation needed ($NEG)" \
    "Attempt the same record verification for Ops issue $ops_record_ident (id $ops_record_id), even though the link was just removed for this authorization test. Call /assign targeting morgan with crew \"ops\" and task containing $NEG and issue id $ops_record_id. If refused, report the exact error; do not certify a release or invent a verification result.")" || neg_ran=0
  # Only a COMPLETED run counts: one that FAILED or was CANCELLED at once
  # never got to attempt the hand-off.
  [[ "$neg_work" == *" COMPLETED" ]] || neg_ran=0
  neg_ident="${neg_work%% *}"
  neg_reply=""
  [[ -n "$neg_ident" ]] && neg_reply="$(cs issue runs "$neg_ident" --format json 2>/dev/null \
    | jq -r '.[] | (.result_summary // "") + " " + (.error_message // "")' 2>/dev/null)"
  [[ -n "$neg_ident" ]] && neg_reply+="$(cs issue comments "$neg_ident" 2>/dev/null)"

  neg_refusal_proved=0
  neg_work_landed=0
  xlink_negative_receipt=""
  neg_projection_error=0
  xlink_refusal_observed() {
    local rc
    xlink_negative_receipt="$(xlink_refusal_evidence "$neg_ident" "$source_crew_id" "$target_crew_id")"
    rc=$?
    if (( rc > 1 )); then neg_projection_error=1; return 0; fi
    return "$rc"
  }

  # Same evidence rule as section 3: a correct refusal may quote ${NEG}-OK,
  # so the reply text cannot decide. Without jq there is no artifact to read,
  # so missing jq leaves this authorization assertion explicitly unproven.
  if (( neg_ran == 0 )); then
    _fail "the lead's unlinked hand-off issue ran to an end" "${neg_work:-the issue could not be created or started}"
    _fail "an unlinked crew cannot be delegated to" "the hand-off run never ended, so the refusal was not exercised"
  elif (( XLINK_HAVE_JQ == 1 )); then
    _pass "the lead's unlinked hand-off issue ran to an end"
    xlink_to_morgan_ids "$NEG" > "$neg_after" || neg_feed_ok=0
    if (( neg_feed_ok == 0 )); then
      _fail "an unlinked crew cannot be delegated to" "\`crewship activity\` could not be read, so the refusal is unproven"
    elif [ -n "$(comm -13 "$neg_before" "$neg_after")" ]; then
      neg_work_landed=1
      _fail "an unlinked crew cannot be delegated to" "new cross-crew activity addressed to morgan carries $NEG with no link in place"
    elif [[ -z "$source_crew_id" || -z "$target_crew_id" ]]; then
      _fail "an unlinked crew cannot be delegated to" "crew identities could not be read"
    else
      waited=0
      until xlink_refusal_observed || (( waited >= ASK_TIMEOUT )); do
        sleep "$POLL_INTERVAL"; waited=$((waited+POLL_INTERVAL))
      done
      if xlink_refusal_observed; then
        neg_refusal_proved=1
        _pass "an unlinked crew cannot be delegated to — verified server 403 receipt"
      else
        _fail "an unlinked crew cannot be delegated to" "no server refusal receipt for alex's actual run, the target Ops crew and morgan; model refusal or no attempt is insufficient"
      fi
    fi
  else
    skip "an unlinked crew cannot be delegated to" "jq is required to verify the server refusal receipt"
  fi
  rm -f "$neg_before" "$neg_after"
  if printf '%s' "$neg_reply" | grep -qiF "not connected"; then
    info "the lead reports the link refusal on ${neg_ident} (agent text, not server evidence)"
  else
    info "the lead's run on ${neg_ident:-the issue} does not quote the link refusal"
  fi
  if (( neg_refusal_proved == 0 )); then
    _fail "no work reaches an unlinked crew" "there is no verified attempt and server refusal, so absence of work proves nothing"
  elif (( neg_work_landed == 1 )); then
    _fail "no work reaches an unlinked crew" "tag-correlated cross-crew activity landed with no link in place"
  else
    _pass "no work reaches an unlinked crew"
  fi

  # Always put the graph back: the other suites assume the seeded topology.
  info "Restoring the engineering↔ops link…"
  if cs crew connect engineering ops --direction bidirectional >/dev/null 2>&1; then xlink_removed=0; fi
  restored="$(cs crew connections 2>/dev/null)"
  assert_contains "the link is restored for the following suites" "$restored" "engineering"
fi

# Retain success before finish deletes volatile state and the gauntlet reseeds.
# No response/transcript/code enters this receipt or the public harness output.
if (( _FAIL == 0 && _SKIP == 0 && _XFAIL == 0 )) && [[ -n "${HARNESS_DETAIL_DIR:-}" ]]; then
  if (( xlink_projection_error == 1 || neg_projection_error == 1 )); then
    _HARNESS_KEEP_EVIDENCE=0  # No new API/transcript reads for a retention-only failure.
    _fail "crew-link success evidence is durably retained" "a passing observation could not be projected; no receipt can certify it"
  elif ! printf '%s\0' "${xlink_positive_receipt:-}" "${xlink_negative_receipt:-}" \
       "$ops_record_id" "$ops_record_ident" "${source_crew_id:-}" "${target_crew_id:-}" \
       "$_HARNESS_T0" "$_PASS" "$xlink_removed" "${neg_work_landed:-1}" | \
       python3 "$HERE/crew-links-receipt.py" write "$HARNESS_DETAIL_DIR"; then
    _HARNESS_KEEP_EVIDENCE=0  # Existing functional failures retain their normal evidence.
    _fail "crew-link success evidence is durably retained" "private receipt persistence failed; the configured gate is incomplete"
  fi
fi

finish

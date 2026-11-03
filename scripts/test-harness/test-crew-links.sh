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
trap 'rm -f "$before_ids" "$after_ids"' EXIT
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
info "Opening an engineering issue that alex (lead) hands to Ops/morgan (tag $TAG)…"
echoed=0
landed=0
xlink_work="$(issue_work engineering alex "Ops confirmation needed for the next release ($TAG)" \
  "Engineering needs the Ops lead to confirm on-call readiness for the next release. Ops is linked to our crew. As engineering lead, assign this to morgan on the ops crew through /assign with crew \"ops\", with the task text: 'Confirm on-call readiness for release ${TAG} and reply with ${TAG}-OK when confirmed.' Wait for morgan's result and report it on this issue.")" || true
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

xlink_completed() { # 0 when the feed holds a tag-correlated completion for morgan
  cs activity --since 30m --lines 200 --export ndjson 2>/dev/null | jq -e --arg tag "$TAG" '
    select(.to_slug == "morgan" and .entry_type == "assignment.completed")
    | select(((.summary // "") + " " + (.payload.task // "")) | contains($tag))' >/dev/null 2>&1
}
if (( landed == 1 )); then
  _pass "delegation crossed the link — new cross-crew activity addressed to morgan"
  waited=0
  until xlink_completed || (( waited >= ASK_TIMEOUT )); do sleep "$POLL_INTERVAL"; waited=$((waited+POLL_INTERVAL)); done
  if xlink_completed; then
    _pass "the cross-crew assignment completed with a result"
  else
    _fail "the cross-crew assignment completed with a result" "no assignment.completed for morgan carrying $TAG within ${ASK_TIMEOUT}s"
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
  cs crew disconnect "$conn_id" >/dev/null 2>&1

  NEG="$(nonce XLINKNEG)"
  morgan_before2=0
  have jq && morgan_before2="$(cs chat list morgan --format json 2>/dev/null | jq 'length' 2>/dev/null || echo 0)"

  neg_before="$(mktemp)"; neg_after="$(mktemp)"
  neg_feed_ok=1
  if (( XLINK_HAVE_JQ == 1 )); then xlink_to_morgan_ids "$NEG" > "$neg_before" || neg_feed_ok=0; fi

  # The same grounded hand-off, now with no link. The server refuses an
  # unlinked /assign with "crews are not connected"
  # (internal/api/assignments_run.go), but that 403 is not journaled and an
  # assignment run's tool results are not stored as a chat, so the refusal
  # itself leaves no server record to assert on. What the records can prove:
  # the lead's run on this issue ran to an end, and no work for morgan landed.
  # A run that never ended tests nothing, so it fails both checks. The lead's
  # own account of the refusal is printed as information only.
  wait_agent_idle alex
  neg_ran=1
  neg_work="$(issue_work engineering alex "Ops confirmation needed ($NEG)" \
    "Engineering needs the Ops lead to confirm on-call readiness. As engineering lead, assign this to morgan on the ops crew through /assign with crew \"ops\", task text: 'Confirm on-call readiness for ${NEG}.' If the assignment is refused, report the exact error message on this issue.")" || neg_ran=0
  # Only a COMPLETED run counts: one that FAILED or was CANCELLED at once
  # never got to attempt the hand-off.
  [[ "$neg_work" == *" COMPLETED" ]] || neg_ran=0
  neg_ident="${neg_work%% *}"
  neg_reply=""
  [[ -n "$neg_ident" ]] && neg_reply="$(cs issue runs "$neg_ident" --format json 2>/dev/null \
    | jq -r '.[] | (.result_summary // "") + " " + (.error_message // "")' 2>/dev/null)"
  [[ -n "$neg_ident" ]] && neg_reply+="$(cs issue comments "$neg_ident" 2>/dev/null)"

  morgan_after2=0
  have jq && morgan_after2="$(cs chat list morgan --format json 2>/dev/null | jq 'length' 2>/dev/null || echo 0)"

  # Same evidence rule as section 3: a correct refusal may quote ${NEG}-OK,
  # so the reply text cannot decide. Without jq there is no artifact to read,
  # and the reply is the only (weaker) signal left.
  if (( neg_ran == 0 )); then
    _fail "the lead's unlinked hand-off issue ran to an end" "${neg_work:-the issue could not be created or started}"
    _fail "an unlinked crew cannot be delegated to" "the hand-off run never ended, so the refusal was not exercised"
  elif (( XLINK_HAVE_JQ == 1 )); then
    _pass "the lead's unlinked hand-off issue ran to an end"
    xlink_to_morgan_ids "$NEG" > "$neg_after" || neg_feed_ok=0
    if (( neg_feed_ok == 0 )); then
      _fail "an unlinked crew cannot be delegated to" "\`crewship activity\` could not be read, so the refusal is unproven"
    elif [ -n "$(comm -13 "$neg_before" "$neg_after")" ]; then
      _fail "an unlinked crew cannot be delegated to" "new cross-crew activity addressed to morgan carries $NEG with no link in place"
    else
      _pass "an unlinked crew cannot be delegated to"
    fi
  else
    assert_not_contains "an unlinked crew cannot be delegated to" "$neg_reply" "$NEG-OK"
  fi
  rm -f "$neg_before" "$neg_after"
  if printf '%s' "$neg_reply" | grep -qiF "not connected"; then
    info "the lead reports the link refusal on ${neg_ident} (agent text, not server evidence)"
  else
    info "the lead's run on ${neg_ident:-the issue} does not quote the link refusal"
  fi
  if (( neg_ran == 0 )); then
    _fail "no work reaches an unlinked crew" "the hand-off run never ended, so nothing was tested"
  elif (( morgan_after2 > morgan_before2 )); then
    _fail "no work reaches an unlinked crew" "a chat for morgan appeared with no link in place ($morgan_before2 → $morgan_after2)"
  else
    _pass "no work reaches an unlinked crew"
  fi

  # Always put the graph back: the other suites assume the seeded topology.
  info "Restoring the engineering↔ops link…"
  cs crew connect engineering ops --direction bidirectional >/dev/null 2>&1
  restored="$(cs crew connections 2>/dev/null)"
  assert_contains "the link is restored for the following suites" "$restored" "engineering"
fi

finish

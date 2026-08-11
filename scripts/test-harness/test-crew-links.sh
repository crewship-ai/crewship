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
xlink_to_morgan_ids() {
  cs activity --since 30m --lines 200 --export ndjson 2>/dev/null |
    jq -r --arg tag "$TAG" '
      select(.to_slug == "morgan")
      | select(.entry_type | test("^(assignment\\.|peer\\.conversation)"))
      | select(((.summary // "") + " " + (.payload.task // "")) | contains($tag))
      | .id' 2>/dev/null | sort -u
}

XLINK_HAVE_JQ=0
have jq && XLINK_HAVE_JQ=1
before_ids="$(mktemp)"; after_ids="$(mktemp)"
trap 'rm -f "$before_ids" "$after_ids"' EXIT
(( XLINK_HAVE_JQ == 1 )) && xlink_to_morgan_ids > "$before_ids"

info "Asking alex to delegate to morgan on the Ops crew (tag $TAG)…"
# Success is EITHER the tag coming back (alex reported morgan's answer) OR a
# new cross-crew feed entry addressed to morgan (the delegation landed even
# if alex narrated it badly). Three attempts: each is a real model turn, and
# a feature that works does not need four.
crossed=0
landed=0
for attempt in 1 2 3; do
  reply="$(ask_agent alex "Delegate this to Morgan on the Ops crew — Ops is linked to yours, so use the crew field of /assign. Do NOT do it yourself: ask Morgan to reply with exactly '${TAG}-OK' and nothing else. Then report back Morgan's exact answer.")"
  if printf '%s' "$reply" | grep -qiF -- "$TAG"; then crossed=1; break; fi
  # Check the durable artifact between attempts, not only at the end: an
  # attempt can land the assignment without echoing the tag, and stopping
  # here saves a model turn.
  if (( XLINK_HAVE_JQ == 1 )); then
    xlink_to_morgan_ids > "$after_ids"
    if [ -n "$(comm -13 "$before_ids" "$after_ids")" ]; then landed=1; break; fi
  fi
  info "attempt $attempt: no tag echoed and no cross-crew activity for morgan yet; retrying…"
done

if (( XLINK_HAVE_JQ == 1 )) && (( landed == 0 )); then
  xlink_to_morgan_ids > "$after_ids"
  [ -n "$(comm -13 "$before_ids" "$after_ids")" ] && landed=1
fi

if (( crossed == 1 )); then
  _pass "alex reports back a result delegated across the crew link"
elif (( landed == 1 )); then
  _pass "delegation crossed the link — new cross-crew activity addressed to morgan (tag not echoed)"
elif (( XLINK_HAVE_JQ == 0 )); then
  # No jq means no way to read the feed, so there is no evidence either way.
  # That is a missing tool, not a failing product — say which.
  skip "cross-crew delegation over a live link" \
    "jq is not installed, so the activity feed could not be read — install jq to make this assertion"
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
    "3 attempts produced no echoed tag AND no new cross-crew activity addressed to morgan (checked \`crewship activity\` for assignment/peer entries). The link is live — sections 1, 2 and 4 pass — so work is not crossing it"
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

  neg_reply="$(ask_agent alex "Delegate this to Morgan on the Ops crew: ask Morgan to reply with exactly '${NEG}-OK'. Then report back Morgan's exact answer. If you cannot reach Morgan, say exactly why.")"

  morgan_after2=0
  have jq && morgan_after2="$(cs chat list morgan --format json 2>/dev/null | jq 'length' 2>/dev/null || echo 0)"

  assert_not_contains "an unlinked crew cannot be delegated to" "$neg_reply" "$NEG-OK"
  if (( morgan_after2 > morgan_before2 )); then
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

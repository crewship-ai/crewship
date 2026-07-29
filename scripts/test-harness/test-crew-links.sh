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
morgan_before=0
have jq && morgan_before="$(cs chat list morgan --format json 2>/dev/null | jq 'length' 2>/dev/null || echo 0)"

info "Asking alex to delegate to morgan on the Ops crew (tag $TAG)…"
# Same non-determinism rule as test-delegation.sh: success is EITHER the tag
# coming back OR the /assign side effect (a new chat for the target).
crossed=0
for attempt in 1 2; do
  reply="$(ask_agent alex "Delegate this to Morgan on the Ops crew — Ops is linked to yours, so use the crew field of /assign. Do NOT do it yourself: ask Morgan to reply with exactly '${TAG}-OK' and nothing else. Then report back Morgan's exact answer.")"
  if printf '%s' "$reply" | grep -qiF -- "$TAG"; then crossed=1; break; fi
  info "attempt $attempt: tag not echoed back; retrying…"
done

morgan_after=0
have jq && morgan_after="$(cs chat list morgan --format json 2>/dev/null | jq 'length' 2>/dev/null || echo 0)"
peer_chat=0
(( morgan_after > morgan_before )) && peer_chat=1

if (( crossed == 1 )); then
  _pass "alex reports back a result delegated across the crew link"
elif (( peer_chat == 1 )); then
  _pass "delegation crossed the link (new chat for morgan $morgan_before → $morgan_after; tag not echoed)"
else
  _fail "cross-crew delegation over a live link" \
    "alex neither echoed the tag nor opened a chat for morgan over 2 attempts — the link is active, so this is the failure this suite exists for"
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

#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Orchestration — lead → peer delegation, and ephemeral hire.
#
# Crewship's headline orchestration is that a LEAD decomposes a request and
# delegates to peers (internally via /assign), and can HIRE a short-lived
# specialist for a one-off. Neither has a direct CLI verb — they happen when
# you talk to the lead — so this test drives them behaviourally and corroborates
# via observable side effects (a new chat session for the peer, the hired agent
# / its approval gate showing up).
#
# Engineering crew: alex(lead) · sam · robin. Ops crew: morgan(lead) · riley.

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight

# ─────────────────────────────────────────────────────────────────────────────
section "1. Lead → peer delegation (alex delegates to sam)"
# ─────────────────────────────────────────────────────────────────────────────
TAG="$(nonce DELEG)"
# Count sam's chats before, so we can report the assignment opening a new one.
sam_before=0
if have jq; then
  sam_before="$(cs chat list sam --format json 2>/dev/null | jq 'length' 2>/dev/null || echo 0)"
fi

# The tag is in the prompt, so a reply (or a quoted refusal) that contains it
# proves nothing (#4855). Only the assignment /assign wrote counts: an
# assignment.created entry addressed to sam whose task carries the tag, and an
# assignment.completed entry for that same assignment id with a success
# outcome. Non-zero when the feed cannot be read, never an empty "no entry".
deleg_assignment_ids() { # <created|completed> -> assignment ids for $TAG
  local feed
  feed="$(cs activity --since 30m --lines 200 --export ndjson 2>/dev/null)" || return 1
  printf '%s\n' "$feed" | jq -rs --arg tag "$TAG" --arg kind "$1" '
    [ .[] | select(.to_slug == "sam" and .entry_type == "assignment.created")
      | select((.payload.task // "") | contains($tag))
      | .payload.assignment_id | select(type == "string" and length > 0) ] as $created
    | if $kind == "created" then $created[]
      else .[] | select(.entry_type == "assignment.completed")
        | select(.payload.outcome | . == "SUCCEEDED" or . == "NO_CHANGE" or . == "WORK_CREATED")
        | .payload.assignment_id | select(. as $id | $created | index($id)) end' 2>/dev/null | sort -u
}

info "Asking alex to delegate a concrete subtask to sam (tag $TAG)…"
# Behavioral LLM delegation is non-deterministic: the lead may phrase the
# report differently or answer inline. Retry once. An ask the server refused
# (429, the lead still holds other work) is not an inline answer and is
# reported as such.
delegated=0
asks_answered=0
ask_refused=0
feed_ok=1
for attempt in 1 2; do
  # A lead that still holds earlier work answers the next ask with 429.
  wait_agent_free alex || finish
  ask_agent alex "Delegate this to your peer Sam, do NOT do it yourself: \
ask Sam to reply with exactly the string '${TAG}-OK' and nothing else. Then \
report back to me Sam's exact answer." >/dev/null
  ask_rc=$?
  # The exit status decides, never the reply: a failed ask can still leave a
  # partial reply behind.
  if (( ask_rc != 0 )); then
    ask_refused=$((ask_refused+1))
    info "attempt $attempt: the ask itself failed (see ask_agent message above)"
    continue
  fi
  asks_answered=$((asks_answered+1))
  if have jq; then
    created="$(deleg_assignment_ids created)" || { feed_ok=0; break; }
    if [[ -n "$created" ]]; then delegated=1; break; fi
  fi
  info "attempt $attempt: no assignment to sam carries the tag; retrying…"
done

sam_after=0
have jq && sam_after="$(cs chat list sam --format json 2>/dev/null | jq 'length' 2>/dev/null || echo 0)"
peer_chat=0
(( sam_after > sam_before )) && peer_chat=1

if (( asks_answered == 0 )); then
  _fail "alex accepts the delegation ask" "both asks exited non-zero ($ask_refused of 2, a partial reply does not count as an answer); the delegation was never confirmed, so this is a capacity or transport failure, not a skipped feature"
elif ! have jq; then
  skip "lead→peer delegation" "jq is not installed, so the activity feed could not be read — install jq to make this assertion"
elif (( feed_ok == 0 )); then
  _fail "lead→peer delegation" "\`crewship activity\` could not be read, so there is no evidence either way"
elif (( delegated == 1 )); then
  _pass "alex's delegation reached sam (assignment.created addressed to sam carries $TAG)"
  waited=0
  until [[ -n "$(deleg_assignment_ids completed)" ]] || (( waited >= ASK_TIMEOUT )); do
    sleep "$POLL_INTERVAL"; waited=$((waited+POLL_INTERVAL))
  done
  if [[ -n "$(deleg_assignment_ids completed)" ]]; then
    _pass "the delegated assignment to sam completed"
  else
    _fail "the delegated assignment to sam completed" "no assignment.completed with a success outcome joined to the tagged assignment within ${ASK_TIMEOUT}s"
  fi
elif (( ask_refused > 0 )); then
  # One refused ask plus one inline answer is not the two chances the skip
  # below stands for, so the refusal decides.
  _fail "lead→peer delegation" "$ask_refused of 2 asks exited non-zero and the answered one carried no tagged assignment to sam, so delegation was never confirmed"
else
  # Both asks succeeded and no assignment to sam carries the tag: the lead did
  # not delegate. That is the model's choice, so it stays a skip. The stage e2e
  # leg does not count a run with skips as release evidence.
  skip "lead→peer delegation" "non-deterministic: both asks succeeded but no assignment addressed to sam carried $TAG over 2 attempts, so the lead may have answered inline; a tag in the reply is not evidence"
fi

if have jq; then
  if (( peer_chat == 1 )); then
    _pass "delegation opened a new chat session for sam ($sam_before → $sam_after)"
  else
    skip "delegation opened a peer chat" "sam chat count unchanged ($sam_before) — lead may have answered inline (no assignment-list CLI to confirm)"
  fi
else
  skip "peer-chat corroboration" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "2. Ephemeral hire (lead hires a short-lived specialist)"
# ─────────────────────────────────────────────────────────────────────────────
# Pick a real crew-template slug from the catalogue.
TMPL="${TMPL:-}"
if [[ -z "$TMPL" ]] && have jq; then
  TMPL="$(cs template list --format json 2>/dev/null | jq -r '.[0].slug // empty' 2>/dev/null)"
fi
TMPL="${TMPL:-devops-sre}"

agents_before=0
have jq && agents_before="$(cs agent list --format json 2>/dev/null | jq 'length' 2>/dev/null || echo 0)"

info "Hiring an ephemeral agent into ops from template '$TMPL' (ttl 15m)…"
if cs hire --crew ops --template "$TMPL" --reason "harness: ephemeral hire smoke" \
     --ttl 15 --yes >/tmp/cs-hire.out 2>&1; then
  # Hire succeeded outright (crew autonomy = autonomous).
  if have jq; then
    agents_after="$(cs agent list --format json 2>/dev/null | jq 'length' 2>/dev/null || echo 0)"
    if (( agents_after > agents_before )); then
      _pass "ephemeral agent joined ops ($agents_before → $agents_after agents)"
    else
      skip "ephemeral agent visible in roster" "count unchanged — may be PENDING_REVIEW"
    fi
  else
    _pass "hire command accepted (install jq to verify roster delta)"
  fi
else
  # Most likely gated by crew autonomy → lands as a blocking inbox waitpoint.
  info "hire did not complete outright — checking for an approval gate in the inbox…"
  # Captured first: `grep -q` closes the pipe at its first match and, under
  # lib.sh's `pipefail`, the writer's SIGPIPE becomes the pipeline's status —
  # a match that reads as a failure. Same shape that made test-pages.sh report
  # a registered command as missing once the listing outgrew the pipe buffer.
  _waitpoints="$(cs inbox list --kind waitpoint --state all 2>/dev/null || true)"
  if grep -qiE 'hire|ephemeral|contractor' <<<"$_waitpoints"; then
    _pass "gated hire surfaced an approval waitpoint in the inbox (autonomy=guided)"
  else
    skip "ephemeral hire" "hire neither completed nor produced a recognizable waitpoint: $(head -c 160 /tmp/cs-hire.out | tr '\n' ' ')"
  fi
fi

finish

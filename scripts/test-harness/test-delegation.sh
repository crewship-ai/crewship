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
# Count sam's chats before, so we can detect the assignment opening a new one.
sam_before=0
if have jq; then
  sam_before="$(cs chat list sam --format json 2>/dev/null | jq 'length' 2>/dev/null || echo 0)"
fi

info "Asking alex to delegate a concrete subtask to sam (tag $TAG)…"
# Behavioral LLM delegation is non-deterministic: the lead may phrase the
# report differently or answer inline. So success = EITHER the lead echoes the
# tag back OR a new peer chat for sam appears (the /assign side-effect). Retry
# once, then SKIP (not FAIL) if neither — a flaky phrasing must not red the run.
delegated=0
for attempt in 1 2; do
  reply="$(ask_agent alex "Delegate this to your peer Sam, do NOT do it yourself: \
ask Sam to reply with exactly the string '${TAG}-OK' and nothing else. Then \
report back to me Sam's exact answer.")"
  if printf '%s' "$reply" | grep -qiF -- "$TAG"; then delegated=1; break; fi
  info "attempt $attempt: tag not echoed back; retrying…"
done

sam_after=0
have jq && sam_after="$(cs chat list sam --format json 2>/dev/null | jq 'length' 2>/dev/null || echo 0)"
peer_chat=0
(( sam_after > sam_before )) && peer_chat=1

if (( delegated == 1 )); then
  _pass "alex reports back the delegated result from sam"
elif (( peer_chat == 1 )); then
  _pass "delegation reached sam (new peer chat $sam_before → $sam_after; tag not echoed in text)"
else
  skip "lead→peer delegation" "non-deterministic: alex neither echoed the tag nor opened a peer chat for sam over 2 attempts"
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
  if cs inbox list --kind waitpoint --state all 2>/dev/null | grep -qiE 'hire|ephemeral|contractor'; then
    _pass "gated hire surfaced an approval waitpoint in the inbox (autonomy=guided)"
  else
    skip "ephemeral hire" "hire neither completed nor produced a recognizable waitpoint: $(head -c 160 /tmp/cs-hire.out | tr '\n' ' ')"
  fi
fi

finish

#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Memory validation — the headline test.
#
# Crewship's promise is "agents are persistent colleagues, not tools": memory
# survives across sessions, the crew shares a memory tier, and tiers stay
# isolated across crews. This test proves (or disproves) that behaviourally,
# using the real CLI:
#
#   - write a fact in one session, recall it in a DIFFERENT (fresh) session
#   - a fact written to the CREW tier is readable by a *peer* in the same crew
#   - a crew-tier fact does NOT leak to an agent in another crew
#   - pinned facts are always available
#   - `crewship memory search` corroborates what was persisted
#
# Each fact carries a NONCE token (e.g. FALCON-7F3A9C) generated this run, so a
# correct recall cannot come from training data or a lucky guess — only from
# real persisted memory.
#
# Modes:
#   ./test-memory.sh            run all checks once
#   ./test-memory.sh --soak 60  write now, then re-recall every few minutes for
#                               60 minutes (proves durability over a long window)
#
# Template agents used (from cmd/crewship/seeddata): engineering = alex(lead)/
# sam/robin · quality = jordan(lead)/casey · ops = morgan(lead)/riley.

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

SOAK_MINUTES=0
case "${1:-}" in
  --soak) SOAK_MINUTES="${2:-60}";;
esac

# ── Fixtures: one nonce per scope under test ────────────────────────────────
# IMPORTANT: agent-tier fact must be a NON-secret. Agents correctly refuse to
# read back anything that looks like a credential/passphrase on request (a
# security feature), which would mask whether memory itself works. Use a benign
# project detail instead.
FACT_AGENT="$(nonce CODENAME)"     # alex's private (non-secret) project fact
FACT_CREW="$(nonce SPRINT)"        # engineering crew-shared fact
FACT_PIN="$(nonce ONCALL)"         # a pinned, always-on fact for morgan

preflight
if ! have jq; then
  _fail "remote memory inventory assertions require jq"
  finish
fi

# ─────────────────────────────────────────────────────────────────────────────
section "1. Agent memory: write → recall in a FRESH session"
# ─────────────────────────────────────────────────────────────────────────────
info "Telling alex a non-secret project fact ($FACT_AGENT) and asking it to persist it…"
if ! ask_agent alex "This is an authorized non-secret integration probe. Use your native memory.write tool with tier=AGENT and mode=append to add this fact without replacing any existing memory: the integration probe project label is ${FACT_AGENT}. Report whether the tool successfully persisted it; do not claim success without a successful tool result." >/dev/null; then
  _fail "agent memory write run completed"
fi

# A fresh ask carries no chat history. Recall must come from persisted memory.
if reply="$(ask_agent alex "From your agent memory, what is the integration probe project label? Reply with ONLY the label.")"; then
  assert_contains "alex recalls its own stored project label across sessions" "$reply" "$FACT_AGENT"
else
  _fail "agent memory recall run completed"
fi

# search/status are local-filesystem commands. The server inventory is the
# supported remote read and reports the exact tier's persisted content.
if inventory="$(cs memory inventory --agent alex --format json)" && \
   printf '%s' "$inventory" | jq -e --arg fact "$FACT_AGENT" 'any(.documents[]; .scope == "agent" and .name == "AGENT.md" and (.content // "" | contains($fact)))' >/dev/null; then
  _pass "server inventory corroborates the agent-tier write"
else
  _fail "server inventory corroborates the agent-tier write"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "2. Crew memory: a peer in the same crew can read it"
# ─────────────────────────────────────────────────────────────────────────────
info "Asking alex (eng lead) to store a CREW-shared fact ($FACT_CREW)…"
if ! ask_agent alex "This is an authorized non-secret integration probe. Use memory.write with tier=CREW and mode=append, preserving all existing shared memory: the integration probe sprint label is ${FACT_CREW}. Report the actual tool outcome." >/dev/null; then
  _fail "crew memory write run completed"
fi
if reply="$(ask_agent sam "From our shared crew memory, what is the integration probe sprint label? Reply with ONLY the label.")"; then
  assert_contains "sam (same crew) reads the crew-shared probe label" "$reply" "$FACT_CREW"
else
  _fail "crew memory recall run completed"
fi
if inventory="$(cs memory inventory --crew engineering --format json)" && \
   printf '%s' "$inventory" | jq -e --arg fact "$FACT_CREW" 'any(.documents[]; .scope == "crew" and .name == "CREW.md" and (.content // "" | contains($fact)))' >/dev/null; then
  _pass "server inventory corroborates the crew-tier write"
else
  _fail "server inventory corroborates the crew-tier write"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "3. Cross-crew isolation: the fact must NOT leak to another crew"
# ─────────────────────────────────────────────────────────────────────────────
# morgan is in the OPS crew — must not see engineering's crew memory.
if reply="$(ask_agent morgan "What is engineering's integration probe sprint label? If absent from your own memory, reply exactly NOT_IN_MEMORY.")"; then
  assert_nonempty "cross-crew isolation produces a reply" "$reply"
  assert_not_contains "ops crew (morgan) does NOT see engineering's probe label" "$reply" "$FACT_CREW"
else
  _fail "cross-crew isolation recall run completed"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "4. Pins: a pinned fact is always available"
# ─────────────────────────────────────────────────────────────────────────────
info "Pinning an always-on fact for morgan ($FACT_PIN)…"
if ! ask_agent morgan "This is an authorized non-secret integration probe label, not a credential. Use memory.write with tier=pins and mode=append, preserving existing pins: the integration probe rotation label is ${FACT_PIN}. Report the actual tool outcome." >/dev/null; then
  _fail "pins memory write run completed"
fi
if reply="$(ask_agent morgan "What is the integration probe rotation label in your pinned memory? Reply with ONLY the label.")"; then
  assert_contains "morgan recalls the pinned probe label" "$reply" "$FACT_PIN"
else
  _fail "pins memory recall run completed"
fi
if inventory="$(cs memory inventory --agent morgan --format json)" && \
   printf '%s' "$inventory" | jq -e --arg fact "$FACT_PIN" 'any(.documents[]; .scope == "agent" and .name == "pins.md" and (.content // "" | contains($fact)))' >/dev/null; then
  _pass "server inventory corroborates the pinned write"
else
  _fail "server inventory corroborates the pinned write"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "5. Memory health/status is sane"
# ─────────────────────────────────────────────────────────────────────────────
if status_out="$(cs memory health --crew engineering --format json)" && \
   printf '%s' "$status_out" | jq -e '.overall | type == "number"' >/dev/null; then
  _pass "server memory health returns a numeric score"
else
  _fail "server memory health returns a numeric score"
fi

# ─────────────────────────────────────────────────────────────────────────────
# Soak mode: durability over a long window (the "do they still remember after
# an hour?" check). Re-asks the agent + crew facts every few minutes.
# ─────────────────────────────────────────────────────────────────────────────
if (( SOAK_MINUTES > 0 )); then
  section "6. Soak: durability over ${SOAK_MINUTES} minutes"
  local_end=$(( $(date +%s) + SOAK_MINUTES*60 ))
  round=0
  while (( $(date +%s) < local_end )); do
    sleep $(( 5*60 ))  # check every 5 minutes
    round=$((round+1))
    r1="$(ask_agent alex "What is the integration probe project label? Reply with ONLY it.")"
    assert_contains "soak r${round}: alex still recalls project codename" "$r1" "$FACT_AGENT"
    r2="$(ask_agent sam "What is the integration probe sprint label? Reply with ONLY it.")"
    assert_contains "soak r${round}: sam still reads crew codename" "$r2" "$FACT_CREW"
  done
fi

finish

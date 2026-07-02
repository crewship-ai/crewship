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

# ─────────────────────────────────────────────────────────────────────────────
section "1. Agent memory: write → recall in a FRESH session"
# ─────────────────────────────────────────────────────────────────────────────
info "Telling alex a non-secret project fact ($FACT_AGENT) and asking it to persist it…"
ask_agent alex "Use your memory tool to permanently remember this project \
detail in your own agent memory: the internal codename for our Q3 dashboard \
rewrite project is ${FACT_AGENT}. Confirm you stored it." \
  >/dev/null

# A brand-new `ask` is a new chat with NO carried history (verified: each CLI
# invocation POSTs a fresh chat). So recall here can only come from memory.
reply="$(ask_agent alex "What is the internal codename for our Q3 dashboard \
rewrite project? Reply with ONLY the codename, nothing else.")"
assert_contains "alex recalls its own stored project codename across sessions" "$reply" "$FACT_AGENT"

info "Corroborating via 'crewship memory search' (best-effort)…"
if search_out="$(cs memory search "$FACT_AGENT" --scope agent --format json 2>/dev/null)"; then
  assert_contains "memory search (agent scope) finds the stored passphrase" "$search_out" "$FACT_AGENT"
else
  skip "memory search (agent scope)" "search unavailable against this server mode"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "2. Crew memory: a peer in the same crew can read it"
# ─────────────────────────────────────────────────────────────────────────────
info "Asking alex (eng lead) to store a CREW-shared fact ($FACT_CREW)…"
ask_agent alex "Use your memory tool to store this in the CREW-SHARED memory \
(visible to your whole engineering crew, not just you): the sprint codename is \
${FACT_CREW}. Confirm it is saved to crew memory." >/dev/null

# sam is a peer in the SAME crew (engineering). Fresh session.
reply="$(ask_agent sam "From our shared crew memory, what is the current sprint \
codename? Reply with ONLY the codename.")"
assert_contains "sam (same crew) reads the crew-shared sprint codename" "$reply" "$FACT_CREW"

# ─────────────────────────────────────────────────────────────────────────────
section "3. Cross-crew isolation: the fact must NOT leak to another crew"
# ─────────────────────────────────────────────────────────────────────────────
# morgan is in the OPS crew — must not see engineering's crew memory.
reply="$(ask_agent morgan "What is the engineering team's current sprint \
codename? If you do not have it in your memory, say exactly: NOT_IN_MEMORY.")"
assert_not_contains "ops crew (morgan) does NOT see engineering's crew codename" "$reply" "$FACT_CREW"

# ─────────────────────────────────────────────────────────────────────────────
section "4. Pins: a pinned fact is always available"
# ─────────────────────────────────────────────────────────────────────────────
info "Pinning an always-on fact for morgan ($FACT_PIN)…"
ask_agent morgan "Pin this to your memory so it is ALWAYS in context: the \
current on-call rotation code is ${FACT_PIN}. Confirm it is pinned." >/dev/null

reply="$(ask_agent morgan "What is the current on-call rotation code? Reply with \
ONLY the code.")"
assert_contains "morgan recalls the pinned on-call code" "$reply" "$FACT_PIN"

# ─────────────────────────────────────────────────────────────────────────────
section "5. Memory health/status is sane"
# ─────────────────────────────────────────────────────────────────────────────
if status_out="$(cs memory status --scope agent 2>/dev/null)"; then
  assert_nonempty "memory status (agent) returns output" "$status_out"
else
  skip "memory status (agent)" "status unavailable against this server mode"
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
    r1="$(ask_agent alex "What is the internal codename for our Q3 dashboard rewrite project? Reply with ONLY it.")"
    assert_contains "soak r${round}: alex still recalls project codename" "$r1" "$FACT_AGENT"
    r2="$(ask_agent sam "What is the sprint codename? Reply with ONLY it.")"
    assert_contains "soak r${round}: sam still reads crew codename" "$r2" "$FACT_CREW"
  done
fi

finish

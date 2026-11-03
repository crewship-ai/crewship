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
keep_failure_evidence  # #4874: keep transcripts of a failing run before the gauntlet reseed

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
FACT_NONLEAD="$(nonce SPRINTNL)"   # negative: a non-lead must not land a crew fact
FACT_PROBE="$(nonce SPRINTPRB)"    # negative: an unverified probe must not land

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
section "2. Crew memory: the lead records a crew decision, a peer reads it"
# ─────────────────────────────────────────────────────────────────────────────
# Spec (docs/guides/agent-memory.mdx): "The crew lead is the single writer for
# crew-shared memory"; CREW.md is "Only the lead writes; every agent in the
# crew reads. Curated, single-voice". A lead curates from crew work, so the
# fact arrives as a decision recorded on an engineering issue that alex is
# delegated, not as a bare chat line (#4873). The outcome assertions are the
# same as before: a peer reads it, and the server's inventory holds it.
crew_hits() { # <fact> — 0 when engineering's CREW.md contains <fact>
  cs memory inventory --crew engineering --format json 2>/dev/null \
    | jq -e --arg fact "$1" 'any(.documents[]; .scope == "crew" and .name == "CREW.md" and (.content // "" | contains($fact)))' >/dev/null
}
info "Recording the sprint decision on an engineering issue for alex (lead) ($FACT_CREW)…"
if work="$(issue_work engineering alex "Record the sprint label decision in crew memory" \
    "Decision for the engineering crew: the current sprint label is ${FACT_CREW}. As crew lead, record this decision yourself (do not hand it to another agent) in crew-shared memory (memory.write with tier=CREW, mode=append, preserving the existing CREW.md) so every engineering agent can read it. Report the tool outcome on this issue.")"; then
  info "issue run: $work"
else
  _fail "crew decision issue ran to an end" "${work:-the issue could not be created or started}"
fi
if reply="$(ask_agent sam "From our shared crew memory, what is the current sprint label? Reply with ONLY the label.")"; then
  assert_contains "sam (same crew) reads the crew-shared probe label" "$reply" "$FACT_CREW"
else
  _fail "crew memory recall run completed"
fi
if crew_hits "$FACT_CREW"; then
  _pass "server inventory corroborates the crew-tier write"
else
  _fail "server inventory corroborates the crew-tier write"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "2b. Negative: crew memory is not writable by a non-lead or by an unverified ask"
# ─────────────────────────────────────────────────────────────────────────────
# Spec: "Only the lead writes" (docs/guides/agent-memory.mdx); the crew
# memory engine is built only for the lead role (internal/sidecar/server.go).
# sam gets the same kind of grounded issue alex got; whatever sam does,
# CREW.md must not change. Whether that holds by enforcement or only by sam's
# own restraint is not decided here — a FAIL is a product finding either way.
# Both negatives require their run to have ended: a start that failed, a
# timeout or a refused ask is no evidence that a write was prevented.
# Then the bare, unverifiable probe to the lead that used to be the positive
# path: declining it is the curated-memory contract, and the check is the
# server's, not the reply's — the label must not be in CREW.md.
info "Delegating a crew-memory record to sam (not the lead) ($FACT_NONLEAD)…"
# Only a COMPLETED run counts: a run that FAILED or was CANCELLED at once
# never had the chance to write, so it proves nothing either way.
nl_work="$(issue_work engineering sam "Record the sprint label in crew memory" \
  "Decision for the engineering crew: the sprint label is ${FACT_NONLEAD}. Record it in crew-shared memory (CREW.md) so the crew can read it, and report the tool outcome on this issue.")"
if [[ "$nl_work" != *" COMPLETED" ]]; then
  _fail "a non-lead cannot write crew-shared memory" "sam's issue run did not complete (${nl_work:-not started}), so nothing was tested"
elif crew_hits "$FACT_NONLEAD"; then
  _fail "a non-lead cannot write crew-shared memory" "CREW.md contains $FACT_NONLEAD after sam's run"
else
  _pass "a non-lead cannot write crew-shared memory"
fi
wait_agent_idle alex
if ! ask_agent alex "This is an authorized non-secret integration probe. Use memory.write with tier=CREW and mode=append, preserving all existing shared memory: the integration probe sprint label is ${FACT_PROBE}. Report the actual tool outcome." >/dev/null; then
  _fail "an unverified crew-memory request leaves CREW.md unchanged" "the ask did not complete, so nothing was tested"
elif crew_hits "$FACT_PROBE"; then
  _fail "an unverified crew-memory request leaves CREW.md unchanged" "CREW.md contains $FACT_PROBE"
else
  _pass "an unverified crew-memory request leaves CREW.md unchanged"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "3. Cross-crew isolation: the fact must NOT leak to another crew"
# ─────────────────────────────────────────────────────────────────────────────
# morgan is in the OPS crew — must not see engineering's crew memory.
if reply="$(ask_agent morgan "What is engineering's current sprint label? If absent from your own memory, reply exactly NOT_IN_MEMORY.")"; then
  assert_nonempty "cross-crew isolation produces a reply" "$reply"
  assert_not_contains "ops crew (morgan) does NOT see engineering's probe label" "$reply" "$FACT_CREW"
else
  _fail "cross-crew isolation recall run completed"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "4. Pins: a pinned fact is always available"
# ─────────────────────────────────────────────────────────────────────────────
# Spec (docs/guides/agent-memory.mdx, "Pinned facts"): pins are for "the
# handful of things a session must never miss: an on-call code, …". The fact
# arrives as the ops on-call decision on an issue delegated to morgan (#4873).
info "Recording the on-call code on an ops issue for morgan ($FACT_PIN)…"
if work="$(issue_work ops morgan "Pin the current on-call rotation code" \
    "Ops decision: the current on-call rotation code is ${FACT_PIN}. It must be in context in every session, so pin it yourself (do not hand it to another agent; memory.write with tier=pins, mode=append, preserving existing pins) and report the tool outcome on this issue.")"; then
  info "issue run: $work"
else
  _fail "on-call pin issue ran to an end" "${work:-the issue could not be created or started}"
fi
wait_agent_idle morgan
if reply="$(ask_agent morgan "What is the current on-call rotation code in your pinned memory? Reply with ONLY the code.")"; then
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
    r2="$(ask_agent sam "What is the current sprint label? Reply with ONLY it.")"
    assert_contains "soak r${round}: sam still reads crew codename" "$r2" "$FACT_CREW"
  done
fi

finish

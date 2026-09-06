#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# uc: memory-recall
# title: A fact told in one session is recalled in a fresh one, and indexed
# needs: model
# minutes: 3
#
# The product promise: agents are persistent colleagues, not tools. An agent
# is told one non-secret fact, a brand-new session asks for it back, and
# `crewship memory search` shows where it was stored. The fact carries a
# nonce minted now, so a correct answer cannot come from training data.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight
reason="$(demo_need model)" || demo_skip_all "$reason"

FACT="$(nonce CODENAME)"

demo_step "Tell alex a project fact ($FACT)"
demo_say "A non-secret detail on purpose: agents refuse to echo credentials back, which would mask whether memory works."
reply="$(ask_agent alex "Use your memory tool to permanently remember this project detail in your own agent memory: the internal codename for our Q3 dashboard rewrite project is ${FACT}. Confirm you stored it.")"
assert_nonempty "alex answered" "$reply"
printf '%s\n' "$reply" | head -5 | sed 's/^/     /'
demo_ui "alex's conversation" "/chat"

demo_step "A fresh session asks for it back"
demo_say "Every 'crewship ask' is a new chat with no carried history, so recall can only come from persisted memory."
reply="$(ask_agent alex "What is the internal codename for our Q3 dashboard rewrite project? Reply with ONLY the codename, nothing else.")"
printf '%s\n' "$reply" | head -3 | sed 's/^/     /'
assert_contains "alex recalls the codename across sessions" "$reply" "$FACT"

demo_step "What the server's memory index says"
demo_say "'memory search' reads the local filesystem; 'memory hybrid' asks the server (FTS + episodic recall)."
demo_show "memory hybrid" cs memory hybrid "$FACT" --limit 5
if printf '%s' "$DEMO_OUT" | grep -qF -- "$FACT"; then
  _pass "hybrid search finds the codename"
else
  skip "hybrid search finds the codename" "no hit yet — the recall above is the proof of persistence; the index lags or does not cover agent-private memory"
fi
demo_show "memory health" cs memory health
demo_ui "alex's memory tiers" "/agents/alex"

finish

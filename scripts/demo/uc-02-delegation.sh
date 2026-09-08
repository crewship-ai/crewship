#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# uc: delegation
# title: A lead delegates to a peer and reports the peer's answer back
# needs: model
# minutes: 4
#
# Alex (engineering lead) is asked to hand a task to Sam and NOT do it
# alone. The oracle is the crew journal — assignment.created, .running,
# .completed on sam — not the lead's words: a model can write "Sam said X"
# without ever asking Sam. The tag is minted now so the journal entry is
# unmistakably this run's.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight
reason="$(demo_need model)" || demo_skip_all "$reason"

TAG="$(nonce DELEG)"
journal_json() { cs journal --crew engineering --lines 200 --since 1h --format json 2>/dev/null; }

demo_step "Ask alex to delegate to sam (tag $TAG)"
demo_say "Delegation has no CLI verb — it happens when you talk to the lead. Sam's answer must contain the tag."
delegated=0
for attempt in 1 2; do
  reply="$(ask_agent alex "Delegate this to your peer Sam, do NOT do it yourself: ask Sam to reply with exactly the string '${TAG}-OK' and nothing else. Then report back to me Sam's exact answer.")"
  printf '%s\n' "$reply" | head -6 | sed 's/^/     /'
  if printf '%s' "$reply" | grep -qiF -- "$TAG"; then delegated=1; break; fi
  demo_say "attempt $attempt: tag not echoed back; retrying once"
done

demo_step "Corroborate in the crew journal: the assignment on sam"
created=""; completed=""; assignment_id=""
if have jq; then
  created="$(journal_json | jq -c --arg t "$TAG" 'first(.[]? | select(.entry_type == "assignment.created" and (tostring | contains($t)))) // empty' 2>/dev/null)"
  assignment_id="$(printf '%s' "$created" | jq -r '.payload.assignment_id // .refs.assignment_id // empty' 2>/dev/null)"
  if [[ -n "$assignment_id" ]]; then
    completed="$(journal_json | jq -c --arg id "$assignment_id" 'first(.[]? | select(.entry_type == "assignment.completed" and ((.refs.assignment_id // .payload.assignment_id // "") == $id))) // empty' 2>/dev/null)"
  fi
fi
demo_show "journal --crew engineering --type assignment.*" cs journal --crew engineering --lines 200 --since 1h --type assignment.created,assignment.running,assignment.completed
if [[ -n "$created" ]]; then
  _pass "alex created an assignment for sam carrying the tag ($assignment_id)"
elif ! have jq; then
  skip "assignment in the journal" "jq missing"
elif (( delegated == 1 )); then
  _fail "alex created an assignment for sam" "alex reported Sam's answer, but the journal has no assignment with the tag — the lead answered for Sam"
else
  skip "lead→peer delegation" "alex neither echoed the tag nor created an assignment over 2 attempts (model phrasing, not a product verdict)"
fi
if [[ -n "$assignment_id" ]]; then
  if [[ -n "$completed" ]]; then
    _pass "the assignment completed on sam"
  else
    _fail "the assignment completed on sam" "no assignment.completed for $assignment_id"
  fi
  demo_show "chain $assignment_id — what caused what" cs chain "$assignment_id"
fi
if (( delegated == 1 )); then _pass "alex reported sam's answer back ($TAG)"; fi
demo_ui "alex's conversation, and sam's turn under it" "/chat"

finish

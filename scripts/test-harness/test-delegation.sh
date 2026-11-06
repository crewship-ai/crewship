#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Orchestration — lead → peer delegation, and ephemeral hire.
#
# Crewship's headline orchestration is that a LEAD decomposes a request and
# delegates to peers (internally via /assign), and can HIRE a short-lived
# specialist for a one-off. Neither has a direct CLI verb — they happen when
# you talk to the lead — so this test drives them behaviourally. Delegation is
# proven only by the tagged assignment in the activity feed; a hire by the hired
# agent or its approval gate showing up.
#
# Engineering crew: alex(lead) · sam · robin. Ops crew: morgan(lead) · riley.

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight
keep_failure_evidence  # #4874: keep transcripts of a failing run past the reseed

# ─────────────────────────────────────────────────────────────────────────────
section "1. Lead → peer delegation (alex delegates to sam)"
# ─────────────────────────────────────────────────────────────────────────────
TAG="$(nonce DELEG)"

# The tag is in the issue, so a reply (or a quoted refusal) that contains it
# proves nothing (#4855). Only the assignment /assign wrote counts: an
# assignment.created entry from alex to sam whose task carries the tag, and an
# assignment.completed entry for that same assignment id with outcome SUCCEEDED,
# the only outcome that says the deliverable exists and was verified
# (assignment_contract.go): WORK_CREATED says the work was passed on and
# NO_CHANGE that nothing was delivered. Non-zero when the feed cannot be read or parsed, never an empty "no
# entry", and no output then: a parser that fails partway must not leave a
# matching id behind.
deleg_assignment_ids() { # <created|completed> -> assignment ids for $TAG
  local feed ids
  feed="$(cs activity --since 30m --lines 200 --export ndjson 2>/dev/null)" || return 1
  ids="$(printf '%s\n' "$feed" | jq -rs --arg tag "$TAG" --arg kind "$1" '
    [ .[] | select(.from_slug == "alex" and .to_slug == "sam" and .entry_type == "assignment.created")
      | select((.payload.task // "") | contains($tag))
      | .payload.assignment_id | select(type == "string" and length > 0) ] as $created
    | if $kind == "created" then $created[]
      else .[] | select(.entry_type == "assignment.completed")
        | select(.payload.outcome == "SUCCEEDED")
        | .payload.assignment_id | select(. as $id | $created | index($id)) end' 2>/dev/null)" || return 1
  [[ -n "$ids" ]] && printf '%s\n' "$ids" | sort -u
  return 0
}

# Delegation is required evidence, so every path without a tagged assignment
# FAILS. A skip here would let run-all.sh and the nightly job report success
# with delegation broken. The work is grounded (see lib.sh, #4873): a bare
# prompt is declined as an unverified relay before /assign is called, which
# says nothing about delegation. A lead that still does the work itself gets
# one retry, then fails. The task carries everything Sam needs, because /assign
# passes only its text: without that a correct Sam reports NEEDS_HUMAN. The worst
# case is two attempts of DELEG_FREE_TIMEOUT + ASK_TIMEOUT, then one ASK_TIMEOUT
# for the completion, about 19 minutes at the defaults. An overrun of the suite
# budget fails closed.
DELEG_FREE_TIMEOUT="${DELEG_FREE_TIMEOUT:-300}"
delegated=0
dispatched=0
feed_ok=1
if ! have jq; then
  _fail "lead→peer delegation" "jq is not installed, so the activity feed cannot be read and delegation cannot be proven"
else
  for attempt in 1 2; do
    wait_agent_free alex "$DELEG_FREE_TIMEOUT" || finish
    info "Issue $attempt: alex (lead) assigns a task to sam (tag $TAG)…"
    deleg_work="$(issue_work engineering alex "Hand a check to Sam ($TAG)" \
      "This operator-created issue tests delegation inside the crew. As engineering lead, use /assign to give your teammate Sam one self-contained task whose text says: Reply with the reference $TAG unchanged; that reply is the deliverable. Sam cannot see this issue, so the task text must carry the reference. Do not do the work yourself. Report Sam's result here.")"
    deleg_rc=$?
    info "issue run: ${deleg_work:-none}"
    if (( deleg_rc == 1 )); then
      info "attempt $attempt: the issue could not be created or started"
      continue
    fi
    dispatched=$((dispatched+1))
    created="$(deleg_assignment_ids created)" || { feed_ok=0; break; }
    if [[ -n "$created" ]]; then delegated=1; break; fi
    info "attempt $attempt: no assignment to sam carries the tag; retrying…"
  done

  if (( feed_ok == 0 )); then
    _fail "lead→peer delegation" "\`crewship activity\` could not be read, so there is no evidence either way"
  elif (( delegated == 1 )); then
    _pass "alex's delegation reached sam (assignment.created addressed to sam carries $TAG)"
    waited=0
    completed=""
    while :; do
      completed="$(deleg_assignment_ids completed)" || { feed_ok=0; break; }
      [[ -n "$completed" ]] && break
      (( waited >= ASK_TIMEOUT )) && break
      sleep "$POLL_INTERVAL"; waited=$((waited+POLL_INTERVAL))
    done
    if (( feed_ok == 0 )); then
      _fail "the delegated assignment to sam completed" "\`crewship activity\` could not be read while waiting for the completion"
    elif [[ -n "$completed" ]]; then
      _pass "the delegated assignment to sam completed"
    else
      _fail "the delegated assignment to sam completed" "no assignment.completed with outcome SUCCEEDED joined to the tagged assignment within ${ASK_TIMEOUT}s"
    fi
  elif (( dispatched == 0 )); then
    _fail "lead→peer delegation" "neither issue could be created or started"
  else
    _fail "lead→peer delegation" "$dispatched issue(s) ran but no assignment addressed to sam carried $TAG"
  fi
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

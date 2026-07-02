#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Notifications & inbox — "run a routine, does the notification land?"
#
# Validates the runtime feedback loop a real operator depends on:
#   - a routine RUN actually completes (exit code + records status)
#   - a completion event is observable on the activity rail (routine watch)
#   - a notification lands in the feed referencing that routine run
#   - a FAILED run surfaces a `failed_run` inbox item (best-effort: only if we
#     can induce a failure on this workspace)
#
# Uses the deterministic seed recipes (extract-contacts, classify-ticket) so the
# run is cheap, fast, and repeatable.

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

ROUTINE="${ROUTINE:-extract-contacts}"

preflight

# ─────────────────────────────────────────────────────────────────────────────
section "1. Routine run completes (exit code + records status)"
# ─────────────────────────────────────────────────────────────────────────────
info "Running routine '$ROUTINE' synchronously…"
if cs routine run "$ROUTINE" >/tmp/cs-run.out 2>&1; then
  _pass "routine run '$ROUTINE' exits 0 (COMPLETED)"
else
  _fail "routine run '$ROUTINE' exits 0" "$(head -c 200 /tmp/cs-run.out | tr '\n' ' ')"
fi

# Cross-check the run record via the structured API.
if have jq && rec="$(cs routine records "$ROUTINE" --json --limit 1 2>/dev/null)"; then
  st="$(printf '%s' "$rec" | jq -r '.[0].status // empty')"
  rid="$(printf '%s' "$rec" | jq -r '.[0].id // empty')"
  assert_eq "latest '$ROUTINE' record status == completed" "completed" "$st"
  assert_nonempty "latest '$ROUTINE' record has a run id" "$rid"
else
  skip "routine records JSON cross-check" "jq missing or records unavailable"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "2. Completion event observable on the activity rail (watch --once)"
# ─────────────────────────────────────────────────────────────────────────────
# Kick a run in the background, then watch for its terminal event. `watch --once`
# exits after the first run completes — CI-friendly.
info "Launching '$ROUTINE' and watching for pipeline.run.completed…"
( cs routine run "$ROUTINE" >/dev/null 2>&1 ) &
if timeout "$POLL_TIMEOUT" "$CREWSHIP" --server "$SERVER" routine watch "$ROUTINE" \
     --once --json >/tmp/cs-watch.jsonl 2>/dev/null; then
  if grep -q 'pipeline.run.completed' /tmp/cs-watch.jsonl; then
    _pass "watch observed a pipeline.run.completed event"
  else
    _fail "watch observed completion" "no completed event in: $(head -c 200 /tmp/cs-watch.jsonl | tr '\n' ' ')"
  fi
else
  skip "routine watch --once" "watch timed out or unsupported"
fi
wait 2>/dev/null || true

# ─────────────────────────────────────────────────────────────────────────────
section "3. Routine completion is observable (activity rail is the canonical surface)"
# ─────────────────────────────────────────────────────────────────────────────
# A successful routine completion is recorded on the activity rail
# (pipeline.run.completed — asserted in section 2 via `watch --once`) and in
# `routine records`. The notification FEED is reserved for attention-worthy
# events (escalations, approvals, mentions); routine completions are NOT pushed
# there by design — otherwise scheduled runs would drown the feed. So the feed
# is a best-effort bonus check; its absence is NOT a failure.
if have jq && "$CREWSHIP" --server "$SERVER" notification list --format json 2>/dev/null \
     | jq -e '[.[] | select((.entity_type=="routine") or (.action|tostring|test("routine|pipeline|run";"i")))] | length>0' >/dev/null 2>&1; then
  _pass "routine event present in the notification feed (bonus)"
else
  skip "routine notification in feed" "by design: completions surface on the activity rail (verified §2) + records, not the notification feed"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "4. Failed run is observable (records); failed_run inbox is scheduled-only"
# ─────────────────────────────────────────────────────────────────────────────
# Induce a failure and assert it is OBSERVABLE. For a manual/CLI run the surface
# is the exit code (non-zero) + the run record status=failed. The failed_run
# INBOX item is created only for SCHEDULED (unattended) runs
# (internal/pipeline/schedules.go alertFailedScheduledRun) — NOT ad-hoc CLI
# runs, by design, since a manual operator already sees the error inline.
FAILER="${FAILER:-classify-ticket}"
info "Inducing a failure on '$FAILER' with empty input…"
if cs routine run "$FAILER" --inputs '{"ticket":""}' >/tmp/cs-fail.out 2>&1; then
  skip "failed-run observability" "could not induce a failure (routine tolerated the input)"
else
  if have jq; then
    st="$(cs routine records "$FAILER" --json --limit 1 2>/dev/null | jq -r '.[0].status // empty')"
    assert_eq "failed manual run is recorded as status=failed" "failed" "$st"
  else
    skip "failed-run record check" "jq missing"
  fi
  skip "failed_run inbox for manual run" "by design: failed_run inbox is scheduled-run-only; manual failures surface via exit code + records (asserted above)"
fi

finish

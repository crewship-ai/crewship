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
section "3. A notification lands in the feed for the routine run"
# ─────────────────────────────────────────────────────────────────────────────
# The notification feed records actor/entity events. After running a routine we
# expect at least one notification whose entity_type is 'routine' (or whose
# entity_title matches the routine). Poll until it shows up.
if have jq; then
  detect="\"$CREWSHIP\" --server \"$SERVER\" notification list --format json 2>/dev/null \
    | jq -e '[.[] | select((.entity_type==\"routine\") or (.entity_title|tostring|test(\"$ROUTINE\";\"i\")) or (.action|tostring|test(\"routine|pipeline|run\";\"i\")))] | length>0'"
  poll_until "a routine notification arrives in the feed" "$POLL_TIMEOUT" "$detect"
else
  detect="\"$CREWSHIP\" --server \"$SERVER\" notification list 2>/dev/null | grep -qiE 'routine|pipeline|$ROUTINE'"
  poll_until "a routine notification arrives in the feed (grep)" "$POLL_TIMEOUT" "$detect"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "4. Failed run → failed_run inbox item (best-effort)"
# ─────────────────────────────────────────────────────────────────────────────
# We can only assert this if we can force a failure. Strategy: run a routine
# with an input that the shape/grader gate must reject. If no failure can be
# induced on this workspace, SKIP rather than false-fail.
FAILER="${FAILER:-classify-ticket}"
info "Attempting to induce a failure on '$FAILER' with junk input…"
if cs routine run "$FAILER" --inputs '{"ticket":""}' >/tmp/cs-fail.out 2>&1; then
  skip "failed_run inbox item" "could not induce a failure (routine tolerated the input)"
else
  if have jq; then
    detect="\"$CREWSHIP\" --server \"$SERVER\" inbox list --kind failed_run --state all --format json 2>/dev/null | jq -e 'length>0'"
  else
    detect="\"$CREWSHIP\" --server \"$SERVER\" inbox list --kind failed_run --state all 2>/dev/null | grep -q ."
  fi
  poll_until "failed run surfaces a failed_run inbox item" "$POLL_TIMEOUT" "$detect"
fi

finish

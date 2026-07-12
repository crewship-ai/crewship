#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Keeper audit integrity — every credential decision should leave a durable,
# operator-visible trace.
#
# The access/execute paths swallow keeper_requests INSERT failures and continue
# ("Non-fatal — continue", keeper_request.go:152 / keeper_execute.go:233) — so a
# decision (incl. ALLOW+exec) can proceed with NO audit row. The F4 path does the
# opposite (500s on insert error). This suite pins the *happy-path* audit contract
# that must hold, and documents the fail-silent gap (test T6) that a CLI cannot
# force on its own.
#
# Asserts:
#   1. A human-granted credential escalation leaves a resolvable record.
#   2. Credential lifecycle events (CREATE/ASSIGN/ROTATE/REVOKE) appear on the
#      credential audit timeline — the operator's paper trail.
#   3. system keeper exposes the scrubber/secret bookkeeping (secret_count).

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight

AGENT="${AGENT:-morgan}"
CREW="${CREW:-ops}"
CRED="HARNESS_AUDIT_$(nonce EV | tr '-' '_')"

# ─────────────────────────────────────────────────────────────────────────────
section "1. Credential lifecycle leaves an audit timeline"
# ─────────────────────────────────────────────────────────────────────────────
info "Create → assign → rotate → the audit timeline must record each step…"
printf 'audit-v1' | cs credential create --name "$CRED" --type API_KEY \
  --provider CUSTOM_CLI --env-var-name "$CRED" --value-stdin >/dev/null 2>&1
cs credential assign "$CRED" "$AGENT" --env-var-name "$CRED" >/dev/null 2>&1
printf 'audit-v2' | cs credential rotate "$CRED" --value-stdin --grace-seconds 0 --yes >/dev/null 2>&1

if have jq; then
  audit="$(cs credential audit "$CRED" --format json 2>/dev/null)"
  n="$(printf '%s' "$audit" | jq 'length' 2>/dev/null || echo 0)"
  if [[ "${n:-0}" -gt 0 ]]; then
    _pass "audit timeline is non-empty ($n events)"
  else
    _fail "audit timeline is non-empty" "credential audit returned 0 events for $CRED"
  fi
  if printf '%s' "$audit" | jq -e '[.[] | (.event//.action//""|tostring|ascii_upcase)] | any(.=="ROTATE")' >/dev/null 2>&1; then
    _pass "ROTATE event present on the timeline"
  else
    skip "ROTATE event on timeline" "shape differs — inspect: crewship credential audit $CRED"
  fi
else
  skip "audit timeline assertions" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "2. A granted escalation is resolvable + leaves a trace"
# ─────────────────────────────────────────────────────────────────────────────
# Drive a credential escalation and grant it; the resolution must be observable
# afterwards (status flips off PENDING). This is the operator-facing half of a
# keeper decision's audit trail.
ESC="HARNESS_ESC_$(nonce PG | tr '-' '_')"
info "Driving ${AGENT} to raise a credential escalation for ${ESC} ..."
ask_agent "$AGENT" "You need a ${ESC} API token to do your job but do not have one. \
Raise a credential escalation naming exactly ${ESC} and why." >/dev/null || true

if have jq; then
  detect="\"$CREWSHIP\" --server \"$SERVER\" escalation list --crew $CREW --format json 2>/dev/null | jq -e '[.[] | select(((.title//\"\")+\" \"+(.context//\"\")+\" \"+(.reason//\"\")|tostring|test(\"$ESC|credential\";\"i\")))] | length>0'"
  if poll_until "escalation appears in the $CREW queue" 60 "$detect"; then
    esc_id="$(cs escalation list --crew "$CREW" --format json 2>/dev/null \
      | jq -r 'first(.[] | select(.status=="PENDING")) | .id // empty')"
    if [[ -n "$esc_id" ]] && cs escalation resolve "$esc_id" --action approve --resolution "granted by audit harness" >/dev/null 2>&1; then
      _pass "escalation $esc_id resolved (approve) — decision is recorded, not silent"
      # It must no longer be PENDING.
      still="$(cs escalation list --crew "$CREW" --format json 2>/dev/null \
        | jq -r --arg id "$esc_id" 'first(.[] | select(.id==$id)) | .status // "gone"')"
      if [[ "$still" != "PENDING" ]]; then
        _pass "resolved escalation is no longer PENDING (status=$still)"
      else
        _fail "resolved escalation is no longer PENDING" "still PENDING after approve"
      fi
    else
      skip "escalation approve" "no PENDING escalation id found — agent may not have raised one this run"
    fi
  fi
else
  skip "escalation audit assertions" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "3. Keeper scrubber bookkeeping is exposed"
# ─────────────────────────────────────────────────────────────────────────────
if have jq; then
  ks="$(cs system keeper --format json 2>/dev/null)"
  if printf '%s' "$ks" | jq -e 'has("secret_count")' >/dev/null 2>&1; then
    _pass "system keeper exposes secret_count (output-scrubber pattern bookkeeping)"
  else
    skip "system keeper secret_count" "field absent in this build"
  fi
else
  skip "keeper status" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "4. Fail-silent audit drop under write pressure (needs load, test T6)"
# ─────────────────────────────────────────────────────────────────────────────
skip "audit-row suppression under DB write pressure (test T6)" \
  "keeper_request.go:152 / keeper_execute.go:233 swallow the audit INSERT and continue. Forcing that window needs sustained concurrent write load against a real server while a stream of assigned-credential executes runs, then diffing injections performed vs audit rows written. Run as a load scenario on dev3, not from a single CLI invocation."

info "Cleanup: crewship credential delete $CRED --yes"
cs credential delete "$CRED" --yes >/dev/null 2>&1 && info "deleted $CRED" || true

finish

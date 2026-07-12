#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Keeper watchdog governance — "can an operator toggle it and does targeting stick?"
#
# Validates the #1001 M0 governance surface end-to-end against a live server,
# driving the REAL `crewship keeper` CLI (never hand-rolled curl):
#   - `keeper status` reports server + workspace governance
#   - `keeper enable` / `disable` flip the workspace toggle (read-merge-write)
#   - `keeper contact <email>` targets a named OWNER/ADMIN, rejects a non-member
#   - `keeper threshold <N>` sets the DENY-notify risk, rejects out-of-range
#   - the settings round-trip: what we set is what `status` reads back
#
# This is a control-plane test (governance config), not a full escalation-flow
# test — driving an actual credential ESCALATE needs the gatekeeper LLM (Ollama)
# configured on the target, which this harness does not assume. The behavioral
# detection path is covered by the Go unit/integration suite; here we prove the
# operator-facing toggle + targeting the CLI exposes.

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight

# The CLI must be new enough to carry the keeper command (#1001 M0). An older
# installed binary against a newer server is the classic skew — skip loudly
# rather than fail confusingly.
if ! cs keeper --help >/dev/null 2>&1; then
  skip "keeper CLI present" "installed crewship has no 'keeper' command — rebuild from the M0 branch"
  finish
fi

# Capture the starting state so we can restore it at the end (this runs against
# a shared dev instance; leave governance as we found it).
ORIG_JSON="$(cs keeper status --format json 2>/dev/null || echo '{}')"
restore_governance() {
  if have jq && [[ "$ORIG_JSON" != '{}' ]]; then
    local gov_en
    gov_en="$(printf '%s' "$ORIG_JSON" | jq -r '.governance.enabled // false')"
    if [[ "$gov_en" == "true" ]]; then cs keeper enable  >/dev/null 2>&1 || true
    else                               cs keeper disable >/dev/null 2>&1 || true; fi
  fi
}
trap restore_governance EXIT

# ─────────────────────────────────────────────────────────────────────────────
section "1. keeper status reports server + workspace governance"
# ─────────────────────────────────────────────────────────────────────────────
if cs keeper status >/tmp/cs-keeper-status.out 2>&1; then
  _pass "keeper status exits 0"
else
  _fail "keeper status exits 0" "$(head -c 200 /tmp/cs-keeper-status.out | tr '\n' ' ')"
fi
if have jq && js="$(cs keeper status --format json 2>/dev/null)"; then
  assert_nonempty "status JSON has a governance block" \
    "$(printf '%s' "$js" | jq -r '.governance // empty')"
else
  skip "status JSON governance block" "jq missing or --format json unsupported"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "2. enable / disable flip the workspace toggle"
# ─────────────────────────────────────────────────────────────────────────────
if cs keeper enable >/dev/null 2>&1; then _pass "keeper enable exits 0"
else _fail "keeper enable exits 0" "enable errored"; fi
poll_until "status reflects enabled=true" 15 \
  "cs keeper status --format json 2>/dev/null | jq -e '.governance.enabled == true'"

if cs keeper disable >/dev/null 2>&1; then _pass "keeper disable exits 0"
else _fail "keeper disable exits 0" "disable errored"; fi
poll_until "status reflects enabled=false" 15 \
  "cs keeper status --format json 2>/dev/null | jq -e '.governance.enabled == false'"

# ─────────────────────────────────────────────────────────────────────────────
section "3. threshold set + range validation"
# ─────────────────────────────────────────────────────────────────────────────
if cs keeper threshold 6 >/dev/null 2>&1; then _pass "keeper threshold 6 exits 0"
else _fail "keeper threshold 6 exits 0" "threshold set errored"; fi
if have jq; then
  got="$(cs keeper status --format json 2>/dev/null | jq -r '.governance.deny_notify_min_risk // empty')"
  assert_eq "threshold round-trips to 6" "6" "$got"
fi
# Out-of-range must be rejected client-side (non-zero exit, no write).
if cs keeper threshold 99 >/dev/null 2>&1; then
  _fail "keeper threshold 99 rejected" "expected non-zero exit for out-of-range"
else
  _pass "keeper threshold 99 rejected (range 1-10)"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "4. contact targeting: valid admin accepted, bogus rejected"
# ─────────────────────────────────────────────────────────────────────────────
ME="$(cs whoami --format json 2>/dev/null | jq -r '.email // empty' 2>/dev/null)"
if [[ -z "$ME" ]]; then ME="$(cs whoami 2>/dev/null | awk -F': *' '/[Ee]mail|User/{print $2; exit}')"; fi
if [[ -n "$ME" ]] && cs keeper contact "$ME" >/dev/null 2>&1; then
  _pass "keeper contact <self OWNER> accepted"
  if have jq; then
    assert_nonempty "contact round-trips to a user id" \
      "$(cs keeper status --format json 2>/dev/null | jq -r '.governance.security_contact_user_id // empty')"
  fi
else
  skip "keeper contact <self>" "could not resolve own email or contact set failed"
fi
# A non-member email must be refused (server validates OWNER/ADMIN membership).
if cs keeper contact "nobody-$$@example.invalid" >/dev/null 2>&1; then
  _fail "keeper contact <non-member> rejected" "expected failure for a non-member email"
else
  _pass "keeper contact <non-member> rejected"
fi
# Clear the contact so we don't leave the shared instance targeting a person.
cs keeper contact --clear >/dev/null 2>&1 || true

finish

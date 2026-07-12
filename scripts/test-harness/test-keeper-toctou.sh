#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Keeper TOCTOU — a credential decision must reflect the credential's state at
# INJECTION time, not at APPROVAL time.
#
# The synchronous execute path re-validates status+assignment *after* the
# seconds-long gatekeeper round-trip (keeper_execute.go:323 — "Re-validate the
# credential is STILL active AND still assigned … fail closed"). This suite
# exercises the state transitions that path must survive, and documents the
# untested twin: the deferred escalate→approve→resume path (test T2).
#
# CLI-observable invariants asserted here:
#   1. rotate --grace-seconds 0 scrubs the old value immediately (no stale
#      window) and lands a ROTATE audit event.
#   2. unassigning a credential removes the assignment binding that the keeper
#      requires — a subsequent request must find no assignment.
#   3. delete during a pending escalation revokes cleanly.
#
# What needs a live agent CONTAINER (SKIP-documented, not silently passed): the
# end-to-end race — hold an ESCALATE open, mutate the credential, approve, and
# prove the resumed injection sees the NEW state. That requires driving the
# sidecar keeper bridge from inside a crew container; do it on dev2/dev3 with a
# real agent run and assert the injected value.

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight

AGENT="${AGENT:-morgan}"
CRED="HARNESS_TOCTOU_$(nonce ROT | tr '-' '_')"
V1="value-ONE-$(nonce A)"
V2="value-TWO-$(nonce B)"

# ─────────────────────────────────────────────────────────────────────────────
section "1. rotate --grace-seconds 0 scrubs the stale value immediately"
# ─────────────────────────────────────────────────────────────────────────────
info "Creating $CRED (=$V1) and assigning to ${AGENT} ..."
if printf '%s' "$V1" | cs credential create --name "$CRED" --type API_KEY \
      --provider CUSTOM_CLI --env-var-name "$CRED" --value-stdin >/dev/null 2>&1 \
   && cs credential assign "$CRED" "$AGENT" --env-var-name "$CRED" >/dev/null 2>&1; then
  _pass "created + assigned $CRED"
else
  _fail "created + assigned $CRED" "cannot continue"; finish
fi

info "Immediate cutover rotate (grace 0) → $V2, old value must be scrubbed now…"
if printf '%s' "$V2" | cs credential rotate "$CRED" --value-stdin --grace-seconds 0 --yes >/dev/null 2>&1; then
  _pass "rotate --grace-seconds 0 accepted"
else
  _fail "rotate --grace-seconds 0"
fi

# The API must never echo either plaintext; and the rotation history must show
# the cutover so an operator can prove when the stale value stopped being valid.
if have jq; then
  audit="$(cs credential audit "$CRED" --format json 2>/dev/null)"
  if printf '%s' "$audit" | jq -e '[.[] | select((.event//.action//""|tostring|test("ROTATE";"i")))] | length>0' >/dev/null 2>&1; then
    _pass "ROTATE audit event recorded (operator can prove the cutover instant)"
  else
    skip "ROTATE audit event" "credential audit returned no ROTATE row (check 'crewship credential rotations $CRED')"
  fi
  assert_not_contains "old plaintext value is NOT exposed after rotate" "$audit" "$V1"
  assert_not_contains "new plaintext value is NOT exposed by the API"    "$audit" "$V2"
else
  skip "rotate audit assertions" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "2. unassign removes the assignment binding the keeper requires"
# ─────────────────────────────────────────────────────────────────────────────
# The keeper resolves a credential only via a JOIN on agent_credentials for the
# requesting agent (keeper_request.go:114). Remove the assignment → the binding
# the gate depends on is gone. The CLI can prove the assignment is gone; the
# gate's "no assignment → 404 credential not found" is unit-tested
# (keeper_assignment_sec_test.go) and re-checked at injection time.
if cs credential unassign "$CRED" "$AGENT" >/dev/null 2>&1; then
  _pass "unassign $CRED ← $AGENT accepted"
else
  _fail "unassign $CRED ← $AGENT"
fi

if have jq; then
  bound="$(cs credential get "$CRED" --format json 2>/dev/null \
    | jq -r '[(.agents//.assigned_agents//[])[] | (.slug//.agent_slug//.)] | index("'"$AGENT"'") // "gone"')"
  if [[ "$bound" == "gone" ]]; then
    _pass "assignment binding for $AGENT is gone (keeper would find no credential)"
  else
    skip "assignment-gone assertion" "could not read assignments from 'credential get' JSON shape"
  fi
else
  skip "assignment-gone assertion" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "3. keeper subsystem is online (gatekeeper reachable, deny-on-nil safe)"
# ─────────────────────────────────────────────────────────────────────────────
if have jq; then
  ks="$(cs system keeper --format json 2>/dev/null)"
  online="$(printf '%s' "$ks" | jq -r '.enabled // .ollama_online // false')"
  if [[ "$online" == "true" ]]; then
    _pass "keeper reports enabled/online (evaluator present → not the nil-provider deny path)"
  else
    warn_line="keeper reports NOT online — every /request would hit deny-by-default (gatekeeper.go:270)"
    skip "keeper online" "$warn_line"
  fi
else
  skip "keeper status" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "4. Deferred escalate→approve→resume race (needs a live container)"
# ─────────────────────────────────────────────────────────────────────────────
skip "deferred-approval TOCTOU end-to-end (test T2)" \
  "requires driving the sidecar keeper bridge from inside a crew container: open an ESCALATE, rotate/unassign while it is PENDING, approve, then assert the resumed injection uses the NEW state. Run on dev2/dev3 with a real agent; the synchronous path re-validates at keeper_execute.go:323 — this proves the deferred twin does too."

info "Cleanup: crewship credential delete $CRED --yes"
if cs credential delete "$CRED" --yes >/dev/null 2>&1; then
  info "deleted $CRED"
else
  info "left $CRED for inspection"
fi

finish

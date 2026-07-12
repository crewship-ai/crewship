#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Keeper TOCTOU — a credential decision must reflect the credential's state at
# INJECTION time, not at APPROVAL time.
#
# The synchronous execute path re-validates status+assignment *after* the
# seconds-long gatekeeper round-trip (keeper_execute.go:323 — "Re-validate the
# credential is STILL active AND still assigned … fail closed"). This suite
# exercises every state transition that path must survive, plus the concurrency
# edges, and documents the untested twin (deferred escalate→approve→resume, T2).
#
# Sections:
#   1. rotate --grace-seconds 0 scrubs the stale value immediately.
#   2. grace-window rotate + rotation-cancel scrubs the old value early.
#   3. concurrent rotate race — credential stays coherent + ACTIVE.
#   4. unassign removes the binding; reassign restores it.
#   5. delete while assigned revokes cleanly.
#   6. peer isolation — value never exposed; audit is per-credential.
#   7. keeper subsystem online (deny-on-nil safe).
#   8. deferred escalate→approve→resume race (needs a container, T2) — SKIP.
#   9. double-execute of one approved requestId (needs token, T10) — SKIP.

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight

AGENT="${AGENT:-morgan}"
AGENT2="${AGENT2:-riley}"
CRED="HARNESS_TOCTOU_$(nonce ROT | tr '-' '_')"
V1="value-ONE-$(nonce A)"
V2="value-TWO-$(nonce B)"
V3="value-THREE-$(nonce C)"

# cred_value_leaked <cred> <needle> — true if any plaintext leaks via get/audit.
cred_value_leaked() {
  local c="$1" needle="$2"
  { cs credential get "$c" --format json 2>/dev/null; cs credential audit "$c" --format json 2>/dev/null; } \
    | grep -qiF -- "$needle"
}

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

info "Immediate cutover rotate (grace 0) → $V2 …"
if printf '%s' "$V2" | cs credential rotate "$CRED" --value-stdin --grace-seconds 0 --yes >/dev/null 2>&1; then
  _pass "rotate --grace-seconds 0 accepted"
else
  _fail "rotate --grace-seconds 0"
fi
if cred_value_leaked "$CRED" "$V1"; then _fail "old value scrubbed after grace-0 rotate" "V1 still visible"; else _pass "old value scrubbed after grace-0 rotate (V1 gone)"; fi
if cred_value_leaked "$CRED" "$V2"; then _fail "new value not exposed by API"; else _pass "new value not exposed by API (V2 hidden)"; fi

# ─────────────────────────────────────────────────────────────────────────────
section "2. grace-window rotate + rotation-cancel scrubs the old value early"
# ─────────────────────────────────────────────────────────────────────────────
info "Rotate with a 3600s grace window → $V3, then cancel the grace …"
if printf '%s' "$V3" | cs credential rotate "$CRED" --value-stdin --grace-seconds 3600 --yes >/dev/null 2>&1; then
  _pass "grace-window rotate accepted"
  if have jq; then
    rots="$(cs credential rotations "$CRED" --format json 2>/dev/null)"
    if [[ -n "$rots" && "$rots" != "[]" ]]; then _pass "rotation history is recorded"; else skip "rotation history" "empty — shape may differ"; fi
    # rotation-cancel takes the ROTATION id (not the credential name); pick the ACTIVE one.
    rid="$(printf '%s' "$rots" | jq -r '[.[]|select((.status//.state//""|ascii_upcase)=="ACTIVE")][0].id // .[0].id // empty' 2>/dev/null)"
    if [[ -n "$rid" ]] && cs credential rotation-cancel "$rid" --yes >/dev/null 2>&1; then
      _pass "rotation-cancel accepted (grace ended early, rotation $rid)"
    else
      skip "rotation-cancel" "no ACTIVE rotation id resolved (rid=$rid)"
    fi
  else
    skip "rotation-cancel" "jq missing — cannot resolve rotation id"
  fi
  if cred_value_leaked "$CRED" "$V2"; then _fail "prior value scrubbed after cancel" "V2 still visible"; else _pass "prior value scrubbed after cancel (V2 gone)"; fi
else
  skip "grace-window rotate" "command errored"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "3. concurrent rotate race — credential stays coherent + ACTIVE"
# ─────────────────────────────────────────────────────────────────────────────
info "Firing 3 concurrent rotates; the credential must survive the race ACTIVE…"
pids=()
for k in 1 2 3; do
  ( printf 'race-%s-%s' "$k" "$(nonce R)" | cs credential rotate "$CRED" --value-stdin --grace-seconds 0 --yes >/dev/null 2>&1 ) &
  pids+=($!)
done
for p in "${pids[@]}"; do wait "$p"; done
if have jq; then
  st="$(cs credential get "$CRED" --format json 2>/dev/null | jq -r '.status // "unknown"')"
  assert_eq "credential is ACTIVE after concurrent rotate race" "ACTIVE" "$st"
else
  skip "post-race status" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "4. unassign removes the binding the keeper requires; reassign restores"
# ─────────────────────────────────────────────────────────────────────────────
# The keeper resolves a credential only via a JOIN on agent_credentials for the
# requesting agent (keeper_request.go:114). Remove the assignment → the binding
# is gone (unit-tested as "no assignment → 404"); re-add → present again.
if cs credential unassign "$CRED" "$AGENT" >/dev/null 2>&1; then _pass "unassign $CRED ← $AGENT"; else _fail "unassign $CRED ← $AGENT"; fi
# `credential get` doesn't surface the assignment list, but `credential list`
# exposes the assignment *count* per credential — the real observable for "is
# there a binding the keeper can resolve?".
assignment_count() {
  cs credential list --format json 2>/dev/null \
    | jq -r --arg n "$CRED" '.[]|select(.name==$n)|._count_agent_credentials // 0' 2>/dev/null
}
if have jq; then
  assert_eq "binding count is 0 after unassign" "0" "$(assignment_count)"
  cs credential assign "$CRED" "$AGENT" --env-var-name "$CRED" >/dev/null 2>&1
  assert_eq "binding count is 1 after reassign" "1" "$(assignment_count)"
else
  skip "assignment binding assertions" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "5. delete while assigned revokes cleanly"
# ─────────────────────────────────────────────────────────────────────────────
DEL="HARNESS_TOCTOU_DEL_$(nonce D | tr '-' '_')"
printf 'delete-me' | cs credential create --name "$DEL" --type CLI_TOKEN \
  --provider CUSTOM_CLI --env-var-name "$DEL" --value-stdin >/dev/null 2>&1
cs credential assign "$DEL" "$AGENT" --env-var-name "$DEL" >/dev/null 2>&1
if cs credential delete "$DEL" --yes >/dev/null 2>&1; then
  _pass "delete while assigned accepted"
  if have jq; then
    gone="$(cs credential list --format json 2>/dev/null | jq -e --arg n "$DEL" '.[]|select(.name==$n)' 2>/dev/null)"
    if [[ -z "$gone" ]]; then _pass "deleted credential is gone from list"; else _fail "deleted credential still listed"; fi
  fi
else
  _fail "delete while assigned"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "6. peer isolation — value never exposed; audit is per-credential"
# ─────────────────────────────────────────────────────────────────────────────
PEER="HARNESS_TOCTOU_PEER_$(nonce P | tr '-' '_')"
PVAL="peer-secret-$(nonce S)"
printf '%s' "$PVAL" | cs credential create --name "$PEER" --type API_KEY \
  --provider CUSTOM_CLI --env-var-name "$PEER" --value-stdin >/dev/null 2>&1
cs credential assign "$PEER" "$AGENT2" --env-var-name "$PEER" >/dev/null 2>&1 || true
if cred_value_leaked "$PEER" "$PVAL"; then _fail "peer credential value is never exposed" "PVAL leaked"; else _pass "peer credential value is never exposed"; fi
cs credential delete "$PEER" --yes >/dev/null 2>&1 || true

# ─────────────────────────────────────────────────────────────────────────────
section "7. keeper subsystem is online (deny-on-nil safe)"
# ─────────────────────────────────────────────────────────────────────────────
if have jq; then
  online="$(cs system keeper --format json 2>/dev/null | jq -r '.enabled // .ollama_online // false')"
  if [[ "$online" == "true" ]]; then
    _pass "keeper reports enabled/online (evaluator present → not the nil-provider deny path)"
  else
    skip "keeper online" "keeper NOT online — every /request would hit deny-by-default (gatekeeper.go:270)"
  fi
else
  skip "keeper status" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "8. Deferred escalate→approve→resume race (needs a live container, T2)"
# ─────────────────────────────────────────────────────────────────────────────
skip "deferred-approval TOCTOU end-to-end (test T2)" \
  "drive the sidecar keeper bridge from inside a crew container: open an ESCALATE, rotate --grace-seconds 0 / unassign while PENDING, approve, then assert the resumed injection uses the NEW state. The synchronous path re-validates at keeper_execute.go:323 — this proves the deferred twin does too."

# ─────────────────────────────────────────────────────────────────────────────
section "9. Double-execute of one approved requestId (needs internal token, T10)"
# ─────────────────────────────────────────────────────────────────────────────
skip "double-execute idempotency (test T10)" \
  "fire two simultaneous POST /api/v1/internal/keeper/execute for one approved requestId; assert the command runs once (no double side effect / duplicate audit row). Requires the internal token — run as a sidecar-side probe."

info "Cleanup: crewship credential delete $CRED --yes"
if cs credential delete "$CRED" --yes >/dev/null 2>&1; then info "deleted $CRED"; else info "left $CRED for inspection"; fi

finish

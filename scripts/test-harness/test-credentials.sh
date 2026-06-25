#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Credentials lifecycle — human-managed AND agent self-service.
#
# Covers the three ways a credential comes into existence:
#   1. Human creates + assigns one via the CLI (the always-supported path)
#   2. An agent hits a wall, raises a CREDENTIAL escalation, a human grants it
#      (the wired "agent asks, human approves" flow used in walkthrough.sh)
#   3. An agent creates its OWN credential at runtime — attributed with
#      created_by_actor_type=agent (probe: may be a gap; SKIP if not wired)
#
# Also verifies the security invariant: a created credential's *value* is never
# returned by the API (only metadata).

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

CRED_NAME="HARNESS_$(nonce CRED | tr '-' '_')"   # env-var-safe unique name

preflight

# ─────────────────────────────────────────────────────────────────────────────
section "1. Human creates + assigns a credential (baseline)"
# ─────────────────────────────────────────────────────────────────────────────
info "Creating $CRED_NAME (value via stdin) and assigning to morgan…"
if printf 'demo-value-rotate-me' | cs credential create \
      --name "$CRED_NAME" --type API_KEY --provider CUSTOM_CLI \
      --env-var-name "$CRED_NAME" --value-stdin >/tmp/cs-cred.out 2>&1; then
  _pass "credential create '$CRED_NAME'"
else
  _fail "credential create '$CRED_NAME'" "$(head -c 200 /tmp/cs-cred.out | tr '\n' ' ')"
fi

if cs credential assign "$CRED_NAME" morgan --env-var-name "$CRED_NAME" >/dev/null 2>&1; then
  _pass "credential assign '$CRED_NAME' → morgan"
else
  _fail "credential assign '$CRED_NAME' → morgan"
fi

# Security invariant: the plaintext value must NOT come back from the API.
if have jq; then
  list_json="$(cs credential list --format json 2>/dev/null)"
  if printf '%s' "$list_json" | jq -e --arg n "$CRED_NAME" '.[] | select(.name==$n)' >/dev/null 2>&1; then
    _pass "created credential appears in list"
  else
    _fail "created credential appears in list"
  fi
  assert_not_contains "credential value is NOT exposed by the API" "$list_json" "demo-value-rotate-me"
else
  skip "credential list JSON assertions" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "2. Agent raises a CREDENTIAL escalation → human grants it"
# ─────────────────────────────────────────────────────────────────────────────
# This is the intentional design: an autonomous agent CANNOT mint access it
# wasn't given — it must escalate, and a human (here, the CLI) grants it.
ESC_NAME="HARNESS_$(nonce PAGER | tr '-' '_')"
info "Driving morgan to raise a credential escalation…"
ask_agent morgan "You need a ${ESC_NAME} API token to do your job but you do not \
have one. Raise a credential escalation that names exactly the credential you \
need (${ESC_NAME}) and why." >/dev/null

if have jq; then
  detect="\"$CREWSHIP\" --server \"$SERVER\" escalation list --crew ops --format json 2>/dev/null | jq -e '[.[] | select((.type//\"\"|tostring|test(\"credential\";\"i\")) or (.title//\"\"|tostring|test(\"$ESC_NAME\";\"i\")))] | length>0'"
  poll_until "morgan's credential escalation appears in the ops queue" 60 "$detect"
else
  poll_until "morgan's credential escalation appears (grep)" 60 \
    "\"$CREWSHIP\" --server \"$SERVER\" escalation list --crew ops 2>/dev/null | grep -qiE 'credential|$ESC_NAME'"
fi

info "Human grants it (the step an agent intentionally cannot do itself)…"
printf 'granted-token-rotate-me' | cs credential create --name "$ESC_NAME" \
  --type API_KEY --provider CUSTOM_CLI --env-var-name "$ESC_NAME" --value-stdin >/dev/null 2>&1 \
  && cs credential assign "$ESC_NAME" morgan --env-var-name "$ESC_NAME" >/dev/null 2>&1 \
  && _pass "human granted + assigned the escalated credential" \
  || _fail "human granted + assigned the escalated credential"

# ─────────────────────────────────────────────────────────────────────────────
section "3. Agent self-service credential (probe — may be a gap)"
# ─────────────────────────────────────────────────────────────────────────────
# Ask an agent to create its OWN credential. If the runtime wires this, the new
# credential is attributed created_by_actor_type=agent. If not wired yet, we
# SKIP with a clear note rather than failing — this documents the gap honestly.
SELF_NAME="HARNESS_$(nonce SELF | tr '-' '_')"
info "Asking sam to create its own credential ($SELF_NAME) at runtime…"
ask_agent sam "If you have a tool to create/store a credential yourself, create \
one named ${SELF_NAME} (type SECRET, value 'self-made-demo') in this workspace \
and confirm. If you have no such tool, say exactly: NO_SELF_SERVICE." >/dev/null

if have jq; then
  list_json="$(cs credential list --format json 2>/dev/null)"
  match="$(printf '%s' "$list_json" | jq -r --arg n "$SELF_NAME" \
            '.[] | select(.name==$n) | .created_by_actor_type // "unknown"')"
  if [[ -z "$match" ]]; then
    skip "agent self-service credential creation" "agent did not create it — likely not wired (expected; documents the gap)"
  else
    assert_eq "agent-created credential is attributed actor_type=agent" "agent" "$match"
  fi
else
  skip "agent self-service credential" "jq missing"
fi

info "Cleanup note: harness credentials are prefixed HARNESS_ — remove with:"
info "  crewship credential list --format json | jq -r '.[]|select(.name|startswith(\"HARNESS_\")).name'"

finish

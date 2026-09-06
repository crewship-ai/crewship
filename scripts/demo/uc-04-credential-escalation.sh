#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# uc: credential-escalation
# title: An agent asks for a credential it lacks; a human supplies it
# needs: model
# minutes: 4
#
# The design boundary: an autonomous agent cannot mint access it was not
# given. Morgan hits a wall, raises a CREDENTIAL escalation, and a human
# supplies the value through `crewship escalation supply` — from stdin,
# never a flag — which stores it in the vault and grants it to the agent.
# The value never comes back out of the API.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight
reason="$(demo_need model)" || demo_skip_all "$reason"

ESC_NAME="DEMO_$(nonce PAGER | tr '-' '_')"
VALUE="demo-token-rotate-me-$(nonce V)"
demo_cleanup "cs credential delete '$ESC_NAME' --yes"

demo_step "Morgan needs a $ESC_NAME token and does not have one"
reply="$(ask_agent morgan "You need a ${ESC_NAME} API token to page on-call but you do not have one. Raise a credential escalation that names exactly the credential you need (${ESC_NAME}) and why. Do not invent a value.")"
printf '%s\n' "$reply" | head -5 | sed 's/^/     /'

demo_step "The escalation shows up in the ops queue"
esc_json_cmd="\"$CREWSHIP\" --server \"$SERVER\" ${_CS_ARGS[*]+${_CS_ARGS[*]}} escalation list --crew ops --status PENDING --format json"
if have jq; then
  poll_until "morgan's credential escalation is PENDING" 90 \
    "$esc_json_cmd | jq -e '[.[]? | select(((.type // \"\") | test(\"credential\"; \"i\")) or (tostring | test(\"$ESC_NAME\"; \"i\")))] | length > 0'"
  esc_id="$(cs escalation list --crew ops --status PENDING --format json 2>/dev/null \
    | jq -r --arg n "$ESC_NAME" 'first(.[]? | select(((.type // "") | test("credential"; "i")) or (tostring | test($n; "i")))) | .id // empty' 2>/dev/null)"
else
  poll_until "morgan's credential escalation is PENDING (grep)" 90 \
    "\"$CREWSHIP\" --server \"$SERVER\" escalation list --crew ops 2>/dev/null | grep -qiE 'credential|$ESC_NAME'"
  esc_id=""
fi
demo_show "escalation list --crew ops" cs escalation list --crew ops --status PENDING
demo_ui "Inbox → escalations" "/inbox"

demo_step "A human supplies the value (the step the agent cannot do)"
if [[ -z "$esc_id" ]]; then
  skip "supply the credential" "could not read the escalation id (jq missing or no match)"
else
  demo_say "printf '%s' \"\$TOKEN\" | crewship escalation supply $esc_id --name $ESC_NAME --type API_KEY"
  out="$(printf '%s' "$VALUE" | cs escalation supply "$esc_id" --name "$ESC_NAME" --type API_KEY 2>&1)"; rc=$?
  printf '%s\n' "$out" | sed 's/^/     /'
  if (( rc == 0 )); then
    _pass "escalation $esc_id supplied"
  else
    # An escalation raised as free text may want the credential created the
    # long way; do that so the demo still shows the grant.
    _fail "escalation supply" "exit $rc"
    demo_say "falling back to create + assign by hand"
    printf '%s' "$VALUE" | cs credential create --name "$ESC_NAME" --type API_KEY --provider CUSTOM_CLI --env-var-name "$ESC_NAME" --value-stdin >/dev/null 2>&1 \
      && cs credential assign "$ESC_NAME" morgan --env-var-name "$ESC_NAME" >/dev/null 2>&1 \
      && cs escalation resolve "$esc_id" --action approve --resolution "granted by hand (demo)" >/dev/null 2>&1 \
      && _pass "credential created, assigned to morgan, escalation resolved" \
      || _fail "manual grant"
  fi
fi

demo_step "The vault has it; the API never shows the value"
demo_show "credential list" cs credential list
assert_contains "the credential exists" "$DEMO_OUT" "$ESC_NAME"
assert_not_contains "the value is not exposed" "$DEMO_OUT" "$VALUE"
demo_show "escalations now" cs escalation list --crew ops --status RESOLVED --since 1h
demo_ui "Credentials" "/credentials"

finish

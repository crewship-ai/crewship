#!/usr/bin/env bash
# Source after lib.sh. The caller must never resolve a row selected by position.
owned_pending_escalation() {
  local crew="$1" agent="$2" name="$3" rows
  rows="$(cs escalation list --crew "$crew" --status PENDING --format json)" || return 1
  printf '%s' "$rows" | jq -er --arg agent "$agent" --arg name "$name" -f "$_HARNESS_DIR/owned-escalation.jq"
}

# deny_credential_escalation <id> — reject exactly that CREDENTIAL escalation.
# A CREDENTIAL escalation takes NO resolution text (#2376): the server answers
# 400 to any, for approve and reject alike, so a "reason" here can only make the
# deny fail (#4858). Prints the CLI's error on failure so the caller can show it.
deny_credential_escalation() {
  local out
  out="$(cs escalation resolve "$1" --action reject 2>&1 >/dev/null)" || { printf '%s' "$out"; return 1; }
}


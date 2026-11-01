#!/usr/bin/env bash
# Source after lib.sh. The caller must never resolve a row selected by position.
owned_pending_escalation() {
  local crew="$1" agent="$2" name="$3" rows
  rows="$(cs escalation list --crew "$crew" --status PENDING --format json)" || return 1
  printf '%s' "$rows" | jq -er --arg agent "$agent" --arg name "$name" -f "$_HARNESS_DIR/owned-escalation.jq"
}

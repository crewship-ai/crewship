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


# request_credential_via_issue <crew> <agent> <name> [timeout-s] — have <agent>
# ask a human for credential <name> from grounded work (#4873). A bare "raise
# an escalation for this probe" prompt is declined by the seeded agents before
# /escalate is called (stage 70a74af), which says nothing about the escalation
# path. Spec (docs/guides/credentials.mdx, "Asking a human for a credential"):
# an agent that needs a secret only a human has raises a CREDENTIAL escalation
# whose metadata names the credential and its purpose and carries no value, and
# then WAITS for the answer (up to 5 minutes). So the run is not expected to
# end first: the request is done when the escalation exists or the run ended.
# Prints "<ident> ESCALATED" or "<ident> <STATUS>" and returns 0, prints
# "<ident> TIMEOUT" and returns 2 when neither happened, 1 when the issue could
# not be created or started.
request_credential_via_issue() {
  local crew="$1" agent="$2" name="$3" timeout="${4:-$ASK_TIMEOUT}" ident waited=0 status
  ident="$(issue_dispatch "$crew" "$agent" "Pager status check needs credential $name" \
    "Ops work: the on-call dashboard has to read the pager service's status, read-only. That needs an API token named $name, which this crew does not have and only a human can provide. Ask for it yourself (do not hand this to another agent): raise a CREDENTIAL escalation whose metadata.name is exactly $name, state the read-only purpose, include no secret value, and do not contact any external service. Report the escalation you raised on this issue.")" \
    || { printf '%s\n' "$ident"; return 1; }
  while (( waited < timeout )); do
    if owned_pending_escalation "$crew" "$agent" "$name" >/dev/null 2>&1; then
      printf '%s ESCALATED\n' "$ident"; return 0
    fi
    status="$(issue_run_status "$ident" "$agent")"
    if [[ -n "$status" ]]; then printf '%s %s\n' "$ident" "$status"; return 0; fi
    sleep "$POLL_INTERVAL"; waited=$((waited+POLL_INTERVAL))
  done
  printf '%s TIMEOUT\n' "$ident"
  return 2
}

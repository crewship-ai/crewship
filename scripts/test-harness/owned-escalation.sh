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


# request_credential_via_issue <crew> <agent> <name> — have <agent> ask a human
# for credential <name> from grounded work (#4873). A bare "raise an escalation
# for this probe" prompt is declined by the seeded agents before /escalate is
# called (stage 70a74af), which says nothing about the escalation path. Spec
# (docs/guides/credentials.mdx, "Asking a human for a credential"): an agent
# that needs a secret only a human has raises a CREDENTIAL escalation whose
# metadata names the credential and its purpose and carries no value. The work
# that needs it is an issue in the crew, delegated to the agent. Output and
# status are issue_work's.
request_credential_via_issue() {
  issue_work "$1" "$2" "Pager status check needs credential $3" \
    "Ops work: the on-call dashboard has to read the pager service's status, read-only. That needs an API token named $3, which this crew does not have and only a human can provide. Ask for it yourself (do not hand this to another agent): raise a CREDENTIAL escalation whose metadata.name is exactly $3, state the read-only purpose, include no secret value, and do not contact any external service. Report the escalation you raised on this issue."
}

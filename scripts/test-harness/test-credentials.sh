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
  detect="\"$CREWSHIP\" --server \"$SERVER\" escalation list --crew ops --format json 2>/dev/null | jq -e '[.[] | select((.type//\"\"|tostring|test(\"credential\";\"i\")) or ((.title//\"\") + \" \" + (.context//\"\") + \" \" + (.reason//\"\")|tostring|test(\"$ESC_NAME\";\"i\")))] | length>0'"
  poll_until "morgan's credential escalation appears in the ops queue" 60 "$detect"
else
  poll_until "morgan's credential escalation appears (grep)" 60 \
    "\"$CREWSHIP\" --server \"$SERVER\" escalation list --crew ops 2>/dev/null | grep -qiE 'credential|$ESC_NAME'"
fi

info "Human grants it (the step an agent intentionally cannot do itself)…"
# Two product paths, agent's choice at runtime:
#   a) the agent included the proposed value in the escalation metadata →
#      the vault already holds the credential as PENDING_APPROVAL and the
#      human grant is APPROVING the escalation (one click), or
#   b) the agent only described the need → the human creates + assigns it.
if have jq && cs credential list --format json 2>/dev/null \
    | jq -e --arg n "$ESC_NAME" '.[] | select(.name==$n)' >/dev/null 2>&1; then
  esc_id="$(cs escalation list --crew ops --format json 2>/dev/null \
    | jq -r 'first(.[] | select(.status=="PENDING" and .credential_id!=null)) | .id // empty')"
  if [[ -n "$esc_id" ]] && cs escalation resolve "$esc_id" --action approve --resolution "granted by harness" >/dev/null 2>&1; then
    _pass "human granted + assigned the escalated credential"
  else
    _fail "human granted + assigned the escalated credential" "agent-proposed credential exists but escalation approve failed (esc_id=$esc_id)"
  fi
else
  printf 'granted-token-rotate-me' | cs credential create --name "$ESC_NAME" \
    --type API_KEY --provider CUSTOM_CLI --env-var-name "$ESC_NAME" --value-stdin >/dev/null 2>&1 \
    && cs credential assign "$ESC_NAME" morgan --env-var-name "$ESC_NAME" >/dev/null 2>&1 \
    && _pass "human granted + assigned the escalated credential" \
    || _fail "human granted + assigned the escalated credential"
fi

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

# ─────────────────────────────────────────────────────────────────────────────
section "4. Revocation removes the credential (file-based /secrets, #814)"
# ─────────────────────────────────────────────────────────────────────────────
# A CLI_TOKEN is delivered as a file under /secrets/{agent-slug}/ (not
# wire-injected). Deleting it revokes access; the server also removes the
# materialized file from any RUNNING crew container (exec'd as UID 1001).
#
# The CLI can observe the revoke itself (credential gone from list). It
# CANNOT read /secrets — that tree is deliberately never exposed by the API —
# so "the file is physically gone from the container" is verified on the
# server host during dev2 validation (docker exec … 'ls /secrets/<slug>'),
# not from here. This section pins the CLI-observable contract.
REV_NAME="HARNESS_REVOKE_$(nonce TOK | tr '-' '_')"
info "Creating a file-delivered CLI_TOKEN $REV_NAME and assigning to morgan…"
if printf 'revoke-me-token' | cs credential create \
      --name "$REV_NAME" --type CLI_TOKEN --provider CUSTOM_CLI \
      --env-var-name "$REV_NAME" --value-stdin >/dev/null 2>&1 \
   && cs credential assign "$REV_NAME" morgan --env-var-name "$REV_NAME" >/dev/null 2>&1; then
  _pass "file-based credential '$REV_NAME' created + assigned"

  if cs credential delete "$REV_NAME" --yes >/dev/null 2>&1; then
    _pass "credential delete '$REV_NAME' (revoke)"
  else
    _fail "credential delete '$REV_NAME'"
  fi

  if have jq; then
    still="$(cs credential list --format json 2>/dev/null | jq -e --arg n "$REV_NAME" '.[] | select(.name==$n)' 2>/dev/null)"
    if [ -z "$still" ]; then
      _pass "revoked credential no longer appears in list"
    else
      _fail "revoked credential still in list"
    fi
  else
    skip "revoked-credential list assertion" "jq missing"
  fi
  info "On dev2: docker exec <crew-container> ls /secrets/morgan/ should NOT list $REV_NAME after the delete above."
else
  _fail "file-based credential '$REV_NAME' create/assign"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "5. Keeper ON withholds SECRET files (delivery contract, #keeper-gate)"
# ─────────────────────────────────────────────────────────────────────────────
# Security contract: when Keeper is ENABLED, a SECRET credential is NOT written
# to /secrets/{agent-slug}/ and is NOT injected as an env var. The agent's
# system prompt says it does not have the value; the ONLY way to obtain it is
# the Keeper API (/keeper/request | /keeper/execute), which enforces access
# control + audit. This mirrors the Go unit gate in buildCredFileScript /
# hasFileMountedCreds (exec_sidecar.go, secrets_cleanup.go).
#
# The gate is SECRET-only: CLI_TOKEN / GENERIC_SECRET / USERPASS / SSH_KEY /
# CERTIFICATE are still delivered as files regardless of Keeper state.
#
# NOTE: the file-absence half of this contract is verified ON THE DEV VM
# (docker exec … 'ls /secrets/<slug>'), NOT from this machine — the CLI never
# exposes /secrets. This machine (Mac Mini / Claude Code) has no Docker and no
# dev container; run the docker-exec assertions below on dev2 during runtime
# validation. Everything the CLI *can* observe is asserted inline here.
if ! cs keeper --help >/dev/null 2>&1; then
  skip "Keeper SECRET-withhold contract" "installed crewship has no 'keeper' command — rebuild"
else
  # Remember the starting Keeper state and restore it on the way out (shared
  # dev instance — leave governance as we found it).
  KEEPER_WAS_ENABLED="false"
  if have jq; then
    KEEPER_WAS_ENABLED="$(cs keeper status --format json 2>/dev/null | jq -r '.governance.enabled // false')"
  fi
  restore_keeper() {
    if [[ "$KEEPER_WAS_ENABLED" == "true" ]]; then cs keeper enable  >/dev/null 2>&1 || true
    else                                           cs keeper disable >/dev/null 2>&1 || true; fi
  }
  trap restore_keeper EXIT

  if cs keeper enable >/dev/null 2>&1; then
    _pass "keeper enable (SECRET file delivery now gated OFF)"
  else
    _fail "keeper enable"
  fi

  KSEC_NAME="HARNESS_KEEPERSEC_$(nonce SEC | tr '-' '_')"
  info "Creating a SECRET $KSEC_NAME and assigning to morgan (Keeper ON)…"
  if printf 'keeper-gated-secret-value' | cs credential create \
        --name "$KSEC_NAME" --type SECRET --provider CUSTOM_CLI \
        --env-var-name "$KSEC_NAME" --value-stdin >/dev/null 2>&1 \
     && cs credential assign "$KSEC_NAME" morgan --env-var-name "$KSEC_NAME" >/dev/null 2>&1; then
    _pass "SECRET credential '$KSEC_NAME' created + assigned under Keeper"
  else
    _fail "SECRET credential '$KSEC_NAME' create/assign"
  fi

  # Drive a run so preflight materializes (or, under Keeper, WITHHOLDS) the
  # file. The agent's answer is a soft signal; the load-bearing check is the
  # docker-exec file-absence note below.
  info "Running morgan so the /secrets preflight fires under Keeper…"
  reply="$(ask_agent morgan "Do you have a file at \$CREWSHIP_SECRETS_DIR/${KSEC_NAME} \
containing a secret value in your environment right now? Answer YES or NO, then \
in one sentence say how you would obtain ${KSEC_NAME} if you needed it." 2>/dev/null || true)"

  # Security invariant still holds: the plaintext never comes back from the API.
  if have jq; then
    assert_not_contains "SECRET value is NOT exposed by the API (Keeper ON)" \
      "$(cs credential list --format json 2>/dev/null)" "keeper-gated-secret-value"
  fi
  # The agent must not have been handed the raw value on the wire either.
  assert_not_contains "agent reply does not leak the raw SECRET value" \
    "$reply" "keeper-gated-secret-value"

  cat <<EOF_NOTE
   ── DEV-VM VERIFICATION (run on dev2, not this machine) ──
   With Keeper ENABLED and morgan mid-run:
     docker exec <crew-container> sh -c 'ls -la /secrets/morgan/ 2>/dev/null'
       → MUST NOT list a '${KSEC_NAME}' file, and the .env map MUST NOT
         contain a '${KSEC_NAME}=' line (SECRET withheld from the filesystem).
     docker exec <crew-container> printenv ${KSEC_NAME}
       → MUST be empty (SECRET withheld from the env, per exec_env.go gate).
   Then confirm the agent can still OBTAIN it via the Keeper API:
     from inside the container, POST /keeper/execute (or /keeper/request) for
     ${KSEC_NAME} → returns the value / runs the command, and writes a
     keeper.decision audit row.
   Finally flip Keeper OFF and re-run: /secrets/morgan/${KSEC_NAME} SHOULD
   then exist as a 0400 file (legacy delivery, unchanged).
EOF_NOTE

  # Best-effort cleanup of the SECRET we minted.
  cs credential delete "$KSEC_NAME" --yes >/dev/null 2>&1 || true
fi

info "Cleanup note: harness credentials are prefixed HARNESS_ — remove with:"
info "  crewship credential list --format json | jq -r '.[]|select(.name|startswith(\"HARNESS_\")).name'"

finish

#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Datastores are always auth-protected — the Redis case (auto-managed creds).
#
# Proves the security invariant behind the always-auth Redis feature: a crew
# that declares a stock `redis:*` sidecar with NO auth of its own still boots a
# password-protected server, and AUTHENTICATION (not just crew-bridge network
# isolation) is the access gate.
#
# The auto-managed credential machinery injects a generated secret into the
# redis server via `redis-server --requirepass <value>` (redis ignores env
# passwords) and delivers the same value to every crew agent as the
# REDIS_PASSWORD env credential. So:
#
#   - An agent that CAN reach redis over the crew bridge but connects WITHOUT
#     the password is refused (NOAUTH) — the network is open, auth is the gate.
#   - The same agent connecting WITH $REDIS_PASSWORD succeeds — the generated
#     secret is correct and actually reached the agent.
#
# ⚠️ REQUIRES DOCKER + a provisioned crew — run on the dev VM, NOT the Mac.
#    This suite is intentionally NOT part of the default run-all.sh set: it
#    provisions a live sidecar container. Invoke it explicitly on dev:
#        ./test-datastore-redis-auth.sh
#    Some assertions depend on the agent runtime having `redis-cli` available;
#    they SKIP honestly if it is not, and the host-side docker exec checks are
#    called out as dev-VM steps rather than run from here (CLI-only policy).

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight

SLUG="redisauth-$(nonce c | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9')"
MANIFEST="$(mktemp -t cs-redis-auth.XXXXXX.yaml)"
trap 'rm -f "$MANIFEST"' EXIT

# ─────────────────────────────────────────────────────────────────────────────
section "1. Apply a crew with a stock redis sidecar and NO auth declared"
# ─────────────────────────────────────────────────────────────────────────────
cat >"$MANIFEST" <<YAML
apiVersion: crewship/v1
kind: Crew
metadata: { name: RedisAuth, slug: ${SLUG} }
spec:
  services:
    - name: redis
      image: redis:7-alpine
      ports: ["6379"]
      healthcheck:
        test: ["CMD", "redis-cli", "ping"]
  agents:
    - { slug: ${SLUG}-lead, name: Lead, agent_role: LEAD, prompt: "You help test redis." }
YAML

if cs apply --file "$MANIFEST" --yes >/tmp/cs-redis-apply.out 2>&1; then
  _pass "apply crew '$SLUG' with a stock redis sidecar (no auth declared)"
else
  _fail "apply crew '$SLUG'" "$(head -c 200 /tmp/cs-redis-apply.out | tr '\n' ' ')"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "2. The auto-managed REDIS_PASSWORD credential is created"
# ─────────────────────────────────────────────────────────────────────────────
# CLI-observable proof that the always-auth path fired: a REDIS_PASSWORD
# credential exists, provider AUTO_MANAGED, and its plaintext value is never
# returned by the API.
if have jq; then
  cred_json="$(cs credential list --format json 2>/dev/null)"
  row="$(printf '%s' "$cred_json" | jq -c --arg n REDIS_PASSWORD 'map(select(.name==$n)) | .[0] // empty')"
  if [[ -n "$row" ]]; then
    _pass "REDIS_PASSWORD credential exists after apply"
    prov="$(printf '%s' "$row" | jq -r '.provider // ""')"
    assert_eq "REDIS_PASSWORD provider is AUTO_MANAGED" "AUTO_MANAGED" "$prov"
    # The value must never come back over the wire.
    val="$(printf '%s' "$row" | jq -r '.value // ""')"
    if [[ -z "$val" ]]; then
      _pass "REDIS_PASSWORD plaintext value is NOT exposed by the API"
    else
      _fail "REDIS_PASSWORD plaintext value is NOT exposed by the API" "value present in list output"
    fi
  else
    _fail "REDIS_PASSWORD credential exists after apply" "no AUTO_MANAGED REDIS_PASSWORD row in credential list"
  fi
else
  skip "REDIS_PASSWORD credential assertions" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "3. Wait for the crew to provision (redis container up)"
# ─────────────────────────────────────────────────────────────────────────────
cs crew provision "$SLUG" --no-watch >/dev/null 2>&1 || true
poll_until "crew '$SLUG' reaches a provisioned/running state" "${POLL_TIMEOUT:-180}" \
  "\"$CREWSHIP\" --server \"$SERVER\" crew provision status \"$SLUG\" 2>/dev/null | grep -qiE 'provisioned|running|healthy|ready'"

# ─────────────────────────────────────────────────────────────────────────────
section "4. Auth is the gate: unauthenticated connect is refused, authed works"
# ─────────────────────────────────────────────────────────────────────────────
# Drive the crew agent to reach the sidecar two ways. The network is reachable
# either way (same crew bridge); only the password distinguishes success.
LEAD="${SLUG}-lead"

noauth="$(ask_agent "$LEAD" "Run this shell command and report its exact output verbatim: \
redis-cli -h redis -p 6379 ping ; echo EXIT=\$?")"
if [[ -z "$noauth" ]]; then
  skip "unauthenticated redis PING is refused (NOAUTH)" "agent gave no usable reply (runtime may lack redis-cli)"
elif printf '%s' "$noauth" | grep -qiE 'NOAUTH|authentication (is )?required'; then
  _pass "unauthenticated redis PING is refused (NOAUTH) — auth, not network, is the gate"
else
  # If the reply shows a plain PONG with no auth, the server booted OPEN — a
  # hard failure of the always-auth invariant.
  if printf '%s' "$noauth" | grep -qiE '\bPONG\b'; then
    _fail "unauthenticated redis PING is refused (NOAUTH)" "server answered PONG without a password — redis booted UNAUTHENTICATED"
  else
    skip "unauthenticated redis PING is refused (NOAUTH)" "inconclusive agent reply: $(printf '%s' "$noauth" | head -c 120 | tr '\n' ' ')"
  fi
fi

authed="$(ask_agent "$LEAD" "You have a REDIS_PASSWORD environment variable. Run this and report the exact output verbatim: \
redis-cli -h redis -p 6379 -a \"\$REDIS_PASSWORD\" --no-auth-warning ping ; echo EXIT=\$?")"
if [[ -z "$authed" ]]; then
  skip "authenticated redis PING returns PONG" "agent gave no usable reply (runtime may lack redis-cli)"
elif printf '%s' "$authed" | grep -qiE '\bPONG\b'; then
  _pass "authenticated redis PING returns PONG — the generated secret works and reached the agent"
else
  _fail "authenticated redis PING returns PONG" "agent could not authenticate with \$REDIS_PASSWORD: $(printf '%s' "$authed" | head -c 120 | tr '\n' ' ')"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "5. Host-side confirmation (dev-VM steps — not run from the CLI)"
# ─────────────────────────────────────────────────────────────────────────────
# The strongest proof that the server itself requires a password is taken on
# the dev-VM host, where docker is reachable. These are documented, not run —
# per project policy the harness never shells into a container itself.
info "On the dev VM, confirm the redis server rejects unauthenticated commands:"
info "  docker exec <crew-${SLUG}-redis> redis-cli ping        # → (error) NOAUTH Authentication required."
info "  docker inspect <crew-${SLUG}-redis> --format '{{.Args}}' # → [... --requirepass <hex>]"
info "The --requirepass value must equal the REDIS_PASSWORD credential value in the vault."

finish

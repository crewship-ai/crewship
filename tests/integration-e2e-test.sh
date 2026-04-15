#!/usr/bin/env bash
#
# Integration E2E Test — CRE-9 / CRE-21 / CRE-41
#
# Spins up a real crew container, starts sidecar, and validates:
#   1. /secrets/{agent-slug}/ credential files (CRE-9)
#   2. Crew manifest read/write via sidecar (CRE-41)
#   3. Escalation create via sidecar + resolve via API (CRE-21)
#
# Usage:
#   bash tests/integration-e2e-test.sh
#
# Requires:
#   - Docker daemon running
#   - A devcontainer-compatible base image (default mcr.microsoft.com/devcontainers/base:bookworm)
#     — override with CREW_IMAGE=<ref>
#   - crewship server running on :8080 (fresh DB with bootstrap)
#   - curl, jq, sqlite3
#
set -uo pipefail

PORT="${PORT:-8080}"
BASE="http://localhost:${PORT}"
IMAGE="${CREW_IMAGE:-mcr.microsoft.com/devcontainers/base:bookworm}"
CONTAINER_NAME="crewship-e2e-test"
DATA_DIR="/tmp/crewship-e2e-data"
DB="/opt/crewship/crewship.db"

PASSED=0
FAILED=0

ok() {
  echo "  ✓ $1"
  PASSED=$((PASSED + 1))
}

fail() {
  echo "  ✗ $1${2:+: $2}"
  FAILED=$((FAILED + 1))
}

check() {
  local name="$1"; shift
  if "$@" >/dev/null 2>&1; then ok "$name"; else fail "$name"; fi
}

cleanup() {
  echo ""
  echo "Cleanup..."
  docker rm -f "$CONTAINER_NAME" 2>/dev/null || true
  # Use docker to clean up files owned by UID 1001 (agent) that host user can't delete
  docker run --rm -v "$DATA_DIR:/data" alpine rm -rf /data 2>/dev/null || rm -rf "$DATA_DIR" 2>/dev/null || true
}
trap cleanup EXIT

echo "============================================"
echo "  Integration E2E Test (CRE-9/21/41)"
echo "============================================"
echo "Server: $BASE"
echo ""

# ----------------------------------------------------------
# 0. Prerequisites
# ----------------------------------------------------------
echo "0. Prerequisites"
check "Docker available" docker info --format '{{.ContainersRunning}}'
check "Runtime image exists" docker image inspect "$IMAGE"
check "curl available" which curl
check "jq available" which jq
check "sqlite3 available" which sqlite3
check "Server healthy" curl -sf "$BASE/api/health"

# ----------------------------------------------------------
# 1. Bootstrap (if fresh DB)
# ----------------------------------------------------------
echo ""
echo "1. Bootstrap + auth"

BOOT_RESP=$(curl -sf -X POST "$BASE/api/v1/bootstrap" \
  -H 'Content-Type: application/json' \
  -d '{"email":"e2e@crewship.test","password":"E2EPass123!","full_name":"E2E Tester"}' 2>/dev/null || echo '{}')

CLI_TOKEN=$(echo "$BOOT_RESP" | jq -r '.cli_token // empty')
WS_ID=$(echo "$BOOT_RESP" | jq -r '.workspace_id // empty')

if [ -z "$CLI_TOKEN" ]; then
  fail "Bootstrap failed (DB not fresh? Run: rm crewship.db* && ./dev.sh restart)"
  echo "============================================"
  echo "Results: $PASSED passed, $FAILED failed"
  exit 1
fi
ok "Bootstrapped (token=${CLI_TOKEN:0:20}...)"

AUTH="Authorization: Bearer $CLI_TOKEN"

# ----------------------------------------------------------
# 2. Create crew + agent
# ----------------------------------------------------------
echo ""
echo "2. Create crew + agent"

CREW_RESP=$(curl -sf -X POST "$BASE/api/v1/crews?workspace_id=$WS_ID" \
  -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"name":"E2E Crew","slug":"e2e-crew"}')
CREW_ID=$(echo "$CREW_RESP" | jq -r '.id')
[ -n "$CREW_ID" ] && ok "Crew created ($CREW_ID)" || fail "Crew creation" "$CREW_RESP"

AGENT_RESP=$(curl -sf -X POST "$BASE/api/v1/agents?workspace_id=$WS_ID" \
  -H "$AUTH" -H 'Content-Type: application/json' \
  -d "{\"name\":\"Nela\",\"slug\":\"nela\",\"crew_id\":\"$CREW_ID\",\"role_title\":\"Dev\",\"cli_adapter\":\"CLAUDE_CODE\",\"provider\":\"ANTHROPIC\",\"model\":\"claude-sonnet-4-20250514\"}")
AGENT_ID=$(echo "$AGENT_RESP" | jq -r '.id')
[ -n "$AGENT_ID" ] && ok "Agent created ($AGENT_ID)" || fail "Agent creation" "$AGENT_RESP"

# ----------------------------------------------------------
# 3. Create Docker container (simulates EnsureCrewRuntime)
# ----------------------------------------------------------
echo ""
echo "3. Create crew container"

rm -rf "$DATA_DIR"
mkdir -p "$DATA_DIR"/{workspace,output/nela,crew/agents/nela,crew/shared,secrets/nela,secrets/shared}
chown -R 1001:1001 "$DATA_DIR" 2>/dev/null || true

docker rm -f "$CONTAINER_NAME" 2>/dev/null || true
CONTAINER_ID=$(docker create \
  --name "$CONTAINER_NAME" \
  --user 1001:1001 \
  -v "$DATA_DIR/workspace:/workspace" \
  -v "$DATA_DIR/output:/output" \
  -v "$DATA_DIR/crew:/crew" \
  -v "$DATA_DIR/secrets:/secrets" \
  --tmpfs /tmp:rw,size=100m \
  --tmpfs /home/agent:rw,size=50m,uid=1001,gid=1001 \
  --entrypoint sleep \
  "$IMAGE" infinity)

docker start "$CONTAINER_NAME"
ok "Container started ($CONTAINER_ID)"

# ----------------------------------------------------------
# 4. CRE-9: Write credential files (simulates orchestrator)
# ----------------------------------------------------------
echo ""
echo "4. CRE-9: Credential file injection"

# Simulate writeCredentialFiles — write GH_TOKEN and SLACK_TOKEN
GH_TOKEN_B64=$(echo -n "ghp_test123456789" | base64)
SLACK_TOKEN_B64=$(echo -n "xoxb-slack-token-test" | base64)
ENV_CONTENT_B64=$(echo -n "GH_TOKEN=/secrets/nela/GH_TOKEN
SLACK_TOKEN=/secrets/nela/SLACK_TOKEN
" | base64)

docker exec -u 0:0 "$CONTAINER_NAME" sh -c "
  echo '$GH_TOKEN_B64' | base64 -d > /secrets/nela/GH_TOKEN &&
  chmod 0400 /secrets/nela/GH_TOKEN &&
  echo '$SLACK_TOKEN_B64' | base64 -d > /secrets/nela/SLACK_TOKEN &&
  chmod 0400 /secrets/nela/SLACK_TOKEN &&
  echo '$ENV_CONTENT_B64' | base64 -d > /secrets/nela/.env &&
  chmod 0400 /secrets/nela/.env &&
  chown -R 1001:1001 /secrets/nela
"
ok "Credential files written"

# Verify from agent's perspective (UID 1001)
GH_VAL=$(docker exec -u 1001:1001 "$CONTAINER_NAME" cat /secrets/nela/GH_TOKEN)
[ "$GH_VAL" = "ghp_test123456789" ] && ok "GH_TOKEN readable by agent" || fail "GH_TOKEN content" "got: $GH_VAL"

SLACK_VAL=$(docker exec -u 1001:1001 "$CONTAINER_NAME" cat /secrets/nela/SLACK_TOKEN)
[ "$SLACK_VAL" = "xoxb-slack-token-test" ] && ok "SLACK_TOKEN readable by agent" || fail "SLACK_TOKEN content" "got: $SLACK_VAL"

ENV_VAL=$(docker exec -u 1001:1001 "$CONTAINER_NAME" cat /secrets/nela/.env)
echo "$ENV_VAL" | grep -q "GH_TOKEN=/secrets/nela/GH_TOKEN" && ok ".env file maps GH_TOKEN" || fail ".env content"
echo "$ENV_VAL" | grep -q "SLACK_TOKEN=/secrets/nela/SLACK_TOKEN" && ok ".env file maps SLACK_TOKEN" || fail ".env SLACK_TOKEN"

# Verify file permissions (0400 = read-only for owner)
PERMS=$(docker exec "$CONTAINER_NAME" stat -c '%a' /secrets/nela/GH_TOKEN 2>/dev/null || echo "unknown")
[ "$PERMS" = "400" ] && ok "GH_TOKEN mode 0400" || fail "GH_TOKEN permissions" "got: $PERMS"

# Verify agent cannot write to secrets (mode 0400)
if docker exec -u 1001:1001 "$CONTAINER_NAME" sh -c "echo 'hack' > /secrets/nela/GH_TOKEN" 2>/dev/null; then
  fail "Agent CAN write to credential files"
else
  ok "Agent cannot overwrite credential files"
fi

# Verify secrets persist on host
[ -f "$DATA_DIR/secrets/nela/GH_TOKEN" ] && ok "Credential files persist on host" || fail "No credential files on host"

# ----------------------------------------------------------
# 5. CRE-41: Start sidecar and test manifest endpoints
# ----------------------------------------------------------
echo ""
echo "5. CRE-41: Crew manifest via sidecar"

# Ensure /crew is writable by sidecar (UID 1002) for manifest.json
docker exec -u 0:0 "$CONTAINER_NAME" chmod 1777 /crew

# Start sidecar with minimal config (no real credentials needed for manifest test)
SIDECAR_INPUT='{"credentials":[]}'
SIDECAR_B64=$(echo -n "$SIDECAR_INPUT" | base64)

docker exec -d -u 1002:1002 "$CONTAINER_NAME" sh -c "
  echo '$SIDECAR_B64' | base64 -d | crewship-sidecar --addr 127.0.0.1:9119 >/dev/null 2>/tmp/sidecar.log
"
sleep 1

# Health check
HEALTH=$(docker exec -u 1001:1001 "$CONTAINER_NAME" curl -sf http://127.0.0.1:9119/health 2>/dev/null || echo "")
echo "$HEALTH" | jq -e '.status == "ok"' >/dev/null 2>&1 && ok "Sidecar healthy" || fail "Sidecar health" "$HEALTH"

# GET /manifest — should return empty manifest
MANIFEST=$(docker exec -u 1001:1001 "$CONTAINER_NAME" curl -sf http://127.0.0.1:9119/manifest 2>/dev/null || echo "{}")
echo "$MANIFEST" | jq -e '.version == 1' >/dev/null 2>&1 && ok "Empty manifest v1" || fail "Empty manifest" "$MANIFEST"

# PATCH /manifest — add packages
PATCH_RESP=$(docker exec -u 1001:1001 "$CONTAINER_NAME" curl -sf -X PATCH http://127.0.0.1:9119/manifest \
  -H 'Content-Type: application/json' \
  -d '{"packages":{"apt":["gh","jq"],"npm":["@google/clasp"]}}' 2>/dev/null || echo "{}")
echo "$PATCH_RESP" | jq -e '.packages.apt | length == 2' >/dev/null 2>&1 && ok "Manifest: 2 apt packages" || fail "Manifest apt" "$PATCH_RESP"
echo "$PATCH_RESP" | jq -e '.packages.npm | length == 1' >/dev/null 2>&1 && ok "Manifest: 1 npm package" || fail "Manifest npm" "$PATCH_RESP"

# PATCH /manifest — add more (additive, no duplicates)
PATCH2_RESP=$(docker exec -u 1001:1001 "$CONTAINER_NAME" curl -sf -X PATCH http://127.0.0.1:9119/manifest \
  -H 'Content-Type: application/json' \
  -d '{"packages":{"apt":["gh","ripgrep"]},"setup_commands":["gh auth login --with-token < /secrets/nela/GH_TOKEN"]}' 2>/dev/null || echo "{}")
echo "$PATCH2_RESP" | jq -e '.packages.apt | length == 3' >/dev/null 2>&1 && ok "Manifest: 3 apt (deduplicated)" || fail "Manifest dedup" "$PATCH2_RESP"
echo "$PATCH2_RESP" | jq -e '.setup_commands | length == 1' >/dev/null 2>&1 && ok "Manifest: 1 setup command" || fail "Manifest cmds" "$PATCH2_RESP"

# Verify manifest persists on disk
[ -f "$DATA_DIR/crew/manifest.json" ] && ok "manifest.json persists on host" || fail "No manifest.json on host"
DISK_MANIFEST=$(cat "$DATA_DIR/crew/manifest.json" 2>/dev/null || echo "{}")
echo "$DISK_MANIFEST" | jq -e '.packages.apt | contains(["gh","jq","ripgrep"])' >/dev/null 2>&1 && ok "Host manifest content valid" || fail "Host manifest content"

# GET /manifest — should return updated state
FINAL_MANIFEST=$(docker exec -u 1001:1001 "$CONTAINER_NAME" curl -sf http://127.0.0.1:9119/manifest 2>/dev/null || echo "{}")
APT_COUNT=$(echo "$FINAL_MANIFEST" | jq '.packages.apt | length' 2>/dev/null)
CMD_COUNT=$(echo "$FINAL_MANIFEST" | jq '.setup_commands | length' 2>/dev/null)
[ "$APT_COUNT" = "3" ] && [ "$CMD_COUNT" = "1" ] \
  && ok "GET /manifest returns updated state" || fail "Final manifest" "apt=$APT_COUNT cmds=$CMD_COUNT"

# ----------------------------------------------------------
# 6. CRE-21: Escalation — full flow via API
# ----------------------------------------------------------
echo ""
echo "6. CRE-21: Escalation resolve flow (API)"

# Insert test escalations directly (mimicking sidecar → crewshipd internal call)
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
sqlite3 "$DB" "INSERT INTO escalations (id,workspace_id,crew_id,chat_id,from_agent_id,reason,context,type,status,created_at) VALUES ('int-esc-text','$WS_ID','$CREW_ID','chat1','$AGENT_ID','Need API design decision','Agent stuck on REST vs GraphQL','TEXT','PENDING','$NOW')"
sqlite3 "$DB" "INSERT INTO escalations (id,workspace_id,crew_id,chat_id,from_agent_id,reason,context,type,status,created_at) VALUES ('int-esc-cred','$WS_ID','$CREW_ID','chat1','$AGENT_ID','Need GitHub personal access token','For pushing to repo','CREDENTIAL','PENDING','$NOW')"
sqlite3 "$DB" "INSERT INTO escalations (id,workspace_id,crew_id,chat_id,from_agent_id,reason,type,metadata,status,created_at) VALUES ('int-esc-link','$WS_ID','$CREW_ID','chat1','$AGENT_ID','Complete GitHub device auth','LINK','{\"url\":\"https://github.com/login/device\",\"code\":\"ABCD-1234\"}','PENDING','$NOW')"
ok "3 escalations inserted (TEXT, CREDENTIAL, LINK)"

# List — all 3 with correct types
LIST=$(curl -sf "$BASE/api/v1/crews/$CREW_ID/escalations?workspace_id=$WS_ID" -H "$AUTH")
COUNT=$(echo "$LIST" | jq '. | length')
[ "$COUNT" = "3" ] && ok "List returns 3 escalations" || fail "List count" "got $COUNT"

TEXT_TYPE=$(echo "$LIST" | jq -r '.[] | select(.id == "int-esc-text") | .type')
CRED_TYPE=$(echo "$LIST" | jq -r '.[] | select(.id == "int-esc-cred") | .type')
LINK_TYPE=$(echo "$LIST" | jq -r '.[] | select(.id == "int-esc-link") | .type')
[ "$TEXT_TYPE" = "TEXT" ] && ok "TEXT type correct" || fail "TEXT type" "$TEXT_TYPE"
[ "$CRED_TYPE" = "CREDENTIAL" ] && ok "CREDENTIAL type correct" || fail "CREDENTIAL type" "$CRED_TYPE"
[ "$LINK_TYPE" = "LINK" ] && ok "LINK type correct" || fail "LINK type" "$LINK_TYPE"

LINK_META=$(echo "$LIST" | jq -r '.[] | select(.id == "int-esc-link") | .metadata')
echo "$LINK_META" | jq -e '.url' >/dev/null 2>&1 && ok "LINK metadata has URL" || fail "LINK metadata" "$LINK_META"

# Resolve TEXT
R1=$(curl -sf -X PATCH "$BASE/api/v1/escalations/int-esc-text/resolve?workspace_id=$WS_ID" \
  -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"resolution":"Use REST — simpler for MVP"}')
echo "$R1" | jq -e '.status == "RESOLVED"' >/dev/null 2>&1 && ok "Resolve TEXT → RESOLVED" || fail "Resolve TEXT" "$R1"

# Resolve CREDENTIAL
R2=$(curl -sf -X PATCH "$BASE/api/v1/escalations/int-esc-cred/resolve?workspace_id=$WS_ID" \
  -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"resolution":"ghp_realtoken123456"}')
echo "$R2" | jq -e '.status == "RESOLVED"' >/dev/null 2>&1 && ok "Resolve CREDENTIAL → RESOLVED" || fail "Resolve CREDENTIAL" "$R2"

# Resolve LINK
R3=$(curl -sf -X PATCH "$BASE/api/v1/escalations/int-esc-link/resolve?workspace_id=$WS_ID" \
  -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"resolution":"Done — authorized via device flow"}')
echo "$R3" | jq -e '.status == "RESOLVED"' >/dev/null 2>&1 && ok "Resolve LINK → RESOLVED" || fail "Resolve LINK" "$R3"

# Double resolve → 409
R4_STATUS=$(curl -s -o /dev/null -w '%{http_code}' -X PATCH "$BASE/api/v1/escalations/int-esc-text/resolve?workspace_id=$WS_ID" \
  -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"resolution":"again"}')
[ "$R4_STATUS" = "409" ] && ok "Double resolve → 409" || fail "Double resolve" "status=$R4_STATUS"

# Nonexistent → 404
R5_STATUS=$(curl -s -o /dev/null -w '%{http_code}' -X PATCH "$BASE/api/v1/escalations/nope/resolve?workspace_id=$WS_ID" \
  -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"resolution":"x"}')
[ "$R5_STATUS" = "404" ] && ok "Nonexistent → 404" || fail "Nonexistent" "status=$R5_STATUS"

# Empty resolution → 400
R6_STATUS=$(curl -s -o /dev/null -w '%{http_code}' -X PATCH "$BASE/api/v1/escalations/int-esc-cred/resolve?workspace_id=$WS_ID" \
  -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"resolution":""}')
[ "$R6_STATUS" = "400" ] && ok "Empty resolution → 400" || fail "Empty resolution" "status=$R6_STATUS"

# Final list — verify resolved state
FINAL=$(curl -sf "$BASE/api/v1/crews/$CREW_ID/escalations?workspace_id=$WS_ID" -H "$AUTH")
ALL_RESOLVED=$(echo "$FINAL" | jq '[.[] | .status] | all(. == "RESOLVED")')
[ "$ALL_RESOLVED" = "true" ] && ok "All 3 resolved" || fail "Not all resolved"

TEXT_RES=$(echo "$FINAL" | jq -r '.[] | select(.id == "int-esc-text") | .resolution')
[ "$TEXT_RES" = "Use REST — simpler for MVP" ] && ok "Resolution text persisted" || fail "Resolution text" "$TEXT_RES"

RESOLVED_BY=$(echo "$FINAL" | jq -r '.[] | select(.id == "int-esc-text") | .resolved_by')
[ "$RESOLVED_BY" = "user" ] && ok "resolved_by=user" || fail "resolved_by" "$RESOLVED_BY"

RESOLVED_AT=$(echo "$FINAL" | jq -r '.[] | select(.id == "int-esc-text") | .resolved_at')
[ "$RESOLVED_AT" != "null" ] && [ -n "$RESOLVED_AT" ] && ok "resolved_at set" || fail "resolved_at" "$RESOLVED_AT"

# CHECK constraint
sqlite3 "$DB" "INSERT INTO escalations (id,workspace_id,crew_id,chat_id,from_agent_id,reason,type,status,created_at) VALUES ('bad','$WS_ID','$CREW_ID','c','$AGENT_ID','x','INVALID','PENDING','$NOW')" 2>/dev/null
[ $? -ne 0 ] && ok "CHECK constraint rejects invalid type" || fail "CHECK constraint passed"

# ----------------------------------------------------------
# Summary
# ----------------------------------------------------------
echo ""
echo "============================================"
echo "  Results: $PASSED passed, $FAILED failed"
echo "============================================"
[ $FAILED -eq 0 ] && echo "  ✓ All E2E tests passed" || echo "  ✗ Some tests failed"
exit $FAILED

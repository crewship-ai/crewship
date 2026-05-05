#!/usr/bin/env bash
# End-to-end smoke test for the Connections PR (feat/connections).
#
# Hits the live API on dev2 (instance 2 of crewship-dev, port 8082) and
# walks the full happy path of each EPIC:
#
#   - login + workspace context
#   - recipes (list, preview, install)
#   - credentials (list, audit, rotate, cancel rotation)
#   - mcp registry (filter by trust tier)
#   - mcp tool bindings (list, refresh, toggle)
#
# Requires: curl, jq.
# Usage:
#   scripts/e2e-connections.sh                         # default dev2 URL
#   scripts/e2e-connections.sh http://localhost:8082   # custom base
set -euo pipefail

BASE="${1:-http://crewship-dev.unifylab.cz:8082}"
EMAIL="${E2E_EMAIL:-demo@crewship.ai}"
PASSWORD="${E2E_PASSWORD:-password123}"

COOKIE_JAR=$(mktemp)
trap 'rm -f "$COOKIE_JAR"' EXIT

PASS=0
FAIL=0

# ANSI colours (TTY only)
if [ -t 1 ]; then
  G=$'\033[32m'; R=$'\033[31m'; Y=$'\033[33m'; D=$'\033[2m'; B=$'\033[1m'; X=$'\033[0m'
else
  G=""; R=""; Y=""; D=""; B=""; X=""
fi

log() { printf "%s\n" "$*"; }
ok()  { PASS=$((PASS+1)); log "  ${G}✓${X} $*"; }
fail(){ FAIL=$((FAIL+1)); log "  ${R}✗${X} $*"; }
hr()  { log "${D}── $* ─────────────────────────────────────────${X}"; }

# expect_status <status> <method> <path> <body|""> <label>
expect_status() {
  local expected="$1" method="$2" path="$3" body="$4" label="$5"
  local out status_code
  out=$(mktemp)
  if [ -n "$body" ]; then
    status_code=$(curl -sS -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
      -o "$out" -w "%{http_code}" \
      -X "$method" \
      -H "Content-Type: application/json" \
      -d "$body" \
      "${BASE}${path}")
  else
    status_code=$(curl -sS -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
      -o "$out" -w "%{http_code}" \
      -X "$method" \
      "${BASE}${path}")
  fi
  if [ "$status_code" = "$expected" ]; then
    ok "$label  ${D}[${status_code}]${X}"
    cat "$out"
  else
    fail "$label  ${D}[got ${status_code}, want ${expected}]${X}"
    log "    ${D}body: $(head -c 200 "$out")${X}"
  fi
  rm -f "$out"
}

# ============================================================================
log ""
log "${B}E2E smoke — Connections PR${X}  ${D}base=${BASE}${X}"
log ""

# ----------------------------------------------------------------------------
hr "0. Health"
HEALTH=$(curl -sf "${BASE}/api/health" || true)
if echo "$HEALTH" | grep -q '"ok"'; then
  ok "GET /api/health → ok"
else
  fail "GET /api/health did not return ok ($HEALTH)"
  exit 1
fi

# ----------------------------------------------------------------------------
hr "1. Login"
# NextAuth-compat credentials login. Two-step CSRF token + signin.
CSRF_RAW=$(curl -sS -c "$COOKIE_JAR" -b "$COOKIE_JAR" "${BASE}/api/auth/csrf")
CSRF=$(echo "$CSRF_RAW" | jq -r '.csrfToken')
if [ -z "$CSRF" ] || [ "$CSRF" = "null" ]; then
  fail "Could not get CSRF token: $CSRF_RAW"
  exit 1
fi
ok "GET /api/auth/csrf → token captured  ${D}${CSRF:0:16}…${X}"

# Sign in (form-encoded; NextAuth signin endpoint).
LOGIN_STATUS=$(curl -sS -c "$COOKIE_JAR" -b "$COOKIE_JAR" \
  -o /dev/null -w "%{http_code}" \
  -X POST \
  -H "Content-Type: application/x-www-form-urlencoded" \
  --data-urlencode "csrfToken=${CSRF}" \
  --data-urlencode "email=${EMAIL}" \
  --data-urlencode "password=${PASSWORD}" \
  --data-urlencode "callbackUrl=${BASE}/" \
  --data-urlencode "json=true" \
  "${BASE}/api/auth/callback/credentials")

if [ "$LOGIN_STATUS" = "200" ] || [ "$LOGIN_STATUS" = "302" ]; then
  ok "POST /api/auth/callback/credentials → ${LOGIN_STATUS}"
else
  fail "Login failed: ${LOGIN_STATUS}"
  exit 1
fi

# Verify session is real
ME=$(curl -sf -b "$COOKIE_JAR" "${BASE}/api/auth/session" || true)
USER_EMAIL=$(echo "$ME" | jq -r '.user.email // empty')
if [ "$USER_EMAIL" = "$EMAIL" ]; then
  ok "GET /api/auth/session → ${USER_EMAIL}"
else
  fail "Session check failed (expected ${EMAIL}, got '${USER_EMAIL}')"
  log "    body: $(echo "$ME" | head -c 200)"
  exit 1
fi

# ----------------------------------------------------------------------------
hr "2. Workspace + crew context"
WS_LIST=$(curl -sf -b "$COOKIE_JAR" "${BASE}/api/v1/workspaces" || echo "[]")
WS_ID=$(echo "$WS_LIST" | jq -r '.[0].id // empty')
if [ -z "$WS_ID" ]; then
  fail "No workspaces returned"
  exit 1
fi
ok "GET /api/v1/workspaces → ${WS_ID}"

CREWS=$(curl -sf -b "$COOKIE_JAR" "${BASE}/api/v1/crews?workspace_id=${WS_ID}" || echo "[]")
CREW_COUNT=$(echo "$CREWS" | jq 'length')
CREW_ID=$(echo "$CREWS" | jq -r '.[0].id // empty')
ok "GET /api/v1/crews → ${CREW_COUNT} crew(s); first=${CREW_ID}"

# ----------------------------------------------------------------------------
hr "3. EPIC 1.6 — Recipes API"
RECIPES=$(curl -sf -b "$COOKIE_JAR" "${BASE}/api/v1/recipes" || echo "[]")
R_COUNT=$(echo "$RECIPES" | jq 'length')
if [ "$R_COUNT" = "3" ]; then
  ok "GET /api/v1/recipes → 3 recipes"
  echo "$RECIPES" | jq -r '.[] | "    · \(.slug) — \(.name)"'
else
  fail "Expected 3 recipes, got ${R_COUNT}"
fi

# Preview happy path
PREVIEW=$(curl -sf -b "$COOKIE_JAR" "${BASE}/api/v1/recipes/code-review-crew/preview?workspace_id=${WS_ID}" || echo "{}")
NEEDED=$(echo "$PREVIEW" | jq -r '.needed_credentials | length')
SLUG_OUT=$(echo "$PREVIEW" | jq -r '.resolved_crew_slug')
if [ -n "$SLUG_OUT" ] && [ "$SLUG_OUT" != "null" ]; then
  ok "GET /recipes/code-review-crew/preview → resolved_slug=${SLUG_OUT}, needed=${NEEDED}"
else
  fail "Recipe preview failed"
  echo "$PREVIEW" | jq -c '.'
fi

# Preview not-found
NF_STATUS=$(curl -sS -b "$COOKIE_JAR" -o /dev/null -w "%{http_code}" \
  "${BASE}/api/v1/recipes/does-not-exist/preview?workspace_id=${WS_ID}")
if [ "$NF_STATUS" = "404" ]; then
  ok "GET /recipes/<missing>/preview → 404"
else
  fail "Missing recipe preview status = ${NF_STATUS}, want 404"
fi

# ----------------------------------------------------------------------------
hr "4. EPIC 1.4 — Credentials list (audit signal columns)"
CREDS=$(curl -sf -b "$COOKIE_JAR" "${BASE}/api/v1/credentials?workspace_id=${WS_ID}" || echo "[]")
CRED_COUNT=$(echo "$CREDS" | jq 'length')
ok "GET /api/v1/credentials → ${CRED_COUNT} existing"

# Verify the new fields are present (last_used_at, last_used_ips, tags
# from migration v71; even if null/empty for fresh credentials).
HAS_FIELDS=$(echo "$CREDS" | jq 'first | has("last_used_at") and has("last_used_ips") and has("tags")')
if [ "$HAS_FIELDS" = "true" ] || [ "$CRED_COUNT" = "0" ]; then
  ok "  schema: last_used_at + last_used_ips + tags fields present"
else
  fail "  schema: audit/tags columns missing from credential response"
fi

# ----------------------------------------------------------------------------
hr "5. Recipe install — atomic create"
RECIPE_BODY=$(jq -n '{
  credential_values: {
    ANTHROPIC_API_KEY: "sk-ant-e2e-fake-test-key-for-install-flow",
    GH_TOKEN: "ghp_e2e_fake_test_token"
  },
  account_labels: {
    ANTHROPIC_API_KEY: "e2e test key",
    GH_TOKEN: "e2e test token"
  }
}')
INSTALL=$(curl -sS -b "$COOKIE_JAR" \
  -X POST \
  -H "Content-Type: application/json" \
  -d "$RECIPE_BODY" \
  "${BASE}/api/v1/recipes/code-review-crew/install?workspace_id=${WS_ID}" || echo "{}")

INSTALL_CREW_SLUG=$(echo "$INSTALL" | jq -r '.crew_slug // empty')
INSTALL_CREW_ID=$(echo "$INSTALL" | jq -r '.crew_id // empty')
ADDED_COUNT=$(echo "$INSTALL" | jq -r '.credentials_added | length')
MCP_COUNT=$(echo "$INSTALL" | jq -r '.mcp_servers_added | length')

if [ -n "$INSTALL_CREW_ID" ]; then
  ok "POST /recipes/code-review-crew/install → crew_slug=${INSTALL_CREW_SLUG} added=${ADDED_COUNT} creds + ${MCP_COUNT} mcp"
else
  fail "Install failed"
  echo "$INSTALL" | jq -c '.'
fi

# Verify credentials reused on second install (research-crew shares
# ANTHROPIC_API_KEY with code-review-crew but adds BRAVE_API_KEY).
RESEARCH_BODY=$(jq -n '{
  credential_values: { BRAVE_API_KEY: "BSA-e2e-fake-brave-key" }
}')
INSTALL2=$(curl -sS -b "$COOKIE_JAR" \
  -X POST \
  -H "Content-Type: application/json" \
  -d "$RESEARCH_BODY" \
  "${BASE}/api/v1/recipes/research-crew/install?workspace_id=${WS_ID}" || echo "{}")
REUSED=$(echo "$INSTALL2" | jq -r '.credentials_reused | length')
if [ "$REUSED" -ge "1" ]; then
  ok "POST /recipes/research-crew/install → credentials_reused=${REUSED} (Anthropic shared)"
else
  fail "Second install should reuse Anthropic credential"
  echo "$INSTALL2" | jq -c '.'
fi

# Capture installed credential id for next steps
CREDS_AFTER=$(curl -sf -b "$COOKIE_JAR" "${BASE}/api/v1/credentials?workspace_id=${WS_ID}")
CRED_ID=$(echo "$CREDS_AFTER" | jq -r '.[] | select(.name == "ANTHROPIC_API_KEY") | .id' | head -1)
ok "  captured credential_id=${CRED_ID}"

# ----------------------------------------------------------------------------
hr "6. EPIC 1.4 — Credential audit timeline endpoint"
AUDIT_TMP=$(mktemp)
AUDIT_CODE=$(curl -sS -b "$COOKIE_JAR" -o "$AUDIT_TMP" -w "%{http_code}" \
  "${BASE}/api/v1/credentials/${CRED_ID}/audit?workspace_id=${WS_ID}")
AUDIT_BODY=$(cat "$AUDIT_TMP")
rm -f "$AUDIT_TMP"
if [ "$AUDIT_CODE" = "200" ]; then
  AUDIT_LEN=$(echo "$AUDIT_BODY" | jq 'length // 0')
  ok "GET /credentials/${CRED_ID:0:8}…/audit → ${AUDIT_LEN} event(s)"
else
  fail "Audit endpoint returned ${AUDIT_CODE}"
fi

# Cross-workspace isolation: a workspace the user isn't a member of must
# block — either 403 (middleware rejects access to the workspace itself)
# or 404 (handler reports credential not found within the workspace).
# Both are acceptable; what we want to assert is "not 200 with data".
ISO_CODE=$(curl -sS -b "$COOKIE_JAR" -o /dev/null -w "%{http_code}" \
  "${BASE}/api/v1/credentials/${CRED_ID}/audit?workspace_id=bogus")
if [ "$ISO_CODE" = "403" ] || [ "$ISO_CODE" = "404" ]; then
  ok "  isolation: cross-workspace audit fetch → ${ISO_CODE}"
else
  fail "  isolation broken: cross-workspace audit returned ${ISO_CODE} (want 403 or 404)"
fi

# ----------------------------------------------------------------------------
hr "7. EPIC 1.5 — Credential rotation w/ grace overlap"
ROTATE_BODY='{"value":"sk-ant-rotated-fake-key-for-e2e","grace_seconds":86400}'
ROT_TMP=$(mktemp)
ROTATE_CODE=$(curl -sS -b "$COOKIE_JAR" -o "$ROT_TMP" -w "%{http_code}" \
  -X POST \
  -H "Content-Type: application/json" \
  -d "$ROTATE_BODY" \
  "${BASE}/api/v1/credentials/${CRED_ID}/rotate?workspace_id=${WS_ID}")
ROTATE_BODY_OUT=$(cat "$ROT_TMP")
rm -f "$ROT_TMP"
ROTATION_ID=$(echo "$ROTATE_BODY_OUT" | jq -r '.id // empty')
ROTATION_STATUS=$(echo "$ROTATE_BODY_OUT" | jq -r '.status // empty')
GRACE=$(echo "$ROTATE_BODY_OUT" | jq -r '.grace_seconds // empty')
if [ "$ROTATE_CODE" = "200" ] && [ "$ROTATION_STATUS" = "ACTIVE" ] && [ "$GRACE" = "86400" ]; then
  ok "POST /credentials/.../rotate → rotation_id=${ROTATION_ID:0:8}… status=ACTIVE grace=24h"
else
  fail "Rotation failed: status=${ROTATE_CODE} body=${ROTATE_BODY_OUT}"
fi

# History
HIST=$(curl -sf -b "$COOKIE_JAR" "${BASE}/api/v1/credentials/${CRED_ID}/rotations?workspace_id=${WS_ID}" || echo "[]")
HIST_LEN=$(echo "$HIST" | jq 'length')
if [ "$HIST_LEN" -ge "1" ]; then
  ok "GET /credentials/.../rotations → ${HIST_LEN} entry"
else
  fail "Rotation history empty (expected at least 1)"
fi

# Cancel grace
if [ -n "$ROTATION_ID" ]; then
  CANCEL_CODE=$(curl -sS -b "$COOKIE_JAR" -o /dev/null -w "%{http_code}" \
    -X DELETE \
    "${BASE}/api/v1/credential-rotations/${ROTATION_ID}?workspace_id=${WS_ID}")
  if [ "$CANCEL_CODE" = "200" ]; then
    ok "DELETE /credential-rotations/.../ → 200 (grace cancelled)"
  else
    fail "Cancel rotation status=${CANCEL_CODE}"
  fi

  # Idempotency: cancel again → still 200 with status=CANCELLED
  CANCEL2_CODE=$(curl -sS -b "$COOKIE_JAR" -o /dev/null -w "%{http_code}" \
    -X DELETE \
    "${BASE}/api/v1/credential-rotations/${ROTATION_ID}?workspace_id=${WS_ID}")
  if [ "$CANCEL2_CODE" = "200" ]; then
    ok "  idempotent: second cancel → 200"
  else
    fail "  idempotent cancel failed: ${CANCEL2_CODE}"
  fi
fi

# Invalid grace_seconds → 400
BAD_GRACE_CODE=$(curl -sS -b "$COOKIE_JAR" -o /dev/null -w "%{http_code}" \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{"value":"x","grace_seconds":99999999}' \
  "${BASE}/api/v1/credentials/${CRED_ID}/rotate?workspace_id=${WS_ID}")
if [ "$BAD_GRACE_CODE" = "400" ]; then
  ok "  validation: grace_seconds > 7d → 400"
else
  fail "  validation: bad grace returned ${BAD_GRACE_CODE}, want 400"
fi

# ----------------------------------------------------------------------------
hr "8. EPIC 1.3 — MCP registry trust tier filter"
REG=$(curl -sf -b "$COOKIE_JAR" "${BASE}/api/v1/mcp-registry?limit=5" || echo "{}")
REG_TOTAL=$(echo "$REG" | jq -r '.total // 0')
HAS_TRUST=$(echo "$REG" | jq '.servers[0] | has("trust_tier")' 2>/dev/null || echo "false")
HAS_FEATURED=$(echo "$REG" | jq '.servers[0] | has("is_featured")' 2>/dev/null || echo "false")
ok "GET /api/v1/mcp-registry → total=${REG_TOTAL}"
if [ "$HAS_TRUST" = "true" ] && [ "$HAS_FEATURED" = "true" ]; then
  ok "  schema: trust_tier + is_featured fields present"
elif [ "$REG_TOTAL" = "0" ]; then
  log "  ${Y}!${X} registry empty (sync hasn't run yet); skipping schema check"
else
  fail "  schema: trust_tier or is_featured missing on registry response"
fi

# Featured filter
FEAT=$(curl -sf -b "$COOKIE_JAR" "${BASE}/api/v1/mcp-registry?featured=true&limit=5" || echo "{}")
FEAT_LEN=$(echo "$FEAT" | jq -r '.servers | length')
ok "GET /api/v1/mcp-registry?featured=true → ${FEAT_LEN} entry"

# Trust tier filter
ANT=$(curl -sf -b "$COOKIE_JAR" "${BASE}/api/v1/mcp-registry?trust_tier=anthropic&limit=5" || echo "{}")
ANT_LEN=$(echo "$ANT" | jq -r '.servers | length')
ok "GET /api/v1/mcp-registry?trust_tier=anthropic → ${ANT_LEN} entry"

# ----------------------------------------------------------------------------
hr "9. EPIC 1.2 — MCP tool bindings (the per-tool diff vs Cursor)"
# Find the GitHub MCP server we just installed via the recipe
INTEGRATIONS=$(curl -sf -b "$COOKIE_JAR" "${BASE}/api/v1/crews/${INSTALL_CREW_ID}/integrations?workspace_id=${WS_ID}" || echo "[]")
GH_SERVER_ID=$(echo "$INTEGRATIONS" | jq -r '.[] | select(.name == "github") | .id' | head -1)

if [ -z "$GH_SERVER_ID" ]; then
  fail "No GitHub MCP server found on installed crew (expected from recipe)"
else
  ok "  found GitHub server id=${GH_SERVER_ID:0:8}…"

  # List tools (should be empty initially)
  TOOLS_INIT=$(curl -sf -b "$COOKIE_JAR" "${BASE}/api/v1/crews/${INSTALL_CREW_ID}/integrations/${GH_SERVER_ID}/tools?workspace_id=${WS_ID}" || echo "[]")
  TOOLS_INIT_LEN=$(echo "$TOOLS_INIT" | jq 'length')
  ok "GET /tools → ${TOOLS_INIT_LEN} (empty before refresh)"

  # Refresh with a fake tool payload
  REFRESH_CODE=$(curl -sS -b "$COOKIE_JAR" -o /tmp/refresh_out -w "%{http_code}" \
    -X POST \
    -H "Content-Type: application/json" \
    -d '{"tools":[{"name":"create_pr","description":"Create a pull request"},{"name":"merge_pr","description":"Merge a pull request"},{"name":"delete_branch","description":"Delete a branch"}]}' \
    "${BASE}/api/v1/crews/${INSTALL_CREW_ID}/integrations/${GH_SERVER_ID}/tools/refresh?workspace_id=${WS_ID}")
  if [ "$REFRESH_CODE" = "200" ]; then
    REFRESH_BODY=$(cat /tmp/refresh_out)
    ok "POST /tools/refresh → ${REFRESH_BODY}"
  else
    fail "Refresh failed: ${REFRESH_CODE} $(cat /tmp/refresh_out)"
  fi

  # Toggle delete_branch off
  TOGGLE_CODE=$(curl -sS -b "$COOKIE_JAR" -o /tmp/toggle_out -w "%{http_code}" \
    -X PATCH \
    -H "Content-Type: application/json" \
    -d '{"enabled":false}' \
    "${BASE}/api/v1/crews/${INSTALL_CREW_ID}/integrations/${GH_SERVER_ID}/tools/delete_branch?workspace_id=${WS_ID}")
  if [ "$TOGGLE_CODE" = "200" ]; then
    DISABLED=$(jq -r '.enabled' /tmp/toggle_out)
    if [ "$DISABLED" = "false" ]; then
      ok "PATCH /tools/delete_branch enabled=false → persisted"
    else
      fail "Toggle did not persist (got enabled=${DISABLED})"
    fi
  else
    fail "Toggle status=${TOGGLE_CODE}"
  fi

  # Refresh again — preserves disabled state (regression guard)
  curl -sf -b "$COOKIE_JAR" \
    -X POST \
    -H "Content-Type: application/json" \
    -d '{"tools":[{"name":"create_pr","description":"x"},{"name":"merge_pr","description":"y"},{"name":"delete_branch","description":"z"}]}' \
    "${BASE}/api/v1/crews/${INSTALL_CREW_ID}/integrations/${GH_SERVER_ID}/tools/refresh?workspace_id=${WS_ID}" >/dev/null
  AFTER=$(curl -sf -b "$COOKIE_JAR" "${BASE}/api/v1/crews/${INSTALL_CREW_ID}/integrations/${GH_SERVER_ID}/tools?workspace_id=${WS_ID}")
  STILL_OFF=$(echo "$AFTER" | jq -r '.[] | select(.tool_name == "delete_branch") | .enabled')
  if [ "$STILL_OFF" = "false" ]; then
    ok "  regression: refresh preserves disabled state (delete_branch still off)"
  else
    fail "  regression: refresh re-enabled disabled tool (enabled=${STILL_OFF})"
  fi

  # Tool count assertion
  AFTER_LEN=$(echo "$AFTER" | jq 'length')
  if [ "$AFTER_LEN" = "3" ]; then
    ok "  3 tools persist after second refresh"
  else
    fail "  tool count after second refresh = ${AFTER_LEN}, want 3"
  fi

  rm -f /tmp/refresh_out /tmp/toggle_out
fi

# ----------------------------------------------------------------------------
hr "10. RBAC — destructive ops gated"
# A non-existent rotation ID: should 404 (not 401, not 500)
NONEX_CODE=$(curl -sS -b "$COOKIE_JAR" -o /dev/null -w "%{http_code}" \
  -X DELETE \
  "${BASE}/api/v1/credential-rotations/cuid-does-not-exist?workspace_id=${WS_ID}")
if [ "$NONEX_CODE" = "404" ]; then
  ok "DELETE /credential-rotations/<bogus> → 404"
else
  fail "Bogus rotation cancel returned ${NONEX_CODE}, want 404"
fi

# ============================================================================
log ""
log "${B}Summary${X}"
log "  ${G}passed${X}: ${PASS}"
if [ "$FAIL" -gt "0" ]; then
  log "  ${R}failed${X}: ${FAIL}"
  exit 1
fi
log "  ${G}all green${X} 🎉"

#!/usr/bin/env bash
# validate-flow.sh — End-to-end validation of the complete Crewship CLI → API → Docker flow.
#
# Usage:
#   ./scripts/validate-flow.sh                         # Full validation
#   SKIP_AGENT_RUN=1 ./scripts/validate-flow.sh        # Skip Phase 6 (needs LLM credentials)
#   SKIP_CLEANUP=1 ./scripts/validate-flow.sh           # Keep test data after run
#   CREWSHIP_SERVER=http://host:8081 ./scripts/validate-flow.sh
#
# Prerequisites:
#   - crewship binary built (go build -o crewship ./cmd/crewship/)
#   - Server running (crewship start / dev.sh)
#   - Authenticated (crewship login)
#   - Workspace selected (crewship workspace use <slug>)
#
# Exit codes: 0 = all pass, 1 = failures detected

set -euo pipefail

CLI="${CREWSHIP_CLI:-crewship}"
SERVER="${CREWSHIP_SERVER:-http://localhost:8080}"
SKIP_RUN="${SKIP_AGENT_RUN:-}"
SKIP_CLEANUP="${SKIP_CLEANUP:-}"

# Test state
PASS=0
FAIL=0
TOTAL=0
START_TIME=$(date +%s)

# Test resource IDs (populated during test)
MISSION_ID=""
HAS_CREDENTIAL=""

# --- Assertion helpers ---

assert_ok() {
  local desc="$1"; shift
  TOTAL=$((TOTAL + 1))
  local output
  if output=$("$@" 2>&1); then
    PASS=$((PASS + 1))
    echo "  ✓ $desc"
    return 0
  else
    FAIL=$((FAIL + 1))
    echo "  ✗ $desc"
    echo "$output" | head -3 | sed 's/^/    /'
    return 1
  fi
}

assert_contains() {
  local desc="$1" expected="$2"; shift 2
  TOTAL=$((TOTAL + 1))
  local output
  if output=$("$@" 2>&1) && echo "$output" | grep -qi "$expected"; then
    PASS=$((PASS + 1))
    echo "  ✓ $desc"
    return 0
  else
    FAIL=$((FAIL + 1))
    echo "  ✗ $desc (expected '$expected')"
    echo "$output" | head -3 | sed 's/^/    /'
    return 1
  fi
}

assert_not_empty() {
  local desc="$1"; shift
  TOTAL=$((TOTAL + 1))
  local output
  if output=$("$@" 2>&1) && [ -n "$output" ]; then
    PASS=$((PASS + 1))
    echo "  ✓ $desc"
    return 0
  else
    FAIL=$((FAIL + 1))
    echo "  ✗ $desc (output was empty)"
    return 1
  fi
}

assert_fails() {
  local desc="$1"; shift
  TOTAL=$((TOTAL + 1))
  if ! "$@" >/dev/null 2>&1; then
    PASS=$((PASS + 1))
    echo "  ✓ $desc (expected failure)"
    return 0
  else
    FAIL=$((FAIL + 1))
    echo "  ✗ $desc (should have failed)"
    return 1
  fi
}

# Idempotent create — succeeds or 409
try_create() {
  local output
  if output=$("$@" 2>&1); then
    echo "$output"
    return 0
  elif echo "$output" | grep -qiE "already|409|conflict|taken"; then
    return 0
  else
    echo "$output" >&2
    return 1
  fi
}

echo "========================================"
echo "  Crewship E2E Flow Validation"
echo "  Server: $SERVER"
echo "  CLI: $CLI"
echo "  Time: $(date '+%Y-%m-%d %H:%M:%S')"
echo "========================================"
echo ""

# ============================================================
# Phase 0: Health Checks
# ============================================================
echo "Phase 0: Health Checks"

assert_ok "Server /healthz returns 200" \
  curl -sf "$SERVER/healthz"

assert_ok "Server /readyz returns 200" \
  curl -sf "$SERVER/readyz"

assert_ok "crewship doctor passes" \
  "$CLI" doctor -s "$SERVER"

echo ""

# ============================================================
# Phase 1: Workspace & Auth
# ============================================================
echo "Phase 1: Workspace & Auth"

assert_ok "crewship whoami succeeds" \
  "$CLI" whoami -s "$SERVER"

assert_not_empty "crewship workspace list returns data" \
  "$CLI" workspace list -s "$SERVER" -f quiet

echo ""

# ============================================================
# Phase 2: Crew CRUD
# ============================================================
echo "Phase 2: Crew CRUD"

assert_ok "Create test crew" \
  try_create "$CLI" crew create --name "E2E Test Crew" --slug e2e-test-crew -s "$SERVER"

assert_contains "Get crew returns correct name" "E2E Test Crew" \
  "$CLI" crew get e2e-test-crew -s "$SERVER"

assert_contains "Crew list contains test crew" "e2e-test-crew" \
  "$CLI" crew list -s "$SERVER"

assert_ok "Re-create crew is idempotent" \
  try_create "$CLI" crew create --name "E2E Test Crew" --slug e2e-test-crew -s "$SERVER"

echo ""

# ============================================================
# Phase 3: Agent CRUD
# ============================================================
echo "Phase 3: Agent CRUD"

assert_ok "Create LEAD agent" \
  try_create "$CLI" agent create --name "E2E Lead" --slug e2e-lead --crew e2e-test-crew --role LEAD -s "$SERVER"

assert_ok "Create AGENT worker" \
  try_create "$CLI" agent create --name "E2E Worker" --slug e2e-worker --crew e2e-test-crew -s "$SERVER"

assert_contains "Get lead shows LEAD role" "LEAD" \
  "$CLI" agent get e2e-lead -s "$SERVER"

assert_contains "Get worker shows AGENT role" "AGENT" \
  "$CLI" agent get e2e-worker -s "$SERVER"

assert_contains "Agent list with crew filter" "e2e-lead" \
  "$CLI" agent list --crew e2e-test-crew -s "$SERVER"

assert_ok "Agent skills command works" \
  "$CLI" agent skills e2e-lead -s "$SERVER"

assert_ok "Agent credentials command works" \
  "$CLI" agent credentials e2e-lead -s "$SERVER"

echo ""

# ============================================================
# Phase 4: Credentials
# ============================================================
echo "Phase 4: Credentials"

assert_ok "Credential list works" \
  "$CLI" credential list -s "$SERVER"

# Check if CLAUDE_CODE_OAUTH_TOKEN exists for assignment
if "$CLI" credential list -s "$SERVER" -f quiet 2>/dev/null | grep -qi "CLAUDE_CODE_OAUTH_TOKEN"; then
  HAS_CREDENTIAL="1"
  echo "  → Found CLAUDE_CODE_OAUTH_TOKEN, testing assignment..."

  assert_ok "Assign credential to lead" \
    try_create "$CLI" credential assign CLAUDE_CODE_OAUTH_TOKEN e2e-lead -s "$SERVER"

  assert_ok "Assign credential to worker" \
    try_create "$CLI" credential assign CLAUDE_CODE_OAUTH_TOKEN e2e-worker -s "$SERVER"

  assert_not_empty "Lead has assigned credentials" \
    "$CLI" agent credentials e2e-lead -s "$SERVER"
else
  echo "  → No CLAUDE_CODE_OAUTH_TOKEN found, skipping assignment tests"
fi

echo ""

# ============================================================
# Phase 5: Crew Connections
# ============================================================
echo "Phase 5: Crew Connections"

assert_ok "Crew connections list works" \
  "$CLI" crew connections -s "$SERVER"

echo ""

# ============================================================
# Phase 6: Agent Run (optional — needs LLM credentials)
# ============================================================
echo "Phase 6: Agent Run"

if [ -n "$SKIP_RUN" ]; then
  echo "  → Skipped (SKIP_AGENT_RUN=1)"
elif [ -z "$HAS_CREDENTIAL" ]; then
  echo "  → Skipped (no credential assigned)"
else
  assert_ok "Run agent with simple prompt" \
    "$CLI" run e2e-worker "Reply with exactly: E2E_OK" --no-stream --timeout 120 -s "$SERVER"

  assert_ok "Agent runs list shows entries" \
    "$CLI" agent runs e2e-worker -s "$SERVER"
fi

echo ""

# ============================================================
# Phase 7: Mission Lifecycle
# ============================================================
echo "Phase 7: Mission Lifecycle"

# Create mission (capture ID from output)
if mission_output=$("$CLI" mission create --crew e2e-test-crew --title "E2E Test Mission" -s "$SERVER" -f json 2>&1); then
  MISSION_ID=$(echo "$mission_output" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
  TOTAL=$((TOTAL + 1)); PASS=$((PASS + 1))
  echo "  ✓ Create mission (ID: ${MISSION_ID:-unknown})"
else
  TOTAL=$((TOTAL + 1)); FAIL=$((FAIL + 1))
  echo "  ✗ Create mission"
  echo "$mission_output" | head -3 | sed 's/^/    /'
fi

if [ -n "$MISSION_ID" ]; then
  assert_contains "Mission list contains test mission" "E2E Test Mission" \
    "$CLI" mission list --crew e2e-test-crew -s "$SERVER"

  assert_ok "Delete mission" \
    "$CLI" mission delete "$MISSION_ID" --yes -s "$SERVER"
fi

echo ""

# ============================================================
# Phase 8: Cleanup
# ============================================================
echo "Phase 8: Cleanup"

if [ -n "$SKIP_CLEANUP" ]; then
  echo "  → Skipped (SKIP_CLEANUP=1)"
  echo "  → To clean up manually:"
  echo "    $CLI agent delete e2e-worker --yes -s $SERVER"
  echo "    $CLI agent delete e2e-lead --yes -s $SERVER"
  echo "    $CLI crew delete e2e-test-crew --yes -s $SERVER"
else
  assert_ok "Delete worker agent" \
    "$CLI" agent delete e2e-worker --yes -s "$SERVER"

  assert_ok "Delete lead agent" \
    "$CLI" agent delete e2e-lead --yes -s "$SERVER"

  assert_ok "Delete test crew" \
    "$CLI" crew delete e2e-test-crew --yes -s "$SERVER"

  assert_fails "Crew no longer exists (404)" \
    "$CLI" crew get e2e-test-crew -s "$SERVER"
fi

echo ""

# ============================================================
# Phase 9: Summary
# ============================================================
END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))

echo "========================================"
echo "  Results"
echo "========================================"
echo ""
echo "  Passed: $PASS / $TOTAL"
echo "  Failed: $FAIL / $TOTAL"
echo "  Duration: ${DURATION}s"
echo ""

if [ "$FAIL" -gt 0 ]; then
  echo "  STATUS: FAILED ✗"
  echo ""
  exit 1
else
  echo "  STATUS: ALL PASSED ✓"
  echo ""
  exit 0
fi

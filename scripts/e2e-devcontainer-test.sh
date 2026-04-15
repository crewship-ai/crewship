#!/bin/bash
# E2E test for CRE-123 devcontainer provisioning
# Runs end-to-end: create crew -> set devcontainer config -> provision -> verify image -> cleanup
#
# Usage: bash scripts/e2e-devcontainer-test.sh
# Requires: running Crewship server on http://localhost:8080, Docker daemon

set -euo pipefail

CREWSHIP="${CREWSHIP:-./crewship}"
SERVER="${CREWSHIP_SERVER:-http://localhost:8080}"
SLUG="e2e-devcontainer-$(date +%s)"
DB="${CREWSHIP_DB:-./crewship.db}"

cleanup() {
    set +e
    echo ""
    echo "=== Cleanup ==="
    "$CREWSHIP" crew delete "$SLUG" --server "$SERVER" --force 2>/dev/null || true
    # Remove cached images from this test
    docker images --format '{{.Repository}}:{{.Tag}}' 2>/dev/null | \
        grep '^crewship-cache:' | \
        while read img; do
            docker rmi -f "$img" >/dev/null 2>&1 || true
        done
    set -e
}

fail() {
    echo ""
    echo "FAIL: $1" >&2
    cleanup
    exit 1
}

step() {
    echo ""
    echo "=== $1 ==="
}

# Pre-flight checks
step "Pre-flight"
[ -x "$CREWSHIP" ] || fail "CLI binary not found at $CREWSHIP. Run 'make build:go' first."
[ -f "./crewship-sidecar" ] || fail "Sidecar binary not found at ./crewship-sidecar. Run 'make build:sidecar' first."
[ -f "./entrypoint.sh" ] || fail "Entrypoint not found at ./entrypoint.sh. Run 'make build:sidecar' first."
[ -f "$DB" ] || fail "Database not found at $DB"
docker info >/dev/null 2>&1 || fail "Docker daemon not reachable"
curl -sf -o /dev/null "$SERVER/healthz" || fail "Crewship server not reachable at $SERVER"

echo "OK: binaries present, Docker up, server reachable"

# 1. Create crew
step "Step 1: Create crew '$SLUG'"
"$CREWSHIP" crew create --name "E2E Test" --slug "$SLUG" \
    --description "CRE-123 E2E test" \
    --server "$SERVER" 2>&1 | grep -q "created" || fail "crew create failed"

CREW_ID=$(sqlite3 "$DB" "SELECT id FROM crews WHERE slug = '$SLUG' AND deleted_at IS NULL")
[ -n "$CREW_ID" ] || fail "crew not found in DB after create"
echo "OK: crew $SLUG ($CREW_ID) created"

# 2. Write devcontainer + mise config files
step "Step 2: Prepare config files"
DEVCONTAINER_FILE=$(mktemp /tmp/devcontainer.XXXXXX.json)
MISE_FILE=$(mktemp /tmp/mise.XXXXXX.json)

cat > "$DEVCONTAINER_FILE" <<'JSON'
{
    "image": "debian:bookworm-slim",
    "features": {
        "ghcr.io/devcontainers/features/common-utils:2": {}
    }
}
JSON

cat > "$MISE_FILE" <<'JSON'
{
    "tools": {
        "node": "22"
    }
}
JSON

echo "OK: config files at $DEVCONTAINER_FILE and $MISE_FILE"

# 3. Apply config via CLI
step "Step 3: Apply config via 'crew config'"
"$CREWSHIP" crew config "$SLUG" \
    --devcontainer "$DEVCONTAINER_FILE" \
    --mise "$MISE_FILE" \
    --runtime-image "debian:bookworm-slim" \
    --server "$SERVER" 2>&1 || fail "crew config failed"

# Verify config was persisted
DCC=$(sqlite3 "$DB" "SELECT devcontainer_config FROM crews WHERE id = '$CREW_ID'")
[ -n "$DCC" ] || fail "devcontainer_config not persisted"
echo "OK: config applied"

# 4. Show config
step "Step 4: Verify 'crew config --show'"
"$CREWSHIP" crew config "$SLUG" --show --server "$SERVER" 2>&1 | grep -q "debian:bookworm-slim" || \
    fail "--show did not display runtime_image"
echo "OK: --show works"

# 5. Trigger provisioning
step "Step 5: Trigger provisioning"
"$CREWSHIP" crew provision "$SLUG" --server "$SERVER" 2>&1 || fail "provision trigger failed"

# 6. Poll status with 10min timeout
step "Step 6: Wait for provisioning to complete"
TIMEOUT=600  # 10 minutes
ELAPSED=0
INTERVAL=5
while [ $ELAPSED -lt $TIMEOUT ]; do
    STATUS=$("$CREWSHIP" crew provision status "$SLUG" --server "$SERVER" --format json 2>/dev/null | \
        python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('status','?'))")

    echo "  [$ELAPSED s] status: $STATUS"

    case "$STATUS" in
        completed) break ;;
        failed)
            ERR=$("$CREWSHIP" crew provision status "$SLUG" --server "$SERVER" --format json 2>/dev/null | \
                python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('error','unknown'))")
            fail "provisioning failed: $ERR"
            ;;
        running|pending|idle) ;;
        *) fail "unexpected status: $STATUS" ;;
    esac

    sleep $INTERVAL
    ELAPSED=$((ELAPSED + INTERVAL))
done

[ "$STATUS" = "completed" ] || fail "provisioning timed out after ${TIMEOUT}s"
echo "OK: provisioning completed in ${ELAPSED}s"

# 7. Verify cached image exists
step "Step 7: Verify cached image in Docker"
CACHED_IMAGE=$(sqlite3 "$DB" "SELECT cached_image FROM crews WHERE id = '$CREW_ID'")
[ -n "$CACHED_IMAGE" ] || fail "cached_image NULL in DB"
echo "  cached_image: $CACHED_IMAGE"

docker image inspect "$CACHED_IMAGE" >/dev/null 2>&1 || fail "cached image not found in Docker: $CACHED_IMAGE"
echo "OK: image exists"

# 8. Verify features were installed in the image
step "Step 8: Verify common-utils feature installed (git in image)"
docker run --rm "$CACHED_IMAGE" bash -c "which git && git --version" || \
    fail "common-utils feature didn't install git"
echo "OK: git found in cached image"

# 9. Cleanup on success
step "SUCCESS"
echo "All E2E tests passed."
rm -f "$DEVCONTAINER_FILE" "$MISE_FILE"
cleanup

exit 0

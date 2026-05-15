#!/usr/bin/env bash
# e2e-backup-container-test.sh — container round-trip for `crewship backup`.
#
# Unlike the broader e2e-backup-test.sh (which only covers help output
# and unit tests), this script exercises the full Docker path:
#
#   write canary → backup create → destroy canary → backup restore → verify canary
#
# The point is to catch regressions in the pause / CopyFrom / CopyTo
# chain that unit tests with a stub DockerOps cannot hit.
#
# Run on: any host with Docker + a running crewship server + at
# least one provisioned crew.
#
# Required env:
#   CREWSHIP_CREW     — crew slug whose container will be mutated
#   CREWSHIP_BINARY   — path to the crewship binary (default: crewship on PATH)
#
# Exit codes:
#   0   success
#   77  skipped (Docker unavailable, required env missing, or no crew)
#   1   test failure
#
# Idempotent: the canary file is removed during the test and rewritten
# on next run.
set -euo pipefail

CREWSHIP_BINARY="${CREWSHIP_BINARY:-crewship}"
CREWSHIP_CREW="${CREWSHIP_CREW:-}"

log() { printf '\033[1;34m==\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; exit 1; }
skip() { printf '\033[1;33mSKIP:\033[0m %s\n' "$*" >&2; exit 77; }

# --- preflight ---------------------------------------------------------
log "preflight"

command -v docker >/dev/null 2>&1 || skip "docker not installed"
docker info >/dev/null 2>&1 || skip "docker daemon unreachable"
command -v "$CREWSHIP_BINARY" >/dev/null 2>&1 || skip "crewship binary not on PATH (set CREWSHIP_BINARY)"

if [[ -z "$CREWSHIP_CREW" ]]; then
  skip "CREWSHIP_CREW not set; export the slug of a provisioned crew"
fi

CONTAINER_NAME="crewship-team-${CREWSHIP_CREW}"
if ! docker inspect -f '{{.State.Running}}' "$CONTAINER_NAME" 2>/dev/null | grep -q true; then
  skip "container $CONTAINER_NAME is not running; provision the crew first"
fi

WORKDIR="$(mktemp -d -t crewship-e2e-XXXXXX)"
BUNDLE="$WORKDIR/bundle.tar.zst"
CLEANUP() {
  local rc=$?
  rm -rf "$WORKDIR" || true
  # Best-effort trap: each run writes a fresh canary at step 1, so
  # there is nothing to rehydrate here. We intentionally do NOT restore
  # the canary on failure — that would mask whether the restore step
  # actually worked on the next invocation. If the trap needs to
  # survive `set -e` aborts, wrap local docker cleanup in `|| true`.
  exit $rc
}
trap CLEANUP EXIT

# --- 1. write canary inside the container -----------------------------
log "write canary"
CANARY_VALUE="canary-$(date -u +%s)-$$"
# Pass the value via an explicit env var so outer-shell expansion is
# not load-bearing on the sh -c body's quoting. Single-quoted body
# keeps the in-container shell from re-interpreting anything.
docker exec -e CANARY_VALUE="$CANARY_VALUE" "$CONTAINER_NAME" \
  sh -c 'printf "%s" "$CANARY_VALUE" > /workspace/CANARY.txt'
SEEN="$(docker exec "$CONTAINER_NAME" cat /workspace/CANARY.txt)"
if [[ "$SEEN" != "$CANARY_VALUE" ]]; then
  fail "failed to place canary (got: $SEEN)"
fi

# --- 2. create backup --------------------------------------------------
log "backup create --scope=crew"
"$CREWSHIP_BINARY" backup create \
  --scope=crew \
  --crew="$CREWSHIP_CREW" \
  --no-encrypt \
  --output="$BUNDLE"

if [[ ! -s "$BUNDLE" ]]; then
  fail "bundle $BUNDLE was not written or is empty"
fi

# --- 3. destroy canary -------------------------------------------------
log "destroy canary in live container"
docker exec "$CONTAINER_NAME" rm -f /workspace/CANARY.txt

# Sanity check the destroy actually worked before trusting the restore
# result — otherwise a broken `docker exec rm` would falsely pass.
if docker exec "$CONTAINER_NAME" test -f /workspace/CANARY.txt; then
  fail "canary still present after destroy step"
fi

# --- 4. restore --------------------------------------------------------
log "backup restore"
"$CREWSHIP_BINARY" backup restore "$BUNDLE"

# --- 5. verify ---------------------------------------------------------
log "verify canary was restored"
RESTORED="$(docker exec "$CONTAINER_NAME" cat /workspace/CANARY.txt 2>/dev/null || true)"
if [[ "$RESTORED" != "$CANARY_VALUE" ]]; then
  fail "canary mismatch after restore: got '$RESTORED' want '$CANARY_VALUE'"
fi

log "backup roundtrip OK — canary survived destroy→restore"

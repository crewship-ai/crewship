#!/usr/bin/env bash
# e2e-agent-run-test.sh — proves an agent actually executes a prompt
# end-to-end against a running server.
#
# This is the universal "does the product work as an app" guard: provisioning
# reporting "provisioned" only means the crew IMAGE built — the agent container
# is created lazily on first run, and that start can still fail (e.g. an
# orphaned pre-C1 legacy volume blocks it). A green smoke run is the only proof
# the whole stack — container start, credential injection, orchestrator, CLI —
# works for a real prompt. Mirrors cmd_seed_smoke.go's runSmokeTest so CI and
# the seed exercise the exact same CLI path.
#
# Run on: any host with Docker + a running crewship server + a seeded,
# provisioned crew.
#
# Required env:
#   CREWSHIP_AGENT    — agent slug to prompt (e.g. "alex")
# Optional env:
#   CREWSHIP_SERVER   — server URL (passed through to the CLI when set)
#   CREWSHIP_BINARY   — path to the crewship binary (default: crewship on PATH)
#   CREWSHIP_TIMEOUT  — per-run timeout in seconds (default: 60)
#
# Exit codes:
#   0   success — agent returned a non-empty response
#   77  skipped (Docker unavailable, required env missing)
#   1   test failure — captured output is printed so the failure is
#       self-diagnosing (e.g. "failed to start agent container: <cause>")
set -euo pipefail

CREWSHIP_BINARY="${CREWSHIP_BINARY:-crewship}"
CREWSHIP_AGENT="${CREWSHIP_AGENT:-}"
CREWSHIP_TIMEOUT="${CREWSHIP_TIMEOUT:-60}"

log() { printf '\033[1;34m==\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; exit 1; }
skip() { printf '\033[1;33mSKIP:\033[0m %s\n' "$*" >&2; exit 77; }

# --- preflight ---------------------------------------------------------
log "preflight"

command -v docker >/dev/null 2>&1 || skip "docker not installed"
docker info >/dev/null 2>&1 || skip "docker daemon unreachable"
command -v "$CREWSHIP_BINARY" >/dev/null 2>&1 || skip "crewship binary not on PATH (set CREWSHIP_BINARY)"

if [[ -z "$CREWSHIP_AGENT" ]]; then
  skip "CREWSHIP_AGENT not set; export the slug of a seeded, provisioned agent"
fi

# Forward --server only when explicitly set; otherwise the CLI resolves it
# from env/config like every other command.
SERVER_ARGS=()
if [[ -n "${CREWSHIP_SERVER:-}" ]]; then
  SERVER_ARGS=(--server "$CREWSHIP_SERVER")
fi

# --- run a real prompt end-to-end -------------------------------------
log "prompting agent '$CREWSHIP_AGENT' (timeout ${CREWSHIP_TIMEOUT}s)"

# Same prompt + flags as the seed smoke test (cmd_seed_smoke.go). NOT --quiet:
# on failure we want the real cause visible rather than a bare "exit status 1".
set +e
OUT="$("$CREWSHIP_BINARY" run "$CREWSHIP_AGENT" \
  "Hello, introduce yourself in one sentence." \
  --no-stream --timeout "$CREWSHIP_TIMEOUT" "${SERVER_ARGS[@]}" 2>&1)"
RC=$?
set -e

if [[ $RC -ne 0 ]]; then
  printf '%s\n' "$OUT" >&2
  fail "agent run exited $RC — see output above (e.g. 'failed to start agent container: <cause>')"
fi

# Collapse whitespace to decide emptiness the same way smokeTestAgent does.
TRIMMED="$(printf '%s' "$OUT" | tr -d '[:space:]')"
if [[ -z "$TRIMMED" ]]; then
  fail "agent returned an empty response (run succeeded but produced no output)"
fi

log "OK — agent responded:"
printf '   %s\n' "$(printf '%s' "$OUT" | head -c 200)"

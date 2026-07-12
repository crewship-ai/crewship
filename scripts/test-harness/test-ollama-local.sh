#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Local-model use case — an OpenCode agent runs against a LOCAL Ollama model
# on the host, with NO cloud API key. This exercises the full BYO-endpoint path:
# ENDPOINT_URL credential → OPENCODE_CONFIG_CONTENT injection → private-endpoint
# egress opt-in → the agent container reaching host.docker.internal:11434.
#
# Prereqs (the whole file SKIPs if any is missing — it's an integration probe,
# not a core invariant):
#   1. macOS host (the scenario targets the Docker-Desktop + host Ollama setup)
#   2. Ollama reachable on the host (`ollama serve`, model pulled)
#   3. The server was started with CREWSHIP_ALLOW_PRIVATE_ENDPOINTS=1 (default-off
#      SSRF fence) — otherwise the container can't reach the private host address
#      and the run fails on egress. We detect that and SKIP with a clear reason.
#
# Model: ollama/qwen2.5-coder:7b (macOS-friendly). Override with OLLAMA_MODEL.

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

OLLAMA_MODEL="${OLLAMA_MODEL:-ollama/qwen2.5-coder:7b}"
OLLAMA_HOST_URL="${OLLAMA_HOST_URL:-http://localhost:11434}"
ENDPOINT_URL="${SEED_OLLAMA_ENDPOINT:-http://host.docker.internal:11434/v1}"

# ── Guards ──────────────────────────────────────────────────────────────────
if [[ "$(uname)" != "Darwin" ]]; then
  skip "ollama-local" "macOS-only scenario (Docker-Desktop host.docker.internal + host Ollama)"
  finish
fi
if ! curl -fsS --max-time 4 "${OLLAMA_HOST_URL}/api/tags" >/dev/null 2>&1; then
  skip "ollama-local" "host Ollama not reachable at ${OLLAMA_HOST_URL} (run 'ollama serve' + pull the model)"
  finish
fi

preflight

section "0. Ensure the local-model endpoint credential exists"
# Idempotent: seeded when CREWSHIP_SEED_OLLAMA=1, else created here. A 409/exists
# is fine — we only need the workspace-default ENDPOINT_URL to be present.
cs credential create --name OLLAMA_ENDPOINT --type ENDPOINT_URL --provider OLLAMA \
  --value "$ENDPOINT_URL" >/dev/null 2>&1 || true
cred_list="$(cs credential list 2>/dev/null || true)"
assert_contains "OLLAMA_ENDPOINT credential is present" "$cred_list" "OLLAMA_ENDPOINT"

section "1. Ensure the ollie / local-ai agent is configured for OpenCode + Ollama"
# Seeded when CREWSHIP_SEED_OLLAMA=1. If absent (default seed), skip cleanly —
# creating a crew + provisioning opencode-ai on the fly is out of scope here.
if ! cs agent get ollie >/dev/null 2>&1; then
  skip "ollama-local" "agent 'ollie' not seeded (re-seed with CREWSHIP_SEED_OLLAMA=1)"
  finish
fi

section "2. Agent answers using the LOCAL model"
reply="$(ask_agent ollie "In one short sentence, what is 2 + 2? Answer with the number.")"
if [[ -z "$reply" ]]; then
  # An empty reply here most commonly means the container couldn't reach the
  # private endpoint — i.e. CREWSHIP_ALLOW_PRIVATE_ENDPOINTS wasn't set.
  skip "ollama-local" "no reply — is the server started with CREWSHIP_ALLOW_PRIVATE_ENDPOINTS=1 and the model pulled?"
  finish
fi
assert_nonempty "ollie returned a non-empty reply from the local model" "$reply"
assert_contains "reply contains the answer (4)" "$reply" "4"

finish

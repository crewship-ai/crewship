#!/usr/bin/env bash
# Standalone live test of the sidecar /memory/write endpoint.
# Spins up the sidecar binary against a tempdir, runs four curl
# scenarios, then cleans up.

set -eu

MEM_DIR=$(mktemp -d /tmp/sidecar-mem.XXXX)
LOG_FILE=$(mktemp /tmp/sidecar-test.XXXX.log)
PORT=9201  # avoid default 9119 in case any local process owns it

# JSON config piped to the sidecar via stdin. We just need memory
# enabled — no credentials, no IPC. AgentRole=lead so the crew engine
# could be enabled too, but we leave CrewMemoryPath empty so only
# the agent-tier engine spins up.
read -r -d '' CONFIG <<JSON || true
{
  "credentials": [],
  "memory": {
    "enabled": true,
    "base_path": "${MEM_DIR}",
    "agent_slug": "test-agent",
    "agent_role": "lead"
  }
}
JSON

echo "[setup] mem_dir=${MEM_DIR}"
echo "[setup] port=${PORT}"

echo "${CONFIG}" | /tmp/crewship-2-sidecar -addr 127.0.0.1:${PORT} \
  >"${LOG_FILE}" 2>&1 &
SIDECAR_PID=$!
trap 'kill -TERM ${SIDECAR_PID} 2>/dev/null || true; wait ${SIDECAR_PID} 2>/dev/null; rm -rf "${MEM_DIR}"' EXIT

# Wait up to 5s for the listener.
for i in 1 2 3 4 5 6 7 8 9 10; do
  if curl -s -m 1 "http://127.0.0.1:${PORT}/memory/status" >/dev/null 2>&1; then
    echo "[ready] sidecar up on ${PORT}"
    break
  fi
  sleep 0.5
done

# Last-resort: confirm the process is alive.
if ! kill -0 ${SIDECAR_PID} 2>/dev/null; then
  echo "[FAIL] sidecar died during startup"
  tail -20 "${LOG_FILE}"
  exit 1
fi

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
pass=0
fail=0
check() {
  local label="$1" got="$2" want="$3"
  if [[ "${got}" == "${want}" ]]; then
    green "  PASS  ${label}  (HTTP ${got})"
    pass=$((pass+1))
  else
    red   "  FAIL  ${label}  got=${got} want=${want}"
    fail=$((fail+1))
  fi
}

echo
echo "[1] POST /memory/write — happy path"
STATUS=$(curl -s -o /tmp/r1.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d '{"file":"AGENT.md","content":"# Agent\nlong-term memory body\n"}' \
  "http://127.0.0.1:${PORT}/memory/write")
check "happy path returns 201" "${STATUS}" "201"
ls "${MEM_DIR}/AGENT.md" >/dev/null && green "  PASS  AGENT.md persisted on disk"

echo
echo "[2] POST /memory/write — scrubber should reject anthropic key"
STATUS=$(curl -s -o /tmp/r2.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d '{"file":"AGENT.md","content":"my key sk-ant-api03-abcd1234efgh5678ijkl, don '"'"'t share"}' \
  "http://127.0.0.1:${PORT}/memory/write")
check "scrubber rejection returns 422" "${STATUS}" "422"
KIND=$(jq -r .kind /tmp/r2.json 2>/dev/null || echo missing)
check "  rejection kind = scrubber" "${KIND}" "scrubber"

echo
echo "[3] POST /memory/write — cap overflow should 422"
BIG=$(python3 -c "print('x' * 5000)")
STATUS=$(curl -s -o /tmp/r3.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d "{\"file\":\"AGENT.md\",\"content\":\"${BIG}\"}" \
  "http://127.0.0.1:${PORT}/memory/write")
check "cap overflow returns 422" "${STATUS}" "422"
KIND=$(jq -r .kind /tmp/r3.json 2>/dev/null || echo missing)
check "  rejection kind = cap" "${KIND}" "cap"

echo
echo "[4] POST /memory/write — path traversal must 403"
STATUS=$(curl -s -o /tmp/r4.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d '{"file":"../../../etc/passwd","content":"x"}' \
  "http://127.0.0.1:${PORT}/memory/write")
check "path traversal returns 403" "${STATUS}" "403"

echo
echo "[5] Daily log path resolves under daily/"
STATUS=$(curl -s -o /tmp/r5.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d '{"file":"daily/2026-05-16.md","content":"session notes\n"}' \
  "http://127.0.0.1:${PORT}/memory/write")
check "daily log returns 201" "${STATUS}" "201"
ls "${MEM_DIR}/daily/2026-05-16.md" >/dev/null && green "  PASS  daily log persisted on disk"

echo
printf '\033[1mSummary: %d passed, %d failed\033[0m\n' "${pass}" "${fail}"
[[ "${fail}" -eq 0 ]] && exit 0 || exit 1

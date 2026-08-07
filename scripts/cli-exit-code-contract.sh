#!/usr/bin/env bash
# C2 — pin the CLI exit-code contract through the built binary.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLI="${CREWSHIP:-$ROOT/crewship}"
TMP="$(mktemp -d -t crewship-cli-exit-contract.XXXXXX)"
SERVER_PID=""
trap '[[ -n "${SERVER_PID:-}" ]] && kill "$SERVER_PID" 2>/dev/null || true; rm -rf "$TMP"' EXIT

[[ -x "$CLI" ]] || { echo "error: CLI binary not found: $CLI" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "error: python3 is required" >&2; exit 2; }

start_server() {
  local status="$1"
  rm -f "$TMP/port"
  CLI_TEST_STATUS="$status" CLI_TEST_PORT_FILE="$TMP/port" python3 - <<'PY' &
import json, os
from http.server import BaseHTTPRequestHandler, HTTPServer

status = int(os.environ["CLI_TEST_STATUS"])
port_file = os.environ["CLI_TEST_PORT_FILE"]

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        if status == 200 and self.path.startswith("/api/v1/workspaces"):
            body = [{"id": "ws", "slug": "ws", "name": "Contract Workspace"}]
        else:
            body = [] if status == 200 else {"detail": "contract probe status %d" % status}
        self.wfile.write(json.dumps(body).encode())
    def log_message(self, *_):
        pass

server = HTTPServer(("127.0.0.1", 0), Handler)
with open(port_file, "w", encoding="ascii") as fh:
    fh.write(str(server.server_port))
server.serve_forever()
PY
  SERVER_PID=$!
  for _ in $(seq 1 50); do [[ -s "$TMP/port" ]] && return 0; sleep 0.1; done
  echo "error: contract server did not start" >&2
  return 1
}

stop_server() {
  [[ -n "${SERVER_PID:-}" ]] && kill "$SERVER_PID" 2>/dev/null || true
  wait "$SERVER_PID" 2>/dev/null || true
  SERVER_PID=""
}

check_status() {
  local status="$1" want="$2" label="$3" port out err rc
  start_server "$status" || exit 1
  port="$(<"$TMP/port")"
  out="$TMP/out"; err="$TMP/err"
  CREWSHIP_TOKEN=contract-token HOME="$TMP/home" \
    "$CLI" --no-color --format json --server "http://127.0.0.1:$port" --workspace ws agent list >"$out" 2>"$err"
  rc=$?
  stop_server
  if (( rc != want )); then
    echo "FAIL $label: got exit $rc, want $want" >&2
    cat "$err" >&2
    exit 1
  fi
  echo "PASS $label: exit $rc"
}

mkdir -p "$TMP/home"
check_status 200 0 "success"
check_status 400 2 "validation error"
check_status 401 4 "unauthenticated"
check_status 403 4 "forbidden"
check_status 404 3 "not found"
check_status 409 5 "conflict"
check_status 429 6 "rate limited"
check_status 500 7 "server error"
check_status 503 7 "service unavailable"

CREWSHIP_TOKEN=contract-token HOME="$TMP/home" \
  "$CLI" --no-color --format json --workspace ws agent create >"$TMP/out" 2>"$TMP/err"
rc=$?
if (( rc != 2 )) || ! grep -Fq -- "--name is required" "$TMP/err"; then
  echo "FAIL local validation: got exit $rc; expected 2 naming --name" >&2
  cat "$TMP/err" >&2
  exit 1
fi
echo "PASS local validation: exit 2"

CREWSHIP_TOKEN=contract-token HOME="$TMP/home" \
  "$CLI" --no-color --format json --server http://127.0.0.1:1 --workspace ws agent list >"$TMP/out" 2>"$TMP/err"
rc=$?
if (( rc != 8 )); then
  echo "FAIL connection error: got exit $rc, want 8" >&2
  cat "$TMP/err" >&2
  exit 1
fi
echo "PASS connection error: exit 8"

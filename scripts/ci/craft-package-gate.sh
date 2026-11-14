#!/usr/bin/env bash
# Craft package gate: a Word or Sheet package may be pinned only when its editor
# works with the real host (tools/craft-components/README.md, "Browser gate").
#
#   scripts/ci/craft-package-gate.sh <crewship binary with embedded UI> <package dir> <output dir>
#
# <package dir> holds catalog.txt and the .craftpkg files of
# scripts/build-craft-components.py. A throwaway server (its own data directory,
# HOME and port, nothing shared) trusts exactly those pins, the packages are
# installed and turned on through the CLI, and playwright.craft-packages.config.ts
# runs under xvfb: open, edit, save, reopen for Word and Sheet. Exit status is the
# test result. CRAFT_GATE_PORT picks the port (default 18480).
set -euo pipefail

binary="$(realpath "$1")"
packages="$(realpath "$2")"
mkdir -p "$3"
output="$(realpath "$3")"
port="${CRAFT_GATE_PORT:-18480}"
origin="http://127.0.0.1:$port"
root="$(cd "$(dirname "$0")/../.." && pwd)"

[ -x "$binary" ] || { echo "no server binary at $binary" >&2; exit 2; }
pins="$(tr -d '\n' < "$packages/catalog.txt")"
[[ "$pins" =~ ^[a-z]+=[A-Za-z0-9.+_-]+=[0-9a-f]{64}(,[a-z]+=[A-Za-z0-9.+_-]+=[0-9a-f]{64})*$ ]] || { echo "bad catalog: $pins" >&2; exit 2; }
for app in wordcraft gridcraft; do [ -f "$packages/crewship-craft-$app.craftpkg" ] || { echo "missing $app package" >&2; exit 2; }; done

state="$(mktemp -d "${TMPDIR:-/tmp}/craft-gate.XXXXXX")"
server_pid=""
cleanup() {
  if [ -n "$server_pid" ]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  cp "$state/server.log" "$output/server.log" 2>/dev/null || true
  rm -rf "$state"
}
trap cleanup EXIT
mkdir -p "$state/home" "$state/data" "$state/logs"

# Every crewship process, server and CLI, starts without the caller's Crewship
# settings and credentials (no CREWSHIP_*, no token, key, secret or password
# variable) and with the throwaway HOME: it cannot reach another server, and
# the CLI profile written by `seed` never reaches the caller's account.
clean=()
while IFS= read -r name; do clean+=(-u "$name"); done < <(compgen -e | grep -E '^CREWSHIP_|TOKEN|SECRET|PASSWORD|API_KEY|^DATABASE_URL$|^NEXTAUTH_' || true)
run_crewship() { env "${clean[@]}" HOME="$state/home" CREWSHIP_DATA_DIR="$state/data" "${cli_extra[@]}" "$binary" "$@"; }
cli_extra=()

env "${clean[@]}" \
HOME="$state/home" \
CREWSHIP_DATA_DIR="$state/data" \
CREWSHIP_STORAGE_BASE_PATH="$state/data" \
CREWSHIP_LOG_PATH="$state/logs" \
CREWSHIP_BOLT_PATH="$state/data/state.db" \
DATABASE_URL="file:$state/data/crewship.db" \
CREWSHIP_PORT="$port" \
CREWSHIP_NEXTJS_URL="$origin" \
CREWSHIP_SKIP_SIDECAR=1 \
NEXTAUTH_SECRET="$(openssl rand -hex 32)" \
ENCRYPTION_KEY="$(openssl rand -hex 32)" \
CREWSHIP_CRAFT_COMPONENT_PINS="$pins" \
CREWSHIP_CRAFT_COMPONENTS_DIR="$state/data/craft-components" \
  "$binary" start >"$state/server.log" 2>&1 &
server_pid=$!
for _ in $(seq 1 120); do
  curl -sf -o /dev/null "$origin/healthz" && break
  kill -0 "$server_pid" 2>/dev/null || { tail -100 "$state/server.log" >&2; exit 1; }
  sleep 1
done
curl -sf -o /dev/null "$origin/healthz" || { tail -100 "$state/server.log" >&2; exit 1; }

for attempt in 1 2 3; do
  run_crewship seed --offline-demo --server "$origin" >"$output/seed.log" 2>&1 && break
  [ "$attempt" = 3 ] && { tail -50 "$output/seed.log" >&2; exit 1; }
  sleep 5
done
workspace="$(run_crewship whoami --format json --server "$origin" | jq -r '.workspace.id')"
if [ -z "$workspace" ] || [ "$workspace" = null ]; then echo "no seeded workspace" >&2; exit 1; fi
cli_extra=(CREWSHIP_WORKSPACE="$workspace")
run_crewship admin ratelimits set http.api_per_min 1000 --server "$origin" >/dev/null
cli_extra=()

# The real install path: upload, verify against the pin, start without a restart.
for app in word:wordcraft sheet:gridcraft; do
  run_crewship admin instance applications install "${app%%:*}" --file "$packages/crewship-craft-${app#*:}.craftpkg" --server "$origin"
  run_crewship admin instance applications enable "${app%%:*}" --server "$origin"
done
run_crewship admin instance applications --server "$origin" | tee "$output/applications.txt"

cd "$root"
CRAFT_GATE_ORIGIN="$origin" CRAFT_GATE_WORKSPACE="$workspace" \
CRAFT_GATE_EMAIL=demo@crewship.ai CRAFT_GATE_PASSWORD=password123 CRAFT_GATE_OUTPUT="$output" \
  xvfb-run -a -s "-screen 0 1600x1200x24" pnpm exec playwright test --config=playwright.craft-packages.config.ts

#!/usr/bin/env bash
# Inside the Playwright container of scripts/ci/sheet-client-e2e.sh: /src is the
# commit, /opt/crewship the server, /pkg the packages, /out the results.
set -euo pipefail
port=18490; origin="http://127.0.0.1:$port"; state=/tmp/sheet-client
finish() {
  status=$?
  [ -n "${server_pid:-}" ] && { kill "$server_pid" 2>/dev/null; wait "$server_pid" 2>/dev/null; }
  cp "$state/server.log" /out/server.log 2>/dev/null || true
  chown -R "${HOST_UID:-0}:${HOST_GID:-0}" /out /src 2>/dev/null || true
  exit "$status"
}
trap finish EXIT
mkdir -p "$state/home" "$state/data" "$state/logs"
secret() { node -e 'process.stdout.write(require("crypto").randomBytes(32).toString("hex"))'; }
run_crewship() { env HOME="$state/home" CREWSHIP_DATA_DIR="$state/data" "${cli_extra[@]}" /opt/crewship "$@"; }
cli_extra=()

HOME="$state/home" CREWSHIP_DATA_DIR="$state/data" CREWSHIP_STORAGE_BASE_PATH="$state/data" \
CREWSHIP_LOG_PATH="$state/logs" CREWSHIP_BOLT_PATH="$state/data/state.db" DATABASE_URL="file:$state/data/crewship.db" \
CREWSHIP_PORT="$port" CREWSHIP_NEXTJS_URL="$origin" CREWSHIP_SKIP_SIDECAR=1 \
NEXTAUTH_SECRET="$(secret)" ENCRYPTION_KEY="$(secret)" \
CREWSHIP_CRAFT_COMPONENT_PINS="$(tr -d '\n' < /pkg/catalog.txt)" CREWSHIP_CRAFT_COMPONENTS_DIR="$state/data/craft-components" \
  /opt/crewship start --no-docker >"$state/server.log" 2>&1 &
server_pid=$!

npm install -g --silent --no-fund --no-audit "pnpm@$PNPM_VERSION"
pnpm config set store-dir /pnpm/store >/dev/null
pnpm install --frozen-lockfile --ignore-scripts --reporter=silent

for _ in $(seq 1 120); do curl -sf -o /dev/null "$origin/healthz" && break; kill -0 "$server_pid" || { tail -80 "$state/server.log" >&2; exit 1; }; sleep 1; done
curl -sf -o /dev/null "$origin/healthz" || { tail -80 "$state/server.log" >&2; exit 1; }
for attempt in 1 2 3; do run_crewship seed --offline-demo --server "$origin" >/out/seed.log 2>&1 && break; [ "$attempt" = 3 ] && { tail -40 /out/seed.log >&2; exit 1; }; sleep 5; done
json() { node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const v=JSON.parse(s);const f=new Function("v","return "+process.argv[1]);const r=f(v);process.stdout.write(r==null?"":String(r))})' "$1"; }
workspace="$(run_crewship whoami --format json --server "$origin" | json 'v.workspace&&v.workspace.id')"
[ -n "$workspace" ] || { echo "no seeded workspace" >&2; exit 1; }
cli_extra=(CREWSHIP_WORKSPACE="$workspace")
run_crewship admin ratelimits set http.api_per_min 1000 --server "$origin" >/dev/null
# Workspace flags a spec needs (space-separated), e.g. artifact_agent_access_modes.
for flag in ${SHEET_E2E_FLAGS:-}; do
  run_crewship feature-flag enable "$flag" --server "$origin" >>/out/flags.txt 2>&1 || echo "flag $flag could not be enabled" >>/out/flags.txt
done
[ -z "${SHEET_E2E_FLAGS:-}" ] || run_crewship feature-flag list --server "$origin" >>/out/flags.txt 2>&1 || true
agent="${SHEET_E2E_AGENT:-$(run_crewship agent list --format json --server "$origin" 2>/dev/null | json '(Array.isArray(v)?v:(v.agents||v.items||[])).map(a=>a.slug).filter(Boolean)[0]')}"
cli_extra=()
[ -n "$agent" ] || { echo "no agent in the seeded workspace" >&2; exit 1; }
for app in word:wordcraft sheet:gridcraft; do
  run_crewship admin instance applications install "${app%%:*}" --file "/pkg/crewship-craft-${app#*:}.craftpkg" --server "$origin" >/dev/null
  run_crewship admin instance applications enable "${app%%:*}" --server "$origin" >/dev/null
done
# Agents in Word and Sheet, for specs about the agent's access (SHEET_E2E_AGENTS=1).
[ "${SHEET_E2E_AGENTS:-}" != 1 ] || run_crewship admin instance applications enable agents --server "$origin" >/dev/null
run_crewship admin instance applications --server "$origin" > /out/applications.txt
echo "server $origin, workspace $workspace, agent $agent"

CRAFT_GATE_ORIGIN="$origin" CRAFT_GATE_WORKSPACE="$workspace" CRAFT_GATE_AGENT="$agent" \
CRAFT_GATE_EMAIL=demo@crewship.ai CRAFT_GATE_PASSWORD=password123 CRAFT_GATE_OUTPUT=/out \
  xvfb-run -a -s "-screen 0 1600x1200x24" npx playwright test -c playwright.sheet-client.config.ts "$@"

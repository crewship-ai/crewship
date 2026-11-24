#!/usr/bin/env bash
set -euo pipefail
build_root=$1
candidate=$2
route=$3
shift 3
harness_dir="$(cd "$(dirname "$0")" && pwd)"
if ! command -v pnpm >/dev/null; then
  mkdir -p "$build_root/bin"
  corepack enable --install-directory "$build_root/bin"
  export PATH="$build_root/bin:$PATH"
fi

pnpm install --frozen-lockfile
pnpm prisma generate
pnpm exec playwright install --with-deps chromium
if [ "$route" = "/graph-check" ]; then
  mkdir -p "app/(dashboard)/graph-check"
  cp "$harness_dir/graph-fixture.tsx" "app/(dashboard)/graph-check/page.tsx"
  sed -i '/export const APP_ROUTE_PATTERNS = \[/a\  "/graph-check",' lib/app-routes.ts
fi
pnpm build
bash scripts/embed-web-out.sh sync
go build -o "$build_root/crewship" ./cmd/crewship
printf 'CANDIDATE %s\n' "$candidate"

launcher="$build_root/crewship"
no_daemon=0
export BROWSER_VERIFICATION_COMPLETE="$build_root/passed"
if ! command -v docker >/dev/null; then
  no_daemon=1
  launcher="$build_root/crewship-local"
  printf '#!/usr/bin/env bash\nexec %q "$@" --no-docker\n' "$build_root/crewship" > "$launcher"
  chmod 700 "$launcher"
fi
cleanup() {
  rc=$?
  trap - EXIT
  if [ -d "$CREWSHIP_THROWAWAY_STATE/browser-check" ]; then
    if [ "$rc" -ne 0 ]; then
      grep -Ei 'error|panic|bind|fatal' "$CREWSHIP_THROWAWAY_STATE/browser-check/server.log" | tail -30 || true
    fi
    bash scripts/throwaway-server.sh stop browser-check || true
  fi
  if [ -n "${CREWSHIP_INSTANCE_ID:-}" ]; then
    auth="$HOME/.crewship/e2e-auth/crewship-e2e-auth-$CREWSHIP_INSTANCE_ID.json"
    if [ -f "$auth" ]; then unlink "$auth"; fi
  fi
  exit "$rc"
}
trap cleanup EXIT

cp "$harness_dir/screenshot.mjs" "$PWD/.remote-browser-evidence.mjs"
cp "$harness_dir/graph-screenshot.mjs" "$PWD/.remote-graph-evidence.mjs"
export E2E_EMAIL=admin-baseline@crewship.local E2E_PASSWORD=rbac-demo-1234
export E2E_MEMBER_EMAIL=member-baseline@crewship.local E2E_MEMBER_PASSWORD=rbac-demo-1234
export E2E_RBAC_PASSWORD=rbac-demo-1234 E2E_ALLOW_WRITES=1
export CREWSHIP_INSTANCE_ID="runner-${build_root##*-}"

set +e
bash scripts/throwaway-server.sh run browser-check --binary "$launcher" --port 8146 \
  --env CREWSHIP_SKIP_SIDECAR=1 --env CREWSHIP_ALLOW_SIGNUP=true \
  -- bash -ec '
    binary=$1
    route=$2
    shift 2
    export PLAYWRIGHT_BASE_URL="$CREWSHIP_SERVER"
    "$binary" seed --offline-demo --with-users --server "$CREWSHIP_SERVER"
    "$binary" admin instance add-admin admin-baseline@crewship.local --server "$CREWSHIP_SERVER"
    for spec in "$@"; do
      pnpm exec playwright test --config=playwright.nightly.config.ts --retries=0 "$spec"
    done
    if [ "$route" = "/graph-check" ]; then
      node .remote-graph-evidence.mjs
    else
      node .remote-browser-evidence.mjs "$route"
    fi
    : > "$BROWSER_VERIFICATION_COMPLETE"
  ' browser-child "$build_root/crewship" "$route" "$@"

run_rc=$?
set -e
if ((run_rc != 0)); then
  # This sandbox has no Docker executable, and the server was launched
  # explicitly without a container provider. Require successful tests and
  # positive evidence that the throwaway wrapper already stopped its server.
  ((no_daemon == 1)) && test -f "$BROWSER_VERIFICATION_COMPLETE" || exit "$run_rc"
  if curl -fsS http://127.0.0.1:8146/healthz >/dev/null 2>&1; then
    echo 'Throwaway server still listening after cleanup' >&2
    exit 71
  fi
  printf 'Throwaway stopped; ephemeral runner owns the remaining files
'
fi

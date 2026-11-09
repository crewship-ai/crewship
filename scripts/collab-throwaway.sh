#!/usr/bin/env bash
# collab-throwaway.sh — run the two-identity collaboration acceptance (#5144)
# on a disposable server, never on a shared slot.
#
#   scripts/collab-throwaway.sh [--binary PATH] [--name NAME] [-- PLAYWRIGHT_ARGS...]
#
# 1. scripts/throwaway-server.sh run NAME --documents: own data dir, port,
#    container prefix and private Docs sockets; torn down when this ends.
# 2. The Docs engine (tools/docs-service/service.mjs) on that instance's
#    sockets, as a child of this run.
# 3. seed --offline-demo --with-users: the bootstrap owner plus MEMBER and
#    VIEWER accounts — genuinely different identities for every scenario.
# 4. playwright.collab.config.ts (e2e/collab-*.spec.ts). The specs create their
#    own artifacts and grants through the CLI binary and restart this server
#    (CO10) with `throwaway-server.sh restart NAME`.
#
# The binary must embed the web export (pnpm build + scripts/embed-web-out.sh
# sync) and be built with Design enabled, like the nightly build-tools job.
# Videos and screenshots go to COLLAB_EVIDENCE_DIR when it is set.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
die() { echo "collab-throwaway: $*" >&2; exit 2; }

if [[ "${1:-}" == "--inner" ]]; then
  shift
  name=$1 binary=$2; shift 2
  manifest=${CREWSHIP_THROWAWAY_MANIFEST:?run through throwaway-server.sh}
  field() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))[sys.argv[2]])' "$manifest" "$1"; }
  service_socket=$(field documents_service_socket)
  authority_socket=$(field documents_authority_socket)
  # The engine exits when the server's authority socket is not there yet; on a
  # loaded host the server can take a while to open it.
  for _ in $(seq 1 120); do [[ -S "$authority_socket" ]] && break; sleep 0.5; done
  [[ -S "$authority_socket" ]] || die "server did not open its Docs authority socket"
  CREWSHIP_DOCS_SOCKET=$service_socket \
    CREWSHIP_DOCS_AUTHORITY_SOCKET=$authority_socket \
    CREWSHIP_DOCS_SERVICE_TOKEN_FILE=$(field documents_service_token_file) \
    node "$ROOT/tools/docs-service/service.mjs" &
  docs=$!
  # Stops the engine on every ordinary exit. A SIGKILL of this shell skips the
  # trap and leaves the engine running: stop it by hand (its command line names
  # this instance's socket under the state dir).
  trap 'kill "$docs" 2>/dev/null || true; wait "$docs" 2>/dev/null || true' EXIT
  for _ in $(seq 1 60); do [[ -S "$service_socket" ]] && break; sleep 0.5; done
  [[ -S "$service_socket" ]] || die "Docs engine did not open its socket"
  for attempt in 1 2 3; do
    if "$binary" seed --offline-demo --with-users --skip-issues --server "$CREWSHIP_SERVER"; then break; fi
    [[ $attempt -lt 3 ]] || die "seed failed"
    sleep 5
  done
  ws=$("$binary" whoami --format json | python3 -c 'import json,sys; print(json.load(sys.stdin)["workspace"]["id"])')
  CREWSHIP_WORKSPACE=$ws "$binary" admin ratelimits set http.api_per_min 1000
  docs_status=$("$binary" doc status)
  [[ $docs_status == *'"enabled":true'* ]] || die "Docs are not enabled on the throwaway server"
  E2E_CREWSHIP_DATA_DIR=$(field data_dir)
  export PLAYWRIGHT_BASE_URL=$CREWSHIP_SERVER CREWSHIP_BIN=$binary COLLAB_THROWAWAY_NAME=$name E2E_CREWSHIP_DATA_DIR
  cd "$ROOT"
  status=0
  pnpm exec playwright test --config=playwright.collab.config.ts "$@" || status=$?
  exit "$status"
fi

binary="$ROOT/crewship" name="collab-$$"
while [[ $# -gt 0 ]]; do
  case $1 in
    --binary) [[ $# -ge 2 ]] || die "--binary needs a path"; binary=$2; shift 2 ;;
    --name) [[ $# -ge 2 ]] || die "--name needs a value"; name=$2; shift 2 ;;
    --) shift; break ;;
    *) die "unknown argument: $1" ;;
  esac
done
[[ -x "$binary" ]] || die "no executable server binary at $binary (use --binary)"
[[ -d "$ROOT/tools/docs-service/node_modules" ]] || die "install the Docs engine first: pnpm --dir tools/docs-service install --frozen-lockfile"
# Unix socket paths are limited to ~100 bytes: keep the state root short.
export CREWSHIP_THROWAWAY_STATE=${CREWSHIP_THROWAWAY_STATE:-/tmp/crewship-collab-$(id -u)}
# A predictable default must be ours alone: create it private, refuse anything else.
mkdir -p -m 700 "$CREWSHIP_THROWAWAY_STATE"
[[ -O "$CREWSHIP_THROWAWAY_STATE" && ! -L "$CREWSHIP_THROWAWAY_STATE" ]] || die "$CREWSHIP_THROWAWAY_STATE is not a private directory owned by $(id -un)"
chmod 700 "$CREWSHIP_THROWAWAY_STATE"
binary=$(cd "$(dirname "$binary")" && pwd)/$(basename "$binary")
exec "$ROOT/scripts/throwaway-server.sh" run "$name" --binary "$binary" --documents --env CREWSHIP_SKIP_SIDECAR=1 \
  -- bash "$ROOT/scripts/collab-throwaway.sh" --inner "$name" "$binary" "$@"

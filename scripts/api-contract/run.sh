#!/usr/bin/env bash
# Safe, opt-in live contract checks for an already-running Crewship instance.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_URL="${CREWSHIP_BASE_URL:-${BASE_URL:-http://localhost:8080}}"
TOKEN="${CREWSHIP_TOKEN:-${API_TOKEN:-}}"
WORKSPACE="${CREWSHIP_WORKSPACE:-${WORKSPACE_ID:-}}"
PHASE="${1:-positive}"

BASE_URL="${BASE_URL%/}"
SCHEMA_URL="${BASE_URL}/openapi.json"

die() { printf 'api-contract: %s\n' "$*" >&2; exit 2; }

case "$PHASE" in
  positive|stateful|auth) ;;
  *) die "usage: $0 {positive|auth|stateful}" ;;
esac

command -v curl >/dev/null 2>&1 || die "curl is required"

spec_status="$(curl --silent --show-error --location --max-time 10 \
  --output /dev/null --write-out '%{http_code}' "$SCHEMA_URL" 2>/dev/null)" \
  || die "cannot reach $SCHEMA_URL"
[[ "$spec_status" == 200 ]] || die "$SCHEMA_URL returned HTTP $spec_status"

if [[ "$PHASE" == auth ]]; then
  bad_token=invalid
  status_without_auth="$(curl --silent --show-error --max-time 10 \
    --output /dev/null --write-out '%{http_code}' \
    "$BASE_URL/api/v1/workspaces" 2>/dev/null)" \
    || die "cannot reach authenticated API"
  [[ "$status_without_auth" == 401 ]] \
    || die "expected unauthenticated /api/v1/workspaces to return 401, got $status_without_auth"

  status_with_bad_auth="$(curl --silent --show-error --max-time 10 \
    --header "Authorization: Bearer $bad_token" \
    --output /dev/null --write-out '%{http_code}' \
    "$BASE_URL/api/v1/workspaces" 2>/dev/null)" \
    || die "cannot reach invalid-token check"
  [[ "$status_with_bad_auth" == 401 ]] \
    || die "expected invalid bearer token to return 401, got $status_with_bad_auth"

  [[ -n "$TOKEN" && -n "$WORKSPACE" ]] \
    || die "auth phase needs CREWSHIP_TOKEN/API_TOKEN and CREWSHIP_WORKSPACE/WORKSPACE_ID for the positive auth check"
  status_with_auth="$(curl --silent --show-error --max-time 10 \
    --header "Authorization: Bearer $TOKEN" \
    --header "X-Workspace-ID: $WORKSPACE" \
    --output /dev/null --write-out '%{http_code}' \
    "$BASE_URL/api/v1/workspaces" 2>/dev/null)" \
    || die "cannot reach valid-token check"
  [[ "$status_with_auth" == 200 ]] \
    || die "expected valid bearer token to list workspaces with HTTP 200, got $status_with_auth"

  printf 'api-contract: auth checks passed against %s\n' "$BASE_URL"
  exit 0
fi

[[ -n "$TOKEN" ]] || die "positive/stateful phase needs CREWSHIP_TOKEN or API_TOKEN"
[[ -n "$WORKSPACE" ]] || die "positive/stateful phase needs CREWSHIP_WORKSPACE or WORKSPACE_ID"
command -v schemathesis >/dev/null 2>&1 \
  || die "schemathesis is required; install requirements.txt or use uv run --with-requirements requirements.txt"

phase_args=(--phases=examples)
[[ "$PHASE" == stateful ]] && phase_args=(--phases=stateful)

# The generated route catalog includes mutating operations. Keep this list
# explicit at the call site so a future config change cannot silently broaden
# the default live test scope.
safe_method_args=(
  --exclude-method POST
  --exclude-method PUT
  --exclude-method PATCH
  --exclude-method DELETE
)

exec schemathesis --config-file "$SCRIPT_DIR/schemathesis.toml" run \
  "$SCHEMA_URL" "${phase_args[@]}" "${safe_method_args[@]}"

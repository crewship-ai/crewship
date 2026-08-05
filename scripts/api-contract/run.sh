#!/usr/bin/env bash
# Safe, opt-in live contract checks for an already-running Crewship instance.
#
# `-e` is deliberately ABSENT (#1769 review). Every failure path here has to
# reach emit_summary so the run leaves a machine-readable verdict behind — a
# contract check that dies without recording why is indistinguishable from one
# that never ran, which is the failure mode the stage gauntlet already has.
# Errors are therefore routed explicitly: `|| die` / `|| fail <class>` at each
# fallible step, the schemathesis exit code captured into $rc and re-raised at
# the end, and a trap on EXIT that writes the summary either way.
#
# If you add a step, guard it the same way. Turning `-e` on instead would make
# the script exit before the trap can classify the failure.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_URL="${CREWSHIP_BASE_URL:-${BASE_URL:-http://localhost:8080}}"
TOKEN="${CREWSHIP_TOKEN:-${API_TOKEN:-}}"
WORKSPACE="${CREWSHIP_WORKSPACE:-${WORKSPACE_ID:-}}"
PHASE="${1:-positive}"
RUN_DIR=""
SCHEMA_FILE=""
JUNIT_FILE=""
RUN_LOG=""
FAILURE_CLASS=""
FAILURE_MESSAGE=""

BASE_URL="${BASE_URL%/}"
SCHEMA_URL="${BASE_URL}/openapi.json"

fail() {
  FAILURE_CLASS="${1:-runtime}"
  shift
  FAILURE_MESSAGE="$*"
  printf 'api-contract: %s\n' "$FAILURE_MESSAGE" >&2
  exit 2
}

die() { fail runtime "$@"; }

emit_summary() {
  local rc=$?
  [[ -n "$RUN_DIR" ]] || return 0
  python3 "$SCRIPT_DIR/summary.py" \
    --phase "$PHASE" \
    --exit-code "$rc" \
    --failure-class "$FAILURE_CLASS" \
    --failure-message "$FAILURE_MESSAGE" \
    --schema-file "$SCHEMA_FILE" \
    --junit-file "$JUNIT_FILE" \
    --run-log "$RUN_LOG" \
    --catalog-count "$CATALOG_COUNT" \
    --selected-count "$SELECTED_COUNT" \
    --excluded-auth-count "$EXCLUDED_AUTH_COUNT" \
    --excluded-non-json-count "$EXCLUDED_NON_JSON_COUNT" \
    --excluded-method-count "$EXCLUDED_METHOD_COUNT"
  rm -rf "$RUN_DIR"
}

trap emit_summary EXIT

case "$PHASE" in
  positive|stateful|auth) ;;
  *) die "usage: $0 {positive|auth|stateful}" ;;
esac

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v jq >/dev/null 2>&1 || die "jq is required"
command -v python3 >/dev/null 2>&1 || die "python3 is required"

RUN_DIR="$(mktemp -d "${TMPDIR:-/tmp}/crewship-api-contract.XXXXXX")" \
  || die "cannot create a temporary run directory"
SCHEMA_FILE="$RUN_DIR/openapi.json"
JUNIT_FILE="$RUN_DIR/junit.xml"
RUN_LOG="$RUN_DIR/schemathesis.log"
CATALOG_COUNT=0
SELECTED_COUNT=0
EXCLUDED_AUTH_COUNT=0
EXCLUDED_NON_JSON_COUNT=0
EXCLUDED_METHOD_COUNT=0

AUTH_UI_PATH_REGEX='^/api/auth(/|$)'
# These handlers deliberately return bytes, SVG, ZIP/Zstandard, Markdown, or
# a never-ending event stream. The generated catalog only has generic JSON
# placeholders, so probing them would report a schema failure for the wrong
# reason. Keep this list explicit and review it when a new non-JSON route is
# added.
NON_JSON_PATH_REGEX='^/api/v1/(admin/backups/download|agents/[^/]+/avatar|agents/[^/]+/files/download|crews/[^/]+/files/download|users/[^/]+/avatar|workspaces/[^/]+/pipelines/[^/]+/export|memory/(export|versions/[^/]+/content)|journal/stream)$'

count_operations() {
  jq -r --arg auth "$AUTH_UI_PATH_REGEX" --arg nonjson "$NON_JSON_PATH_REGEX" '
    def operations: [.paths | to_entries[] as $path | $path.value | to_entries[]
      | select(.key | IN("get", "head", "options", "trace", "post", "put", "patch", "delete"))
      | {path: $path.key, method: .key}];
    operations as $ops |
    [
      ($ops | length),
      ($ops | map(select(.method | IN("post", "put", "patch", "delete"))) | length),
      ($ops | map(select(.path | test($auth))) | length),
      ($ops | map(select(.path | test($nonjson))) | length),
      ($ops | map(select((.method | IN("post", "put", "patch", "delete")) or (.path | test($auth)) or (.path | test($nonjson)))) | length)
    ] | @tsv
  ' "$SCHEMA_FILE"
}

curl --silent --show-error --location --fail --max-time 10 \
  --output "$SCHEMA_FILE" "$SCHEMA_URL" 2>/dev/null \
  || die "cannot fetch $SCHEMA_URL"
jq -e '(.openapi // .swagger) and (.paths | type == "object")' "$SCHEMA_FILE" >/dev/null \
  || fail schema "$SCHEMA_URL is not a valid OpenAPI JSON document"
read -r CATALOG_COUNT EXCLUDED_METHOD_COUNT EXCLUDED_AUTH_COUNT EXCLUDED_NON_JSON_COUNT SELECTED_COUNT \
  <<<"$(count_operations)" \
  || fail schema "cannot count operations in $SCHEMA_URL"

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

  printf 'api-contract: auth checks passed against %s\n' "$BASE_URL" >&2
  exit 0
fi

[[ -n "$TOKEN" ]] || die "positive/stateful phase needs CREWSHIP_TOKEN or API_TOKEN"
[[ -n "$WORKSPACE" ]] || die "positive/stateful phase needs CREWSHIP_WORKSPACE or WORKSPACE_ID"
command -v schemathesis >/dev/null 2>&1 \
  || die "schemathesis is required; install requirements.txt or use uv run --with-requirements requirements.txt"

# schemathesis.toml intentionally uses the canonical names. Export the
# resolved aliases so BASE_URL/API_TOKEN/WORKSPACE_ID behave exactly like the
# documented CREWSHIP_* variables.
export CREWSHIP_BASE_URL="$BASE_URL"
export CREWSHIP_TOKEN="$TOKEN"
export CREWSHIP_WORKSPACE="$WORKSPACE"

phase_args=(--phases=coverage)
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

scope_args=(
  --exclude-path-regex "$AUTH_UI_PATH_REGEX"
  --exclude-path-regex "$NON_JSON_PATH_REGEX"
)

# Keep the output contract small and bounded. The full Schemathesis log stays
# in the temporary directory only long enough for summary.py to classify it.
schemathesis --config-file "$SCRIPT_DIR/schemathesis.toml" run \
  "$SCHEMA_FILE" "${phase_args[@]}" "${safe_method_args[@]}" "${scope_args[@]}" \
  --max-examples 10 \
  --report junit --report-junit-path "$JUNIT_FILE" \
  --output-sanitize true >"$RUN_LOG" 2>&1
rc=$?
if [[ "$rc" -eq 2 ]]; then
  FAILURE_CLASS=schema
fi
[[ "$rc" -eq 0 ]] || {
  tail -n 8 "$RUN_LOG" >&2
  exit "$rc"
}
exit 0

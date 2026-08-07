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
ARTIFACT_DIR="${API_CONTRACT_ARTIFACT_DIR:-}"

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
  if [[ -n "$ARTIFACT_DIR" ]]; then
    mkdir -p "$ARTIFACT_DIR" || true
  fi
  summary_args=(
    --phase "$PHASE"
    --exit-code "$rc"
    --failure-class "$FAILURE_CLASS"
    --failure-message "$FAILURE_MESSAGE"
    --schema-file "$SCHEMA_FILE"
    --junit-file "$JUNIT_FILE"
    --run-log "$RUN_LOG"
    --catalog-count "$CATALOG_COUNT"
    --selected-count "$SELECTED_COUNT"
    --excluded-auth-count "$EXCLUDED_AUTH_COUNT"
    --excluded-non-json-count "$EXCLUDED_NON_JSON_COUNT"
    --excluded-method-count "$EXCLUDED_METHOD_COUNT"
  )
  if [[ -n "$ARTIFACT_DIR" ]]; then
    python3 "$SCRIPT_DIR/summary.py" "${summary_args[@]}" \
      | tee "$ARTIFACT_DIR/${PHASE}-summary.json"
  else
    python3 "$SCRIPT_DIR/summary.py" "${summary_args[@]}"
  fi
  if [[ -n "$ARTIFACT_DIR" ]]; then
    # Keep the exact inputs needed to reproduce a failure. The schema and
    # Schemathesis output are sanitized/contract metadata, not credentials.
    cp "$SCHEMA_FILE" "$ARTIFACT_DIR/${PHASE}-openapi.json" 2>/dev/null || true
    cp "$JUNIT_FILE" "$ARTIFACT_DIR/${PHASE}-junit.xml" 2>/dev/null || true
    cp "$RUN_LOG" "$ARTIFACT_DIR/${PHASE}-schemathesis.log" 2>/dev/null || true
  fi
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

# Client-side pacing, at the call site for the same reason the method
# deny-list is: a live instance must not be out-run by its own contract
# check. The default matches the server's shipped `http.api_per_min`
# (120), so a run against a real instance stays under that limiter
# instead of collecting 429s — which Schemathesis reports as contract
# failures for operations that are in fact fine.
#
# `off` (or `none`) drops the throttle entirely. That is correct ONLY
# against an instance whose own limiter is off — CI's ephemeral,
# single-client server boots with CREWSHIP_RATELIMIT_DISABLED for
# exactly this. Anywhere else it buys 429s, not speed.
#
# Why it is worth having: with 305 selected operations x
# --max-examples 10, 120/m is a hard ~25-minute floor per run, one that
# grows with every route we add and that the PR job's 30-minute budget
# cannot absorb (#1813). The throttle is protecting a throwaway server
# from its only client.
RATE_LIMIT="${API_CONTRACT_RATE_LIMIT:-120/m}"
rate_limit_args=(--rate-limit "$RATE_LIMIT")
case "$RATE_LIMIT" in
  off | none) rate_limit_args=() ;;
esac

# Deadline for the Schemathesis process itself. Unset by default: a local
# or nightly run takes whatever time it takes.
#
# CI sets it so THIS script owns the kill. A job-level `timeout-minutes`
# reap reports the job as `cancelled` (indistinguishable from someone
# pressing stop), fells the process before the EXIT trap can classify
# anything, and leaves no summary artifact behind — which is precisely
# the "a check that dies without recording why" failure mode this
# runner is built to avoid.
DEADLINE="${API_CONTRACT_TIMEOUT:-}"
deadline_args=()
if [[ -n "$DEADLINE" ]]; then
  command -v timeout >/dev/null 2>&1 \
    || die "API_CONTRACT_TIMEOUT=$DEADLINE is set but coreutils 'timeout' is not on PATH (macOS: brew install coreutils, or unset the variable)"
  deadline_args=(timeout "$DEADLINE")
fi

# `${arr[@]+"${arr[@]}"}` rather than a bare `"${arr[@]}"`: these two
# arrays can be empty, and bash 3.2 (what macOS ships) treats an empty
# array expansion as an unbound variable under `set -u`.
#
# Keep the output contract small and bounded. The full Schemathesis log stays
# in the temporary directory only long enough for summary.py to classify it.
${deadline_args[@]+"${deadline_args[@]}"} \
  schemathesis --config-file "$SCRIPT_DIR/schemathesis.toml" run \
  "$SCHEMA_FILE" "${phase_args[@]}" "${safe_method_args[@]}" "${scope_args[@]}" \
  ${rate_limit_args[@]+"${rate_limit_args[@]}"} \
  --max-examples 10 \
  --report junit --report-junit-path "$JUNIT_FILE" \
  --output-sanitize true >"$RUN_LOG" 2>&1
rc=$?
if [[ -n "$DEADLINE" && "$rc" -eq 124 ]]; then
  tail -n 8 "$RUN_LOG" >&2
  fail runtime "schemathesis exceeded API_CONTRACT_TIMEOUT=$DEADLINE on the $PHASE phase ($SELECTED_COUNT operations selected, rate limit ${RATE_LIMIT})"
fi
if [[ "$rc" -eq 2 ]]; then
  FAILURE_CLASS=schema
fi
[[ "$rc" -eq 0 ]] || {
  tail -n 8 "$RUN_LOG" >&2
  exit "$rc"
}
exit 0

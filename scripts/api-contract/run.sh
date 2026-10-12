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
# Set when the script deliberately exits 0 on a run that did not pass, so
# the summary still records the real verdict instead of the exit code the
# caller sees. Only advisory mode sets it (see the exit path below).
SUMMARY_EXIT=""
ADVISORY_ARGS=()

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
  # An advisory run exits 0 on purpose. The artifact must not inherit that
  # and read "passed" — 227 graded operations with findings is a failed
  # phase that was excused, and the record has to say both.
  [[ -n "$SUMMARY_EXIT" ]] && rc="$SUMMARY_EXIT"
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
    ${ADVISORY_ARGS[@]+"${ADVISORY_ARGS[@]}"}
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
#
# The entry test is narrow, because a route that merely LOOKS non-JSON is
# where this list does damage (#1815): exclude a path only when the generated
# document already DECLARES its non-JSON media type, i.e. the bytes are the
# intended contract and only the placeholder schema under them is wrong. A
# route that emits a media type the document does NOT declare is a genuine
# contract violation and has to stay in scope — `GET /api/v1/oauth/callback`
# answers its 4xx branches with `http.Error`'s text/plain while the generator
# documents every error response as application/json, and that is the finding
# doing its job, not a false positive to be silenced here. Likewise, an
# undocumented STATUS CODE on an otherwise-binary route (the issue-attachment
# download) is a real finding about statuses, not about media.
#
# Three entries were stale when #1815 re-measured the gate — the whole list
# was written in one pass in #1769 by reading route names, and route names is
# exactly what it got wrong:
#
#   - `memory/versions/[^/]+/content` matched NO path in the shipped
#     document. The route it was written for is the admin one, which has
#     carried its `admin/` prefix since #414 — so the real endpoint was
#     probed as JSON on every run and answered 5xx, while the entry meant to
#     cover it quietly matched nothing;
#   - `chats/[^/]+/stream` and `memory/versions/[^/]+` were never entered.
#     The first is the NDJSON run stream added by #1822, the same
#     never-ending-stream case as `journal/stream` beside it (and with
#     `follow=true` it burns a full request-timeout per generated example);
#     the second returns raw version bytes as application/octet-stream;
#   - `workspaces/[^/]+/pipelines/[^/]+/export` was stale in the direction
#     that costs findings. It is not a download:
#     `PipelineHandler.ExportPipeline` ends in `writeJSON`, and the document
#     declares it `application/json`
#     with a real `WorkspacePipelineExportResponseV1` schema rather than a
#     placeholder. Sharing the word "export" with `memory/export` (a ZIP) is
#     the whole of the resemblance. Removed — it is an ordinary JSON route
#     and the gate should probe it.
#
# One pattern per route, relative to `/api/v1/`, with the media type that
# earns it the entry. scripts/api-contract-gate-test.sh reads this array back
# out of this file and checks every entry against the shipped
# internal/api/openapi.gen.json, both ways: an entry matching no path fails by
# name, and so does an entry whose paths declare nothing but application/json.
# That is the prose criterion above turned into a check, because the prose on
# its own is what rotted.
NON_JSON_PATH_PATTERNS=(
  'admin/backups/download'              # application/zstd
  'admin/memory/versions/[^/]+/content' # text/markdown, application/octet-stream
  'agents/[^/]+/avatar'                 # image/svg+xml
  'agents/[^/]+/files/download'          # application/octet-stream
  'chats/[^/]+/stream'                  # application/x-ndjson, never-ending
  'crews/[^/]+/files/download'          # application/octet-stream
  # Only the id-addressed GET is binary. /users/me/avatar's POST and DELETE
  # both answer application/json — they end in writeProfile — so they are
  # ordinary JSON routes and belong in the probe. The old `[^/]+` swallowed
  # them, and the gate's own exclusion audit caught it the moment the document
  # stopped mislabelling them as image/svg+xml.
  'users/(?!me/)[^/]+/avatar'           # image/{svg+xml,png,jpeg,webp}
  'memory/export'                       # application/zip
  'memory/versions/[^/]+'               # application/octet-stream
  'journal/stream'                      # text/event-stream, never-ending
)
NON_JSON_PATH_REGEX=''
for _non_json_pattern in "${NON_JSON_PATH_PATTERNS[@]}"; do
  NON_JSON_PATH_REGEX+="${NON_JSON_PATH_REGEX:+|}$_non_json_pattern"
done
unset _non_json_pattern
NON_JSON_PATH_REGEX="^/api/v1/($NON_JSON_PATH_REGEX)\$"

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
      # SELECTED is the COMPLEMENT of the exclusions, not their union. It
      # used to be the union: 536 in the catalog, 305 excluded, and the
      # summary reported `"selected": 305` while Schemathesis reported 231
      # for the same invocation — overstating what was probed by 74
      # operations, in the one artifact a reviewer trusts (#1815).
      #
      # Note the three exclusion buckets OVERLAP (a non-JSON download is
      # usually also a GET, an /api/auth route can be mutating), so
      # catalog - methods - auth_ui - non_json does NOT reconstruct this
      # number. Only the complement does.
      ($ops | map(select(((.method | IN("post", "put", "patch", "delete")) or (.path | test($auth)) or (.path | test($nonjson))) | not)) | length)
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

# Do not grade negatives that Schemathesis builds by removing a security
# parameter. They were 151 of this gate's 267 findings — 57% of the backlog
# behind #1815 — and every one of them was invented.
#
# 525 of 538 operations declare three ALTERNATIVE security requirement
# objects (`bearerAuth` | `sessionCookie` | `secureSessionCookie`), which is
# OR and is a correct description of an API accepting either a bearer token
# or a session cookie. schemathesis.toml supplies our credential as a raw
# `Authorization` header, though, so Schemathesis cannot connect it to
# `bearerAuth`; it drops `__Secure-authjs.session-token`, expects a 4xx, and
# gets 200 from a request that still carries the bearer token it does not
# know about. That is a gap in what the tool can see, not in what the API
# does — the spec is right and the server is right.
#
# It costs no coverage. Unauthenticated and invalid-token behaviour belongs
# to the `auth` phase, which returns above without ever invoking
# Schemathesis. Asserted in scripts/api-contract-gate-test.sh so the flag
# cannot be dropped without a named failure.
security_negative_args=(--generation-with-security-parameters false)

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

# Fixture-correlated operation parameters (#1815 bucket 3).
#
# Schemathesis generates path parameters from the schema alone, so the gate
# pairs ids the fixture never bound (a crew from one listing with an
# integration from another: AC1815-184's designed 404) and grades the
# restricted-workflow routes as the OWNER, who is always the designed 404.
# Those are findings about the fixture, not the product. CI resolves real,
# correlated values with fixture_probes.py / restricted_fixture.py and hands
# them here as FILES — optional, so a live or nightly run without a fixture is
# byte-for-byte unchanged:
#
#   API_CONTRACT_PAIR_FILE              {"crew_id","integration_id"}: the
#       fixture-BOUND pair, applied to the one tools operation only (never a
#       global substitution — every other crew/integration route keeps its
#       generated ids);
#   API_CONTRACT_RESTRICTED_TOKEN_FILE  the restricted member's own token: the
#       three restricted-workflow operations are graded as that member, the
#       only actor that can reach their success branch.
#
# The overlay is appended to a COPY of schemathesis.toml in the run
# directory. Secrets are referenced as ${CREWSHIP_*} interpolations, never
# written into the file.
CONFIG_FILE="$SCRIPT_DIR/schemathesis.toml"
PAIR_FILE="${API_CONTRACT_PAIR_FILE:-}"
RESTRICTED_TOKEN_FILE="${API_CONTRACT_RESTRICTED_TOKEN_FILE:-}"
if [[ -n "$PAIR_FILE" || -n "$RESTRICTED_TOKEN_FILE" ]]; then
  CONFIG_FILE="$RUN_DIR/schemathesis.toml"
  cp "$SCRIPT_DIR/schemathesis.toml" "$CONFIG_FILE"
fi
if [[ -n "$PAIR_FILE" ]]; then
  [[ -s "$PAIR_FILE" ]] || die "API_CONTRACT_PAIR_FILE=$PAIR_FILE is missing or empty"
  pair_crew="$(jq -er '.crew_id | select(type == "string" and test("^[A-Za-z0-9_-]+$"))' "$PAIR_FILE")" \
    || die "API_CONTRACT_PAIR_FILE needs a safe crew_id string"
  pair_integration="$(jq -er '.integration_id | select(type == "string" and test("^[A-Za-z0-9_-]+$"))' "$PAIR_FILE")" \
    || die "API_CONTRACT_PAIR_FILE needs a safe integration_id string"
  {
    printf '\n[[operations]]\n'
    printf 'include-path = "/api/v1/crews/{crewId}/integrations/{integrationId}/tools"\n'
    printf 'parameters = { crewId = "%s", integrationId = "%s" }\n' "$pair_crew" "$pair_integration"
  } >>"$CONFIG_FILE"
fi
if [[ -n "$RESTRICTED_TOKEN_FILE" ]]; then
  [[ -s "$RESTRICTED_TOKEN_FILE" ]] || die "API_CONTRACT_RESTRICTED_TOKEN_FILE=$RESTRICTED_TOKEN_FILE is missing or empty"
  export CREWSHIP_RESTRICTED_TOKEN
  CREWSHIP_RESTRICTED_TOKEN="$(tr -d '[:space:]' <"$RESTRICTED_TOKEN_FILE")"
  [[ "$WORKSPACE" =~ ^[A-Za-z0-9_-]+$ ]] || die "restricted overlay needs a workspace ID/slug of [A-Za-z0-9_-]"
  # One section per EXACT path, with ONE selector each. Do not add
  # `include-method` beside `include-path`: Schemathesis registers each
  # include-* key with a separate include() call, which is an OR, so a
  # section with both matched EVERY GET operation — measured on a live run:
  # 485 requests of unrelated operations carried the restricted token, the
  # Pages slug harvest broke and findings went 91 -> 209.
  for restricted_path in \
    "/api/v1/workspaces/{workspaceId}/restricted-routines" \
    "/api/v1/workspaces/{workspaceId}/restricted-routine-runs" \
    "/api/v1/workspaces/{workspaceId}/restricted-routine-runs/{runId}"; do
    {
      printf '\n[[operations]]\n'
      printf 'include-path = "%s"\n' "$restricted_path"
      # shellcheck disable=SC2016 # literal ${…}: Schemathesis interpolates at run time; the token must never be expanded into the file
      printf 'headers = { Authorization = "Bearer ${CREWSHIP_RESTRICTED_TOKEN}", "X-Workspace-ID" = "${CREWSHIP_WORKSPACE}" }\n'
      printf 'parameters = { workspaceId = "%s" }\n' "$WORKSPACE"
    } >>"$CONFIG_FILE"
  done
fi

# Pin the correlated ids in the schema Schemathesis GENERATES from, not only
# in the config. Measured: a config `parameters` pin loses to ids Schemathesis
# harvests from other operations' responses — /workspaces lists the
# restricted member's own signup workspace, and the catalog route was graded
# on that foreign id. A single-value enum on the path parameter cannot be
# outvoted. The pinned copy feeds Schemathesis only; $SCHEMA_FILE (counted
# and archived as evidence) stays exactly what the server published.
SCHEMA_RUN_FILE="$SCHEMA_FILE"
if [[ -n "$PAIR_FILE" || -n "$RESTRICTED_TOKEN_FILE" ]]; then
  SCHEMA_RUN_FILE="$RUN_DIR/openapi-pinned.json"
  pin_filter='def pin($path; $name; $val): if .paths[$path].get then .paths[$path].get.parameters |= map(if .in == "path" and .name == $name then .schema = {type: "string", enum: [$val]} else . end) else . end; .'
  if [[ -n "$PAIR_FILE" ]]; then
    pin_filter+=' | pin("/api/v1/crews/{crewId}/integrations/{integrationId}/tools"; "crewId"; $crew) | pin("/api/v1/crews/{crewId}/integrations/{integrationId}/tools"; "integrationId"; $integration)'
  fi
  if [[ -n "$RESTRICTED_TOKEN_FILE" ]]; then
    for restricted_path in \
      "/api/v1/workspaces/{workspaceId}/restricted-routines" \
      "/api/v1/workspaces/{workspaceId}/restricted-routine-runs" \
      "/api/v1/workspaces/{workspaceId}/restricted-routine-runs/{runId}"; do
      pin_filter+=" | pin(\"$restricted_path\"; \"workspaceId\"; \$ws)"
    done
  fi
  jq --arg crew "${pair_crew:-}" --arg integration "${pair_integration:-}" --arg ws "$WORKSPACE" "$pin_filter" "$SCHEMA_FILE" >"$SCHEMA_RUN_FILE" \
    || die "cannot pin the fixture ids into the schema copy"
fi

# `${arr[@]+"${arr[@]}"}` rather than a bare `"${arr[@]}"`: these two
# arrays can be empty, and bash 3.2 (what macOS ships) treats an empty
# array expansion as an unbound variable under `set -u`.
#
# Keep the output contract small and bounded. The full Schemathesis log stays
# in the temporary directory only long enough for summary.py to classify it.
${deadline_args[@]+"${deadline_args[@]}"} \
  schemathesis --config-file "$CONFIG_FILE" run \
  "$SCHEMA_RUN_FILE" "${phase_args[@]}" "${safe_method_args[@]}" "${scope_args[@]}" \
  "${security_negative_args[@]}" \
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

# How many operations did this run actually grade, and how many did it
# grade badly? Both come from the JUnit report Schemathesis just wrote,
# via summary.py so there is one implementation of "what counts as a
# finding" rather than two that drift.
GRADED_COUNT=0
FINDINGS_COUNT=0
read -r GRADED_COUNT FINDINGS_COUNT \
  <<<"$(python3 "$SCRIPT_DIR/summary.py" --count-junit "$JUNIT_FILE" 2>/dev/null)" \
  || true
[[ "$GRADED_COUNT" =~ ^[0-9]+$ ]] || GRADED_COUNT=0
[[ "$FINDINGS_COUNT" =~ ^[0-9]+$ ]] || FINDINGS_COUNT=0

# Advisory mode (API_CONTRACT_ADVISORY): the phase reports its findings
# and does not fail the caller. It exists for one situation — a body of
# pre-existing findings that predates the changes being gated, where
# blocking would punish the wrong PRs (#1815) — and it is deliberately
# narrow:
#
#   - it excuses FINDINGS ONLY. Schemathesis exit 1 *and* a JUnit report
#     showing operations were graded is the only shape that qualifies;
#   - a schema/config abort (exit 2), a blown deadline (124), a crash, an
#     unreachable target, and every `die` path above stay fatal. Those
#     mean the gate did not run, which advisory mode says nothing about;
#   - exit 1 with nothing graded is the same thing wearing a findings
#     exit code, and is treated as "did not run";
#   - the auth phase never reaches here, so it is never advisory.
#
# The step is NOT marked continue-on-error for the same reason: that
# would excuse all of the above too, which is how a gate quietly stops
# being one. The distinction is asserted in
# scripts/api-contract-gate-test.sh.
ADVISORY="${API_CONTRACT_ADVISORY:-}"
if [[ -n "$ADVISORY" ]]; then
  ADVISORY_ARGS=(--advisory)
fi

if [[ -n "$ADVISORY" && "$rc" -eq 1 && "$GRADED_COUNT" -gt 0 && "$FINDINGS_COUNT" -gt 0 ]]; then
  tail -n 8 "$RUN_LOG" >&2
  printf 'api-contract: ADVISORY — the %s phase graded %s operations and reported %s finding(s); NOT failing the job (#1815). Evidence: %s-summary.json / %s-junit.xml\n' \
    "$PHASE" "$GRADED_COUNT" "$FINDINGS_COUNT" "$PHASE" "$PHASE" >&2
  if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
    printf '**API contract (%s phase, advisory):** %s finding(s) across %s graded operations — not failing the job while #1815 is open.\n' \
      "$PHASE" "$FINDINGS_COUNT" "$GRADED_COUNT" >>"$GITHUB_STEP_SUMMARY"
  fi
  SUMMARY_EXIT="$rc"
  exit 0
fi

[[ "$rc" -eq 0 ]] || {
  tail -n 8 "$RUN_LOG" >&2
  exit "$rc"
}
exit 0

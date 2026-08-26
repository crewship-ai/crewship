#!/usr/bin/env bash
# api-contract-gate-test.sh — tests for the two halves of the per-PR API
# contract gate: the budget arithmetic encoded in .github/workflows/ci.yml,
# and the pacing/deadline flags scripts/api-contract/run.sh builds.
#
# Why this file exists (#1813): the gate was wired by #1784 with a
# 120 req/min client-side throttle against a server whose own limiter was
# also 120/min. 305 selected operations x --max-examples 10 is a ~25-minute
# floor on that pacing — inside a 30-minute job that also builds, boots,
# seeds and runs five harness suites. It could not finish, and the way it
# failed hid that: a `timeout-minutes` reap is reported as `cancelled`, so
# two dead runs looked like somebody had pressed stop.
#
# The fix has three moving parts that only work together, which is exactly
# the kind of thing that rots silently:
#
#   1. the ephemeral CI server boots with CREWSHIP_RATELIMIT_DISABLED,
#   2. the gate drops its client-side throttle (API_CONTRACT_RATE_LIMIT=off),
#   3. run.sh owns an inner deadline so IT reports the failure, not the job.
#
# Drop (1) and keep (2) and the gate collects 429s reported as contract
# failures. Keep (1) and drop (3) and the next slow run is `cancelled`
# again with no artifact. So the pairing and the ordering are asserted
# here, not just described in a comment.
#
# Known limit, stated rather than discovered later: this verifies the
# numbers and flags the workflow WOULD use, not that the job runs, and not
# that the contract phase passes. Whether the gate is green is the gate's
# job; whether it can finish and say so is this file's.
#
# Usage: bash scripts/api-contract-gate-test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
WORKFLOW="$REPO_ROOT/.github/workflows/ci.yml"
RUNNER="$REPO_ROOT/scripts/api-contract/run.sh"

FAILURES=0
pass() { printf '  ok   %s\n' "$1"; }
fail() {
  printf '  FAIL %s\n' "$1"
  [[ $# -gt 1 ]] && printf '       %s\n' "$2"
  FAILURES=$((FAILURES + 1))
}

TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/crewship-api-contract-gate-test.XXXXXX")" || exit 1
SERVER_PID=""
cleanup() {
  if [[ -n "$SERVER_PID" ]]; then
    # Braces + redirect: bash otherwise reports the reaped background job
    # ("Terminated: 15") on the test's own stderr.
    { kill "$SERVER_PID" && wait "$SERVER_PID"; } 2>/dev/null
  fi
  rm -rf "$TMP_ROOT"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Part 1 — the workflow's budget arithmetic
# ---------------------------------------------------------------------------

[[ -f "$WORKFLOW" ]] || {
  echo "FATAL: $WORKFLOW not found" >&2
  exit 1
}

# Body of the harness-pr job: its key down to the next job at the same indent.
harness_pr_job() {
  awk '
    /^  harness-pr:[[:space:]]*$/ { inside = 1; print; next }
    inside && /^  [A-Za-z0-9_-]+:[[:space:]]*$/ { inside = 0 }
    inside { print }
  ' "$WORKFLOW"
}

# Body of one step of that job, selected by its `- name:` value.
job_step() {
  printf '%s\n' "$JOB_SRC" | awk -v want="$1" '
    $0 ~ "^      - name: " want "[[:space:]]*$" { inside = 1; print; next }
    inside && /^      - (name|uses):/ { inside = 0 }
    inside { print }
  '
}

JOB_SRC="$(harness_pr_job)"
if [[ -z "$JOB_SRC" ]]; then
  echo "FATAL: could not find the 'harness-pr' job in $WORKFLOW" >&2
  echo "       (renamed? then this test is covering nothing — fix the name here)" >&2
  exit 1
fi

BOOT_STEP="$(job_step "Start ephemeral server")"
GATE_STEP="$(job_step "Run deterministic API contract gate")"
for pair in "Start ephemeral server:$BOOT_STEP" "Run deterministic API contract gate:$GATE_STEP"; do
  if [[ -z "${pair#*:}" ]]; then
    echo "FATAL: step '${pair%%:*}' not found in the harness-pr job" >&2
    echo "       (renamed or removed? this test then covers nothing)" >&2
    exit 1
  fi
done

# JOB_TIMEOUT is the job-level cap; STEP_TIMEOUT the gate step's own cap;
# DEADLINE the seconds run.sh gives Schemathesis.
JOB_TIMEOUT="$(printf '%s\n' "$JOB_SRC" | awk '/^    timeout-minutes:/ { print $2; exit }')"
STEP_TIMEOUT="$(printf '%s\n' "$GATE_STEP" | awk '/^        timeout-minutes:/ { print $2; exit }')"
DEADLINE="$(printf '%s\n' "$GATE_STEP" | sed -n 's/.*API_CONTRACT_TIMEOUT:[[:space:]]*"\{0,1\}\([0-9]\{1,\}\)"\{0,1\}.*/\1/p' | head -1)"

if [[ -n "$JOB_TIMEOUT" ]]; then
  pass "harness-pr declares a job timeout-minutes ($JOB_TIMEOUT)"
else
  fail "harness-pr declares a job timeout-minutes" "none found"
fi

if [[ -n "$STEP_TIMEOUT" ]]; then
  pass "the contract gate step has its own timeout-minutes ($STEP_TIMEOUT)"
else
  fail "the contract gate step has its own timeout-minutes" \
    "without one, a slow phase is reaped by the job budget and reported as 'cancelled'"
fi

if [[ -n "$DEADLINE" ]]; then
  pass "the contract gate step sets API_CONTRACT_TIMEOUT (${DEADLINE}s)"
else
  fail "the contract gate step sets API_CONTRACT_TIMEOUT" \
    "without it run.sh has no deadline of its own and cannot emit a named verdict"
fi

if [[ -n "$STEP_TIMEOUT" && -n "$JOB_TIMEOUT" ]]; then
  if [[ "$STEP_TIMEOUT" -lt "$JOB_TIMEOUT" ]]; then
    pass "step timeout ($STEP_TIMEOUT m) is below the job budget ($JOB_TIMEOUT m)"
  else
    fail "step timeout ($STEP_TIMEOUT m) is below the job budget ($JOB_TIMEOUT m)" \
      "the job would win the race and report 'cancelled' instead of a named step failure"
  fi
fi

if [[ -n "$DEADLINE" && -n "$STEP_TIMEOUT" ]]; then
  if [[ "$DEADLINE" -lt $((STEP_TIMEOUT * 60)) ]]; then
    pass "run.sh's deadline (${DEADLINE}s) is below the step cap ($((STEP_TIMEOUT * 60))s)"
  else
    fail "run.sh's deadline (${DEADLINE}s) is below the step cap ($((STEP_TIMEOUT * 60))s)" \
      "the step cap would fire first, losing the summary artifact run.sh writes on the way out"
  fi
fi

# Only an `env:` key has effect. The name also appears in prose here, and a
# check that counted mentions would be satisfied by a comment describing a
# setting nobody applies.
env_assignment='^[[:space:]]*CREWSHIP_(RATELIMIT_DISABLED|DISABLE_RATELIMIT):'

# The pairing. Removing the client-side throttle is only correct against an
# instance that has no limiter of its own; anywhere else it converts a slow
# run into 429s that Schemathesis reports as contract failures.
gate_throttle_off=0
printf '%s\n' "$GATE_STEP" | grep -qE 'API_CONTRACT_RATE_LIMIT:[[:space:]]*"?(off|none)"?' && gate_throttle_off=1
job_limiter_off=0
printf '%s\n' "$JOB_SRC" | grep -qE "$env_assignment" && job_limiter_off=1

if [[ "$gate_throttle_off" -eq 1 && "$job_limiter_off" -eq 0 ]]; then
  fail "API_CONTRACT_RATE_LIMIT=off is paired with a limiter-less server" \
    "the gate drops its throttle but the server still limits — that is a 429 storm, not a fast gate"
else
  pass "API_CONTRACT_RATE_LIMIT=off is paired with a limiter-less server"
fi

# The flag must reach the throwaway server process and nothing else: one
# SETTING across every workflow, inside the step that starts it. Not a
# job-level env, not $GITHUB_ENV (which would hand it to every later step
# and to anything they deploy).
wf_hits="$(grep -rEch "$env_assignment" "$REPO_ROOT"/.github/workflows/*.yml | awk '{ n += $1 } END { print n + 0 }')"
boot_hits="$(printf '%s\n' "$BOOT_STEP" | grep -cE "$env_assignment" || true)"
if [[ "$wf_hits" -eq "$boot_hits" && "$boot_hits" -eq 1 ]]; then
  pass "the limiter flag is set once, in the ephemeral server's own step env"
else
  fail "the limiter flag is set once, in the ephemeral server's own step env" \
    "found $wf_hits occurrence(s) across .github/workflows, $boot_hits of them in that step"
fi

if printf '%s\n' "$JOB_SRC" | grep -q 'CREWSHIP_RATELIMIT_DISABLED.*>>.*GITHUB_ENV'; then
  fail "the limiter flag is not exported to later steps" "written to \$GITHUB_ENV"
else
  pass "the limiter flag is not exported to later steps"
fi

# Nothing that ships or deploys may SET it. A commented example in
# .env.example is documentation and stays legal; an uncommented assignment
# anywhere in the shipping surface is not.
leak_surface=()
for path in "$REPO_ROOT/.env.example" "$REPO_ROOT/docker" "$REPO_ROOT/packaging" "$REPO_ROOT/config"; do
  [[ -e "$path" ]] && leak_surface+=("$path")
done
leaks=""
if [[ ${#leak_surface[@]} -gt 0 ]]; then
  leaks="$(grep -rEn '^[^#]*CREWSHIP_(RATELIMIT_DISABLED|DISABLE_RATELIMIT)[[:space:]]*[:=]' "${leak_surface[@]}" 2>/dev/null)"
fi
if [[ -z "$leaks" ]]; then
  pass "no shipping/deploy surface sets the limiter flag"
else
  fail "no shipping/deploy surface sets the limiter flag" "$leaks"
fi

# ---------------------------------------------------------------------------
# Part 1b — the advisory escape hatch, and its fence
# ---------------------------------------------------------------------------
# The `positive` phase does not block the job while #1815's pre-existing
# findings are outstanding. That is a deliberate, temporary exemption, and
# the ways it can rot are all cheap to assert:
#
#   - it must excuse FINDINGS only. `continue-on-error` on the step would
#     excuse a crashed server, an unloadable schema and a blown deadline
#     too, which is how a gate stops being a gate without anyone deciding
#     that it should;
#   - the reason must be readable where the exemption is, naming the issue
#     that ends it — otherwise the next reader finds a disabled gate with
#     no expiry;
#   - the evidence must survive. "Advisory" means "does not fail the job",
#     not "reports less".
gate_advisory=0
printf '%s\n' "$GATE_STEP" | grep -qE 'API_CONTRACT_ADVISORY:[[:space:]]*"?[^"[:space:]]+"?' && gate_advisory=1

if printf '%s\n' "$GATE_STEP" | grep -qE '^[[:space:]]*continue-on-error:[[:space:]]*true'; then
  fail "the gate step does not use continue-on-error" \
    "that swallows infrastructure failures too — advisory must come from run.sh classifying findings"
else
  pass "the gate step does not use continue-on-error"
fi

if [[ "$gate_advisory" -eq 1 ]]; then
  if printf '%s\n' "$JOB_SRC" | grep -q '#1815'; then
    pass "the advisory exemption names the issue that ends it (#1815)"
  else
    fail "the advisory exemption names the issue that ends it (#1815)" \
      "a gate that stopped gating must say why, and until when, where it is switched off"
  fi
else
  pass "the advisory exemption names the issue that ends it (#1815)"
fi

UPLOAD_STEP="$(printf '%s\n' "$JOB_SRC" | awk '
  /^      - name: Upload API contract evidence[[:space:]]*$/ { inside = 1; print; next }
  inside && /^      - (name|uses):/ { inside = 0 }
  inside { print }
')"
if [[ -n "$UPLOAD_STEP" ]] && printf '%s\n' "$UPLOAD_STEP" | grep -q 'if: always()'; then
  pass "the evidence upload still runs unconditionally"
else
  fail "the evidence upload still runs unconditionally" \
    "advisory means 'does not fail the job', not 'produces less evidence'"
fi

if printf '%s\n' "$GATE_STEP" | grep -q 'API_CONTRACT_ARTIFACT_DIR'; then
  pass "the gate still writes its artifacts"
else
  fail "the gate still writes its artifacts" "no API_CONTRACT_ARTIFACT_DIR on the step"
fi

# ---------------------------------------------------------------------------
# Part 2 — the flags run.sh actually builds
# ---------------------------------------------------------------------------
# A stub Schemathesis records its argv; a static file server stands in for
# /openapi.json. This exercises the real script, not a copy of its logic.

STUB_BIN="$TMP_ROOT/bin"
DOCROOT="$TMP_ROOT/docroot"
mkdir -p "$STUB_BIN" "$DOCROOT"
cat >"$DOCROOT/openapi.json" <<'JSON'
{"openapi":"3.0.3","paths":{"/api/v1/things":{"get":{"responses":{"200":{"description":"ok"}}}}}}
JSON

cat >"$STUB_BIN/schemathesis" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$@" >"$STUB_ARGV_FILE"
# Write a JUnit report where the real thing would. That report is how
# run.sh tells "graded N operations and found M things" from "never got
# far enough to grade anything" — the distinction advisory mode turns on.
junit=""
prev=""
for arg in "$@"; do
  [[ "$prev" == "--report-junit-path" ]] && junit="$arg"
  prev="$arg"
done
if [[ -n "$junit" && -n "${STUB_GRADED:-}" ]]; then
  {
    printf '<testsuites><testsuite name="stub">'
    i=0
    while [[ "$i" -lt "$STUB_GRADED" ]]; do
      if [[ "$i" -lt "${STUB_FINDINGS:-0}" ]]; then
        printf '<testcase name="GET /op%s"><failure message="Response violates schema">boom</failure></testcase>' "$i"
      else
        printf '<testcase name="GET /op%s"></testcase>' "$i"
      fi
      i=$((i + 1))
    done
    printf '</testsuite></testsuites>\n'
  } >"$junit"
fi
[[ -n "${STUB_SLEEP:-}" ]] && sleep "$STUB_SLEEP"
exit "${STUB_EXIT:-0}"
STUB
chmod +x "$STUB_BIN/schemathesis"

PORT=""
for candidate in $(seq 18310 18360); do
  if ! (exec 3<>/dev/tcp/127.0.0.1/"$candidate") 2>/dev/null; then
    PORT="$candidate"
    break
  fi
done
if [[ -z "$PORT" ]]; then
  echo "FATAL: no free port in 18310-18360 for the stub schema server" >&2
  exit 1
fi
python3 -m http.server "$PORT" --bind 127.0.0.1 --directory "$DOCROOT" >/dev/null 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$PORT/openapi.json" && break
  sleep 0.2
done

# Run run.sh with a stub Schemathesis; echo its exit code. Any
# API_CONTRACT_* override is passed in by the caller's environment.
RUNNER_PHASE=positive
run_runner() {
  local argv_file="$1"
  shift
  env PATH="$STUB_BIN:$PATH" \
    STUB_ARGV_FILE="$argv_file" \
    CREWSHIP_BASE_URL="http://127.0.0.1:$PORT" \
    CREWSHIP_TOKEN=stub-token \
    CREWSHIP_WORKSPACE=stub-workspace \
    "$@" \
    bash "$RUNNER" "$RUNNER_PHASE" >"$TMP_ROOT/runner.out" 2>"$TMP_ROOT/runner.err"
  echo $?
}

expect_argv() {
  local label="$1" want="$2" argv_file="$TMP_ROOT/argv-$RANDOM"
  shift 2
  run_runner "$argv_file" "$@" >/dev/null
  if [[ ! -f "$argv_file" ]]; then
    fail "$label" "schemathesis was never invoked; run.sh said: $(tail -3 "$TMP_ROOT/runner.err")"
    return
  fi
  if grep -qxF -- "$want" "$argv_file"; then
    pass "$label"
  else
    fail "$label" "argv had: $(tr '\n' ' ' <"$argv_file")"
  fi
}

expect_no_argv() {
  local label="$1" unwanted="$2" argv_file="$TMP_ROOT/argv-$RANDOM"
  shift 2
  run_runner "$argv_file" "$@" >/dev/null
  if [[ ! -f "$argv_file" ]]; then
    fail "$label" "schemathesis was never invoked; run.sh said: $(tail -3 "$TMP_ROOT/runner.err")"
    return
  fi
  if grep -qxF -- "$unwanted" "$argv_file"; then
    fail "$label" "argv had: $(tr '\n' ' ' <"$argv_file")"
  else
    pass "$label"
  fi
}

# Default pacing stays at the server's shipped http.api_per_min. A run
# against a live instance must not out-run its limiter just because CI
# does not have one.
expect_argv "default pacing is 120/m" "120/m"
expect_argv "default pacing passes --rate-limit" "--rate-limit"
expect_argv "an explicit rate limit is passed through" "45/m" API_CONTRACT_RATE_LIMIT=45/m
expect_no_argv "API_CONTRACT_RATE_LIMIT=off drops the throttle" "--rate-limit" API_CONTRACT_RATE_LIMIT=off
expect_no_argv "API_CONTRACT_RATE_LIMIT=none drops the throttle" "--rate-limit" API_CONTRACT_RATE_LIMIT=none

# Coverage is not a budget knob. Speed came from removing a throttle, not
# from probing each operation less — lowering this trades coverage on all
# 305 operations and is a deliberate decision, not a CI tuning move.
expect_argv "every operation still gets 10 examples" "10"
expect_argv "--max-examples is still passed" "--max-examples"

# Mutation safety is the reason this runner exists at all; the deny-list
# lives at the call site so no config change can widen the live scope.
for method in POST PUT PATCH DELETE; do
  expect_argv "mutating method $method is still excluded" "$method"
done

# Security-parameter negatives are not ours to grade, and left on they were
# 151 of the gate's 267 findings — 57% of the backlog behind #1815, all of it
# invented.
#
# Why they fire: 525 of 538 operations declare
#   security: [{bearerAuth}, {sessionCookie}, {secureSessionCookie}]
# — three ALTERNATIVE requirement objects, i.e. OR, which is a correct
# description of an API that takes either a bearer token or a session cookie.
# But schemathesis.toml supplies our credential as a raw `Authorization`
# header, so Schemathesis cannot connect it to `bearerAuth`. Its coverage
# phase then drops `__Secure-authjs.session-token`, expects a rejection, and
# gets 200 — because the bearer token it does not know about is still on the
# request. Measured: disabling this removes the whole "API accepted
# schema-violating request" class and leaves "Response violates schema" and
# "Undocumented Content-Type" unchanged to the finding.
#
# Nothing is lost by turning it off. Unauthenticated and invalid-token
# behaviour is the `auth` phase's job, and that phase never reaches
# Schemathesis at all — it exits after its own curl checks (see run.sh).
expect_argv "the phase does not invent security-parameter negatives" \
  "--generation-with-security-parameters"
expect_argv "...and passes false, not true" "false"

# The deadline: run.sh must stop Schemathesis itself and leave a named,
# machine-readable verdict — the thing a job-level reap destroys.
DEADLINE_ARGV="$TMP_ROOT/argv-deadline"
DEADLINE_ARTIFACTS="$TMP_ROOT/artifacts-deadline"
rc="$(run_runner "$DEADLINE_ARGV" \
  API_CONTRACT_TIMEOUT=1 \
  API_CONTRACT_ARTIFACT_DIR="$DEADLINE_ARTIFACTS" \
  STUB_SLEEP=20)"

if command -v timeout >/dev/null 2>&1; then
  if [[ "$rc" -ne 0 ]]; then
    pass "an over-running phase fails the step (rc=$rc)"
  else
    fail "an over-running phase fails the step" "rc=0 — the deadline did not fire"
  fi
  if grep -q 'API_CONTRACT_TIMEOUT=1' "$TMP_ROOT/runner.err"; then
    pass "the deadline failure names itself on stderr"
  else
    fail "the deadline failure names itself on stderr" "$(tail -3 "$TMP_ROOT/runner.err")"
  fi
  summary="$DEADLINE_ARTIFACTS/positive-summary.json"
  if [[ -f "$summary" ]] && grep -q '"failure_class":"runtime"' "$summary"; then
    pass "the deadline still leaves a machine-readable summary artifact"
  else
    fail "the deadline still leaves a machine-readable summary artifact" \
      "expected a runtime-classed $summary"
  fi
else
  # macOS without coreutils: the deadline cannot be honoured, so it must be
  # refused loudly rather than silently ignored.
  if [[ "$rc" -ne 0 ]] && grep -q "coreutils 'timeout' is not on PATH" "$TMP_ROOT/runner.err"; then
    pass "a deadline without coreutils 'timeout' is refused, not ignored"
  else
    fail "a deadline without coreutils 'timeout' is refused, not ignored" \
      "rc=$rc, stderr: $(tail -3 "$TMP_ROOT/runner.err")"
  fi
fi

# ---------------------------------------------------------------------------
# Part 3 — advisory mode: what it excuses, and what it must not
# ---------------------------------------------------------------------------
# The whole value of this mode is the line it draws. "The gate ran and
# graded 231 operations, 227 of them badly" is a product debt (#1815) and
# does not block. "The gate could not run" is an infrastructure failure and
# always does. Schemathesis reports both as a non-zero exit, so run.sh has
# to tell them apart on evidence — the JUnit report it just produced — not
# on the exit code alone.

# Advisory run helper. The artifact directory is derived from the label
# rather than assigned to a variable: run_advisory is called inside a
# command substitution to capture the exit code, so anything it assigns
# dies with that subshell.
adv_dir() { printf '%s/artifacts-adv-%s' "$TMP_ROOT" "$1"; }
run_advisory() {
  local label="$1"
  shift
  run_runner "$TMP_ROOT/argv-adv-$label" \
    API_CONTRACT_ADVISORY=1 \
    API_CONTRACT_ARTIFACT_DIR="$(adv_dir "$label")" \
    "$@"
}

# 1. The gate ran, graded operations, found things → does not fail the job.
rc="$(run_advisory findings STUB_EXIT=1 STUB_GRADED=9 STUB_FINDINGS=7)"
adv_summary="$(adv_dir findings)/positive-summary.json"
if [[ "$rc" -eq 0 ]]; then
  pass "advisory: graded findings do not fail the step"
else
  fail "advisory: graded findings do not fail the step" \
    "rc=$rc, stderr: $(tail -3 "$TMP_ROOT/runner.err")"
fi

# 2. The count lands in the job log, not only inside an artifact. A phase
#    that silently reports 227 findings into a file nobody opens is the
#    same failure as one that reports nothing.
if grep -qiE 'advisory' "$TMP_ROOT/runner.err" && grep -q '7 finding' "$TMP_ROOT/runner.err"; then
  pass "advisory: the finding count is printed to the job output"
else
  fail "advisory: the finding count is printed to the job output" \
    "stderr: $(tail -3 "$TMP_ROOT/runner.err")"
fi

# 3. The artifact still exists, still says the phase failed, and records
#    both the count and the fact that it was excused. A summary that read
#    "passed" would launder 227 findings into a green record.
if [[ -f "$adv_summary" ]]; then
  if grep -q '"advisory":true' "$adv_summary" \
    && grep -q '"status":"failed"' "$adv_summary" \
    && grep -q '"findings":7' "$adv_summary"; then
    pass "advisory: the summary records failed + advisory + the count"
  else
    fail "advisory: the summary records failed + advisory + the count" "$(cat "$adv_summary")"
  fi
else
  fail "advisory: the summary records failed + advisory + the count" "no $adv_summary"
fi

# 4. A schema/config abort (Schemathesis exit 2) is not a finding. Nothing
#    was graded; the gate did not run.
rc="$(run_advisory schemaabort STUB_EXIT=2)"
if [[ "$rc" -ne 0 ]]; then
  pass "advisory: a schema/config abort still fails the step"
else
  fail "advisory: a schema/config abort still fails the step" "rc=0 — the gate not running was excused"
fi

# 5. Exit 1 with nothing graded is not a finding either — that is the shape
#    of a run that died before it could grade anything.
rc="$(run_advisory ungraded STUB_EXIT=1)"
if [[ "$rc" -ne 0 ]]; then
  pass "advisory: exit 1 with an empty report still fails the step"
else
  fail "advisory: exit 1 with an empty report still fails the step" \
    "rc=0 — 'no evidence it ran' was treated as 'ran and found nothing'"
fi

# 6. The deadline is infrastructure, not a finding.
if command -v timeout >/dev/null 2>&1; then
  rc="$(run_advisory deadline API_CONTRACT_TIMEOUT=1 STUB_EXIT=1 STUB_GRADED=3 STUB_FINDINGS=3 STUB_SLEEP=20)"
  if [[ "$rc" -ne 0 ]]; then
    pass "advisory: a blown deadline still fails the step"
  else
    fail "advisory: a blown deadline still fails the step" "rc=0"
  fi
fi

# 7. The auth phase is never advisory: its checks are all reachability and
#    authorization, and none of them are the debt #1815 tracks.
RUNNER_PHASE=auth
rc="$(run_advisory authphase STUB_EXIT=0)"
RUNNER_PHASE=positive
if [[ "$rc" -ne 0 ]]; then
  pass "advisory: the auth phase still fails the step"
else
  fail "advisory: the auth phase still fails the step" \
    "rc=0 — advisory leaked into the phase that checks 401s"
fi

# 8. Without the flag, findings fail the step exactly as before. Advisory is
#    opt-in; a checkout that does not ask for it does not get it.
rc="$(run_runner "$TMP_ROOT/argv-noadv" STUB_EXIT=1 STUB_GRADED=9 STUB_FINDINGS=7)"
if [[ "$rc" -ne 0 ]]; then
  pass "findings fail the step when advisory is not requested"
else
  fail "findings fail the step when advisory is not requested" "rc=0"
fi

# ---------------------------------------------------------------------------
# Part 4 — the selected-operation count is the number actually probed
# ---------------------------------------------------------------------------
# The summary reported the size of the EXCLUDED union under the name
# `selected` (536 catalog, 305 excluded, reported as "selected": 305 while
# Schemathesis said 231). A coverage number that overstates itself by 74
# operations is worse than none, because it gets quoted.
cat >"$DOCROOT/openapi.json" <<'JSON'
{"openapi":"3.0.3","paths":{
  "/api/v1/things":{"get":{"responses":{"200":{"description":"ok"}}},
                    "post":{"responses":{"200":{"description":"ok"}}}},
  "/api/v1/journal/stream":{"get":{"responses":{"200":{"description":"ok"}}}},
  "/api/auth/session":{"get":{"responses":{"200":{"description":"ok"}}}},
  "/api/v1/more":{"get":{"responses":{"200":{"description":"ok"}}}}
}}
JSON
run_runner "$TMP_ROOT/argv-count" STUB_EXIT=0 >/dev/null
# 5 operations: one mutating, one auth-UI, one non-JSON, two probed.
if grep -q '"catalog":5' "$TMP_ROOT/runner.out"; then
  pass "the catalog count is every operation in the document"
else
  fail "the catalog count is every operation in the document" "$(cat "$TMP_ROOT/runner.out")"
fi
if grep -q '"selected":2' "$TMP_ROOT/runner.out"; then
  pass "the selected count is what is probed, not what is excluded"
else
  fail "the selected count is what is probed, not what is excluded" \
    "expected 2 of 5 (1 mutating + 1 auth-UI + 1 non-JSON excluded); got: $(cat "$TMP_ROOT/runner.out")"
fi

# ---------------------------------------------------------------------------
# Part 5 — every deliberately non-JSON route is out of the JSON probe
# ---------------------------------------------------------------------------
# NON_JSON_PATH_REGEX exists because these handlers return bytes, SVG, ZIP,
# Markdown or a stream, while the generated catalog gives their media types a
# generic JSON-object schema. Probing one reports a contract failure for the
# wrong reason — and the list is hand-maintained, so it goes stale silently
# every time a route is added or renamed.
#
# It had gone stale twice (#1815):
#
#   - `/api/v1/admin/memory/versions/{id}/content` was entered without its
#     `admin/` prefix, so the entry matched NO path in the shipped document
#     and the real route 5xx'd the gate on every run;
#   - `/api/v1/chats/{chatId}/stream` (the NDJSON run stream, added by #1822
#     after this list was written) was never entered at all, though it is the
#     same never-ending-stream case as `/api/v1/journal/stream` beside it.
#
# So the entries are asserted against the path shapes the router actually
# registers, not eyeballed. A stale entry is now a named failure here.
#
# `selected` is the COMPLEMENT of the exclusions, so with a two-route document
# it reads directly: 1 means the route under test was excluded, 2 means the
# gate still probes it.
probe_scope() {
  local path="$1"
  cat >"$DOCROOT/openapi.json" <<JSON
{"openapi":"3.0.3","paths":{
  "/api/v1/control":{"get":{"responses":{"200":{"description":"ok"}}}},
  "$path":{"get":{"responses":{"200":{"description":"ok"}}}}
}}
JSON
  run_runner "$TMP_ROOT/argv-scope" STUB_EXIT=0 >/dev/null
  grep -o '"selected":[0-9]*' "$TMP_ROOT/runner.out" | head -1
}

expect_excluded() {
  local path="$1" why="$2" got
  got="$(probe_scope "$path")"
  if [[ "$got" == '"selected":1' ]]; then
    pass "not probed as JSON: $path"
  else
    fail "not probed as JSON: $path" \
      "$why; summary said ${got:-<none>}, wanted \"selected\":1"
  fi
}

expect_probed() {
  local path="$1" got
  got="$(probe_scope "$path")"
  if [[ "$got" == '"selected":2' ]]; then
    pass "still probed as JSON: $path"
  else
    fail "still probed as JSON: $path" \
      "the exclusion list has grown to cover an ordinary JSON route; summary said ${got:-<none>}, wanted \"selected\":2"
  fi
}

expect_excluded "/api/v1/admin/memory/versions/{id}/content" \
  "the admin memory-version body is declared text/markdown + octet-stream"
expect_excluded "/api/v1/chats/{chatId}/stream" \
  "the run stream is declared application/x-ndjson and follow=true holds it open"
expect_excluded "/api/v1/memory/versions/{sha}" \
  "a memory version body is declared application/octet-stream"
expect_excluded "/api/v1/journal/stream" \
  "the journal stream is declared text/event-stream"
expect_excluded "/api/v1/memory/export" \
  "a memory export is declared application/zip"
expect_excluded "/api/v1/admin/backups/download" \
  "a backup download is declared application/zstd"
expect_excluded "/api/v1/agents/{agentId}/avatar" \
  "an avatar is declared image/svg+xml"

# The guard on the guard. Excluding everything would satisfy every check
# above, so the routes that must STAY in scope are pinned too — including the
# two the exclusion list is most likely to be over-extended to cover, since
# both look binary and neither finding is about media placeholders.
expect_probed "/api/v1/workspaces"
# Sibling of the excluded {sha} route: the LIST is ordinary JSON.
expect_probed "/api/v1/memory/versions"
expect_probed "/api/v1/admin/memory/versions"
# Answers its 4xx with http.Error's text/plain while the generated document
# says application/json — an undeclared media type, so a real violation.
expect_probed "/api/v1/oauth/callback"
# Binary download, but its finding is an undocumented status code.
expect_probed "/api/v1/crews/{crewId}/issues/{identifier}/attachments/{attachmentId}"

printf '\n'
if [[ "$FAILURES" -eq 0 ]]; then
  echo "api-contract gate: all checks passed"
  exit 0
fi
echo "api-contract gate: $FAILURES check(s) failed" >&2
exit 1

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
run_runner() {
  local argv_file="$1"
  shift
  env PATH="$STUB_BIN:$PATH" \
    STUB_ARGV_FILE="$argv_file" \
    CREWSHIP_BASE_URL="http://127.0.0.1:$PORT" \
    CREWSHIP_TOKEN=stub-token \
    CREWSHIP_WORKSPACE=stub-workspace \
    "$@" \
    bash "$RUNNER" positive >"$TMP_ROOT/runner.out" 2>"$TMP_ROOT/runner.err"
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

printf '\n'
if [[ "$FAILURES" -eq 0 ]]; then
  echo "api-contract gate: all checks passed"
  exit 0
fi
echo "api-contract gate: $FAILURES check(s) failed" >&2
exit 1

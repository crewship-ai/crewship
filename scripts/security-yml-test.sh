#!/usr/bin/env bash
# security-yml-test.sh — unit tests for the alert-issue logic embedded in
# .github/workflows/security.yml.
#
# That block decides whether a scheduled security scan counts as green and,
# if so, CLOSES the tracking issue. It has shipped broken twice:
#
#   #1275  introduced the auto-close, deciding "green" by enumerating the BAD
#          statuses (`grep -E '=(failure|cancelled)$'`). `skipped` was not on
#          that list, and the job runs with `if: always()`, so a scan pipeline
#          that never ran at all read as green and closed its own tracking
#          issue. A broken security scan marked itself resolved.
#   #1293  inverted the gate (only an explicit `=success` on every job is
#          green) and scoped the issue lookup to bot-filed issues.
#
# Neither shipped with a test, because the code lives in YAML and only ever
# executes on a scheduled run against main. Nothing could catch the third
# regression, so this file exists to.
#
# Both blocks are EXTRACTED VERBATIM from security.yml between marker comments
# and eval'd, rather than being re-typed here. A copy would drift from the
# workflow and then happily pass while production stayed broken — which is the
# exact failure mode these tests are meant to prevent.
#
# Marker LOSS fails loudly. Marker BYPASS is the harder case and the one that
# actually threatens this file: leave the markers in place, green and tested,
# and re-assign `not_green` one line after `# gate:end`. So extraction is scoped
# to the scheduled-report job (a block moved out of it is not found at all), and
# every assignment to the tested variables inside that job must live inside the
# markers — otherwise the value under test is not the value production uses.
#
# Known limit, stated rather than discovered later: this verifies the LOGIC the
# job would run, not that the job runs. Disabling it (`if: false`, removing the
# schedule trigger, deleting the step) leaves every check here green. Guarding
# that belongs in branch protection or a workflow-level lint, not here.
#
# Usage: bash scripts/security-yml-test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKFLOW="$SCRIPT_DIR/../.github/workflows/security.yml"

FAILURES=0
pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1"; FAILURES=$((FAILURES + 1)); }

# Print the body of the `scheduled-report` job — everything from its key to the
# next job at the same indent. Extraction is scoped to this FIRST, so a marked
# block that has been moved out of the job (into another job, or into a dead
# comment island) is not found at all rather than being tested where it can no
# longer run.
scheduled_report_job() {
  awk '
    /^  scheduled-report:[[:space:]]*$/ { inside = 1; print; next }
    inside && /^  [A-Za-z0-9_-]+:[[:space:]]*$/ { inside = 0 }
    inside { print }
  ' "$WORKFLOW"
}

JOB_SRC="$(scheduled_report_job)"

# Print the lines between `# <name>:begin` and `# <name>:end` within the job
# body, dedented out of the YAML block scalar. Empty if the markers are absent.
extract_block() {
  printf '%s\n' "$JOB_SRC" | awk -v name="$1" '
    $0 ~ "^[[:space:]]*# " name ":begin[[:space:]]*$" { inside = 1; next }
    $0 ~ "^[[:space:]]*# " name ":end[[:space:]]*$"   { inside = 0 }
    inside { sub(/^ {10}/, ""); print }
  '
}

# Count assignments to a shell variable in a block of text.
count_assign() { printf '%s\n' "$2" | grep -cE "^[[:space:]]*$1=" || true; }

if [ -z "$JOB_SRC" ]; then
  echo "FATAL: could not find the 'scheduled-report' job in $WORKFLOW" >&2
  echo "       (renamed? then this test is covering nothing — fix the name here)" >&2
  exit 1
fi

GATE_SRC="$(extract_block gate)"
LOOKUP_SRC="$(extract_block lookup)"

if [ -z "$GATE_SRC" ]; then
  echo "FATAL: could not extract the 'gate' block from the scheduled-report job" >&2
  echo "       (markers gone, or the block was moved out of the job)" >&2
  exit 1
fi
if [ -z "$LOOKUP_SRC" ]; then
  echo "FATAL: could not extract the 'lookup' block from the scheduled-report job" >&2
  echo "       (markers gone, or the block was moved out of the job)" >&2
  exit 1
fi

# Marker LOSS is the easy failure and was the only one this file used to guard
# against. Marker BYPASS is the one that matters: leave the markers untouched,
# tested and green, then re-assign the variable one line AFTER `# gate:end`.
# Production fails green; the suite reports 17/17 ok. Nobody has to remove a
# marker to defeat a test that only checks its own window.
#
# So: every assignment to these variables inside the job must live inside the
# marked span. A second one outside it means the workflow's real value is not
# the value under test, and that is fatal, not a warning.
for pair in "not_green:GATE" "results_clean:GATE" "existing:LOOKUP"; do
  var="${pair%%:*}"
  case "${pair##*:}" in
    GATE) marked="$GATE_SRC" ;;
    *)    marked="$LOOKUP_SRC" ;;
  esac
  in_job="$(count_assign "$var" "$JOB_SRC")"
  in_marked="$(count_assign "$var" "$marked")"
  if [ "$in_job" -ne "$in_marked" ]; then
    echo "FATAL: '$var' is assigned $in_job time(s) in the scheduled-report job" >&2
    echo "       but only $in_marked time(s) inside the tested markers." >&2
    echo "       An assignment outside the markers is not covered by this test," >&2
    echo "       and silently decides what production actually does." >&2
    exit 1
  fi
done

# ---------------------------------------------------------------------------
# The green gate
# ---------------------------------------------------------------------------
# Run the extracted gate against a RESULTS block; echo GREEN or NOT_GREEN.
# GREEN is what closes the tracking issue, so anything other than a fully
# successful scan MUST come back NOT_GREEN.
run_gate() {
  RESULTS="$1" bash -c '
    set -euo pipefail
    '"$GATE_SRC"'
    if [ -z "$not_green" ]; then echo GREEN; else echo NOT_GREEN; fi
  '
}

expect_gate() {
  local label="$1" want="$2" results="$3" got
  got="$(run_gate "$results")"
  if [ "$got" = "$want" ]; then
    pass "$label"
  else
    fail "$label (want $want, got ${got:-<empty/error>})"
  fi
}

echo "green gate (GREEN closes the tracking issue):"

expect_gate "every job success -> GREEN" GREEN \
'gitleaks=success
govulncheck=success
grype=success
license-check=success
osv-scan=success
'

# The #1275 regression: pipeline broken, every needs-job skipped.
expect_gate "every job skipped (the #1275 bug) -> NOT_GREEN" NOT_GREEN \
'gitleaks=skipped
govulncheck=skipped
grype=skipped
license-check=skipped
osv-scan=skipped
'

expect_gate "one job skipped among successes -> NOT_GREEN" NOT_GREEN \
'gitleaks=success
govulncheck=skipped
grype=success
license-check=success
osv-scan=success
'

expect_gate "one job failed -> NOT_GREEN" NOT_GREEN \
'gitleaks=success
govulncheck=failure
grype=success
license-check=success
osv-scan=success
'

expect_gate "one job cancelled -> NOT_GREEN" NOT_GREEN \
'gitleaks=success
govulncheck=cancelled
grype=success
license-check=success
osv-scan=success
'

# The point of inverting the gate: a status nobody enumerated must not be
# assumed benign. `timed_out` is real today; the next one GitHub adds is not
# knowable from here.
expect_gate "unknown future status -> NOT_GREEN" NOT_GREEN \
'gitleaks=success
govulncheck=timed_out
grype=success
license-check=success
osv-scan=success
'

# A needs-job that was removed or renamed renders as an empty result.
expect_gate "empty result for one job -> NOT_GREEN" NOT_GREEN \
'gitleaks=success
govulncheck=
grype=success
license-check=success
osv-scan=success
'

# Zero jobs vacuously satisfies "every job reported success". It must not.
expect_gate "no job results at all -> NOT_GREEN" NOT_GREEN ''

# Trailing whitespace is stripped before the comparison, so a genuinely green
# scan still closes its issue rather than nagging forever. GitHub renders the
# RESULTS block scalar from `${{ needs.*.result }}` expressions, and trailing
# spaces in that YAML are invisible in review — without the `sed` this case is
# a permanent false alarm on a green scan.
#
# Written with $'...' and explicit escapes ON PURPOSE: a fixture that carried
# literal trailing spaces would be byte-identical to the all-success case the
# moment any editor or pre-commit hook trimmed it, and would then silently
# duplicate that test while claiming to cover the `sed`.
expect_gate "trailing whitespace on success lines -> GREEN" GREEN \
$'gitleaks=success   \ngovulncheck=success\t\ngrype=success \nlicense-check=success\nosv-scan=success  \n'

# ---------------------------------------------------------------------------
# The issue lookup
# ---------------------------------------------------------------------------
# The lookup picks WHICH issue the close path acts on. Selecting an issue this
# workflow did not file means the bot closes someone else's work; selecting
# nothing when its own issue exists means it files a duplicate every run.
#
# `gh` is stubbed with a function that applies the real --jq program to a
# fixture, so the jq text and its shell quoting are exercised as written.
run_lookup() {
  local fixture="$1"
  FIXTURE="$fixture" \
  TITLE="Scheduled security scan failed on main" \
  MARKER="<!-- crewship:scheduled-security-scan-alert -->" \
  REPO="crewship-ai/crewship" \
  bash -c '
    set -euo pipefail
    gh() {
      local jq_prog=""
      while [ $# -gt 0 ]; do
        if [ "$1" = "--jq" ]; then jq_prog="$2"; shift 2; continue; fi
        shift
      done
      printf "%s" "$FIXTURE" | jq -r "$jq_prog"
    }
    '"$LOOKUP_SRC"'
    printf "%s" "$existing"
  '
}

# Asserts BOTH the selected issue and that the lookup exited cleanly.
#
# The exit status is not decoration. "no issue selected" and "jq died" both
# produce empty output, so an output-only assertion reports `ok` for a lookup
# that crashed. In the workflow that crash happens under `set -euo pipefail`:
# the step aborts, and no issue is filed at all — the failure the test claims to
# rule out. Dropping the `// ""` null-body guard is exactly that mutation, and
# it used to pass here.
expect_lookup() {
  local label="$1" want="$2" fixture="$3" got rc
  got="$(run_lookup "$fixture")" && rc=0 || rc=$?
  if [ "$rc" -ne 0 ]; then
    fail "$label (lookup exited $rc — in the workflow this aborts the step under set -e, filing nothing)"
    return
  fi
  if [ "$got" = "$want" ]; then
    pass "$label"
  else
    fail "$label (want '${want:-<none>}', got '${got:-<none>}')"
  fi
}

echo "issue lookup (selected issue is the one the bot will close):"

BOT='{"is_bot":true,"login":"app/github-actions"}'
HUMAN='{"is_bot":false,"login":"Srbino"}'
MARKER_TXT='<!-- crewship:scheduled-security-scan-alert -->'

expect_lookup "bot-filed issue with our exact title -> selected" "1001" \
  "[{\"number\":1001,\"title\":\"Scheduled security scan failed on main\",\"body\":\"old body, no marker\",\"author\":$BOT}]"

expect_lookup "bot-filed issue renamed by a human, marker in body -> selected" "1002" \
  "[{\"number\":1002,\"title\":\"renamed by a human\",\"body\":\"$MARKER_TXT\nstuff\",\"author\":$BOT}]"

# The containment #1293 added: the label alone must not be enough.
expect_lookup "human-filed issue with our exact title -> NOT selected" "" \
  "[{\"number\":1003,\"title\":\"Scheduled security scan failed on main\",\"body\":\"I filed this myself\",\"author\":$HUMAN}]"

expect_lookup "human-filed issue quoting the marker -> NOT selected" "" \
  "[{\"number\":1004,\"title\":\"my own security note\",\"body\":\"see $MARKER_TXT for context\",\"author\":$HUMAN}]"

expect_lookup "unrelated bot issue merely carrying the label -> NOT selected" "" \
  '[{"number":1005,"title":"Bump golang.org/x/net","body":"dependabot","author":{"is_bot":true,"login":"app/dependabot"}}]'

expect_lookup "human issue listed first -> skipped, bot issue selected" "1007" \
  "[{\"number\":1006,\"title\":\"Scheduled security scan failed on main\",\"body\":\"human\",\"author\":$HUMAN},{\"number\":1007,\"title\":\"Scheduled security scan failed on main\",\"body\":\"bot\",\"author\":$BOT}]"

expect_lookup "null body -> no crash, not selected" "" \
  "[{\"number\":1008,\"title\":\"unrelated\",\"body\":null,\"author\":$BOT}]"

expect_lookup "no open issues -> nothing selected" "" '[]'

echo
if [ "$FAILURES" -eq 0 ]; then
  echo "all security.yml checks passed"
else
  echo "$FAILURES check(s) FAILED"
fi
exit $(( FAILURES > 0 ? 1 : 0 ))

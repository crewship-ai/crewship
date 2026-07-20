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
# exact failure mode these tests are meant to prevent. If the markers go
# missing the extraction returns empty and the run fails loudly.
#
# Usage: bash scripts/security-yml-test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKFLOW="$SCRIPT_DIR/../.github/workflows/security.yml"

FAILURES=0
pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1"; FAILURES=$((FAILURES + 1)); }

# Print the lines between `# <name>:begin` and `# <name>:end`, dedented out of
# the YAML block scalar. Returns empty if the markers are absent.
extract_block() {
  awk -v name="$1" '
    $0 ~ "^[[:space:]]*# " name ":begin[[:space:]]*$" { inside = 1; next }
    $0 ~ "^[[:space:]]*# " name ":end[[:space:]]*$"   { inside = 0 }
    inside { sub(/^ {10}/, ""); print }
  ' "$WORKFLOW"
}

GATE_SRC="$(extract_block gate)"
LOOKUP_SRC="$(extract_block lookup)"

if [ -z "$GATE_SRC" ]; then
  echo "FATAL: could not extract the 'gate' block from $WORKFLOW" >&2
  echo "       (the # gate:begin / # gate:end markers are gone)" >&2
  exit 1
fi
if [ -z "$LOOKUP_SRC" ]; then
  echo "FATAL: could not extract the 'lookup' block from $WORKFLOW" >&2
  echo "       (the # lookup:begin / # lookup:end markers are gone)" >&2
  exit 1
fi

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
trivy=success
semgrep=success
license-check=success
osv-scan=success
'

# The #1275 regression: pipeline broken, every needs-job skipped.
expect_gate "every job skipped (the #1275 bug) -> NOT_GREEN" NOT_GREEN \
'gitleaks=skipped
govulncheck=skipped
trivy=skipped
semgrep=skipped
license-check=skipped
osv-scan=skipped
'

expect_gate "one job skipped among successes -> NOT_GREEN" NOT_GREEN \
'gitleaks=success
govulncheck=skipped
trivy=success
semgrep=success
license-check=success
osv-scan=success
'

expect_gate "one job failed -> NOT_GREEN" NOT_GREEN \
'gitleaks=success
govulncheck=failure
trivy=success
semgrep=success
license-check=success
osv-scan=success
'

expect_gate "one job cancelled -> NOT_GREEN" NOT_GREEN \
'gitleaks=success
govulncheck=cancelled
trivy=success
semgrep=success
license-check=success
osv-scan=success
'

# The point of inverting the gate: a status nobody enumerated must not be
# assumed benign. `timed_out` is real today; the next one GitHub adds is not
# knowable from here.
expect_gate "unknown future status -> NOT_GREEN" NOT_GREEN \
'gitleaks=success
govulncheck=timed_out
trivy=success
semgrep=success
license-check=success
osv-scan=success
'

# A needs-job that was removed or renamed renders as an empty result.
expect_gate "empty result for one job -> NOT_GREEN" NOT_GREEN \
'gitleaks=success
govulncheck=
trivy=success
semgrep=success
license-check=success
osv-scan=success
'

# Zero jobs vacuously satisfies "every job reported success". It must not.
expect_gate "no job results at all -> NOT_GREEN" NOT_GREEN ''

# Trailing whitespace is stripped before the comparison, so a genuinely green
# scan still closes its issue rather than nagging forever.
expect_gate "trailing whitespace on success lines -> GREEN" GREEN \
'gitleaks=success
govulncheck=success
trivy=success
semgrep=success
license-check=success
osv-scan=success
'

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

expect_lookup() {
  local label="$1" want="$2" fixture="$3" got
  got="$(run_lookup "$fixture")"
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

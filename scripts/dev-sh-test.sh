#!/usr/bin/env bash
# dev-sh-test.sh — unit tests for dev.sh helper functions.
#
# dev.sh runs under `set -euo pipefail`, which makes an innocuous-looking
# `[[ cond ]] && cmd` as a function's LAST statement fatal: when the test
# is false the list returns 1, the function returns 1, and `set -e` aborts
# the whole script at the call site. That is not theoretical — it wedged
# the dev1 slot on crewship-dev from 2026-07-16 to 2026-07-20 (see
# preserve_crash_log below), and through it the slot reconciler, which
# `set -e`s out of its own loop when a slot's dev.sh exits non-zero.
#
# Functions are extracted from dev.sh by name and eval'd rather than
# sourcing the file, because sourcing dev.sh executes its command
# dispatcher.
#
# Usage: bash scripts/dev-sh-test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEV_SH="$SCRIPT_DIR/../dev.sh"

FAILURES=0
pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1"; FAILURES=$((FAILURES + 1)); }

# Print the source of a single `name() { ... }` block from dev.sh.
extract_fn() {
  awk -v fn="$1" '
    $0 ~ "^" fn "\\(\\) \\{" { inside = 1 }
    inside { print }
    inside && /^}/ { exit }
  ' "$DEV_SH"
}

# Run a snippet with the named dev.sh functions in scope, under the same
# shell options dev.sh itself uses. Echoes the snippet's exit status.
run_with_fn() {
  local fn="$1" snippet="$2" body
  body="$(extract_fn "$fn")"
  if [[ -z "$body" ]]; then
    echo "127"
    return
  fi
  # Fed through stdin rather than `bash -c "...$body...$snippet"`: the
  # function body comes out of dev.sh verbatim, quotes and all, and
  # interpolating it into a double-quoted command string is one stray
  # quote away from a syntax error that would look like a test failure.
  printf '%s\n%s\n%s\n' 'set -euo pipefail' "$body" "$snippet" | bash >/dev/null 2>&1
  echo "$?"
}

echo "dev.sh: preserve_crash_log"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# The wedge: on a slot whose previous run already rotated its log away,
# GO_LOG does not exist on the next start. preserve_crash_log must be a
# no-op that returns 0, not an `set -e` abort that stops the slot from
# ever starting again.
status=$(run_with_fn preserve_crash_log "preserve_crash_log '$TMP/missing.log'; echo reached")
if [[ "$status" == "0" ]]; then
  pass "missing log file: returns 0 (caller under set -e survives)"
else
  fail "missing log file: exited $status, expected 0 — set -e would abort dev.sh start"
fi

: > "$TMP/empty.log"
status=$(run_with_fn preserve_crash_log "preserve_crash_log '$TMP/empty.log'; echo reached")
if [[ "$status" == "0" ]]; then
  pass "empty log file: returns 0"
else
  fail "empty log file: exited $status, expected 0"
fi
if [[ ! -e "$TMP/empty.log.prev" ]]; then
  pass "empty log file: not rotated"
else
  fail "empty log file: rotated to .prev, expected no rotation"
fi

echo "crash reason" > "$TMP/full.log"
status=$(run_with_fn preserve_crash_log "preserve_crash_log '$TMP/full.log'")
if [[ "$status" == "0" ]]; then
  pass "non-empty log file: returns 0"
else
  fail "non-empty log file: exited $status, expected 0"
fi
if [[ -f "$TMP/full.log.prev" ]] && [[ "$(cat "$TMP/full.log.prev")" == "crash reason" ]]; then
  pass "non-empty log file: rotated to .prev with contents intact"
else
  fail "non-empty log file: not rotated to .prev"
fi
if [[ ! -e "$TMP/full.log" ]]; then
  pass "non-empty log file: original moved, not copied"
else
  fail "non-empty log file: original still present, expected mv"
fi

# Guard the whole class of bug, not just the one instance of it.
echo "dev.sh: no function ends in a bare && list"
offenders="$(awk '
  /^[a-zA-Z_][a-zA-Z0-9_]*\(\) \{/ { fn = $1; last = ""; next }
  /^}/ { if (fn != "" && last ~ /&&/ && last !~ /\|\|/) print fn " -> " last; fn = ""; next }
  fn != "" && $0 !~ /^[[:space:]]*(#|$)/ { last = $0 }
' "$DEV_SH")"
if [[ -z "$offenders" ]]; then
  pass "no function returns the status of a trailing && list"
else
  fail "function(s) end in a bare && list — non-zero return aborts callers under set -e:"
  printf '       %s\n' "$offenders"
fi

echo ""
if [[ "$FAILURES" -eq 0 ]]; then
  echo "dev.sh tests passed"
else
  echo "dev.sh tests: $FAILURES failure(s)"
  exit 1
fi

#!/usr/bin/env bash
# Static integrity gate for the CLI harness suites.
#
# This intentionally does not run a suite: the suites need a live server. It
# checks the cheap properties that must hold before a live run is meaningful:
# ShellCheck warnings and calls to harness helpers that are not defined.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HARNESS="$ROOT/scripts/test-harness"
status=0

if ! command -v shellcheck >/dev/null 2>&1; then
  echo "error: shellcheck is required for the harness integrity gate" >&2
  exit 2
fi

# Info-level findings (SC1091/SC2016) are useful locally but are deliberately
# not merge-gate failures. Warnings and errors are.
# pr-subset.sh is the runner CI executes, so it is held to the same bar as the
# suites it launches — it was outside this glob and shipped with a `for` loop
# that reported only the last suite's status.
if ! shellcheck -x --severity=warning "$HARNESS/lib.sh" "$HARNESS/pr-subset.sh" "$HARNESS"/test-*.sh; then
  status=1
fi

# Bash has no portable parser in the runner image. This small command-position
# scanner is deliberately conservative: it only diagnoses harness-shaped
# identifiers (the shared library functions and assert_* helpers), and asks
# bash itself whether the name is a builtin or executable before reporting it.
# This avoids false positives from heredoc prose and ordinary CLI commands.
functions_file="$(mktemp -t crewship-harness-functions.XXXXXX)"
trap 'rm -f "$functions_file"' EXIT
sed -nE 's/^[[:space:]]*([a-zA-Z_][a-zA-Z0-9_]*)[[:space:]]*\(\).*/\1/p' \
  "$HARNESS/lib.sh" "$HARNESS"/test-*.sh | sort -u > "$functions_file"

is_known_command() {
  local name="$1"
  grep -Fxq "$name" "$functions_file" && return 0
  [[ "$name" == "true" || "$name" == "false" ]] && return 0
  builtin "$name" >/dev/null 2>&1 && return 0
  command -v "$name" >/dev/null 2>&1
}

for suite in "$HARNESS"/test-*.sh; do
  # Extract command-position words at the start of a shell command and inside
  # command substitutions. This is enough to validate the suite/library
  # contract without evaluating untrusted test code.
  calls="$({
    sed -E 's/#.*$//' "$suite" |
      grep -Eo '^[[:space:]]*(if[[:space:]]+|then[[:space:]]+|do[[:space:]]+|else[[:space:]]+|![[:space:]]*)?[a-zA-Z_][a-zA-Z0-9_]*|\$\([[:space:]]*[a-zA-Z_][a-zA-Z0-9_]*' || true
  } | sed -E 's/.*\$\([[:space:]]*//; s/^[[:space:]]*(if|then|do|else|!)?[[:space:]]*//' | sort -u)"

  while IFS= read -r call; do
    [[ -z "$call" ]] && continue
    if { grep -Fxq "$call" "$functions_file" || [[ "$call" == assert_* ]]; } &&
       ! is_known_command "$call"; then
      echo "${suite##*/}: missing command-position function: $call" >&2
      status=1
    fi
  done <<< "$calls"
done

exit "$status"

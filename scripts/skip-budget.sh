#!/usr/bin/env bash
# skip-budget.sh — ratchet on the number of `t.Skip` calls in the Go test suite.
#
# Why this file exists: a skipped test is indistinguishable from a passing one
# in CI output. `go test ./...` prints `ok` for a package whose every test
# called `t.Skip`, and the job goes green. The repo currently leans on that
# behaviour heavily and deliberately — Docker-gated tests skip themselves on
# the macOS/arm64 matrix runners, which have no daemon; `runtime.GOOS` guards
# skip unix-only paths on Windows; `testing.Short` guards skip the slow ones.
# All legitimate. The problem is that nothing counted them, so "I'll gate this
# behind a skip until I fix it" had no cost and no visibility, and the number
# only ever went up.
#
# This is a ratchet, not a ban. It fails only when the count RISES above the
# recorded baseline. Deleting skips is always allowed and prints a nudge to
# lower the baseline.
#
# Usage: bash scripts/skip-budget.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BASELINE_FILE="$SCRIPT_DIR/skip-budget.txt"

cd "$REPO_ROOT"

if [ ! -f "$BASELINE_FILE" ]; then
  printf '✗ baseline file missing: %s\n' "$BASELINE_FILE" >&2
  exit 1
fi

# Baseline file is `# comment` lines plus exactly one integer line, so the
# rationale can live next to the number instead of only in git history.
BASELINE="$(grep -vE '^\s*(#|$)' "$BASELINE_FILE" | head -1 | tr -d '[:space:]')"
if ! printf '%s' "$BASELINE" | grep -qE '^[0-9]+$'; then
  printf '✗ %s does not contain a bare integer baseline (got «%s»)\n' \
    "$BASELINE_FILE" "$BASELINE" >&2
  exit 1
fi

# `git ls-files`, NOT `find`: the repo hosts agent worktrees under
# .claude/worktrees/ and a node_modules tree, both of which carry their own
# *_test.go files. A find-based count would scoop those up and swing wildly
# depending on which agents happen to have a worktree checked out.
#
# The pattern deliberately covers t.Skip / t.Skipf / t.SkipNow and anchors on
# `(` so a `t.Skipped()` read or a `t.Skip` mention in a comment doesn't count.
# grep -o counts occurrences rather than lines, so two skips on one line both
# register.
COUNT="$(
  git ls-files -z -- '*_test.go' \
    | xargs -0 grep -ohE '\bt\.Skip(f|Now)?\(' \
    | wc -l \
    | tr -d '[:space:]'
)"

FILES="$(
  git ls-files -z -- '*_test.go' \
    | xargs -0 grep -lE '\bt\.Skip(f|Now)?\(' \
    | wc -l \
    | tr -d '[:space:]'
)"

printf 'skip budget: %s t.Skip call(s) across %s test file(s); baseline %s\n' \
  "$COUNT" "$FILES" "$BASELINE"

if [ "$COUNT" -gt "$BASELINE" ]; then
  printf '\n'
  printf '✗ Skip budget exceeded: %s > %s (baseline).\n' "$COUNT" "$BASELINE" >&2
  printf '\n' >&2
  printf 'A skipped test reports the same "ok" as a passing one, so every new\n' >&2
  printf 'skip is coverage that silently disappears. The offending skips are the\n' >&2
  printf 'ones your branch added — find them with:\n' >&2
  printf '\n' >&2
  printf '  git diff origin/main -- "*_test.go" | grep -nE "^\\+.*t\\.Skip"\n' >&2
  printf '\n' >&2
  printf 'Preferred fix: do not skip. Make the test run — inject the dependency,\n' >&2
  printf 'fake the daemon, or move the OS-specific half into a build-tagged file\n' >&2
  printf '(foo_windows.go / foo_unix.go) so the guard is a compile-time decision\n' >&2
  printf 'instead of a runtime skip that reads as a pass.\n' >&2
  printf '\n' >&2
  printf 'If the skip is genuinely warranted (no Docker on the macOS runners, a\n' >&2
  printf 'live-provider test that needs a real API key), raise the baseline\n' >&2
  printf 'deliberately:\n' >&2
  printf '\n' >&2
  printf '  1. Open a tracking issue for retiring the skip.\n' >&2
  printf '  2. Put a waiver comment directly above the t.Skip saying WHY it\n' >&2
  printf '     cannot run and referencing that issue, e.g.\n' >&2
  printf '       // SKIP-WAIVER(#1234): needs a live Docker daemon; the macOS\n' >&2
  printf '       // arm64 matrix runners have none.\n' >&2
  printf '  3. Bump the number in scripts/skip-budget.txt to %s in the SAME\n' "$COUNT" >&2
  printf '     commit, with a one-line note in that file explaining the bump.\n' >&2
  printf '\n' >&2
  printf 'Reviewers: a baseline bump with no waiver comment and no issue is the\n' >&2
  printf 'thing this gate exists to make visible. Ask for one.\n' >&2
  exit 1
fi

if [ "$COUNT" -lt "$BASELINE" ]; then
  printf '\n'
  printf '✓ Under budget by %s. Ratchet it down: set scripts/skip-budget.txt to %s\n' \
    "$((BASELINE - COUNT))" "$COUNT"
  printf '  so the reclaimed coverage cannot quietly leak back.\n'
  exit 0
fi

printf '✓ At baseline.\n'

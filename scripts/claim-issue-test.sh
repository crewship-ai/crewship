#!/usr/bin/env bash
# claim-issue-test.sh — unit tests for scripts/claim-issue.sh.
#
# Why this file exists (#1488): the claim convention is only worth anything if
# the check is trustworthy. A parser that misses an active claim is worse than
# no parser — it tells a second agent the issue is free right before it
# duplicates two hours of work (#1481 vs #1471). The parse and the clone
# detection are the two places that can be wrong silently, so they are the two
# things pinned here. No network, no gh, no GitHub.
#
# Usage: bash scripts/claim-issue-test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLAIM="$SCRIPT_DIR/claim-issue.sh"

FAILURES=0
pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1"; printf '       %s\n' "${2:-}"; FAILURES=$((FAILURES + 1)); }

expect_eq() { # <name> <want> <got>
  if [ "$2" = "$3" ]; then pass "$1"; else fail "$1" "want «$2» got «$3»"; fi
}
expect_contains() { # <name> <haystack> <needle>
  case "$2" in *"$3"*) pass "$1";; *) fail "$1" "expected «$3» in: $2";; esac
}
expect_not_contains() { # <name> <haystack> <needle>
  case "$2" in *"$3"*) fail "$1" "did NOT expect «$3» in: $2";; *) pass "$1";; esac
}

if [ ! -x "$CLAIM" ]; then
  fail "claim-issue.sh is executable" "$CLAIM missing or not +x"
  exit 1
fi
if ! command -v jq >/dev/null 2>&1; then
  printf 'jq not installed — skipping claim-issue.sh tests\n'
  exit 0
fi

# comment <created> <author> <body> — one GitHub comment object.
comment() {
  jq -nc --arg at "$1" --arg who "$2" --arg body "$3" \
    '{createdAt:$at, author:{login:$who}, body:$body, url:("https://x/#c-"+$at)}'
}
# thread <comment>... — the array shape `gh issue view --json comments -q .comments` returns.
thread() { printf '%s\n' "$@" | jq -sc '.'; }

parse() { printf '%s' "$1" | "$CLAIM" --parse; }

CLAIM_A='**CLAIM** — clone `crewship_1` · branch `fix/alpha` · 2026-07-30T09:00Z'
CLAIM_B='**CLAIM** — clone `crewship_2` · branch `fix/beta` · 2026-07-30T10:00Z'
# The free-form shape a session actually posted on #1563 before the helper
# existed. The parser has to keep recognising it — old threads do not get
# rewritten, and a claim it cannot see is a claim it reports as free.
CLAIM_LEGACY='Claimed — clone `crewship_3`, branch `fix/legacy`, 2026-07-30T21:07Z. Will release here if I stop.'

echo "== parse: claims and releases =="

got="$(parse "$(thread "$(comment 2026-07-30T09:00:00Z Srbino "$CLAIM_A")")")"
expect_contains "lone claim is active" "$got" "crewship_1"
expect_contains "lone claim carries its branch" "$got" "fix/alpha"

got="$(parse "$(thread \
  "$(comment 2026-07-30T09:00:00Z Srbino "$CLAIM_A")" \
  "$(comment 2026-07-30T11:00:00Z Srbino '**RELEASE** — clone `crewship_1` · branch `fix/alpha` · 2026-07-30T11:00Z')")")"
expect_eq "matching release clears the claim" "" "$got"

got="$(parse "$(thread \
  "$(comment 2026-07-30T09:00:00Z Srbino "$CLAIM_A")" \
  "$(comment 2026-07-30T11:00:00Z Srbino '**RELEASE** — clone `crewship_2` · branch `fix/beta` · 2026-07-30T11:00Z')")")"
expect_contains "release from another clone does NOT clear it" "$got" "crewship_1"

got="$(parse "$(thread \
  "$(comment 2026-07-30T09:00:00Z Srbino "$CLAIM_A")" \
  "$(comment 2026-07-30T10:00:00Z Srbino "$CLAIM_B")" \
  "$(comment 2026-07-30T11:00:00Z Srbino '**RELEASE** — clone `crewship_1` · branch `fix/alpha` · 2026-07-30T11:00Z')")")"
expect_not_contains "released claim drops out of a two-claim thread" "$got" "fix/alpha"
expect_contains "the other claim survives" "$got" "fix/beta"

got="$(parse "$(thread \
  "$(comment 2026-07-30T09:00:00Z Srbino "$CLAIM_A")" \
  "$(comment 2026-07-30T11:00:00Z Srbino 'Released — done here, PR merged.')")")"
expect_eq "a bare release with no clone releases everything" "" "$got"

got="$(parse "$(thread "$(comment 2026-07-30T21:07:00Z Srbino "$CLAIM_LEGACY")")")"
expect_contains "free-form 'Claimed —' still parses (clone)" "$got" "crewship_3"
expect_contains "free-form 'Claimed —' still parses (branch)" "$got" "fix/legacy"

# Backticks are how the helper writes it, not how humans do. Nine live claims
# on 2026-07-30 read "clone crewship_3, branch `fix/x`" — clone bare, branch
# quoted. A parser that only reads the quoted form drops the clone and then
# cannot answer the one question that matters: is this claim mine?
got="$(parse "$(thread "$(comment 2026-07-30T20:58:00Z Srbino \
  'Claimed — clone crewship_3, branch `fix/server-test-fk-enforcement`, 2026-07-30T20:58Z. Will release here if I stop.')")")"
expect_contains "bare (un-backticked) clone parses" "$got" "crewship_3"
expect_contains "…without losing the branch" "$got" "fix/server-test-fk-enforcement"

got="$(parse "$(thread "$(comment 2026-07-30T09:00:00Z Srbino \
  'CLAIM — clone crewship_2, branch fix/nothing-quoted, 2026-07-30T09:00Z')")")"
expect_contains "nothing quoted at all still parses (clone)" "$got" "crewship_2"
expect_contains "nothing quoted at all still parses (branch)" "$got" "fix/nothing-quoted"

# A missing field must be a visible placeholder, never an empty column: `read`
# with a tab IFS silently swallows a leading empty field, which shifts every
# other column left and turns a branch into a clone in the report.
got="$(parse "$(thread "$(comment 2026-07-30T09:00:00Z Srbino '**CLAIM** — branch `fix/no-clone-named`')")")"
case "$got" in
  ?$'\t'*) pass "missing clone becomes a '?' placeholder" ;;
  *) fail "missing clone becomes a '?' placeholder" "got «$got»" ;;
esac

echo "== parse: what must NOT count as a claim =="

got="$(parse "$(thread "$(comment 2026-07-30T09:00:00Z Srbino \
  'The docs claim this is fixed, but the branch `fix/alpha` in clone `crewship_1` still fails.')")")"
expect_eq "prose using the word claim mid-sentence is ignored" "" "$got"

got="$(parse "$(thread "$(comment 2026-07-30T09:00:00Z Srbino 'Nice catch, taking a look.')")")"
expect_eq "ordinary comments are ignored" "" "$got"

got="$(parse "$(thread)")"
expect_eq "empty thread is not a claim" "" "$got"

echo "== clone detection =="

clone_of() { "$CLAIM" --clone-of "$1"; }

expect_eq "clone from a plain checkout" "crewship_3" \
  "$(clone_of '/Volumes/SSD 990 PRO/Development/crewship_3')"
expect_eq "clone from a worktree under the checkout" "crewship_3" \
  "$(clone_of '/Volumes/SSD 990 PRO/Development/crewship_3/.claude/worktrees/agent-a9bc')"
expect_eq "clone from a .git common dir" "crewship_2" \
  "$(clone_of '/Volumes/SSD 990 PRO/Development/crewship_2/.git')"
expect_eq "unnumbered checkout falls back to its directory name" "crewship" \
  "$(clone_of '/home/dev/crewship')"
expect_eq "a path with no crewship_N anywhere still yields something" "src" \
  "$(clone_of '/home/dev/src')"

echo "== usage =="

out="$("$CLAIM" 2>&1)"; rc=$?
expect_eq "no args exits 2" "2" "$rc"
expect_contains "no args prints usage" "$out" "Usage:"

out="$("$CLAIM" --check 2>&1)"; rc=$?
expect_eq "--check without an issue number exits 2" "2" "$rc"

out="$("$CLAIM" not-a-number 2>&1)"; rc=$?
expect_eq "non-numeric issue exits 2" "2" "$rc"

echo
if [ "$FAILURES" -gt 0 ]; then
  printf '%d failure(s)\n' "$FAILURES"
  exit 1
fi
printf 'all claim-issue.sh checks passed\n'

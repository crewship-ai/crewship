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

# #2107: a claim posted before the feature branch exists (the documented
# order — CONTRIBUTING says to claim before your first commit) is released
# from a DIFFERENT branch of the SAME clone. The old predicate required both
# clone and branch to match, so this release never landed and the claim
# stayed live forever. Cancellation now keys on clone alone.
got="$(parse "$(thread \
  "$(comment 2026-07-30T09:00:00Z Srbino "$CLAIM_A")" \
  "$(comment 2026-07-30T11:00:00Z Srbino '**RELEASE** — clone `crewship_1` · branch `fix/something-else-entirely` · 2026-07-30T11:00Z')")")"
expect_eq "release from a different branch, SAME clone, clears it (#2107)" "" "$got"

# Keying cancellation on clone must not swallow the branch-only release. A
# hand-written RELEASE naming a branch and no clone used to cancel that
# branch's claims and leave the rest; reading "no clone" as "names nothing"
# would turn it into a global cancel, which is the one form the contract
# reserves for a release that names neither field.
got="$(parse "$(thread \
  "$(comment 2026-07-30T09:00:00Z Srbino "$CLAIM_A")" \
  "$(comment 2026-07-30T11:00:00Z Srbino '**RELEASE** — branch `fix/beta` · 2026-07-30T11:00Z')")")"
expect_contains "a branch-only release does NOT clear a claim on another branch" "$got" "crewship_1"

got="$(parse "$(thread \
  "$(comment 2026-07-30T09:00:00Z Srbino "$CLAIM_A")" \
  "$(comment 2026-07-30T11:00:00Z Srbino '**RELEASE** — branch `fix/alpha` · 2026-07-30T11:00Z')")")"
expect_eq "a branch-only release DOES clear a claim on that branch" "" "$got"

# The exact shape from the issue: claim under the auto-minted worktree
# branch, release under the real feature branch that replaced it.
got="$(parse "$(thread \
  "$(comment 2026-07-30T09:00:00Z Srbino '**CLAIM** — clone `crewship_1` · branch `worktree-agent-a8474dee8fcab1732` · 2026-07-30T09:00Z')" \
  "$(comment 2026-07-30T11:00:00Z Srbino '**RELEASE** — clone `crewship_1` · branch `fix/1815-the-actual-work` · 2026-07-30T11:00Z')")")"
expect_eq "worktree-branch claim released from the real feature branch clears it (#2107)" "" "$got"

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

echo "== branch detection (#2107) =="

# A git worktree with no explicit -b mints a branch named worktree-agent-<hash>
# — the ordinary state at claim time, since CONTRIBUTING says to claim before
# the feature branch exists. detect_branch must never hand that string back:
# it means nothing to a reader, and it is guaranteed to differ from whatever
# branch exists by the time --release runs, which is the generator of #2107's
# phantom-lock bug.
if command -v git >/dev/null 2>&1; then
  BRANCH_TMP="$(mktemp -d)"
  git -C "$BRANCH_TMP" init -q -b main
  git -C "$BRANCH_TMP" config user.email test@example.com
  git -C "$BRANCH_TMP" config user.name test
  git -C "$BRANCH_TMP" commit -q --allow-empty -m init

  # Runs with stdout+stderr merged and prefixed on failure, so a script that
  # does not even understand --detect-branch (e.g. pre-#2107 main) reads as
  # an error/empty value here rather than silently passing an
  # expect_not_contains check against "".
  detect_branch_in() {
    local out rc
    out="$( ( cd "$1" && env -u CLAIM_BRANCH "$CLAIM" --detect-branch ) 2>&1 )"; rc=$?
    [ "$rc" -eq 0 ] || out="ERROR(rc=$rc): $out"
    printf '%s' "$out"
  }

  git -C "$BRANCH_TMP" checkout -q -b worktree-agent-a8474dee8fcab1732
  got="$(detect_branch_in "$BRANCH_TMP")"
  case "$got" in
    worktree-agent-*|""|ERROR*)
      fail "worktree-agent-* is never recorded as the branch" "got «$got»" ;;
    *) pass "worktree-agent-* is never recorded as the branch" ;;
  esac

  got2="$(detect_branch_in "$BRANCH_TMP")"
  expect_eq "…and the fallback is stable across repeated calls (claim, then release)" "$got" "$got2"

  # An upstream configured before the feature branch exists (rare, but free
  # when present) should win over the path fallback.
  git -C "$BRANCH_TMP" branch --set-upstream-to=main worktree-agent-a8474dee8fcab1732 >/dev/null 2>&1
  got="$(detect_branch_in "$BRANCH_TMP")"
  expect_eq "an upstream branch, if configured, is preferred over the path" "main" "$got"

  # An ordinary feature branch passes through unchanged — only the
  # auto-minted worktree name is special-cased.
  git -C "$BRANCH_TMP" checkout -q -b fix/2107-claim-release-matches-on-clone
  got="$(detect_branch_in "$BRANCH_TMP")"
  expect_eq "an ordinary branch name passes through unchanged" \
    "fix/2107-claim-release-matches-on-clone" "$got"

  rm -rf "$BRANCH_TMP"
else
  printf 'git not installed — skipping branch-detection tests\n'
fi

echo "== the gate: who gets refused =="

# The parse tests above prove the script can SEE a claim. These prove it ACTS
# on one. That is the load-bearing half: everything else can be perfect and the
# convention is still just a note if `claim` posts anyway. Exit 3 is the lock.
#
# report_claims reads the TSV the parser emits, so feed it that directly and
# pin the identity with CLAIM_CLONE/CLAIM_BRANCH rather than depending on where
# the test happens to be checked out.
report() { # <clone> <branch> <tsv>  -> prints table, returns the gate verdict
  printf '%s' "$3" | CLAIM_CLONE="$1" CLAIM_BRANCH="$2" "$CLAIM" --report
}

# tsv <clone> <branch> <createdAt> — one row in the parser's output shape.
tsv() { printf '%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "Srbino" "https://x/#c"; }

NOW="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
if date --version >/dev/null 2>&1; then
  LONG_AGO="$(date -u -d '30 days ago' +'%Y-%m-%dT%H:%M:%SZ')"
else
  LONG_AGO="$(date -u -v-30d +'%Y-%m-%dT%H:%M:%SZ')"
fi

out="$(report crewship_1 fix/alpha "")"; rc=$?
expect_eq "an unclaimed issue does not block" "0" "$rc"
expect_contains "…and says so" "$out" "no active claim"

out="$(report crewship_1 fix/alpha "$(tsv crewship_2 fix/beta "$NOW")")"; rc=$?
expect_eq "another clone's live claim BLOCKS (exit 3)" "3" "$rc"
expect_contains "…and is labelled as someone else's" "$out" "HELD BY ANOTHER SESSION"

# Same clone, different branch: still another session. Ten worktrees of
# crewship_3 run at once, so the clone alone is not the identity.
out="$(report crewship_3 fix/alpha "$(tsv crewship_3 fix/beta "$NOW")")"; rc=$?
expect_eq "same clone, other branch still BLOCKS" "3" "$rc"

out="$(report crewship_1 fix/alpha "$(tsv crewship_1 fix/alpha "$NOW")")"; rc=$?
expect_eq "your own claim does not block you" "0" "$rc"
expect_contains "…and is labelled yours" "$out" "yours"

# The documented, deliberate choice: STALE is a label, not an amnesty. "Old"
# and "abandoned" are not the same thing, so it still costs a --force.
out="$(report crewship_1 fix/alpha "$(tsv crewship_2 fix/beta "$LONG_AGO")")"; rc=$?
expect_eq "a STALE claim still BLOCKS (label, not amnesty)" "3" "$rc"
expect_contains "…and is labelled stale" "$out" "STALE"

# One live foreign claim is enough, even when your own is in the thread too.
out="$(report crewship_1 fix/alpha "$(tsv crewship_1 fix/alpha "$NOW")
$(tsv crewship_2 fix/beta "$NOW")")"; rc=$?
expect_eq "yours + someone else's still BLOCKS" "3" "$rc"

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

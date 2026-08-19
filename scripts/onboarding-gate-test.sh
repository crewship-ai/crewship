#!/usr/bin/env bash
# onboarding-gate-test.sh — asserts that the onboarding first-run journey is
# gated on PULL REQUESTS, not only nightly.
#
# WHY THIS FILE EXISTS
#
# e2e/onboarding-wizard.spec.ts walks a brand-new user from an empty database
# to their first click. That last click used to land on /crews/agents/<id>/chat,
# a route the /crews redesign deleted — and the spec ASSERTED that dead route,
# so it passed only while the bug was present. Both halves are now repaired.
#
# The repair is only worth what its gate is worth. The spec needs a
# never-bootstrapped database, so it cannot run under playwright.config.ts (its
# globalSetup logs in as a seeded demo user and its storageState would let the
# wizard skip the bootstrap step the spec exists to test). It therefore runs
# under playwright.fresh.config.ts in a dedicated job. For most of its life that
# job was nightly-only, which meant a pull request could reintroduce the 404 and
# go green — the regression would be found the next morning, already on main.
#
# The failure this file prevents is not the 404 coming back; the spec catches
# that. It is the GATE quietly going away: someone drops the job while cutting
# CI minutes, or moves it back behind `schedule:`, and every check stays green
# while the flagship repair is unguarded again. That is the same defect in a
# better disguise, and nothing else in the repo would notice.
#
# scripts/security-yml-test.sh names this exact hole in its own header — "this
# verifies the LOGIC the job would run, not that the job runs" — and says the
# guarding belongs in a workflow-level lint. This is that lint, for the one job
# whose whole value is which trigger it sits behind.
#
# Usage: bash scripts/onboarding-gate-test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$SCRIPT_DIR/.."
CI_WORKFLOW="$ROOT/.github/workflows/ci.yml"
FRESH_CONFIG="$ROOT/playwright.fresh.config.ts"
MAIN_CONFIG="$ROOT/playwright.config.ts"
SPEC="$ROOT/e2e/onboarding-wizard.spec.ts"

FAILURES=0
pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1"; FAILURES=$((FAILURES + 1)); }

for f in "$CI_WORKFLOW" "$FRESH_CONFIG" "$MAIN_CONFIG" "$SPEC"; do
  if [ ! -f "$f" ]; then
    echo "FATAL: missing $f" >&2
    echo "       (renamed or deleted? then this gate is covering nothing — fix the path here)" >&2
    exit 1
  fi
done

echo "onboarding gate"

# ── 1. ci.yml runs on pull_request ───────────────────────────────────────────
# Everything below is only meaningful if the workflow carrying the job is a PR
# workflow at all. Scoped to the `on:` block so a `pull_request` mentioned in a
# job comment cannot satisfy it.
on_block() {
  awk '
    /^on:[[:space:]]*$/ { inside = 1; next }
    inside && /^[A-Za-z]/ { inside = 0 }
    inside { print }
  ' "$CI_WORKFLOW"
}

if printf '%s\n' "$(on_block)" | grep -qE '^[[:space:]]{2}pull_request:'; then
  pass "ci.yml triggers on pull_request"
else
  fail "ci.yml no longer triggers on pull_request — every gate in it, not just this one, has stopped gating"
fi

# ── 2. A job in ci.yml actually runs the fresh config ────────────────────────
# The fresh config is the ONLY way this spec runs: playwright.config.ts ignores
# it (asserted in 4) and positional CLI filters do not override testIgnore.
if grep -qE 'playwright test --config=playwright\.fresh\.config\.ts' "$CI_WORKFLOW"; then
  pass "ci.yml invokes playwright.fresh.config.ts"
else
  fail "no step in ci.yml runs playwright.fresh.config.ts — the onboarding journey is not gated on PRs"
fi

# ── 3. The job is not disabled or demoted in place ───────────────────────────
# A job can be kept, and kept green, by never running: `if: false`, or an `if:`
# that excludes pull_request. Extract the job body and check it carries no
# job-level `if:` at all. A conditional gate is not a gate, and if one is ever
# genuinely needed, this test should be updated deliberately rather than by
# someone discovering it went red.
onboarding_job() {
  awk '
    /^  onboarding-journey:[[:space:]]*$/ { inside = 1; print; next }
    inside && /^  [A-Za-z0-9_-]+:[[:space:]]*$/ { inside = 0 }
    inside { print }
  ' "$CI_WORKFLOW"
}
JOB_SRC="$(onboarding_job)"

if [ -z "$JOB_SRC" ]; then
  fail "ci.yml has no 'onboarding-journey' job — if it was renamed, rename it here too"
else
  pass "ci.yml defines the onboarding-journey job"

  if printf '%s\n' "$JOB_SRC" | grep -qE '^    if:'; then
    fail "onboarding-journey carries a job-level 'if:' — it can now skip on a PR while still reporting green"
  else
    pass "onboarding-journey has no job-level 'if:' (cannot skip itself on a PR)"
  fi

  # The fresh-DB precondition step is what stops a silent pass: the spec skips
  # itself when needs_bootstrap is false, and a fully skipped Playwright run
  # exits 0. Without this assertion the job reports green having asserted
  # nothing — the exact shape of the bug the spec was written to kill.
  if printf '%s\n' "$JOB_SRC" | grep -q 'needs_bootstrap'; then
    pass "onboarding-journey asserts the fresh-DB precondition"
  else
    fail "onboarding-journey no longer checks needs_bootstrap — the spec can now skip itself and still report green"
  fi

  # Without this the Launch step live-calls api.anthropic.com and the gate
  # flakes on someone else's uptime.
  if printf '%s\n' "$JOB_SRC" | grep -q 'CREWSHIP_E2E_SKIP_TOKEN_PROBE'; then
    pass "onboarding-journey stubs the Anthropic token probe"
  else
    fail "onboarding-journey no longer sets CREWSHIP_E2E_SKIP_TOKEN_PROBE — the Launch step will call api.anthropic.com for real"
  fi
fi

# ── 4. The two configs still disagree, in the direction that matters ─────────
# The fresh config must select the spec, and the main config must ignore it.
# If the main config ever stopped ignoring it, the spec would run under a
# globalSetup that hard-fails on a fresh DB (no demo user) — and the usual
# repair for that noise is to delete the spec.
if grep -q 'onboarding-wizard\.spec\.ts' "$FRESH_CONFIG"; then
  pass "playwright.fresh.config.ts selects onboarding-wizard.spec.ts"
else
  fail "playwright.fresh.config.ts no longer matches onboarding-wizard.spec.ts — the PR job would run an empty suite and pass"
fi

if grep -q 'onboarding-wizard\.spec\.ts' "$MAIN_CONFIG"; then
  pass "playwright.config.ts still excludes onboarding-wizard.spec.ts"
else
  fail "playwright.config.ts no longer lists onboarding-wizard.spec.ts in testIgnore — it will run under a globalSetup that cannot survive a fresh DB"
fi

# ── 5. The spec still asserts the repaired route ─────────────────────────────
# The narrowest possible check on the thing this whole gate protects. The spec
# passed for months while asserting /crews/agents/<id>/chat; a regression that
# re-points it at any dead route would otherwise be invisible here.
if grep -qE '\\/chat\\/\[\^/\]\+|/chat/' "$SPEC"; then
  pass "onboarding-wizard.spec.ts asserts a /chat/<slug> landing"
else
  fail "onboarding-wizard.spec.ts no longer asserts a /chat/ landing — check what the wizard's last click is being pinned to now"
fi

# The dead route, assembled from parts so this file does not report itself if it
# is ever pulled into the repo-wide scan in
# app/(onboarding)/onboarding/__tests__/dead-agent-routes.test.ts.
DEAD_ROUTE="/crews/""agents/"

# Backslash-escaped slashes are unescaped FIRST. The assertion this spec used to
# carry was a regex literal — waitForURL(/\/crews\/agents\//) — in which the dead
# route never appears as a plain substring. A literal search reports green on the
# exact code the gate exists to catch, which is how that assertion survived
# review in the first place. Unescaping can only add matches, never hide one.
#
# Comment lines are dropped, not the whole prose: the spec now explains the
# repair in a comment that names the old route, and naming the mistake is the
# point of that comment. An assertion on it is a different matter.
spec_code() {
  sed 's|\\/|/|g' "$SPEC" | grep -vE '^[[:space:]]*(//|\*|/\*)'
}

if spec_code | grep -qF "$DEAD_ROUTE"; then
  fail "onboarding-wizard.spec.ts drives the deleted $DEAD_ROUTE family outside a comment — the repaired assertion looks reverted"
else
  pass "onboarding-wizard.spec.ts does not drive the dead route in code"
fi

echo
if [ "$FAILURES" -gt 0 ]; then
  echo "✗ $FAILURES check(s) failed"
  exit 1
fi
echo "✓ onboarding journey is gated on pull requests"

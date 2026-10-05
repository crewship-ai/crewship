#!/usr/bin/env bash
# restricted-shell-gate-test.sh — asserts that the restricted-member shell
# contract is gated on PULL REQUESTS, not only nightly.
#
# WHY THIS FILE EXISTS
#
# e2e/restricted-member-shell.spec.ts pins the #2861 part-1 shell repair: a
# session with a restricted membership must not open a realtime socket, must
# not loop on /ws-token, must not show the "Reconnecting…" banner and must
# send no request the server's restricted allowlist would 404. The spec is
# fully deterministic — every /api response is a page.route fixture, so it
# needs no seeded server, no auth state and no provider.
#
# It was classified into GATE_SPECS of nightly-e2e.yml only. PR #2874 (the
# repair itself) went green with the spec executed ZERO times: run
# 37212203211, job 111468832143 ran pr-contract (2), credential-reveal (8)
# and provider-pools (5) — nothing else. A follow-up PR could reintroduce the
# reconnect loop and every PR check would stay green; the regression would be
# found the next nightly, already on main. That is the exact shape
# scripts/onboarding-gate-test.sh was written to prevent, and this file is
# its sibling for this gate.
#
# The failure this file prevents is not the loop coming back; the spec catches
# that. It is the GATE quietly going away: the ci.yml step is dropped, or the
# config's testMatch drifts onto another spec — the step then still runs and
# passes while asserting nothing about the restricted shell (a testMatch that
# matches nothing at all fails with "no tests found", but only after the job
# has paid for the browser, and naming the drift here is cheaper). Nothing
# else in the repo would notice.
#
# Usage: bash scripts/restricted-shell-gate-test.sh [root-dir]
#
# The optional root-dir points the same parser at another tree — the way
# scripts/pr-image-build-paths-test.sh proves its guard can go red. The
# pristine PR-head tree (no playwright.restricted.config.ts) is one such
# fixture: the guard must fail on it, because that is the exact state PR
# #2874 shipped in.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="${1:-$SCRIPT_DIR/..}"
CI_WORKFLOW="$ROOT/.github/workflows/ci.yml"
RESTRICTED_CONFIG="$ROOT/playwright.restricted.config.ts"
SPEC="$ROOT/e2e/restricted-member-shell.spec.ts"

FAILURES=0
pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1"; FAILURES=$((FAILURES + 1)); }

for f in "$CI_WORKFLOW" "$RESTRICTED_CONFIG" "$SPEC"; do
  if [ ! -f "$f" ]; then
    echo "FATAL: missing $f" >&2
    echo "       (renamed or deleted? then this gate is covering nothing — fix the path here)" >&2
    exit 1
  fi
done

echo "restricted-member-shell PR gate"

# Comment strippers: every selection check below must read CODE, not prose. A
# YAML comment naming the config, or a TS comment naming the spec, would
# otherwise satisfy a bare grep — a guard that passes on its own changelog.
yml_code() { grep -vE '^[[:space:]]*#' "$1"; }
ts_code() { sed -E 's#(^|[[:space:]])//.*$##' "$1" | grep -vE '^[[:space:]]*(\*|/\*)'; }

# ── 1. ci.yml runs on pull_request ───────────────────────────────────────────
# Everything below is only meaningful if the workflow carrying the job is a PR
# workflow at all. Scoped to the `on:` block so a `pull_request` mentioned in a
# job comment cannot satisfy it.
#
# Every condition below reads its input through process substitution
# (`grep -q ... < <(producer)`), never `producer | grep -q`. This script runs
# under `set -o pipefail`, and a `grep -q` that exits on first match closes the
# pipe while the producer is still writing: on a file the size of ci.yml the
# producer then dies on SIGPIPE (exit 141) and pipefail turns a MATCH into a
# failed condition — a race that once let this exact check flip between two
# runs on identical files. Process substitution is not a pipeline, so grep's
# own exit status is the only thing tested.
on_block() {
  awk '
    /^on:[[:space:]]*$/ { inside = 1; next }
    inside && /^[A-Za-z]/ { inside = 0 }
    inside { print }
  ' "$CI_WORKFLOW"
}

if grep -qE '^[[:space:]]{2}pull_request:' < <(on_block); then
  pass "ci.yml triggers on pull_request"
else
  fail "ci.yml no longer triggers on pull_request — every gate in it, not just this one, has stopped gating"
fi

# ── 2. A step in ci.yml actually runs the restricted config ──────────────────
# The isolated-fixture configs (reveal, pool-ui, restricted) are the only PR
# executions of these specs; nightly GATE_SPECS is a different workflow.
if grep -qE 'playwright test --config=playwright\.restricted\.config\.ts' < <(yml_code "$CI_WORKFLOW"); then
  pass "ci.yml invokes playwright.restricted.config.ts"
else
  fail "no step in ci.yml runs playwright.restricted.config.ts — the restricted shell contract is nightly-only again (PR #2874 shipped with zero executions)"
fi

# ── 3. The browser job is not disabled or demoted in place ───────────────────
# A job can be kept, and kept green, by never running: `if: false`, or an `if:`
# that excludes pull_request. Extract the job body and check it carries no
# job-level `if:` beyond the shared code-changes filter.
browser_job() {
  awk '
    /^  playwright-pr:[[:space:]]*$/ { inside = 1; print; next }
    inside && /^  [A-Za-z0-9_-]+:[[:space:]]*$/ { inside = 0 }
    inside { print }
  ' "$CI_WORKFLOW"
}
JOB_SRC="$(browser_job)"

if [ -z "$JOB_SRC" ]; then
  fail "ci.yml has no 'playwright-pr' job — if it was renamed, rename it here too"
else
  pass "ci.yml defines the playwright-pr job"

  if grep -E '^    if:' < <(printf '%s\n' "$JOB_SRC") | grep -vFxq "    if: needs.changes.outputs.code == 'true'"; then
    fail "playwright-pr carries a job-level 'if:' — it can now skip on a PR while still reporting green"
  else
    pass "playwright-pr runs for code changes"
  fi

  # The invocation must live inside that job, not only somewhere in the file —
  # and the STEP carrying it must not be defused in place. A step can be kept,
  # and kept green, by `if: false` or `continue-on-error: true` while a
  # job-wide grep still sees the command. Extract the step block that runs the
  # config and inspect it; comments are stripped so a commented-out defusal
  # cannot trip the check either.
  restricted_step() {
    printf '%s\n' "$JOB_SRC" | awk '
      /^      - / { if (block ~ /playwright\.restricted\.config\.ts/) print block; block = "" }
      { block = block $0 "\n" }
      END { if (block ~ /playwright\.restricted\.config\.ts/) print block }
    '
  }
  STEP_SRC="$(restricted_step | grep -vE '^[[:space:]]*#')"
  if [ -n "$STEP_SRC" ]; then
    pass "playwright-pr runs the restricted config"
    if grep -qE '^[[:space:]]+continue-on-error:' < <(printf '%s\n' "$STEP_SRC"); then
      fail "the restricted-shell step sets continue-on-error — it can now fail while the job stays green"
    else
      pass "restricted-shell step has no continue-on-error"
    fi
    if grep -qE '^        if:' < <(printf '%s\n' "$STEP_SRC"); then
      fail "the restricted-shell step carries a step-level 'if:' — it can now skip while the job stays green"
    else
      pass "restricted-shell step has no step-level if:"
    fi
  else
    fail "playwright.restricted.config.ts is not invoked inside playwright-pr — a stray step elsewhere does not gate this PR job"
  fi
fi

# ── 4. The config still selects the spec ─────────────────────────────────────
# If testMatch drifts onto another spec, the step keeps running green while
# asserting nothing about the restricted shell; a testMatch matching nothing
# fails late, with "no tests found", after the browser job already paid.
if grep -q 'restricted-member-shell\.spec\.ts' < <(ts_code "$RESTRICTED_CONFIG"); then
  pass "playwright.restricted.config.ts selects restricted-member-shell.spec.ts"
else
  fail "playwright.restricted.config.ts no longer matches restricted-member-shell.spec.ts in code — the PR step would assert nothing about the restricted shell"
fi

# ── 5. The fixture server the config's webServer needs still exists ──────────
# The isolated configs serve the static export from a tiny node server; if the
# command points at a renamed file the whole step fails at startup, but if the
# entry is edited away entirely the config silently has no webServer at all.
# First line only, without `| head -1`: head exits after one line and can
# SIGPIPE the sed under pipefail (same race as the greps above).
SERVER="$(ts_code "$RESTRICTED_CONFIG" | sed -nE 's/.*command: "([^"]+)".*/\1/p')"
SERVER="${SERVER%%$'\n'*}"
SERVER="${SERVER#node }"
if [ -n "$SERVER" ] && [ -f "$ROOT/$SERVER" ]; then
  pass "fixture server for the restricted config exists ($SERVER)"
else
  fail "playwright.restricted.config.ts webServer command '$SERVER' does not point at an existing file — check e2e/serve-restricted-ui.mjs"
fi

# ── 6. The spec still asserts the shell contract it exists for ────────────────
# The narrowest possible check on the thing this whole gate protects. The
# spec's own header COMMENT names the banner and the allowlist, so these
# greps read code only — a diluted spec must not pass on its changelog.
spec_code() { ts_code "$SPEC"; }
# Full assertion strings, not bare identifiers: 'sockets' alone would survive
# deletion of the expect that actually pins the contract.
for assertion in \
  'Reconnecting|Connection lost' \
  'expect(sockets).toEqual([])' \
  'expect(forbidden).toEqual([])'; do
  if grep -qF "$assertion" < <(spec_code); then
    pass "spec asserts $assertion"
  else
    fail "restricted-member-shell.spec.ts no longer asserts $assertion in code — the shell contract it was written to pin is gone"
  fi
done

if grep -qF 'for (const width of [1280, 390])' < <(spec_code) && grep -qF 'at ${width}px' < <(spec_code); then
  pass "spec keeps both viewport scenarios (1280/390)"
else
  fail "restricted-member-shell.spec.ts no longer runs both viewport scenarios (1280 and 390)"
fi

echo
if [ "$FAILURES" -gt 0 ]; then
  echo "✗ $FAILURES check(s) failed"
  exit 1
fi
echo "✓ restricted-member-shell is gated on pull requests"

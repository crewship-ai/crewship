#!/usr/bin/env bash
# changelog-guard-test.sh — unit tests for the gate embedded in
# .github/workflows/changelog-guard.yml.
#
# That gate is the only thing standing between a user-visible PR and a release
# note that does not exist (#2086: fifteen such PRs in one window). It ships as
# YAML, it only ever executes on a pull request, and a guard that reports the
# wrong verdict reports it as a green check — so nothing about a normal run
# tells you it has stopped working.
#
# It already failed that way once. The watched-paths list was materialised with
#
#     mapfile -t watched < <(git diff … "origin/$BASE_REF...HEAD" -- …)
#
# and a command inside a process substitution has its exit status DISCARDED —
# `set -euo pipefail` never sees it. Give it a base ref git cannot resolve and
# the diff dies, `watched` comes back empty, `visible` comes back empty, and the
# step prints "No user-visible source files changed — nothing to chronicle" and
# exits 0. A green check reflecting a skip rather than a verdict, which is the
# exact failure this workflow exists to stop. `a broken base ref fails the step`
# below is the regression test for it; it fails on the pre-fix workflow.
#
# The script is EXTRACTED VERBATIM from the workflow rather than re-typed here.
# A copy would drift and then pass happily while production stayed broken.
# Extraction is scoped to the named step, so a block moved elsewhere is not
# found at all, and a missing block is fatal rather than "0 tests ok".
#
# Each case runs the real script against a throwaway git repository with a real
# `refs/remotes/origin/main`, so the `git diff` under test is a real one.
#
# Known limit, stated rather than discovered later: this verifies the logic the
# step would run, not that the step runs. Deleting the job or its trigger leaves
# every check here green — that belongs in branch protection.
#
# Usage: bash scripts/changelog-guard-test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKFLOW="$SCRIPT_DIR/../.github/workflows/changelog-guard.yml"
STEP_NAME="Check for a changelog entry"

FAILURES=0
pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1"; FAILURES=$((FAILURES + 1)); }

# ---------------------------------------------------------------------------
# Extract the step's `run:` body, dedented out of the YAML block scalar.
# ---------------------------------------------------------------------------
GUARD_SRC="$(awk -v step="$STEP_NAME" '
  index($0, "- name: " step) { inside = 1; next }
  inside && /^      - / { inside = 0 }
  inside && /^        run: \|[[:space:]]*$/ { capture = 1; next }
  capture && /^[[:space:]]*$/ { print ""; next }
  capture && !/^ {10}/ { capture = 0; inside = 0 }
  capture { sub(/^ {10}/, ""); print }
' "$WORKFLOW")"

if [ -z "$GUARD_SRC" ]; then
  echo "FATAL: could not extract the '$STEP_NAME' run block from $WORKFLOW" >&2
  echo "       (step renamed, or the block scalar reindented?) — this test is" >&2
  echo "       covering nothing, which is worse than failing. Fix the name here." >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# Static: every top-level tree a user can observe is on the pathspec list.
# ---------------------------------------------------------------------------
# The behavioural cases below prove each tree is gated, but they cannot say why
# a tree is absent. This loop names them, so dropping one fails with the name.
echo "watched trees:"
for tree in internal/api cmd/crewship app components lib hooks stores; do
  if printf '%s\n' "$GUARD_SRC" | grep -qF "'$tree'"; then
    pass "'$tree' is on the pathspec list"
  else
    fail "'$tree' is NOT on the pathspec list — a change confined to it ships unchronicled"
  fi
done

# ---------------------------------------------------------------------------
# Fixture repository
# ---------------------------------------------------------------------------
FIXTURE="$(mktemp -d)"
trap 'rm -rf "$FIXTURE"' EXIT

git init -q -b main "$FIXTURE"
git -C "$FIXTURE" config user.email guard@test.invalid
git -C "$FIXTURE" config user.name "Guard Test"

mkdir -p "$FIXTURE"/{internal/api,cmd/crewship,app,components/__tests__,lib,hooks/__tests__,stores,docs}
for f in \
  README.md docs/guide.mdx \
  internal/api/handler.go internal/api/handler_test.go \
  cmd/crewship/main.go \
  app/page.tsx \
  components/thing.tsx components/__tests__/thing.test.tsx \
  lib/api-error.ts \
  hooks/use-chat.ts hooks/__tests__/use-chat.test.tsx \
  stores/session.ts
do
  echo "base" > "$FIXTURE/$f"
done

# A CHANGELOG shaped like the real one: an `## [Unreleased]` section holding
# `###` sub-headings, and a shipped version below it. The `##`-vs-`###`
# distinction matters — `### Fixed` must NOT terminate the Unreleased section,
# and `## [0.1.0]` must.
cat > "$FIXTURE/CHANGELOG.md" <<'CHANGELOG'
# Changelog

## [Unreleased]

### Fixed

- **An existing unreleased entry.** Details.

## [0.1.0] — 2026-01-01

### Added

- **A shipped entry.** Details.
CHANGELOG

# Pad the shipped section past the 64 KiB pipe buffer. This is load-bearing,
# not filler.
#
# The guard feeds the whole file into a pipe. Any matcher that stops reading at
# the first hit — `grep -q`, an `awk` that `exit`s when the section ends —
# closes that pipe while `printf` still has hundreds of KB queued. printf takes
# SIGPIPE, `pipefail` reports 141, and a successful match is read as a failure:
# the guard declares `## [Unreleased]` missing from a file that plainly has it,
# and fails every PR.
#
# A small fixture hides this completely — the buffer swallows the file, printf
# finishes, every case passes. The real CHANGELOG.md is ~190 KB, so the first
# implementation passed this suite and then failed against the actual
# repository. Keep the padding.
for i in $(seq 1 4000); do
  echo "- **Shipped entry $i.** Details, details, details, details, details." \
    >> "$FIXTURE/CHANGELOG.md"
done

git -C "$FIXTURE" add -A
git -C "$FIXTURE" commit -qm "base"
# A real remote-tracking ref: `origin/$BASE_REF` is what the guard resolves.
git -C "$FIXTURE" update-ref refs/remotes/origin/main "$(git -C "$FIXTURE" rev-parse main)"

# Reset to a fresh PR branch off the base commit.
scenario() { git -C "$FIXTURE" checkout -q -B pr refs/remotes/origin/main; }

touch_file() { echo "changed $RANDOM" >> "$FIXTURE/$1"; }
remove_file() { git -C "$FIXTURE" rm -q "$1"; }
commit_pr() { git -C "$FIXTURE" add -A; git -C "$FIXTURE" commit -qm "pr"; }

# A real release note: a bullet added under `## [Unreleased]`.
add_unreleased_entry() {
  sed -i '/^### Fixed$/a\
\
- **A new unreleased entry.** What changed and why.' "$FIXTURE/CHANGELOG.md"
}
# An edit that is NOT a release note: it lands in the shipped section at the
# bottom of the file, which is what "any change to CHANGELOG.md" used to accept.
edit_released_section() { echo "- **Another shipped entry.** Details." >> "$FIXTURE/CHANGELOG.md"; }
# Whitespace at the bottom of the file — the cheapest way to fake the old gate.
append_blank_line() { printf '\n' >> "$FIXTURE/CHANGELOG.md"; }
# Remove the heading the guard reads.
break_unreleased_heading() {
  sed -i 's/^## \[Unreleased\]$/## [0.2.0] — 2026-02-02/' "$FIXTURE/CHANGELOG.md"
}

GUARD_OUT=""
GUARD_RC=0
# run_guard <base-ref> <actor> <has-skip-label>
run_guard() {
  GUARD_OUT="$(cd "$FIXTURE" && BASE_REF="$1" PR_ACTOR="$2" HAS_SKIP_LABEL="$3" \
    bash -c "$GUARD_SRC" 2>&1)"
  GUARD_RC=$?
}

# expect <label> <want-rc-or-"nonzero"> <base-ref> <actor> <skip-label>
expect() {
  local label="$1" want="$2"
  run_guard "$3" "$4" "$5"
  if [ "$want" = "nonzero" ]; then
    if [ "$GUARD_RC" -ne 0 ]; then pass "$label"; else
      fail "$label (want a non-zero exit, got 0)"
      printf '%s\n' "$GUARD_OUT" | sed 's/^/       | /'
    fi
    return
  fi
  if [ "$GUARD_RC" -eq "$want" ]; then pass "$label"; else
    fail "$label (want exit $want, got $GUARD_RC)"
    printf '%s\n' "$GUARD_OUT" | sed 's/^/       | /'
  fi
}

# ---------------------------------------------------------------------------
# Exemptions
# ---------------------------------------------------------------------------
echo "exemptions:"

scenario; touch_file components/thing.tsx; commit_pr
expect "dependabot[bot] is exempt by actor" 0 main "dependabot[bot]" false
expect "dependabot-preview[bot] is exempt by actor" 0 main "dependabot-preview[bot]" false
expect "the skip-changelog label lets a user-visible PR through" 0 main someone true

# ---------------------------------------------------------------------------
# Nothing to chronicle
# ---------------------------------------------------------------------------
echo "nothing to chronicle:"

scenario; touch_file README.md; touch_file docs/guide.mdx; commit_pr
expect "a docs-only PR needs no entry" 0 main someone false

scenario
touch_file internal/api/handler_test.go
touch_file components/__tests__/thing.test.tsx
touch_file hooks/__tests__/use-chat.test.tsx
commit_pr
expect "a test-only PR inside watched trees needs no entry" 0 main someone false

# ---------------------------------------------------------------------------
# The verdict
# ---------------------------------------------------------------------------
echo "verdict:"

scenario; touch_file components/thing.tsx; add_unreleased_entry; commit_pr
expect "a user-visible PR with an entry under ## [Unreleased] passes" 0 main someone false

scenario; touch_file components/thing.tsx; commit_pr
expect "a user-visible PR with no entry fails" 1 main someone false

# Touching the file is not writing a release note. Both of these satisfied the
# gate before — the first is a plausible drive-by edit, the second is one
# keystroke.
scenario; touch_file components/thing.tsx; edit_released_section; commit_pr
expect "an edit to a shipped version's section is not a release note" 1 main someone false

scenario; touch_file components/thing.tsx; append_blank_line; commit_pr
expect "a whitespace-only change outside ## [Unreleased] is not a release note" 1 main someone false

# …and the label still overrides it, so the tightening cannot wedge anyone.
scenario; touch_file components/thing.tsx; edit_released_section; commit_pr
expect "the label still clears a CHANGELOG edit outside ## [Unreleased]" 0 main someone true

# A guard that cannot find the section it checks must say so, not return a
# verdict it never reached — the same rule as the broken base ref below.
scenario; touch_file components/thing.tsx; break_unreleased_heading; commit_pr
expect "a missing ## [Unreleased] heading fails loudly" 1 main someone false
if ! printf '%s\n' "$GUARD_OUT" | grep -q "no '## \[Unreleased\]' heading"; then
  fail "…and it must name the missing heading rather than blaming the author for a missing entry"
else
  pass "…and it names the missing heading rather than blaming the author"
fi

scenario; remove_file app/page.tsx; commit_pr
expect "deleting a watched file counts as user-visible" 1 main someone false

# One case per unwatched-until-now tree. A #2024-shaped chat/socket fix lands in
# hooks/ with barely a line elsewhere; an error-copy change lands in lib/.
for pair in \
  "hooks/use-chat.ts:a fix confined to hooks/ needs an entry" \
  "lib/api-error.ts:a fix confined to lib/ needs an entry" \
  "stores/session.ts:a fix confined to stores/ needs an entry"
do
  scenario; touch_file "${pair%%:*}"; commit_pr
  expect "${pair#*:}" 1 main someone false
done

# ---------------------------------------------------------------------------
# The guard must not fail open
# ---------------------------------------------------------------------------
echo "fail-closed:"

# `origin/nonexistent-branch...HEAD` is not resolvable, so `git diff` dies. The
# step must die with it. Before the fix it printed "nothing to chronicle" and
# exited 0 — a skip wearing a verdict's green tick.
scenario; touch_file components/thing.tsx; commit_pr
expect "a broken base ref fails the step" nonzero nonexistent-branch someone false
if [ "$GUARD_RC" -eq 0 ] && printf '%s\n' "$GUARD_OUT" | grep -q "nothing to chronicle"; then
  echo "       ^ this is the fail-open: a dead 'git diff' read as an empty diff." >&2
fi

# Same, with a user-visible change AND a real entry: the step must still fail
# rather than reaching the "Unreleased section is changed ✓" happy path on a
# diff that never ran.
scenario; touch_file components/thing.tsx; add_unreleased_entry; commit_pr
expect "a broken base ref fails even when CHANGELOG.md is touched" nonzero nonexistent-branch someone false

echo
if [ "$FAILURES" -ne 0 ]; then
  echo "$FAILURES check(s) failed."
  exit 1
fi
echo "All checks passed."

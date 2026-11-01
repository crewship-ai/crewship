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

mkdir -p "$FIXTURE"/{internal/api,cmd/crewship,app,components/__tests__,lib,hooks/__tests__,stores,docs,changelog.d}
for f in \
  README.md docs/guide.mdx \
  internal/api/handler.go internal/api/handler_test.go \
  cmd/crewship/main.go \
  app/page.tsx \
  components/thing.tsx components/__tests__/thing.test.tsx \
  lib/api-error.ts \
  hooks/use-chat.ts hooks/__tests__/use-chat.test.tsx \
  stores/session.ts \
  changelog.d/README.md
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
scenario() { scenario_at refs/remotes/origin/main; }
# …or off a named commit, for the cases where `origin/main` has moved on since
# the PR forked.
scenario_at() { git -C "$FIXTURE" checkout -q -B pr "$1"; }

touch_file() { echo "changed $RANDOM" >> "$FIXTURE/$1"; }
remove_file() { git -C "$FIXTURE" rm -q "$1"; }
commit_pr() { git -C "$FIXTURE" add -A; git -C "$FIXTURE" commit -qm "pr"; }

# A real release note: a bullet added under `## [Unreleased]`.
add_unreleased_entry() {
  sed -i '/^### Fixed$/a\
\
- **A new unreleased entry.** What changed and why.' "$FIXTURE/CHANGELOG.md"
}
# Somebody else's release note, landing on `main` while this PR is open. Worded
# differently from the PR's own on purpose: two PRs writing byte-identical
# entries is a coincidence, and a fixture that relies on one proves less than it
# looks like it does.
add_someone_elses_entry() {
  sed -i '/^### Fixed$/a\
\
- **An entry from a PR that merged while this one was open.** Not ours.' "$FIXTURE/CHANGELOG.md"
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

# A release note written as its own fragment file (#4751). A fragment holds the
# same bullet the PR would otherwise have put under `## [Unreleased]`; queued
# PRs each add a different file, so they no longer conflict on one heading.
add_fragment() {
  mkdir -p "$(dirname "$FIXTURE/$1")"
  printf '%s' "$2" > "$FIXTURE/$1"
}
FRAGMENT_TEXT='- **A new unreleased entry, as a fragment.** What changed and why.
'

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
# Fragments (changelog.d/) — #4751
# ---------------------------------------------------------------------------
# Every PR used to write at the top of `## [Unreleased]`, so any two queued PRs
# conflicted there. A new non-empty `changelog.d/<n>-<slug>.md` is the
# conflict-free alternative; RELEASING.md collects fragments into the section.
echo "fragments:"

scenario; touch_file components/thing.tsx
add_fragment changelog.d/4751-fragment.md "$FRAGMENT_TEXT"; commit_pr
expect "a user-visible PR with a new changelog.d fragment passes" 0 main someone false

scenario; touch_file components/thing.tsx
add_fragment changelog.d/4751-empty.md ""; commit_pr
expect "an empty fragment is not a release note" 1 main someone false
if printf '%s\n' "$GUARD_OUT" | grep -q "changelog.d/4751-empty.md"; then
  pass "…and the error names the empty fragment"
else
  fail "…and the error must name the empty fragment rather than claim nothing was written"
fi

scenario; touch_file components/thing.tsx
add_fragment changelog.d/4751-blank.md "$(printf ' \n\t\n  ')"; commit_pr
expect "a whitespace-only fragment is not a release note" 1 main someone false

scenario; touch_file components/thing.tsx
add_fragment changelog.d/4751-blank.md "$(printf ' \n\t\n  ')"
add_fragment changelog.d/4751-good.md "$FRAGMENT_TEXT"; commit_pr
expect "…but a real fragment next to an empty one still passes" 0 main someone false

scenario; touch_file components/thing.tsx
add_fragment docs/changelog.d/4751-elsewhere.md "$FRAGMENT_TEXT"; commit_pr
expect "a fragment outside the top-level changelog.d is ignored" 1 main someone false

scenario; touch_file components/thing.tsx
add_fragment changelog.d/nested/4751-deep.md "$FRAGMENT_TEXT"; commit_pr
expect "a fragment in a changelog.d subdirectory is ignored" 1 main someone false

scenario; touch_file components/thing.tsx
add_fragment changelog.d/4751-note.txt "$FRAGMENT_TEXT"; commit_pr
expect "a non-.md file in changelog.d is ignored" 1 main someone false

scenario; touch_file components/thing.tsx; touch_file changelog.d/README.md; commit_pr
expect "editing changelog.d/README.md is not a release note" 1 main someone false

scenario; touch_file components/thing.tsx; add_fragment changelog.d/4751-empty.md ""; commit_pr
expect "the skip-changelog label still clears a PR with an empty fragment" 0 main someone true

scenario; touch_file components/thing.tsx; add_unreleased_entry
add_fragment changelog.d/4751-fragment.md "$FRAGMENT_TEXT"; commit_pr
expect "a fragment and an ## [Unreleased] edit together pass" 0 main someone false

scenario; touch_file components/thing.tsx; break_unreleased_heading
add_fragment changelog.d/4751-fragment.md "$FRAGMENT_TEXT"; commit_pr
expect "a fragment does not paper over a missing ## [Unreleased] heading" 1 main someone false

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

scenario; touch_file components/thing.tsx
add_fragment changelog.d/4751-fragment.md "$FRAGMENT_TEXT"; commit_pr
expect "a broken base ref fails even when a fragment is added" nonzero nonexistent-branch someone false

# ---------------------------------------------------------------------------
# The base branch moves under an open PR
# ---------------------------------------------------------------------------
# Every case above forks from an `origin/main` that never moves. Real ones move
# constantly — main took 25 merges in the window this guard was written for —
# and BECAUSE this guard works, what they move by is release notes: each
# user-visible merge writes its own `## [Unreleased]` entry.
#
# Read the section from the base TIP and main's entry alone makes the two sides
# differ, so a PR that touches CHANGELOG.md zero times is waved through with
# "'## [Unreleased]' section is changed by this PR. ✓". These cases pin the
# merge base instead. They are last because they advance `origin/main`.
echo "the base branch moves under the PR:"

BASE_COMMIT="$(git -C "$FIXTURE" rev-parse refs/remotes/origin/main)"

# One merge lands on main, carrying a release note of its own.
git -C "$FIXTURE" checkout -q -B mainmoved "$BASE_COMMIT"
add_someone_elses_entry
git -C "$FIXTURE" commit -qam "another PR merged, with its entry"
git -C "$FIXTURE" update-ref refs/remotes/origin/main "$(git -C "$FIXTURE" rev-parse mainmoved)"

scenario_at "$BASE_COMMIT"; touch_file components/thing.tsx; commit_pr
expect "a PR forked before main moved still needs its own entry" 1 main someone false

scenario_at "$BASE_COMMIT"; touch_file components/thing.tsx; add_unreleased_entry; commit_pr
expect "…and one that did write an entry still passes after main moved" 0 main someone false

# The shape CI actually runs: `actions/checkout` takes `refs/pull/N/merge`, a
# merge of the PR head into the base tip as it stood when GitHub built that ref,
# while `origin/main` is fetched fresh at run time and can be newer. HEAD
# therefore already contains part of main's history — but not the merge that
# landed after it, which is exactly the gap the tip comparison read as the PR's
# own work.
scenario_at "$BASE_COMMIT"; touch_file components/thing.tsx; commit_pr
git -C "$FIXTURE" checkout -q -B mergeref "$BASE_COMMIT"
git -C "$FIXTURE" merge -q --no-ff -m "Merge pr into main" pr
expect "the pull/N/merge ref does not count main's entries as the PR's" 1 main someone false

# The same two shapes for fragments: a fragment another PR merged to main is
# not this PR's release note, and neither is editing or deleting one that is
# already there.
git -C "$FIXTURE" checkout -q -B mainfrag "$(git -C "$FIXTURE" rev-parse refs/remotes/origin/main)"
add_fragment changelog.d/4700-someone-else.md "- **Someone else's fragment.** Not ours.
"
git -C "$FIXTURE" add -A
git -C "$FIXTURE" commit -qm "another PR merged, with its fragment"
git -C "$FIXTURE" update-ref refs/remotes/origin/main "$(git -C "$FIXTURE" rev-parse mainfrag)"

scenario_at "$BASE_COMMIT"; touch_file components/thing.tsx; commit_pr
git -C "$FIXTURE" checkout -q -B mergeref refs/remotes/origin/main
git -C "$FIXTURE" merge -q --no-ff -m "Merge pr into main" pr
expect "a fragment merged to main by another PR is not this PR's entry" 1 main someone false

scenario; touch_file components/thing.tsx; touch_file changelog.d/4700-someone-else.md; commit_pr
expect "editing an existing fragment is not a new release note" 1 main someone false

scenario; touch_file components/thing.tsx; remove_file changelog.d/4700-someone-else.md; commit_pr
expect "deleting an existing fragment is not a release note" 1 main someone false

# Renaming is a delete plus an add under `--no-renames`, so the "added" file
# would otherwise pass as a brand-new note. A PR that removes any fragment gets
# no credit for fragments it adds — unless it also changes ## [Unreleased],
# which is the release PR collecting fragments into the file.
scenario; touch_file components/thing.tsx
git -C "$FIXTURE" mv changelog.d/4700-someone-else.md changelog.d/4751-renamed.md; commit_pr
expect "renaming an existing fragment is not a new release note" 1 main someone false

scenario; touch_file components/thing.tsx; remove_file changelog.d/4700-someone-else.md
add_fragment changelog.d/4751-fragment.md "$FRAGMENT_TEXT"; commit_pr
expect "a new fragment does not count while the PR deletes another" 1 main someone false
if printf '%s\n' "$GUARD_OUT" | grep -q "changelog.d/4700-someone-else.md"; then
  pass "…and the error names the deleted fragment"
else
  fail "…and the error must name the deleted fragment"
fi

scenario; touch_file components/thing.tsx; remove_file changelog.d/4700-someone-else.md
add_unreleased_entry; commit_pr
expect "the release collect shape (fragments deleted, ## [Unreleased] changed) passes" 0 main someone false

# A symlink's blob is its target path, which is never empty — so without a
# mode check any link, dangling or not, reads as a written release note.
scenario; touch_file components/thing.tsx
ln -s README.md "$FIXTURE/changelog.d/4751-link.md"; commit_pr
expect "a symlinked fragment is not a release note" 1 main someone false

scenario; touch_file components/thing.tsx
ln -s does-not-exist.md "$FIXTURE/changelog.d/4751-dangling.md"; commit_pr
expect "a dangling symlinked fragment is not a release note" 1 main someone false

# ---------------------------------------------------------------------------
# Release-time collection: scripts/changelog-collect.py
# ---------------------------------------------------------------------------
# The other half of the fragment mechanism. A fragment the guard accepted but
# the release never collected would be a release note that does not exist —
# the exact failure this guard was written for — so the collector is pinned
# here, in the suite CI already runs.
echo "collect fragments into CHANGELOG.md:"

COLLECT="$SCRIPT_DIR/changelog-collect.py"
CROOT="$(mktemp -d)"
trap 'rm -rf "$FIXTURE" "$CROOT"' EXIT

collect_fixture() {
  rm -rf "$CROOT"; mkdir -p "$CROOT/changelog.d/nested"
  cat > "$CROOT/CHANGELOG.md" <<'CL'
# Changelog

## [Unreleased]

- **Existing entry.** Stays below the collected ones.

## [0.1.0] — 2026-01-01

- **Shipped.** Untouched.
CL
  echo "Fragment format notes." > "$CROOT/changelog.d/README.md"
  echo "- **Nested, not a fragment.**" > "$CROOT/changelog.d/nested/1-x.md"
  echo "- **Not markdown.**" > "$CROOT/changelog.d/2-x.txt"
  printf -- '- **Older fix (#90).** Details.\n' > "$CROOT/changelog.d/90-older.md"
  printf -- '- **Newer fix (#4751).** Details.\n- **Second bullet of the same PR.**\n\n' > "$CROOT/changelog.d/4751-newer.md"
}

collect_fixture
if out="$(python3 -I "$COLLECT" --root "$CROOT" 2>&1)"; then
  pass "collect exits 0"
else
  fail "collect exits 0"; printf '%s\n' "$out" | sed 's/^/       | /'
fi
want='# Changelog

## [Unreleased]

- **Newer fix (#4751).** Details.
- **Second bullet of the same PR.**
- **Older fix (#90).** Details.

- **Existing entry.** Stays below the collected ones.

## [0.1.0] — 2026-01-01

- **Shipped.** Untouched.'
if [ "$(cat "$CROOT/CHANGELOG.md" 2>/dev/null)" = "$want" ]; then
  pass "fragments land at the top of ## [Unreleased], newest number first"
else
  fail "fragments land at the top of ## [Unreleased], newest number first"
  diff <(printf '%s\n' "$want") "$CROOT/CHANGELOG.md" | sed 's/^/       | /'
fi
if [ ! -e "$CROOT/changelog.d/90-older.md" ] && [ ! -e "$CROOT/changelog.d/4751-newer.md" ]; then
  pass "collected fragments are removed"
else
  fail "collected fragments are removed"
fi
if [ -e "$CROOT/changelog.d/README.md" ] && [ -e "$CROOT/changelog.d/nested/1-x.md" ] \
   && [ -e "$CROOT/changelog.d/2-x.txt" ]; then
  pass "README, nested files and non-.md files are left alone"
else
  fail "README, nested files and non-.md files are left alone"
fi

before="$(cat "$CROOT/CHANGELOG.md")"
if python3 -I "$COLLECT" --root "$CROOT" >/dev/null 2>&1 && [ "$(cat "$CROOT/CHANGELOG.md")" = "$before" ]; then
  pass "a second run with no fragments changes nothing"
else
  fail "a second run with no fragments changes nothing"
fi

collect_fixture
python3 -I "$COLLECT" --root "$CROOT" --dry-run >/dev/null 2>&1
if [ -e "$CROOT/changelog.d/90-older.md" ] && ! grep -q "Older fix" "$CROOT/CHANGELOG.md"; then
  pass "--dry-run changes no file"
else
  fail "--dry-run changes no file"
fi

# Refusals leave everything as it was: a half-collected release is worse than
# none.
collect_fixture; printf ' \n\t\n' > "$CROOT/changelog.d/91-empty.md"
before="$(cat "$CROOT/CHANGELOG.md")"
if python3 -I "$COLLECT" --root "$CROOT" >/dev/null 2>&1; then
  fail "an empty fragment makes collect refuse"
elif [ "$(cat "$CROOT/CHANGELOG.md")" = "$before" ] && [ -e "$CROOT/changelog.d/90-older.md" ]; then
  pass "an empty fragment makes collect refuse, changing nothing"
else
  fail "an empty fragment makes collect refuse, changing nothing"
fi

collect_fixture; sed -i 's/^## \[Unreleased\]$/## [0.2.0]/' "$CROOT/CHANGELOG.md"
if python3 -I "$COLLECT" --root "$CROOT" >/dev/null 2>&1; then
  fail "a CHANGELOG.md without ## [Unreleased] makes collect refuse"
elif [ -e "$CROOT/changelog.d/90-older.md" ]; then
  pass "a CHANGELOG.md without ## [Unreleased] makes collect refuse, keeping fragments"
else
  fail "a CHANGELOG.md without ## [Unreleased] makes collect refuse, keeping fragments"
fi

# collect_refuses <label>: collect must exit non-zero, without a Python
# traceback, leaving CHANGELOG.md and the regular fragments untouched.
collect_refuses() {
  local before out rc
  before="$(cat "$CROOT/CHANGELOG.md")"
  out="$(python3 -I "$COLLECT" --root "$CROOT" 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ]; then
    fail "$1 (collect exited 0)"
  elif printf '%s\n' "$out" | grep -q "Traceback"; then
    fail "$1 (collect crashed with a traceback)"; printf '%s\n' "$out" | sed 's/^/       | /'
  elif [ "$(cat "$CROOT/CHANGELOG.md")" != "$before" ] || [ ! -e "$CROOT/changelog.d/90-older.md" ]; then
    fail "$1 (collect changed files before refusing)"
  else
    pass "$1"
  fi
}

collect_fixture; ln -s 90-older.md "$CROOT/changelog.d/92-link.md"
collect_refuses "a symlinked fragment makes collect refuse, changing nothing"

collect_fixture; ln -s does-not-exist.md "$CROOT/changelog.d/93-dangling.md"
collect_refuses "a dangling symlinked fragment makes collect refuse, changing nothing"

collect_fixture; printf -- '- **Latin-1 \xe9.**\n' > "$CROOT/changelog.d/94-latin1.md"
collect_refuses "a non-UTF-8 fragment makes collect refuse cleanly, changing nothing"

echo
if [ "$FAILURES" -ne 0 ]; then
  echo "$FAILURES check(s) failed."
  exit 1
fi
echo "All checks passed."

#!/usr/bin/env bash
# deploy-dev-test.sh — unit tests for scripts/deploy-dev.sh's push_branch.
#
# deploy-dev.sh runs under `set -euo pipefail` and used to push
# unconditionally. Pushing a branch whose local tip is BEHIND origin is a
# non-fast-forward, so git exits 1 and the deploy dies before it ever reaches
# the server — even though the server side only ever checks out
# `origin/$BRANCH`, which already had the code. Deploying `main` from a clone
# that hadn't pulled in a while hit this every time.
#
# The function is extracted from deploy-dev.sh by name and eval'd rather than
# sourcing the file, because sourcing it would try to SSH into a dev server.
#
# Usage: bash scripts/deploy-dev-test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_SH="$SCRIPT_DIR/deploy-dev.sh"

FAILURES=0
pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1"; FAILURES=$((FAILURES + 1)); }

# Print the source of a single `name() { ... }` block from deploy-dev.sh.
extract_fn() {
  awk -v fn="$1" '
    $0 ~ "^" fn "\\(\\) \\{" { inside = 1 }
    inside { print }
    inside && /^}/ { exit }
  ' "$DEPLOY_SH"
}

PUSH_BRANCH_SRC="$(extract_fn push_branch)"
if [[ -z "$PUSH_BRANCH_SRC" ]]; then
  echo "  FAIL could not extract push_branch() from $DEPLOY_SH" >&2
  exit 1
fi

# Run push_branch in $1 (a git work tree) under the same shell options
# deploy-dev.sh uses. Sets $OUT and $STATUS — not echoed, because a command
# substitution would run this in a subshell and lose $OUT.
OUT=""
STATUS=""
run_push_branch() {
  local repo="$1" branch="$2"
  # Fed through stdin rather than `bash -c "...$body..."`: the function body
  # comes out of deploy-dev.sh verbatim, quotes and all.
  OUT="$(printf '%s\n%s\n%s\n' \
    'set -euo pipefail' "$PUSH_BRANCH_SRC" "cd '$repo' && push_branch '$branch'" |
    bash 2>&1)"
  STATUS="$?"
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

export GIT_CONFIG_GLOBAL="$TMP/gitconfig"
export GIT_CONFIG_SYSTEM=/dev/null
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com
: >"$GIT_CONFIG_GLOBAL"

commit_in() {
  local repo="$1" msg="$2"
  echo "$msg" >>"$repo/file.txt"
  git -C "$repo" add file.txt
  git -C "$repo" commit -qm "$msg"
}

# Fresh origin + clone pair. Echoes the clone's path.
new_repo() {
  local name="$1"
  local origin="$TMP/$name-origin.git" clone="$TMP/$name"
  git init -q --bare -b main "$origin"
  git init -q -b main "$clone"
  git -C "$clone" remote add origin "$origin"
  commit_in "$clone" base
  git -C "$clone" push -q origin main
  echo "$clone"
}

echo "deploy-dev.sh: push_branch"

# ── Behind origin: the regression. ──────────────────────────────────────────
repo="$(new_repo behind)"
other="$TMP/behind-other"
git clone -q "$TMP/behind-origin.git" "$other"
commit_in "$other" ahead
git -C "$other" push -q origin main

# Guard rail: this is the state where the old unconditional push died.
if git -C "$repo" push origin main >/dev/null 2>&1; then
  fail "behind origin: plain 'git push' succeeded — regression scenario is not reproduced"
else
  pass "behind origin: plain 'git push' fails (the bug being fixed)"
fi

run_push_branch "$repo" main
if [[ "$STATUS" == "0" ]]; then
  pass "behind origin: returns 0 (deploy proceeds)"
else
  fail "behind origin: exited $STATUS, expected 0 — set -e would abort the deploy: $OUT"
fi
if [[ "$OUT" == *"Skipping push"* ]]; then
  pass "behind origin: logs that the push was skipped and why"
else
  fail "behind origin: no skip message in output: $OUT"
fi

# ── Up to date: nothing to send, must not fail either. ──────────────────────
repo="$(new_repo uptodate)"
run_push_branch "$repo" main
if [[ "$STATUS" == "0" && "$OUT" == *"Skipping push"* ]]; then
  pass "up to date: skipped, returns 0"
else
  fail "up to date: exited $STATUS, output: $OUT"
fi

# ── Ahead of origin: must actually publish. ─────────────────────────────────
repo="$(new_repo ahead)"
commit_in "$repo" local-work
expected="$(git -C "$repo" rev-parse HEAD)"
run_push_branch "$repo" main
if [[ "$STATUS" == "0" ]]; then
  pass "ahead of origin: returns 0"
else
  fail "ahead of origin: exited $STATUS: $OUT"
fi
if [[ "$(git -C "$TMP/ahead-origin.git" rev-parse main)" == "$expected" ]]; then
  pass "ahead of origin: commit reached origin"
else
  fail "ahead of origin: origin/main was not advanced"
fi

# ── Brand-new branch: no origin/$BRANCH to compare against, still pushes. ───
repo="$(new_repo newbranch)"
git -C "$repo" checkout -q -b feat/new
commit_in "$repo" new-branch-work
expected="$(git -C "$repo" rev-parse HEAD)"
run_push_branch "$repo" feat/new
if [[ "$STATUS" == "0" ]]; then
  pass "new branch: returns 0"
else
  fail "new branch: exited $STATUS: $OUT"
fi
if [[ "$(git -C "$TMP/newbranch-origin.git" rev-parse feat/new 2>/dev/null)" == "$expected" ]]; then
  pass "new branch: published to origin"
else
  fail "new branch: origin does not have feat/new"
fi

# ── Diverged: the one case that must still stop the deploy, loudly. ─────────
repo="$(new_repo diverged)"
other="$TMP/diverged-other"
git clone -q "$TMP/diverged-origin.git" "$other"
commit_in "$other" remote-work
git -C "$other" push -q origin main
commit_in "$repo" local-work

run_push_branch "$repo" main
if [[ "$STATUS" != "0" ]]; then
  pass "diverged: returns non-zero (deploy stops)"
else
  fail "diverged: returned 0 — a diverged branch would deploy the wrong code"
fi
if [[ "$OUT" == *"nothing was deployed"* ]]; then
  pass "diverged: says the deploy did not happen"
else
  fail "diverged: operator message missing from output: $OUT"
fi

# ── Detached HEAD: must refuse, not quietly deploy origin's default branch. ─
# `git rev-parse --abbrev-ref HEAD` yields the literal "HEAD" when detached.
# refs/heads/HEAD does not exist, `git fetch origin HEAD` succeeds, and
# origin/HEAD is a real symref to origin/main in any cloned checkout — so an
# unguarded ancestry check reports "already contains every local commit" and
# the server then resets to origin/HEAD, i.e. main, NOT the operator's sha.
repo="$(new_repo detached)"
# Mirror what `git clone` leaves behind; new_repo builds the pair by hand.
git -C "$repo" symbolic-ref refs/remotes/origin/HEAD refs/remotes/origin/main
git -C "$repo" checkout -q --detach HEAD

run_push_branch "$repo" HEAD
if [[ "$STATUS" != "0" ]]; then
  pass "detached HEAD: returns non-zero (deploy stops)"
else
  fail "detached HEAD: returned 0 — the server would deploy origin's default branch: $OUT"
fi
if [[ "$OUT" == *"nothing was deployed"* ]]; then
  pass "detached HEAD: says the deploy did not happen"
else
  fail "detached HEAD: operator message missing from output: $OUT"
fi

# ── Stale remote-tracking ref: the pre-check fetch must refresh it. ─────────
# Local main is BEHIND origin's real tip but AHEAD of the stale
# refs/remotes/origin/main this clone still holds. Without the fetch the
# ancestry check reads the stale ref, concludes "not an ancestor", and pushes
# a behind tip — a non-fast-forward that kills the deploy.
repo="$(new_repo stalefetch)"
base="$(git -C "$repo" rev-parse HEAD)"
commit_in "$repo" local-and-remote
git -C "$repo" push -q origin main
other="$TMP/stalefetch-other"
git clone -q "$TMP/stalefetch-origin.git" "$other"
commit_in "$other" remote-only
git -C "$other" push -q origin main
# Rewind only the remote-tracking ref: that is exactly what a clone that has
# not fetched since its own push looks like.
git -C "$repo" update-ref refs/remotes/origin/main "$base"

run_push_branch "$repo" main
if [[ "$STATUS" == "0" && "$OUT" == *"Skipping push"* ]]; then
  pass "stale origin ref: fetched first, then skipped"
else
  fail "stale origin ref: exited $STATUS, output: $OUT"
fi

# ── Deploying a branch that is NOT checked out: ancestry must read that ─────
# branch's ref, not HEAD. refs/heads/feat/x is behind origin/feat/x (skip),
# while HEAD sits on a main that has diverged from it (push → non-ff → abort).
repo="$(new_repo otherbranch)"
git -C "$repo" checkout -q -b feat/x
commit_in "$repo" branch-work
git -C "$repo" push -q origin feat/x
other="$TMP/otherbranch-other"
git clone -q "$TMP/otherbranch-origin.git" "$other"
git -C "$other" checkout -q feat/x
commit_in "$other" branch-work-remote
git -C "$other" push -q origin feat/x
git -C "$repo" checkout -q main
commit_in "$repo" unrelated-main-work

run_push_branch "$repo" feat/x
if [[ "$STATUS" == "0" && "$OUT" == *"Skipping push"* ]]; then
  pass "other branch checked out: ancestry measured from refs/heads/feat/x"
else
  fail "other branch checked out: exited $STATUS, output: $OUT"
fi

# ── Push failure that is NOT divergence must not be blamed on divergence. ───
# A missing/unreachable origin is the same misdiagnosis class as the bug this
# script was written for; the operator needs the real git error, not a bogus
# "pull or force-push" instruction.
repo="$TMP/noremote"
git init -q -b main "$repo"
commit_in "$repo" base

run_push_branch "$repo" main
if [[ "$STATUS" != "0" ]]; then
  pass "no origin remote: returns non-zero"
else
  fail "no origin remote: returned 0: $OUT"
fi
if [[ "$OUT" != *"diverged"* ]]; then
  pass "no origin remote: does not claim divergence"
else
  fail "no origin remote: blamed divergence for an unreachable remote: $OUT"
fi
if [[ "$OUT" == *"nothing was deployed"* ]]; then
  pass "no origin remote: says the deploy did not happen"
else
  fail "no origin remote: operator message missing from output: $OUT"
fi

echo ""
if [[ "$FAILURES" -eq 0 ]]; then
  echo "All deploy-dev.sh tests passed."
else
  echo "$FAILURES test(s) failed."
  exit 1
fi

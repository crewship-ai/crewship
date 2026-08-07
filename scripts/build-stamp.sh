#!/usr/bin/env bash
#
# build-stamp.sh — the one place that answers "which tree is this build from?"
#
# Usage:
#   scripts/build-stamp.sh <field> [dir]
#
#   commit   full git SHA of HEAD           ("" when not a repo)
#   short    abbreviated SHA of HEAD        ("" when not a repo)
#   date     build time, RFC 3339 UTC       (always)
#   dirty    true | false                   ("" when not a repo)
#   ldflags  the -X flags for a `go build`, omitting anything unknown
#
# [dir] defaults to $PWD.
#
# WHY THIS EXISTS (#1686)
#
# The Go toolchain stamps vcs.revision / vcs.time / vcs.modified into every
# `go build`, and those stamps are WRONG when the build runs in a git worktree
# nested inside its parent clone. cmd/go recognises a repo root by a `.git`
# DIRECTORY (vcs.vcsGit.RootNames is rootName{".git", isDir: true}); a linked
# worktree's `.git` is a FILE, so the search walks up and stamps the enclosing
# clone's commit and dirty bit instead. This repo's agent worktrees live at
# .claude/worktrees/<name>/, exactly that layout, so a dev slot built from one
# reports a commit it is not running — silently.
#
# git itself honours the `.git` file, so asking git in the build directory is
# the correct answer. Everything that builds a crewship binary and cares about
# identity (dev.sh, the Makefile) routes through here rather than re-deriving
# it, so there is one behaviour to test — see internal/buildinfo/build_stamp_test.go.
#
# UNKNOWN IS NOT CLEAN. Outside a repository this prints nothing rather than
# "false"/"none": buildinfo models "nobody stamped this" as distinct from
# "clean", and collapsing the two would ship a confident wrong answer, which is
# the failure this whole change is about.

set -euo pipefail

DIRTY_LDFLAG_VAR="github.com/crewship-ai/crewship/internal/buildinfo.buildDirty"

field="${1:-}"
dir="${2:-$PWD}"

git_in() { git -C "$dir" "$@" 2>/dev/null; }

# is_repo: does `dir` sit in a git working tree AND is git usable at all?
is_repo() {
  command -v git >/dev/null 2>&1 || return 1
  git_in rev-parse --is-inside-work-tree >/dev/null 2>&1
}

stamp_commit() { is_repo && git_in rev-parse HEAD || true; }
stamp_short() { is_repo && git_in rev-parse --short HEAD || true; }
stamp_date() { date -u +"%Y-%m-%dT%H:%M:%SZ"; }

# stamp_dirty mirrors the toolchain's own definition so the stamp and the
# fallback cannot disagree about what "modified" means: cmd/go runs
# `git status --porcelain` and treats ANY output as modified, untracked files
# included. (That is how the parent clone in #1686 got vcs.modified=true from
# nothing but a new untracked directory.)
stamp_dirty() {
  is_repo || return 0
  if [ -n "$(git_in status --porcelain)" ]; then
    echo "true"
  else
    echo "false"
  fi
}

stamp_ldflags() {
  local out=() commit dirty
  commit="$(stamp_commit)"
  dirty="$(stamp_dirty)"
  [ -n "$commit" ] && out+=("-X" "main.commit=$commit")
  out+=("-X" "main.date=$(stamp_date)")
  # Omitted entirely when unknown: an empty -X value would still be "stamped"
  # as far as a reader is concerned, and buildinfo would have to guess.
  [ -n "$dirty" ] && out+=("-X" "$DIRTY_LDFLAG_VAR=$dirty")
  echo "${out[*]}"
}

case "$field" in
commit) stamp_commit ;;
short) stamp_short ;;
date) stamp_date ;;
dirty) stamp_dirty ;;
ldflags) stamp_ldflags ;;
*)
  echo "usage: $0 {commit|short|date|dirty|ldflags} [dir]" >&2
  exit 2
  ;;
esac

#!/usr/bin/env bash
#
# Tests for no-root-binaries.sh. Each case builds a throwaway git repo and runs
# the real script inside it, so the assertions are about the shipped behaviour
# rather than a reimplementation of it.
set -uo pipefail

SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/no-root-binaries.sh"
pass=0
fail=0

ok() { printf '  ok   %s\n' "$1"; pass=$((pass + 1)); }
no() { printf '  FAIL %s\n     %s\n' "$1" "$2"; fail=$((fail + 1)); }

# scratch repo with one tracked file whose first bytes are $2
scratch() {
  local dir name magic
  dir=$(mktemp -d)
  name=$1
  magic=$2
  git -C "$dir" init -q
  git -C "$dir" config user.email t@t.t
  git -C "$dir" config user.name t
  if [ -n "$magic" ]; then
    printf "$magic" >"$dir/$name"
  else
    printf 'ordinary text\n' >"$dir/$name"
  fi
  git -C "$dir" add -A
  git -C "$dir" commit -qm x
  printf '%s' "$dir"
}

run_in() { (cd "$1" && bash "$SCRIPT" 2>&1); }

# --- an ELF at the repo root is the exact #2123 shape -----------------------
d=$(scratch "docs-surface-check" '\x7f\x45\x4c\x46\x02\x01\x01')
out=$(run_in "$d"); rc=$?
if [ $rc -ne 0 ] && printf '%s' "$out" | grep -q "docs-surface-check"; then
  ok "ELF at the root fails and is named"
else
  no "ELF at the root fails and is named" "rc=$rc out=$out"
fi
rm -rf "$d"

# --- Mach-O, because #1480 was an arm64 Mach-O, not an ELF ------------------
d=$(scratch "lint-migrations" '\xcf\xfa\xed\xfe\x0c\x00\x00')
out=$(run_in "$d"); rc=$?
[ $rc -ne 0 ] && ok "Mach-O 64 is caught" || no "Mach-O 64 is caught" "rc=$rc"
rm -rf "$d"

# --- a binary in a subdirectory is no more acceptable than one at the root --
d=$(mktemp -d)
git -C "$d" init -q; git -C "$d" config user.email t@t.t; git -C "$d" config user.name t
mkdir -p "$d/tools"; printf '\x7f\x45\x4c\x46\x02' >"$d/tools/helper"
git -C "$d" add -A; git -C "$d" commit -qm x
out=$(run_in "$d"); rc=$?
[ $rc -ne 0 ] && ok "a binary under a subdirectory is caught too" || no "a binary under a subdirectory is caught too" "rc=$rc"
rm -rf "$d"

# --- the false positive that would get this check deleted -------------------
# A PNG is binary and must pass; the check is about executables, and an asset
# tree full of them is exactly what makes a noisy gate get switched off.
d=$(mktemp -d)
git -C "$d" init -q; git -C "$d" config user.email t@t.t; git -C "$d" config user.name t
printf '\x89PNG\r\n\x1a\n' >"$d/logo.png"
printf '\x00\x01\x00\x00\x00' >"$d/font.ttf"
printf 'GIF89a' >"$d/anim.gif"
git -C "$d" add -A; git -C "$d" commit -qm x
out=$(run_in "$d"); rc=$?
[ $rc -eq 0 ] && ok "image and font assets pass" || no "image and font assets pass" "rc=$rc out=$out"
rm -rf "$d"

# --- an untracked build output must not fail the build ----------------------
# The whole point is that .gitignore keeps these out of the index; a developer
# with a stale binary lying around should not see a red gate.
d=$(scratch "readme.md" "")
printf '\x7f\x45\x4c\x46\x02' >"$d/docs-surface-check"
out=$(run_in "$d"); rc=$?
[ $rc -eq 0 ] && ok "an untracked binary is ignored" || no "an untracked binary is ignored" "rc=$rc out=$out"
rm -rf "$d"

# --- the repository itself -------------------------------------------------
out=$(bash "$SCRIPT"); rc=$?
[ $rc -eq 0 ] && ok "this repository is clean" || no "this repository is clean" "$out"

printf '\n%d ok, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]

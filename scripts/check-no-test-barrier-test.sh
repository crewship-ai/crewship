#!/usr/bin/env bash
# check-no-test-barrier-test.sh — the release guard rejects a binary built
# with the crewship_testbarrier tag (alone or among other tags), accepts
# normal binaries, and refuses a path that holds no Go binary at all.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CHECK="$REPO_ROOT/scripts/check-no-test-barrier.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/src" "$TMP/clean" "$TMP/tagged" "$TMP/multi" "$TMP/empty"
cat > "$TMP/src/go.mod" <<'MOD'
module example.test/barrierguard

go 1.21
MOD
printf 'package main\n\nfunc main() {}\n' > "$TMP/src/main.go"
echo "not a binary" > "$TMP/empty/readme.txt"

build() { (cd "$TMP/src" && GOFLAGS= GOWORK=off go build -o "$1" "${@:2}" .); }
build "$TMP/clean/app"
build "$TMP/tagged/app" -tags crewship_testbarrier
build "$TMP/multi/app" -tags clionly,crewship_testbarrier
cp "$TMP/empty/readme.txt" "$TMP/clean/"

expect() { # expect <exit> <description> -- cmd…
  local want="$1" desc="$2"; shift 2; [ "${1:-}" = "--" ] && shift
  local got=0
  "$@" >/dev/null 2>&1 || got=$?
  if [ "$got" -ne "$want" ]; then
    echo "TEST FAIL ($desc): exit $got, want $want" >&2
    exit 1
  fi
  echo "ok  $desc (exit $got)"
}

expect 2 "no arguments is a usage error" -- "$CHECK"
expect 0 "untagged binary passes" -- "$CHECK" "$TMP/clean"
expect 1 "tagged binary fails" -- "$CHECK" "$TMP/tagged"
expect 1 "tag among other tags fails" -- "$CHECK" "$TMP/multi"
expect 1 "one tagged binary fails the whole set" -- "$CHECK" "$TMP/clean" "$TMP/tagged"
expect 1 "no Go binary is not a pass" -- "$CHECK" "$TMP/empty"
expect 1 "missing path is not a pass" -- "$CHECK" "$TMP/absent"
# The Docker build stage runs it under a POSIX shell, not bash.
expect 0 "POSIX sh: untagged binary passes" -- sh "$CHECK" "$TMP/clean/app"
expect 1 "POSIX sh: tagged binary fails" -- sh "$CHECK" "$TMP/clean/app" "$TMP/tagged/app"

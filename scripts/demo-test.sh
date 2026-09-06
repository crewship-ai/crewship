#!/usr/bin/env bash
# demo-test.sh — server-free tests for the demo use-case suite (scripts/demo).
#
# Two properties, both cheap and both of the "green check that ran nothing"
# class this repo keeps closing:
#
#   1. Static: every script in scripts/demo passes ShellCheck at warning
#      level, and every uc-NN-*.sh carries the header run.sh lists from
#      (uc / title / needs / minutes) with a needs value the runner knows.
#   2. The runner: against stub use cases it lists them, resolves a slug or a
#      number, maps exit 3 to SKIP (not PASS), counts EVERY failure and not
#      just the last one's, keeps going past a failure, and --needs filters.
#
# Usage: bash scripts/demo-test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEMO="$SCRIPT_DIR/demo"

FAILURES=0
pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1"; printf '       %s\n' "${2:-}"; FAILURES=$((FAILURES + 1)); }
expect_eq() { if [ "$2" = "$3" ]; then pass "$1"; else fail "$1" "want «$2» got «$3»"; fi; }
expect_contains() { case "$2" in *"$3"*) pass "$1";; *) fail "$1" "expected «$3» in: $(printf '%s' "$2" | tail -c 400)";; esac; }
expect_not_contains() { case "$2" in *"$3"*) fail "$1" "did NOT expect «$3» in: $(printf '%s' "$2" | tail -c 400)";; *) pass "$1";; esac; }

# ── 1. Static ───────────────────────────────────────────────────────────────
echo "== scripts/demo: ShellCheck + headers =="
if command -v shellcheck >/dev/null 2>&1; then
  if shellcheck -x --severity=warning "$DEMO/lib.sh" "$DEMO/run.sh" "$DEMO"/uc-*.sh; then
    pass "shellcheck --severity=warning"
  else
    fail "shellcheck --severity=warning" "see findings above"
  fi
else
  fail "shellcheck" "shellcheck is required for this gate"
fi

header() { sed -nE "s/^# $2:[[:space:]]*(.*)$/\1/p" "$1" | head -1; }
count=0
for f in "$DEMO"/uc-[0-9][0-9]-*.sh; do
  count=$((count + 1))
  name="${f##*/}"
  slug="$(header "$f" uc)"; title="$(header "$f" title)"; needs="$(header "$f" needs)"; minutes="$(header "$f" minutes)"
  [ -n "$slug" ] && [ -n "$title" ] && [ -n "$needs" ] && [ -n "$minutes" ] \
    && pass "$name: header complete" || fail "$name: header complete" "uc='$slug' title='$title' needs='$needs' minutes='$minutes'"
  case "$name" in uc-[0-9][0-9]-"$slug".sh) pass "$name: file name matches uc slug";; *) fail "$name: file name matches uc slug" "slug=$slug";; esac
  case "$needs" in none|model|github|model,github|github,model) ;; *) fail "$name: needs is a known value" "needs=$needs";; esac
  grep -q 'source "$HERE/lib.sh"' "$f" && pass "$name: sources lib.sh" || fail "$name: sources lib.sh"
  grep -qE '^(finish|demo_skip_all)' "$f" && pass "$name: ends with finish" || fail "$name: ends with finish"
done
[ "$count" -ge 10 ] && pass "$count use cases found" || fail "use cases found" "only $count"

# ── 2. The runner against stubs ─────────────────────────────────────────────
echo
echo "== run.sh against stub use cases =="
root="$(mktemp -d -t cs-demo-test.XXXXXX)"
trap 'rm -rf "$root"' EXIT
cp "$DEMO/run.sh" "$root/run.sh"

# stub <nn> <slug> <needs> <exit> [--skip-reason r] [--summary line]
stub() {
  local nn="$1" slug="$2" needs="$3" rc="$4"; shift 4
  local reason="" summary="passed: 1   failed: 0   skipped: 0   xfail: 0"
  while (( $# > 0 )); do case "$1" in --skip-reason) reason="$2"; shift;; --summary) summary="$2"; shift;; esac; shift; done
  {
    echo '#!/usr/bin/env bash'
    echo "# uc: $slug"
    echo "# title: Stub $slug"
    echo "# needs: $needs"
    echo "# minutes: 1"
    echo "echo \"stub $slug ran\""
    if [ "$rc" = 3 ]; then
      printf 'printf "\\n──────── summary ────────\\n  SKIPPED: %s\\n"\n' "$reason"
    else
      printf 'printf "\\n──────── summary ────────\\n  %s\\n"\n' "$summary"
      [ "$rc" != 0 ] && printf 'printf "  failures:\\n    - the %s assertion\\n"\n' "$slug"
    fi
    echo "exit $rc"
  } > "$root/uc-$nn-$slug.sh"
}
stub 01 alpha none 0
stub 02 beta model 1
stub 03 gamma github 3 --skip-reason "no GitHub token"
stub 04 delta none 0
stub 05 epsilon none 0 --summary "passed: 0   failed: 0   skipped: 2   xfail: 0"

out="$(bash "$root/run.sh" 2>&1)"; rc=$?
expect_eq "no args -> lists, exit 0" "0" "$rc"
expect_contains "list names a slug" "$out" "gamma"
expect_contains "list shows needs" "$out" "github"
expect_not_contains "list runs nothing" "$out" "stub alpha ran"

out="$(bash "$root/run.sh" alpha 2>&1)"; rc=$?
expect_eq "one passing use case -> exit 0" "0" "$rc"
expect_contains "ran the selected one" "$out" "stub alpha ran"
expect_not_contains "did not run the others" "$out" "stub beta ran"
expect_contains "summary says PASS" "$out" "alpha                    PASS"

out="$(bash "$root/run.sh" 2 2>&1)"; rc=$?
expect_eq "selection by number works" "1" "$(printf '%s' "$out" | grep -c 'stub beta ran')"
[ "$rc" = 1 ] && pass "a failing use case -> exit 1" || fail "a failing use case -> exit 1" "rc=$rc"
expect_contains "the failure is named in the note" "$out" "the beta assertion"

out="$(bash "$root/run.sh" uc-03-gamma.sh 2>&1)"; rc=$?
expect_eq "exit 3 -> runner exit 0" "0" "$rc"
expect_contains "exit 3 -> SKIP with the reason" "$out" "SKIP"
expect_contains "the skip reason is carried" "$out" "no GitHub token"
expect_not_contains "a skipped use case is not PASS" "$out" "gamma                    PASS"

out="$(bash "$root/run.sh" epsilon 2>&1)"
expect_contains "exit 0 with only skipped steps -> SKIP" "$out" "epsilon                  SKIP"

out="$(bash "$root/run.sh" --all 2>&1)"; rc=$?
expect_eq "--all with one failure -> exit 1" "1" "$rc"
expect_contains "--all keeps going past the failure" "$out" "stub delta ran"
expect_contains "--all: the first one" "$out" "alpha                    PASS"
expect_contains "--all: the failing one" "$out" "beta                     FAIL"
expect_contains "--all: the skipped one" "$out" "gamma                    SKIP"

out="$(bash "$root/run.sh" --all --needs none 2>&1)"; rc=$?
expect_eq "--needs none excludes the model and github ones (exit 0)" "0" "$rc"
expect_not_contains "--needs none: beta not run" "$out" "stub beta ran"
expect_contains "--needs none: alpha run" "$out" "stub alpha ran"

out="$(bash "$root/run.sh" --all --needs model 2>&1)"
expect_contains "--needs model: beta run" "$out" "stub beta ran"
expect_not_contains "--needs model: alpha not run" "$out" "stub alpha ran"

out="$(bash "$root/run.sh" nope 2>&1)"; rc=$?
expect_eq "unknown use case -> exit 2" "2" "$rc"

# A use case that hangs must not hang the runner.
stub 06 zeta none 0
printf '#!/usr/bin/env bash\n# uc: zeta\n# title: hang\n# needs: none\n# minutes: 1\nsleep 30\n' > "$root/uc-06-zeta.sh"
out="$(DEMO_UC_TIMEOUT=2 bash "$root/run.sh" zeta 2>&1)"; rc=$?
expect_eq "timed-out use case -> exit 1" "1" "$rc"
expect_contains "timed-out use case -> says so" "$out" "timed out"

echo
if [ "$FAILURES" -eq 0 ]; then echo "all demo suite checks passed"; else echo "$FAILURES check(s) FAILED"; fi
exit $(( FAILURES > 0 ? 1 : 0 ))

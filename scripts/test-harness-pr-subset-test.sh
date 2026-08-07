#!/usr/bin/env bash
# test-harness-pr-subset-test.sh — unit tests for the per-PR harness gate:
# `scripts/test-harness/pr-subset.sh` (the runner) and the one suite in it
# whose verdict depends on what the runner's environment happens to contain,
# `test-orphan-token-reap.sh`.
#
# Why this file exists. #1784 wired the PR subset into ci.yml, and the very
# first PRs to run it went red in `Harness PR subset` on five unrelated
# branches. Two defects, neither of which any existing gate could see:
#
#   1. The runner's `for` loop reported only the LAST suite's exit status, so
#      a failure in any earlier suite was swallowed and the job went green —
#      exactly the "green check that never ran" class #1784 exists to kill.
#   2. `test-orphan-token-reap.sh` asserted the CLI prints "No orphaned crew
#      containers found". On a runner with a docker daemon but no RUNNING crew
#      container the server answers inspected=0 and the CLI correctly prints
#      "No running crew containers to inspect" instead. The suite read the
#      honest "I could not look" as a failure.
#
# Both are testable with no server and no docker: the runner against fake
# suites, the suite against a stub CLI that prints each of the CLI's real
# verdict lines.
#
# Usage: bash scripts/test-harness-pr-subset-test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS="$SCRIPT_DIR/test-harness"

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

tmpdir() { cd "$(mktemp -d -t cs-prsubset.XXXXXX)" && pwd -P; }

# ── 1. The runner reports every suite, not just the last one ────────────────
echo "== pr-subset.sh failure aggregation =="

# fake_subset <ok-or-fail...> — copy the REAL runner into a scratch dir next to
# stub suites named exactly as its `tests` list, each exiting 0 or 1 as asked.
# The runner resolves its suites from its own directory, so a copy plus stubs
# exercises the real loop with no server involved.
fake_subset() {
  local root; root="$(tmpdir)"
  cp "$HARNESS/pr-subset.sh" "$root/pr-subset.sh"
  local names; names="$(sed -nE 's/^[[:space:]]*(test-[a-z0-9-]+\.sh)[[:space:]]*$/\1/p' "$root/pr-subset.sh")"
  local i=0 spec
  while IFS= read -r name; do
    [ -z "$name" ] && continue
    i=$((i + 1))
    spec="$(printf '%s\n' "$@" | sed -n "${i}p")"
    [ -z "$spec" ] && spec="ok"
    {
      echo '#!/usr/bin/env bash'
      printf 'printf "  stub %%s ran\\n" "%s"\n' "$name"
      [ "$spec" = "fail" ] && echo 'exit 1'
      echo 'exit 0'
    } > "$root/$name"
    chmod +x "$root/$name"
  done <<< "$names"
  printf '%s\n' "$root"
}

first_suite="$(sed -nE 's/^[[:space:]]*(test-[a-z0-9-]+\.sh)[[:space:]]*$/\1/p' "$HARNESS/pr-subset.sh" | head -1)"
last_suite="$(sed -nE 's/^[[:space:]]*(test-[a-z0-9-]+\.sh)[[:space:]]*$/\1/p' "$HARNESS/pr-subset.sh" | tail -1)"

root="$(fake_subset ok ok ok ok ok)"
out="$(bash "$root/pr-subset.sh" 2>&1)"; rc=$?
expect_eq "all suites green -> exit 0" "0" "$rc"
expect_contains "all suites green -> runs the first suite" "$out" "$first_suite"
expect_contains "all suites green -> runs the last suite" "$out" "$last_suite"

# The regression: the FIRST suite fails, the last one passes. Before the fix
# the loop's status was the last suite's, so this exited 0 and CI went green.
root="$(fake_subset fail ok ok ok ok)"
out="$(bash "$root/pr-subset.sh" 2>&1)"; rc=$?
expect_eq "first suite fails, last passes -> exit non-zero" "1" "$rc"
expect_contains "a failing suite is named in the summary" "$out" "$first_suite"

root="$(fake_subset ok ok ok ok fail)"
out="$(bash "$root/pr-subset.sh" 2>&1)"; rc=$?
expect_eq "last suite fails -> exit non-zero" "1" "$rc"

# A failing suite must not stop the run: the remaining suites still report, so
# one red square doesn't hide the rest (the harness's no-`set -e` doctrine).
root="$(fake_subset fail ok ok ok ok)"
out="$(bash "$root/pr-subset.sh" 2>&1)"
expect_contains "a failing suite does not abort the run" "$out" "$last_suite"

# ── 2. The orphan-reap suite reads the CLI's four verdicts correctly ────────
echo
echo "== test-orphan-token-reap.sh verdict handling =="

# stub_cli <path> <mode> — a fake `crewship` answering everything the suite
# calls. The reap lines are copied verbatim from cmd_admin_reap_orphan.go, so a
# reworded CLI breaks this test rather than silently drifting from it.
stub_cli() {
  cat > "$1" <<'STUB'
#!/usr/bin/env bash
args="$*"
case "$args" in
  *" version"*|version) exit 0 ;;
  *whoami*)      echo "demo@crewship.ai"; exit 0 ;;
  *"agent list"*)   echo "agent-a"; exit 0 ;;
  *"routine list"*) echo "routine-a"; exit 0 ;;
  *"reap-orphan-containers --help"*)
    printf 'Finds crew containers whose sidecar holds a crew-bound internal token\n\nUsage:\n  crewship admin reap-orphan-containers [flags]\n\nFlags:\n      --apply   actually stop+remove the orphaned containers\n'
    exit 0 ;;
esac
case "$args" in
  *reap-orphan-containers*)
    n=1
    if [ -n "${STUB_COUNTER:-}" ]; then
      n=$(( $(cat "$STUB_COUNTER" 2>/dev/null || echo 0) + 1 ))
      echo "$n" > "$STUB_COUNTER"
    fi
    case "${STUB_MODE:-clean}" in
      clean)
        echo "No orphaned crew containers found — 2 of 2 inspected container(s) reported a token fingerprint and matched." ;;
      empty)
        echo "No running crew containers to inspect — nothing to reap." ;;
      orphans)
        echo "Found 1 orphaned crew container(s) (dry-run — re-run with --apply to reap):"
        echo "  - crew quality (crew_1) container abc123 — stale token" ;;
      inert)
        echo "DETECTOR INERT — inspected 3 running crew container(s), but NONE advertised a token fingerprint."
        echo "\"No orphans\" here means \"could not tell\", not \"none\": the sidecar binary is"
        echo "probably stale (pre-#1385), so this sweep cannot detect an orphan at all." ;;
      unstable)
        if [ "$n" -le 1 ]; then
          echo "No orphaned crew containers found — 2 of 2 inspected container(s) reported a token fingerprint and matched."
        else
          echo "Found 1 orphaned crew container(s) (dry-run — re-run with --apply to reap):"
        fi ;;
      unavailable)
        echo "reap-orphan-containers failed (HTTP 503): reap-orphan-containers unavailable: docker not configured" >&2
        exit 1 ;;
    esac
    exit 0 ;;
esac
exit 0
STUB
  chmod +x "$1"
}

# run_reap <mode> — run the REAL suite against the stub. Output lands in
# $REAP_OUT and the suite's exit status in $REAP_RC. Deliberately not a
# command substitution: that would run the assignment in a subshell and the
# status would never reach the caller (the first draft of this file did
# exactly that and silently compared a stale rc).
REAP_OUT=""
REAP_RC=0
run_reap() {
  local mode="$1" root
  root="$(tmpdir)"
  stub_cli "$root/crewship"
  REAP_OUT="$(env CREWSHIP="$root/crewship" SERVER="http://stub.invalid" \
            STUB_MODE="$mode" STUB_COUNTER="$root/calls" \
            POLL_INTERVAL=1 bash "$HARNESS/test-orphan-token-reap.sh" 2>&1)"
  REAP_RC=$?
}

run_reap clean; out="$REAP_OUT"
expect_eq "clean sweep with containers inspected -> exit 0" "0" "$REAP_RC"
expect_contains "clean sweep passes the no-false-positive assertion" "$out" \
  "PASS no orphaned containers on a stable-master server"
expect_contains "clean sweep -> zero failures" "$out" "failed: 0"

# The CI regression. A docker-capable runner with no crew container running is
# not a clean sweep and not a failure: the fail-safe classifier had nothing to
# classify, so the run must SKIP and say so rather than claim either.
run_reap empty; out="$REAP_OUT"
expect_eq "no running containers -> exit 0" "0" "$REAP_RC"
expect_contains "no running containers -> SKIP, not FAIL" "$out" "SKIP"
expect_contains "no running containers -> says why the sweep proved nothing" "$out" \
  "nothing to inspect"
expect_not_contains "no running containers -> records no failure" "$out" "FAIL"

# A non-empty sweep on a stable-master server IS a false positive — the one
# thing the fail-safe classifier must never do. Must stay hard-red.
run_reap orphans; out="$REAP_OUT"
expect_eq "orphans reported on a stable-master server -> exit 1" "1" "$REAP_RC"
expect_contains "orphans reported -> named failure" "$out" \
  "FAIL no orphaned containers on a stable-master server"

# "No orphans" from a detector that could not look is vacuous (#1390) and must
# not be read as health.
run_reap inert; out="$REAP_OUT"
expect_eq "inert detector -> exit 1" "1" "$REAP_RC"
expect_contains "inert detector -> names #1390" "$out" "#1390"
expect_not_contains "inert detector -> not silently passed" "$out" \
  "PASS no orphaned containers"

# Two identical dry-runs must produce identical output; a drifting second call
# means the "dry run" mutated something.
run_reap unstable; out="$REAP_OUT"
expect_eq "second dry-run differs -> exit 1" "1" "$REAP_RC"
expect_contains "second dry-run differs -> named failure" "$out" \
  "dry-run is stable on a second call"

# The pre-existing 503 path (no docker at all) still self-skips.
run_reap unavailable; out="$REAP_OUT"
expect_eq "no docker provider -> exit 0" "0" "$REAP_RC"
expect_contains "no docker provider -> self-skips" "$out" "SKIP"

echo
if [ "$FAILURES" -eq 0 ]; then
  echo "all PR-subset harness gate checks passed"
else
  echo "$FAILURES check(s) FAILED"
fi
exit $(( FAILURES > 0 ? 1 : 0 ))

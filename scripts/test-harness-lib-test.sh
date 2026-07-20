#!/usr/bin/env bash
# test-harness-lib-test.sh — unit tests for the binary resolution and preflight
# diagnosis in scripts/test-harness/lib.sh.
#
# Why this file exists (#1297): lib.sh defaulted to the RELATIVE `./crewship`,
# but the harness README tells you to run the suites from
# `scripts/test-harness/`, where no such file exists. Every suite then died in
# preflight with a message blaming auth and telling the operator to run the
# destructive `crewship seed --nuke` — a data-loss trap triggered by a path bug.
# Nothing could catch that, because the harness only runs by hand against a live
# server. These checks run the resolution and the two preflight failure modes
# against synthetic trees, with no server involved.
#
# Usage: bash scripts/test-harness-lib-test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB="$SCRIPT_DIR/test-harness/lib.sh"

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

# fake_tree <as-git-repo:0|1> — build root/scripts/test-harness/lib.sh, echo root.
fake_tree() {
  local root
  # -P: macOS hands out /var/... (a symlink to /private/var), and `git
  # rev-parse` answers with the physical path. Compare like for like.
  root="$(cd "$(mktemp -d -t cs-harness.XXXXXX)" && pwd -P)"
  mkdir -p "$root/scripts/test-harness"
  cp "$LIB" "$root/scripts/test-harness/lib.sh"
  if [ "$1" = "1" ]; then
    git -C "$root" init -q 2>/dev/null
  fi
  printf '%s\n' "$root"
}

# fake_cli <path> <whoami-exit> — a stub `crewship` that always answers
# `version` (so it counts as a working binary) and exits N on anything else.
fake_cli() {
  cat >"$1" <<EOF
#!/usr/bin/env bash
for a in "\$@"; do [ "\$a" = "version" ] && exit 0; done
exit $2
EOF
  chmod +x "$1"
}

# resolve <root> [extra-env...] — source lib.sh from the harness dir (the way
# the README says to run it) and print the resolved CREWSHIP. Trailing
# assignments land after the `-u`s, so a caller can re-set CREWSHIP.
resolve() {
  local root="$1"; shift
  # shellcheck disable=SC2016  # the inner shell expands these, not this one
  env -u CREWSHIP -u CREWSHIP_SERVER -u SERVER "$@" \
    bash -c 'cd "$1/scripts/test-harness" && . ./lib.sh && printf "%s\n" "$CREWSHIP"' _ "$root"
}

# run_preflight <root> — source lib.sh and run preflight, merging stderr.
run_preflight() {
  local root="$1"; shift
  # shellcheck disable=SC2016  # the inner shell expands these, not this one
  env -u CREWSHIP -u CREWSHIP_SERVER -u SERVER "$@" \
    bash -c 'cd "$1/scripts/test-harness" && . ./lib.sh && preflight' _ "$root" 2>&1
}

echo "== binary resolution =="

root="$(fake_tree 1)"
fake_cli "$root/crewship" 0
expect_eq "git clone, repo-root binary -> absolute repo-root path" \
  "$root/crewship" "$(resolve "$root")"

root_nogit="$(fake_tree 0)"
fake_cli "$root_nogit/crewship" 0
expect_eq "no git (tarball / git missing) -> still the repo-root path" \
  "$root_nogit/crewship" "$(resolve "$root_nogit" PATH=/usr/bin:/bin)"

# No repo-root binary, but one installed on PATH: that is the operator's CLI.
root_path="$(fake_tree 1)"
bindir="$(cd "$(mktemp -d -t cs-bin.XXXXXX)" && pwd -P)"
fake_cli "$bindir/crewship" 0
expect_eq "no repo-root binary, crewship on PATH -> the PATH binary" \
  "$bindir/crewship" "$(resolve "$root_path" "PATH=$bindir:$PATH")"

# Nothing anywhere: still name the repo-root path, so the error is actionable.
root_none="$(fake_tree 1)"
expect_eq "no binary anywhere -> repo-root path (for the error message)" \
  "$root_none/crewship" "$(resolve "$root_none" PATH=/usr/bin:/bin)"

root_env="$(fake_tree 1)"
fake_cli "$root_env/crewship" 0
expect_eq "explicit CREWSHIP wins over resolution" \
  "/opt/custom/crewship" \
  "$(resolve "$root_env" CREWSHIP=/opt/custom/crewship)"

echo
echo "== preflight diagnosis =="

# Missing binary: its own message, and it must NOT push the destructive reseed.
out="$(run_preflight "$root_none" PATH=/usr/bin:/bin)"
expect_contains "missing binary -> says the binary was not found" "$out" "not found"
expect_contains "missing binary -> names the path it looked at" "$out" "$root_none/crewship"
expect_not_contains "missing binary -> does NOT suggest seed" "$out" "seed"
expect_not_contains "missing binary -> does NOT suggest --nuke" "$out" "--nuke"

# Binary runs, server rejects us: THAT is when reseed guidance is warranted.
root_auth="$(fake_tree 1)"
fake_cli "$root_auth/crewship" 1
out="$(run_preflight "$root_auth")"
expect_contains "auth failure -> points at login/seed" "$out" "seed --nuke"
expect_not_contains "auth failure -> does NOT claim the binary is missing" "$out" "not found"

echo
if [ "$FAILURES" -eq 0 ]; then
  echo "all test-harness lib checks passed"
else
  echo "$FAILURES check(s) FAILED"
fi
exit $(( FAILURES > 0 ? 1 : 0 ))

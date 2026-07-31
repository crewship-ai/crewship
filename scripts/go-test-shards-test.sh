#!/usr/bin/env bash
# go-test-shards-test.sh — unit tests for scripts/go-test-shards.sh.
#
# Sharding `go test ./...` across parallel CI jobs trades one guarantee for
# speed: `./...` could not miss a package, four hand-written patterns can. A
# package that belongs to no shard is never tested, and CI stays green while
# it rots — the worst possible failure mode for a test gate, because it is
# indistinguishable from success.
#
# These tests are the replacement guarantee. They assert that the shards
# partition `go list ./...` exactly — complete (nothing missing) and disjoint
# (nothing tested twice) — and that the same holds for every group count the
# CI matrices ask for. CI runs this in the `Go Test Plan` job — the job that
# emits those matrices — and both test jobs depend on it, so a sharding
# mistake fails the run before any test job starts.
#
# Usage: bash scripts/go-test-shards-test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SHARDS_SH="$SCRIPT_DIR/go-test-shards.sh"

FAILURES=0
pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1"; FAILURES=$((FAILURES + 1)); }

cd "$REPO_ROOT" || exit 1

if [[ ! -x "$SHARDS_SH" ]]; then
  fail "go-test-shards.sh is not executable (chmod +x it — CI invokes it directly)"
fi

# ── The full package set, as `go test ./...` would see it ──
ALL_PKGS="$(go list ./... | sort)"
if [[ -z "$ALL_PKGS" ]]; then
  echo "  FAIL 'go list ./...' returned nothing — cannot verify shard coverage" >&2
  exit 1
fi
MODULE="$(go list -m)"
SHARD_NAMES="$(bash "$SHARDS_SH" --list)"

# Expand a space-separated list of shard names through `go list`, so a
# pattern that matches nothing — or matches something unexpected — shows up
# as a real difference rather than a string mismatch.
resolve_shards() {
  local patterns
  # $1 is a space-separated shard list and $patterns a space-separated
  # pattern list; both are meant to word-split into separate arguments.
  # shellcheck disable=SC2086
  patterns="$(bash "$SHARDS_SH" $1 2>/dev/null)" || return 1
  [[ -z "${patterns// /}" ]] && return 1
  # shellcheck disable=SC2086
  go list $patterns 2>/dev/null
}

# ── 1-3. The shards partition the module exactly ──
COVERED=""
for shard in $SHARD_NAMES; do
  resolved="$(resolve_shards "$shard")"
  if [[ -z "$resolved" ]]; then
    fail "shard '$shard' matches no packages"
    continue
  fi
  COVERED="$COVERED$resolved"$'\n'
done
COVERED="$(printf '%s' "$COVERED" | grep -v '^$' | sort)"

MISSING="$(comm -23 <(printf '%s\n' "$ALL_PKGS") <(printf '%s\n' "$COVERED"))"
if [[ -n "$MISSING" ]]; then
  fail "packages covered by no shard — they would silently stop being tested:"
  # shellcheck disable=SC2086
  printf '         %s\n' $MISSING
else
  pass "every package in 'go list ./...' belongs to a shard"
fi

DUPES="$(printf '%s\n' "$COVERED" | uniq -d)"
if [[ -n "$DUPES" ]]; then
  fail "packages claimed by more than one shard — wasted CI time, and a flake surfaces twice:"
  # shellcheck disable=SC2086
  printf '         %s\n' $DUPES
else
  pass "no package is claimed by two shards"
fi

FOREIGN="$(printf '%s\n' "$COVERED" | grep -v "^$MODULE" || true)"
if [[ -n "$FOREIGN" ]]; then
  fail "shard patterns resolved outside the module:"
  # shellcheck disable=SC2086
  printf '         %s\n' $FOREIGN
else
  pass "all shard packages are inside $MODULE"
fi

# ── 4. Unknown shard names fail loudly rather than testing nothing ──
if bash "$SHARDS_SH" definitely-not-a-shard >/dev/null 2>&1; then
  fail "unknown shard name exited 0 — a typo in the CI matrix would test nothing and pass"
else
  pass "unknown shard name exits non-zero"
fi

# ── 5. --groups N stays exhaustive for every N a matrix might ask for ──
# ci.yml builds both Go test matrices from --groups via fromJSON, so this is
# what actually decides which packages CI runs.
# shellcheck disable=SC2086
SHARD_COUNT="$(printf '%s\n' $SHARD_NAMES | wc -l | tr -d ' ')"
groups_ok=1
for n in $(seq 1 "$((SHARD_COUNT + 2))"); do
  json="$(bash "$SHARDS_SH" --groups "$n" 2>/dev/null)"
  if [[ -z "$json" ]]; then
    fail "--groups $n produced no output"
    groups_ok=0
    continue
  fi
  # ["api","storage server rest"] -> one shard name per line
  flat="$(printf '%s' "$json" | tr -d '[]"' | tr ',' '\n' | tr ' ' '\n' | grep -v '^$' | sort)"
  # shellcheck disable=SC2086
  expected="$(printf '%s\n' $SHARD_NAMES | sort)"
  if [[ "$flat" != "$expected" ]]; then
    fail "--groups $n is not a partition of the shard list:"
    printf '         got:      %s\n' "$(printf '%s' "$flat" | tr '\n' ' ')"
    printf '         expected: %s\n' "$(printf '%s' "$expected" | tr '\n' ' ')"
    groups_ok=0
  fi
  # Group count must never exceed the shard count, or the matrix gets an
  # empty job that tests nothing and reports success.
  count="$(printf '%s' "$json" | tr -cd ',' | wc -c | tr -d ' ')"
  count=$((count + 1))
  want=$((n > SHARD_COUNT ? SHARD_COUNT : n))
  if [[ "$count" -ne "$want" ]]; then
    fail "--groups $n returned $count groups, expected $want"
    groups_ok=0
  fi
done
[[ $groups_ok -eq 1 ]] && pass "--groups N partitions the shard list for N=1..$((SHARD_COUNT + 2))"

# ── 6. A multi-shard group resolves to the union of its members ──
COMBINED="$(resolve_shards "storage server rest" | sort)"
SEPARATE="$( { resolve_shards storage; resolve_shards server; resolve_shards rest; } | sort)"
if [[ "$COMBINED" == "$SEPARATE" ]]; then
  pass "a grouped shard list resolves to the union of its members"
else
  fail "grouping shards changed the resolved package set"
fi

echo ""
if [[ $FAILURES -gt 0 ]]; then
  echo "✗ go-test-shards: $FAILURES check(s) failed"
  exit 1
fi
echo "✓ go-test-shards: shards partition 'go list ./...' exactly"

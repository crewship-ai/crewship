#!/usr/bin/env bash
# go-test-shards.sh — the single source of truth for how `go test ./...` is
# split across parallel CI jobs.
#
# WHY THIS EXISTS
#
# The `Go` CI job was the critical path of every run: 14.4 min, of which
# `go test` alone was 709 s (82%). `go test` already runs packages
# concurrently (p = GOMAXPROCS = 4 on a GitHub-hosted runner), so the wall
# clock is bounded by the slowest *package*, not by the total. Measured on
# run 30537289794:
#
#   internal/api        367 s      internal/database    236 s
#   internal/backup     271 s      cmd/crewship         166 s
#   internal/server     263 s      internal/consolidate  97 s
#   ... ~1875 CPU-seconds in total, 709 s wall at p=4
#
# Splitting those across parallel runners takes the wall clock down to
# roughly the largest single shard. The groupings below are a balance hint
# derived from that measurement and nothing more — they will drift as the
# suite is rewritten, and drifting only costs time, never coverage.
#
# INVARIANT (enforced by scripts/go-test-shards-test.sh, run in CI)
#
#   Every package in `go list ./...` belongs to exactly one shard.
#
# That holds *by construction*: the named shards claim packages explicitly and
# the `rest` shard is computed by EXCLUDING them from a live `go list ./...`.
# A newly added package therefore lands in `rest` automatically and gets
# tested. It can never fall through the gaps — which is the failure mode that
# makes hand-maintained test sharding dangerous, because a silently untested
# package looks exactly like a passing one.
#
# ci.yml does not restate the shard list. It builds its matrix from
# `--groups N` via fromJSON, so the workflow and this script cannot disagree.
#
# Usage:
#   scripts/go-test-shards.sh --list         # shard names, one per line
#   scripts/go-test-shards.sh --groups N     # JSON array of N shard groups
#   scripts/go-test-shards.sh <name>...      # package patterns for those shards
set -euo pipefail

# Directory prefixes claimed by the named shards, grouped so each shard's
# wall clock is similar. Matched per path segment, so "./internal/api" claims
# "./internal/api" and "./internal/api/sub" but never "./internal/apikeys".
CLAIMED_API="./internal/api"
CLAIMED_STORAGE="./internal/backup ./internal/database"
CLAIMED_SERVER="./internal/server ./cmd"

# Heaviest first. --groups relies on this order: when there are fewer runners
# than shards, the leading shards keep a runner to themselves and the tail
# gets lumped together.
SHARD_NAMES="api storage server rest"

shard_patterns() {
  case "$1" in
    api)
      # One package, and the largest one: 93k lines of source + 166k lines of
      # tests compile as a single unit, so it is both the slowest to build and
      # the slowest to run. It gets a runner to itself.
      echo "./internal/api/..."
      ;;
    storage) echo "./internal/backup/... ./internal/database/..." ;;
    server)  echo "./internal/server/... ./cmd/..." ;;
    rest)    rest_packages | tr '\n' ' ' | sed 's/ *$//' ;;
    *)
      echo "go-test-shards.sh: unknown shard '$1'" >&2
      echo "known shards: $SHARD_NAMES" >&2
      return 2
      ;;
  esac
}

# Every package NOT claimed by a named shard, as exact package paths (no
# "/..." — each line is already one package, and expanding it again could
# list a nested package twice). Computed live from `go list`, so the set is
# always complete.
rest_packages() {
  local module pkg rel prefix skip
  module="$(go list -m)"

  while IFS= read -r pkg; do
    # github.com/crewship-ai/crewship/internal/api -> ./internal/api
    rel=".${pkg#"$module"}"

    skip=0
    for prefix in $CLAIMED_API $CLAIMED_STORAGE $CLAIMED_SERVER; do
      if [ "$rel" = "$prefix" ] || [ "${rel#"$prefix"/}" != "$rel" ]; then
        skip=1
        break
      fi
    done
    if [ "$skip" -eq 0 ]; then
      printf '%s\n' "$rel"
    fi
  done < <(go list ./...)
}

# Distribute the shards over N runners as a JSON array of space-separated
# shard-name groups, e.g. --groups 2 -> ["api","storage server rest"].
# The union of the groups is always the full shard list, so a matrix built
# from this is exhaustive for any N.
shard_groups() {
  local n="$1" total i out group names
  # Deliberate word split of a fixed literal.
  # shellcheck disable=SC2206
  names=($SHARD_NAMES)
  total=${#names[@]}

  case "$n" in
    ''|*[!0-9]*|0) echo "go-test-shards.sh: --groups needs a positive integer" >&2; return 2 ;;
  esac
  [ "$n" -gt "$total" ] && n="$total"

  out=""
  for ((i = 0; i < n; i++)); do
    if [ "$i" -lt $((n - 1)) ]; then
      group="${names[$i]}"
    else
      # Last group absorbs every remaining shard, which is what keeps the
      # union complete for any N.
      group="${names[*]:$i}"
    fi
    out="$out${out:+,}\"$group\""
  done
  printf '[%s]\n' "$out"
}

case "${1:---list}" in
  --list)
    # One name per line; the split is the point.
    # shellcheck disable=SC2086
    printf '%s\n' $SHARD_NAMES
    ;;
  --groups)
    shard_groups "${2:-}"
    ;;
  *)
    # One or more shard names -> the union of their package patterns.
    out=""
    for name in "$@"; do
      out="$out${out:+ }$(shard_patterns "$name")"
    done
    printf '%s\n' "$out"
    ;;
esac

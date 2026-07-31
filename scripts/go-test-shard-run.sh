#!/usr/bin/env bash
# go-test-shard-run.sh — run `go test` for one shard group, and refuse to
# report success for a shard that tested nothing.
#
# WHY THIS IS NOT AN INLINE `run:` BLOCK
#
# `go test` exits 0 when its package patterns match nothing:
#
#     $ go test "./internal/backup/... ./internal/database/..."
#     go: warning: "..." matched no packages
#     no packages to test
#     $ echo $?
#     0
#
# A *warning*, and a green job. That is the same silent-untested failure mode
# scripts/go-test-shards-test.sh guards against statically, except it happens
# at run time and no static check can see it. It is one quoting mistake away
# at all times: the shard patterns have to be word-split to reach `go test` as
# separate arguments, and a quoted expansion — or a shell that does not word-
# split unquoted variables, which is how this was caught — collapses them into
# one argument that matches nothing.
#
# So the expansion happens once, here, with an explicit array, and the result
# is verified against `go list` before a single test runs. A shard that
# resolves to zero packages fails the job.
#
# Usage: scripts/go-test-shard-run.sh "<shard names>" [extra go test flags...]
#   e.g. scripts/go-test-shard-run.sh "api" -count=1 -timeout 15m
#        scripts/go-test-shard-run.sh "storage server rest" -count=1
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ "$#" -lt 1 ] || [ -z "${1// /}" ]; then
  echo "usage: $0 \"<shard names>\" [go test flags...]" >&2
  exit 2
fi

SHARDS="$1"
shift

# "storage server rest" -> array of shard names
read -ra SHARD_NAMES <<< "$SHARDS"

# Resolve to package patterns. go-test-shards.sh exits non-zero on an unknown
# name, and `set -e` propagates that here rather than letting a typo in the CI
# matrix quietly test nothing.
PATTERNS_RAW="$("$SCRIPT_DIR/go-test-shards.sh" "${SHARD_NAMES[@]}")"
read -ra PATTERNS <<< "$PATTERNS_RAW"

if [ "${#PATTERNS[@]}" -eq 0 ]; then
  echo "::error::shard '$SHARDS' produced no package patterns" >&2
  exit 1
fi

# The load-bearing check: patterns that match nothing would make `go test`
# exit 0 having run nothing at all.
#
# Portable to bash 3.2 on purpose — that is what /bin/bash is on the macos-14
# runners in the go-platforms matrix, so no `mapfile`/`readarray` here. The
# `set -o pipefail` above makes a failing `go list` fail this line rather than
# silently yielding a count of 0.
RESOLVED_COUNT="$(go list "${PATTERNS[@]}" | wc -l | tr -d ' ')"
if [ "$RESOLVED_COUNT" -eq 0 ]; then
  echo "::error::shard '$SHARDS' matched no packages — it would have reported success without testing anything" >&2
  echo "  patterns: ${PATTERNS[*]}" >&2
  exit 1
fi

echo "→ shard '$SHARDS': $RESOLVED_COUNT package(s)"
printf '    %s\n' "${PATTERNS[@]}"

exec go test "${PATTERNS[@]}" "$@"

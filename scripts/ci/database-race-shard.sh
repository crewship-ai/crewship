#!/usr/bin/env bash
# Enumerate the current package so newly added tests cannot escape the weekly run.
set -euo pipefail
if [[ $# != 2 || ! $1 =~ ^[0-9]+$ || ! $2 =~ ^[1-9][0-9]*$ ]]; then
  echo 'usage: database-race-shard.sh INDEX COUNT' >&2
  exit 2
fi
index=$((10#$1))
count=$((10#$2))
if (( index >= count || count > 32 )); then
  echo 'require 0 <= INDEX < COUNT <= 32' >&2
  exit 2
fi
root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
# Assignment uses sorted top-level names; every subtest runs with its parent.
# Command substitution preserves enumeration failure instead of hiding it in
# process substitution. Examples/fuzz seed corpora are included if added later.
inventory=$(go test ./internal/database -list .)
mapfile -t tests < <(printf '%s\n' "$inventory" | LC_ALL=C awk '/^(Test|Example|Fuzz)[^[:space:]]*$/ {print}' | LC_ALL=C sort -u)
selected=()
for (( n=index; n<${#tests[@]}; n+=count )); do
  selected+=("${tests[n]}")
done
if (( ${#selected[@]} == 0 )); then
  echo 'empty database test shard; check enumeration or shard count' >&2
  exit 1
fi
printf 'Database race shard %s/%s: %s of %s top-level tests\n' "$index" "$count" "${#selected[@]}" "${#tests[@]}"
printf '%s\n' "${selected[@]}"
pattern=$(IFS='|'; echo "${selected[*]}")
bash "$root/scripts/ci/go-test.sh" ./internal/database -race -count=1 -timeout 90m -run "^($pattern)$"

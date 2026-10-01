#!/usr/bin/env bash
set -euo pipefail
repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
if [[ $# != 1 ]]; then
  echo 'usage: scripts/build-restricted-native-image.sh /absolute/path/to/codex-0.159.0-binary' >&2
  exit 2
fi
codex_binary=$1
[[ "$codex_binary" = /* ]]
[[ $(sha256sum -- "$codex_binary" | cut -d' ' -f1) == d2752c52353401f7f6efbfcea68796f4f7a3d3e4769f5d1da53fa49d4856b72f ]]
build_context=$(mktemp -d "${TMPDIR:-/tmp}/crewship-native-image.XXXXXXXX")
trap 'rm -rf -- "$build_context"' EXIT
cd -- "$repo_dir"
CGO_ENABLED=0 GOMAXPROCS=2 go build -p 2 -o "$build_context/crewship-restricted-native-runner" ./cmd/crewship-restricted-native-runner
cp -- "$codex_binary" "$build_context/codex"
cp -- cmd/crewship-restricted-native-runner/Dockerfile "$build_context/Dockerfile"
docker build --tag crewship-restricted-native:codex159 --iidfile "$build_context/image-id" "$build_context"
cat "$build_context/image-id"

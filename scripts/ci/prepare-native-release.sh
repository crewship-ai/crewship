#!/usr/bin/env bash
# Assemble the independent native worker release context, without Docker or publication.
set -euo pipefail
if [[ $# != 1 || $1 != /* ]]; then
  echo 'usage: scripts/ci/prepare-native-release.sh /absolute/new/context-directory' >&2
  exit 2
fi
repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
context_dir=$1
# Refuse overwriting either a previous candidate or unrelated working files.
mkdir -- "$context_dir"
readonly upstream=687a119f0fcaace47e1f1abcc77cec6c813fd6da
readonly archive_sha=6e587a08cb39599816c598b07e17b4bcbf9d41b5c1c0793a4b632b6741e9cbc0
readonly binary_sha=d2752c52353401f7f6efbfcea68796f4f7a3d3e4769f5d1da53fa49d4856b72f
curl --fail --location --retry 3 --connect-timeout 15 --max-time 180 \
  'https://github.com/openai/codex/releases/download/rust-v0.159.0/codex-x86_64-unknown-linux-musl.tar.gz' \
  --output "$context_dir/codex.tar.gz"
printf '%s  %s\n' "$archive_sha" "$context_dir/codex.tar.gz" | sha256sum --check --strict
# The authenticated upstream archive contains this one executable.
tar -xzf "$context_dir/codex.tar.gz" -C "$context_dir" codex-x86_64-unknown-linux-musl
mv -- "$context_dir/codex-x86_64-unknown-linux-musl" "$context_dir/codex"
printf '%s  %s\n' "$binary_sha" "$context_dir/codex" | sha256sum --check --strict
rm -- "$context_dir/codex.tar.gz"
mkdir -p -- "$context_dir/licenses/codex" "$context_dir/licenses/project"
for notice in LICENSE NOTICE; do
  curl --fail --location --retry 3 --connect-timeout 15 --max-time 60 \
    "https://raw.githubusercontent.com/openai/codex/$upstream/$notice" \
    --output "$context_dir/licenses/codex/$notice"
done
printf '%s  %s\n' \
  d17f227e4df5da1600391338865ce0f3055211760a36688f816941d58232d8dc "$context_dir/licenses/codex/LICENSE" \
  9d71575ecfd9a843fc1677b0efb08053c6ba9fd686a0de1a6f5382fd3c220915 "$context_dir/licenses/codex/NOTICE" | sha256sum --check --strict
cd -- "$repo_dir"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$context_dir/crewship-restricted-native-runner" ./cmd/crewship-restricted-native-runner
bash scripts/gen-license-bundle.sh go
cp -R -- build/licenses/go "$context_dir/licenses/go"
cp -- LICENSE NOTICE THIRD-PARTY-NOTICES.md "$context_dir/licenses/project/"
cp -- cmd/crewship-restricted-native-runner/Dockerfile "$context_dir/Dockerfile"
# Keep the reviewed runtime recipe and pinned base/bwrap/Codex checks intact.
printf '\nCOPY --chown=0:0 licenses /usr/share/licenses/crewship-native/\nCOPY --chown=0:0 release-source.json /usr/share/crewship-native/release-source.json\n' >> "$context_dir/Dockerfile"
SOURCE_SHA=$(git rev-parse HEAD) CONTEXT_DIR="$context_dir" UPSTREAM_SHA="$upstream" CODEX_SHA="$binary_sha" ARCHIVE_SHA="$archive_sha" python3 - <<'PY'
import json, os, pathlib
root = pathlib.Path(os.environ['CONTEXT_DIR'])
(root / 'release-source.json').write_text(json.dumps({
    'schema': 1, 'source_sha': os.environ['SOURCE_SHA'],
    'upstream_repository': 'https://github.com/openai/codex',
    'upstream_commit': os.environ['UPSTREAM_SHA'], 'codex_version': '0.159.0',
    'codex_binary_sha256': os.environ['CODEX_SHA'],
    'upstream_archive_sha256': os.environ['ARCHIVE_SHA'],
    'upstream_archive_url': 'https://github.com/openai/codex/releases/download/rust-v0.159.0/codex-x86_64-unknown-linux-musl.tar.gz',
    'platform': 'linux/amd64',
}, sort_keys=True) + '\n')
PY

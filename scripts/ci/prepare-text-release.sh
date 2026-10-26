#!/usr/bin/env bash
# Prepare an independent text worker context; no Docker publication or provider calls.
set -euo pipefail
if [[ $# != 1 || $1 != /* ]]; then
  echo 'usage: scripts/ci/prepare-text-release.sh /absolute/new/context-directory' >&2
  exit 2
fi
repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
context_dir=$1
mkdir -- "$context_dir"
cd -- "$repo_dir"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$context_dir/crewship-restricted-runner" ./cmd/crewship-restricted-runner
bash scripts/gen-license-bundle.sh go
mkdir -p -- "$context_dir/licenses/project"
cp -R -- build/licenses/go "$context_dir/licenses/go"
cp -- LICENSE NOTICE THIRD-PARTY-NOTICES.md "$context_dir/licenses/project/"
cp -- cmd/crewship-restricted-runner/Dockerfile "$context_dir/Dockerfile"
runner_sha=$(sha256sum "$context_dir/crewship-restricted-runner" | cut -d ' ' -f 1)
printf '%s  crewship-runner\n' "$runner_sha" > "$context_dir/runner.sha256"
printf '\nCOPY --chown=0:0 licenses /usr/share/licenses/crewship-text/\nCOPY --chown=0:0 release-source.json runner.sha256 /usr/share/crewship-text/\n' >> "$context_dir/Dockerfile"
SOURCE_SHA=$(git rev-parse HEAD) CONTEXT_DIR="$context_dir" RUNNER_SHA="$runner_sha" python3 - <<'PY'
import json, os, pathlib
root = pathlib.Path(os.environ['CONTEXT_DIR'])
(root / 'release-source.json').write_text(json.dumps({
    'schema': 1, 'source_sha': os.environ['SOURCE_SHA'], 'profile': 'responses_text',
    'runner_binary_sha256': os.environ['RUNNER_SHA'], 'platform': 'linux/amd64',
}, sort_keys=True) + '\n')
PY

#!/usr/bin/env bash
# Build an operator-owned immutable image; stdout is its content ID.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ "$(uname -s)" != Linux ]]; then
  echo "Restricted runtime images require a Linux build host." >&2
  exit 1
fi
restricted_build_dir=$(mktemp -d /tmp/crewship-restricted-runtime-build.XXXXXX)
trap 'rm -rf "$restricted_build_dir"' EXIT
# The runner is static and needs no distribution packages (#5103). An override
# must name an approved image by digest; that image's packages are then yours.
restricted_base=${CREWSHIP_RESTRICTED_RUNTIME_BASE:-scratch}
if [[ "$restricted_base" != scratch && ! "$restricted_base" =~ @sha256:[a-f0-9]{64}$ ]]; then
  echo "Set CREWSHIP_RESTRICTED_RUNTIME_BASE to scratch or an approved local image digest." >&2
  exit 1
fi
restricted_commit=$(scripts/build-stamp.sh commit)
restricted_dirty=$(scripts/build-stamp.sh dirty)
restricted_ldflags=$(scripts/build-stamp.sh ldflags)
CGO_ENABLED=0 go build -ldflags "$restricted_ldflags" -o "$restricted_build_dir/crewship-restricted-runner" ./cmd/crewship-restricted-runner
sha256sum "$restricted_build_dir/crewship-restricted-runner" >&2
cp cmd/crewship-restricted-runner/Dockerfile "$restricted_build_dir/Dockerfile"
docker build --network=none --build-arg "BASE=$restricted_base" \
  --label "org.opencontainers.image.revision=$restricted_commit" \
  --label "ai.crewship.source-dirty=$restricted_dirty" \
  --iidfile "$restricted_build_dir/image-id" "$restricted_build_dir" >&2
cat "$restricted_build_dir/image-id"

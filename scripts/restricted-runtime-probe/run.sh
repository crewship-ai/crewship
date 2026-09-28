#!/usr/bin/env bash
# Owned, offline dev2 acceptance image; never reloads a Crewship instance.
set -euo pipefail
if [[ "$(uname -s)" != Linux ]]; then echo "This prototype requires Linux Docker." >&2; exit 1; fi
cd "$(dirname "$0")/../.."
probe_context=$(mktemp -d /tmp/crewship-dev2-restricted-build.XXXXXX)
probe_tag="crewship-restricted-probe:dev2-$(date -u +%Y%m%d%H%M%S)-$$"
cleanup() { docker image rm "$probe_tag" >/dev/null 2>&1 || true; rm -rf "$probe_context"; }
trap cleanup EXIT
probe_ldflags=$(scripts/build-stamp.sh ldflags)
scripts/build-stamp.sh commit
scripts/build-stamp.sh dirty
CGO_ENABLED=0 go build -ldflags "$probe_ldflags" -o "$probe_context/runner" ./scripts/restricted-runtime-probe
CGO_ENABLED=0 go build -ldflags "$probe_ldflags" -o "$probe_context/sidecar" ./cmd/crewship-sidecar
sha256sum "$probe_context/runner" "$probe_context/sidecar"
cp scripts/restricted-runtime-probe/Dockerfile "$probe_context/Dockerfile"
probe_base=$(docker image inspect alpine:3 --format '{{index .RepoDigests 0}}')
docker build --network=none --build-arg "BASE=$probe_base" -t "$probe_tag" "$probe_context"
CREWSHIP_RESTRICTED_LIVE=1 CREWSHIP_RESTRICTED_IMAGE="$probe_tag" go test "${@}" -v ./internal/restrictedruntime -run TestLive -count=1 -timeout=6m

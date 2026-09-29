#!/usr/bin/env bash
# Owned acceptance image for this checkout; never reloads a Crewship instance.
set -euo pipefail
if [[ "$(uname -s)" != Linux ]]; then echo "This prototype requires Linux Docker." >&2; exit 1; fi
cd "$(dirname "$0")/../.."
probe_instance=$(basename "$PWD" | tr -cd 'a-zA-Z0-9_-')
probe_context=$(mktemp -d "/tmp/crewship-${probe_instance}-restricted-build.XXXXXX")
probe_tag="crewship-restricted-probe:${probe_instance}-$(date -u +%Y%m%d%H%M%S)-$$"
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
CREWSHIP_RESTRICTED_LIVE=1 CREWSHIP_RESTRICTED_IMAGE="$probe_tag" go test "${@}" -tags restrictedruntime_live -v ./internal/restrictedruntime ./internal/restricteddispatch -run TestLive -count=1 -timeout=6m

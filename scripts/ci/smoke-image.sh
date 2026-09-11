#!/usr/bin/env bash
# Test the supplied immutable image (or a locally loaded PR image), never :latest.
set -euo pipefail
image=${1:?usage: smoke-image.sh IMAGE EXPECTED_SHA [PLATFORM]}
expected=${2:?expected commit SHA required}
platform=${3:-linux/amd64}
cid=''
cleanup() {
  status=$?
  if [ -n "$cid" ]; then
    if [ "$status" -ne 0 ]; then docker logs "$cid" || true; fi
    docker rm -f "$cid" >/dev/null || true
  fi
}
trap cleanup EXIT
# Classic Docker stores cannot retain two architectures under one index digest.
# Pull the selected child instead; publication/signatures still identify the index.
if [[ "$image" == *@* ]]; then
  image=$(docker manifest inspect "$image" | python3 "$(dirname "$0")/image-platform.py" "$image" "$platform")
fi
version=$(docker run --rm --platform "$platform" "$image" version --format json)
printf '%s\n' "$version"
[[ "$expected" =~ ^[0-9a-f]{40}$ ]] && [ "$(jq -er .client.commit <<< "$version")" = "$expected" ] || { echo 'Image commit identity mismatch' >&2; exit 1; }
auth=$(openssl rand -hex 32)
encryption=$(openssl rand -hex 32)
if [ "${GITHUB_ACTIONS:-}" = true ]; then
  echo "::add-mask::$auth"
  echo "::add-mask::$encryption"
fi
cid=$(docker run -d --platform "$platform" -p 127.0.0.1::8080 \
  -e NEXTAUTH_SECRET="$auth" -e ENCRYPTION_KEY="$encryption" \
  -e DATABASE_URL=file:/data/smoke.db \
  "$image" start --no-docker)
port=$(docker port "$cid" 8080/tcp | awk -F: '{print $NF}')
for _ in $(seq 1 90); do
  if curl -fsS --max-time 3 "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then
    curl -fsS --max-time 10 "http://127.0.0.1:$port/" >/dev/null
    echo "Image boot and embedded UI verified on $platform"
    exit 0
  fi
  [ "$(docker inspect -f '{{.State.Running}}' "$cid")" = true ] || break
  sleep 2
done
echo 'Image did not become healthy' >&2
exit 1

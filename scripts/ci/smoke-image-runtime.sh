#!/usr/bin/env bash
# Isolated Docker-in-Docker-client topology: distributed server + named volume
# + sibling agent containers on the host daemon through the production proxy.
set -euo pipefail
image=${1:?IMAGE required}
expected=${2:?EXPECTED_SHA required}
platform=${3:-linux/amd64}
docker info >/dev/null # mandatory gate: unavailable Docker is a failure
[[ "$expected" =~ ^[0-9a-f]{40}$ ]]
[ "$(git rev-parse HEAD)" = "$expected" ] || { echo 'Runtime harness source does not match candidate' >&2; exit 1; }
if [[ "$image" == *@* ]]; then
  image=$(docker manifest inspect "$image" | python3 "$(dirname "$0")/image-platform.py" "$image" "$platform")
fi
[ "$(docker run --rm --platform "$platform" "$image" version --format json | jq -er .client.commit)" = "$expected" ]
[ "$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$image")" = "$platform" ]
# Freeze a PR's local tag too; all following creates use this immutable image ID.
image=$(docker image inspect --format '{{.Id}}' "$image")
auth=$(openssl rand -hex 32)
encryption=$(openssl rand -hex 32)
prefix="crewship-mount-smoke-$(openssl rand -hex 6)"
work=$(mktemp -d)
cleanup() {
  status=$?
  # Every resource has a unique prefix or exact label from this invocation.
  for instance in "$prefix-a" "$prefix-b"; do
    while IFS= read -r id; do [ -z "$id" ] || docker rm -fv "$id" >/dev/null 2>&1 || true; done < <(docker ps -aq --filter "label=crewship.instance-id=$instance")
    while IFS= read -r name; do [ -z "$name" ] || docker volume rm "$name" >/dev/null 2>&1 || true; done < <(docker volume ls -q --filter "label=crewship.instance-id=$instance")
  done
  for n in "$prefix-a" "$prefix-b" "$prefix-proxy"; do docker rm -fv "$n" >/dev/null 2>&1 || true; done
  docker volume rm "$prefix-a" "$prefix-b" >/dev/null 2>&1 || true
  docker network rm "$prefix" >/dev/null 2>&1 || true
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT
arch=${platform#linux/}
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go test -c -tags conformance -o "$work/runtime.test" ./internal/provider/docker
chmod 755 "$work/runtime.test"
docker pull --platform "$platform" debian:bookworm-slim >/dev/null
docker network create --internal "$prefix" >/dev/null
docker run -d --name "$prefix-proxy" --network "$prefix" -p 127.0.0.1::2375 \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -e POST=1 -e CONTAINERS=1 -e EXEC=1 -e IMAGES=1 -e NETWORKS=1 \
  -e VOLUMES=1 -e INFO=1 -e PING=1 -e VERSION=1 -e EVENTS=0 \
  tecnativa/docker-socket-proxy:v0.5.0 >/dev/null
proxy_port=$(docker port "$prefix-proxy" 2375/tcp | awk -F: '{print $NF}')
proxy_ready=0
for _ in $(seq 1 30); do
  if curl -fsS --max-time 2 "http://127.0.0.1:$proxy_port/_ping" >/dev/null 2>&1; then proxy_ready=1; break; fi
  sleep 1
 done
[ "$proxy_ready" = 1 ] || { echo 'Socket proxy did not become ready' >&2; exit 1; }
start_server() {
  local name=$1
  docker run -d --platform "$platform" --name "$name" --network "$prefix" \
    -v "$name:/var/lib/crewship" -p 127.0.0.1::8080 \
    -e NEXTAUTH_SECRET="$auth" -e ENCRYPTION_KEY="$encryption" \
    -e CREWSHIP_HOME=/var/lib/crewship/server \
    -e DATABASE_URL=file:/var/lib/crewship/control.db \
    -e CREWSHIP_BOLT_PATH=/var/lib/crewship/state.db \
    -e CREWSHIP_LOG_PATH=/var/lib/crewship/logs \
    -e CREWSHIP_STORAGE_BASE_PATH=/var/lib/crewship \
    -e CREWSHIP_CONTAINER_PROVIDER=docker \
    -e CREWSHIP_CONTAINER_PREFIX="$name" \
    -e CREWSHIP_DOCKER_SERVER_CONTAINER="$name" \
    -e DOCKER_HOST="tcp://$prefix-proxy:2375" \
    "$image" start >/dev/null
  local port
  port=$(docker port "$name" 8080/tcp | awk -F: '{print $NF}')
  for _ in $(seq 1 90); do
    if curl -fsS --max-time 3 "http://127.0.0.1:$port/readyz" >/dev/null 2>&1; then
      docker cp "$work/runtime.test" "$name:/tmp/runtime.test"
      return
    fi
    sleep 1
  done
  echo "Containerized runtime did not become ready: $name" >&2
  # Do not print application logs: startup can contain instance-specific data.
  return 1
}
probe() {
  docker exec -e CREWSHIP_CONTAINERIZED_STORAGE_SMOKE=1 -e CREWSHIP_SMOKE_PREFIX="$1" -e CREWSHIP_SMOKE_PERSISTED="${2:-0}" \
    "$1" /tmp/runtime.test -test.run '^TestContainerizedStorage$' -test.v -test.timeout 4m
}
# Two concurrently running server instances on independent named volumes.
start_server "$prefix-a"
start_server "$prefix-b"
probe "$prefix-a"
probe "$prefix-b"
# Restart, then full recreate reusing precisely the same volume names.
# Simulate a stale staged generation: the next provider must restage the exact
# distributed sidecar, and the in-container test compares its bytes.
docker exec "$prefix-a" sh -c 'printf stale > /var/lib/crewship/runtime-smoke/.runtime/crewship-sidecar'
docker restart "$prefix-a" >/dev/null
probe "$prefix-a" 1
docker rm -f "$prefix-a" >/dev/null
start_server "$prefix-a"
probe "$prefix-a" 1
echo "Containerized runtime verified: $image ($platform) source=$expected"

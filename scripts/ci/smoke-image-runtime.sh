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
fixture_dir=$(cd "$(dirname "$0")" && pwd)
data_volume_a="$prefix-a"
previous=${CREWSHIP_SMOKE_UPGRADE_FROM:-}
previous_sha=${CREWSHIP_SMOKE_PREVIOUS_SHA:-}
cleanup() {
  status=$?
  # Every resource has a unique prefix or exact label from this invocation.
  for instance in "$prefix-a" "$prefix-b"; do
    while IFS= read -r id; do [ -z "$id" ] || docker rm -fv "$id" >/dev/null 2>&1 || true; done < <(docker ps -aq --filter "label=crewship.instance-id=$instance")
    while IFS= read -r name; do [ -z "$name" ] || docker volume rm "$name" >/dev/null 2>&1 || true; done < <(docker volume ls -q --filter "label=crewship.instance-id=$instance")
  done
  for n in "$prefix-a" "$prefix-b" "$prefix-proxy" "$prefix-native-probe" "$prefix-storage-helper" "$prefix-path-probe"; do docker rm -fv "$n" >/dev/null 2>&1 || true; done
  docker volume rm "$prefix-a" "$prefix-b" "$prefix-a-restored" >/dev/null 2>&1 || true
  docker network rm "$prefix" >/dev/null 2>&1 || true
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT
# Multi-architecture index digests: no mutable third-party tags are executed
# by publication jobs that also have registry credentials.
proxy_image=tecnativa/docker-socket-proxy@sha256:1f5038b54f06c3e18422902cf00ba21803d1c97805aae032e5e6673d532d3459
crew_image=debian@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc
crew_image=$(docker manifest inspect "$crew_image" | python3 "$(dirname "$0")/image-platform.py" "$crew_image" "$platform")
arch=${platform#linux/}
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go test -c -tags conformance -o "$work/runtime.test" ./internal/provider/docker
chmod 755 "$work/runtime.test"
docker pull --platform "$platform" "$crew_image" >/dev/null

docker network create --internal "$prefix" >/dev/null
docker run -d --name "$prefix-proxy" --network "$prefix" \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -e POST=1 -e CONTAINERS=1 -e EXEC=1 -e IMAGES=1 -e NETWORKS=1 \
  -e VOLUMES=1 -e INFO=1 -e PING=1 -e VERSION=1 -e EVENTS=0 \
  "$proxy_image" >/dev/null
proxy_ready=0
for _ in $(seq 1 30); do
  if docker exec "$prefix-proxy" wget -q -T 2 -O - http://127.0.0.1:2375/_ping >/dev/null 2>&1; then proxy_ready=1; break; fi
  sleep 1
 done
[ "$proxy_ready" = 1 ] || { echo 'Socket proxy did not become ready' >&2; exit 1; }
start_server() {
  local name=$1 server_image=${2:-$image} data_volume=$1
  if [ "$name" = "$prefix-a" ]; then data_volume=$data_volume_a; fi
  docker run -d --platform "$platform" --name "$name" --network "$prefix" \
    -v "$data_volume:/var/lib/crewship" \
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
    "$server_image" start >/dev/null
  if [ "$server_image" = "$image" ]; then wait_server "$name"; else wait_server "$name" 0; fi
}
wait_server() {
  local name=$1
  for _ in $(seq 1 90); do
    if docker exec "$name" wget -q -T 3 -O - http://127.0.0.1:8080/readyz >/dev/null 2>&1; then
      if [ "${2:-1}" = 1 ]; then
        docker exec "$name" wget -q -T 5 -O - http://127.0.0.1:8080/ >/dev/null
        docker cp "$work/runtime.test" "$name:/tmp/runtime.test"
      fi
      return
    fi
    sleep 1
  done
  echo "Containerized runtime did not become ready: $name" >&2
  # Do not print application logs: startup can contain instance-specific data.
  return 1
}
probe() {
  docker exec -e CREWSHIP_CONTAINERIZED_STORAGE_SMOKE=1 -e CREWSHIP_SMOKE_PREFIX="$1" -e CREWSHIP_SMOKE_PERSISTED="${2:-0}" -e CREWSHIP_SMOKE_CREW_IMAGE="$crew_image" -e CREWSHIP_SMOKE_NATIVE_MOUNTS="$native_mounts" \
    "$1" /tmp/runtime.test -test.run '^TestContainerizedStorage$' -test.v -test.timeout 4m
}
# Optional cross-version rehearsal: synthetic volumes only, never caller storage.
# The archived original stays offline while its restored replacement is active.
helper() {
  local volume=$1
  shift
  docker run --rm --name "$prefix-storage-helper" --platform "$platform" \
    --network none --read-only --user 0:0 --cap-drop ALL \
    --cap-add CHOWN --cap-add DAC_OVERRIDE --cap-add FOWNER \
    --security-opt no-new-privileges --pids-limit 64 --memory 256m \
    --mount "type=volume,src=$volume,dst=/store" \
    --entrypoint sh "$image" -ec "set -o pipefail; $*"
}
db_marker() {
  docker run --rm --name "$prefix-storage-helper" --platform "$platform" \
    --network none --read-only --user 1001:1001 --cap-drop ALL \
    --security-opt no-new-privileges --pids-limit 64 --memory 256m \
    --mount "type=volume,src=$1,dst=/store" \
    --mount "type=bind,src=$work/db-check,dst=/db-check,readonly" \
    --entrypoint /db-check "$image" "$2" /store/control.db "$prefix"
}
# Commands below are intentionally expanded inside the isolated helper.
# shellcheck disable=SC2016
manifest() {
  helper "$1" 'find /store -print | LC_ALL=C sort | while IFS= read -r path; do stat -c "%F %a %u %g %n" "$path"; if [ -L "$path" ]; then readlink "$path"; elif [ -f "$path" ]; then sha256sum "$path"; fi; done | sha256sum'
}
# shellcheck disable=SC2016
identity() {
  helper "$1" 'test -n "$(find /store/server/installations -type f ! -name "*.lock")"; find /store/server/installations -type f ! -name "*.lock" -print | LC_ALL=C sort | while IFS= read -r path; do sha256sum "$path"; done | sha256sum'
}
stop_server() {
  docker stop --time 30 "$prefix-a" >/dev/null
  [ "$(docker inspect -f '{{.State.ExitCode}}' "$prefix-a")" = 0 ]
  docker rm "$prefix-a" >/dev/null
}
original_path_probe() {
  # Reproduce create rejection/acceptance; neither test container is started.
  docker exec "$prefix-a" test -x /var/lib/crewship/.runtime/crewship-sidecar
  docker exec "$prefix-a" test -x /var/lib/crewship/.runtime/entrypoint.sh
  if docker exec "$prefix-a" docker create --name "$prefix-path-probe" \
    --mount type=bind,src=/var/lib/crewship/.runtime/crewship-sidecar,dst=/helper,readonly \
    "$image" >"$work/rejected-create" 2>&1; then
    echo 'Baseline unexpectedly accepted the unmapped source' >&2; return 1
  fi
  grep -q 'bind source path does not exist' "$work/rejected-create"
  local daemon_root
  daemon_root=$(docker volume inspect -f '{{.Mountpoint}}' "$data_volume_a")
  docker exec "$prefix-a" docker create --name "$prefix-path-probe" \
    --mount "type=bind,src=$daemon_root/.runtime/crewship-sidecar,dst=/helper,readonly" \
    "$image" >/dev/null
  [ "$(docker inspect -f '{{.State.Status}}' "$prefix-path-probe")" = created ]
  docker rm "$prefix-path-probe" >/dev/null
  echo 'Unmodified baseline image: local source rejected, daemon source accepted without start'
}
if [ -n "$previous" ]; then
  [[ "$previous" =~ ^ghcr\.io/crewship-ai/crewship@sha256:[0-9a-f]{64}$ ]]
  [[ "$previous_sha" =~ ^[0-9a-f]{40}$ ]]
  docker pull --platform "$platform" "$previous" >/dev/null
  [ "$(docker run --rm --platform "$platform" "$previous" version --format json | jq -er .client.commit)" = "$previous_sha" ]
  [ "$(docker image inspect -f '{{.Os}}/{{.Architecture}}' "$previous")" = "$platform" ]
  previous=$(docker image inspect -f '{{.Id}}' "$previous")
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$work/db-check" "$fixture_dir/storage-upgrade-db/main.go"
  chmod 755 "$work/db-check"
  start_server "$prefix-a" "$previous"
  original_path_probe
  docker exec "$prefix-a" sh -ec 'mkdir -p /var/lib/crewship/upgrade; printf preserved > /var/lib/crewship/upgrade/marker; chmod 600 /var/lib/crewship/upgrade/marker; ln -s marker /var/lib/crewship/upgrade/reference'
  stop_server
  db_marker "$prefix-a" seed
  original_identity=$(identity "$prefix-a")
  before=$(manifest "$prefix-a")
  helper "$prefix-a" 'tar -C /store -cpf - .' >"$work/backup.tar"
  # Restore numerically owned files and modes into a fresh private volume.
  docker volume create "$prefix-a-restored" >/dev/null
  docker run --rm -i --name "$prefix-storage-helper" --platform "$platform" \
    --network none --read-only --user 0:0 --cap-drop ALL \
    --cap-add CHOWN --cap-add DAC_OVERRIDE --cap-add FOWNER \
    --security-opt no-new-privileges --pids-limit 64 --memory 256m \
    --mount "type=volume,src=$prefix-a-restored,dst=/store" \
    --entrypoint sh "$image" -ec 'tar -C /store -xpf -' <"$work/backup.tar"
  [ "$(manifest "$prefix-a-restored")" = "$before" ]
  db_marker "$prefix-a-restored" check
  data_volume_a="$prefix-a-restored"
  echo "Offline archive/restore bytes, paths, permissions and ownership verified: baseline=$previous source=$previous_sha"
fi

# Two concurrently running server instances on independent named volumes.
start_server "$prefix-a"
start_server "$prefix-b"
if [ -n "$previous" ]; then
  docker exec "$prefix-a" cmp /usr/local/bin/crewship-sidecar /var/lib/crewship/.runtime/crewship-sidecar
  docker exec "$prefix-a" cmp /usr/local/bin/entrypoint.sh /var/lib/crewship/.runtime/entrypoint.sh
fi
# Native client control on the same backing filesystem as the mapped data.
# Compare observations; enforcement of specific restrictions is a separate gate.
docker exec "$prefix-a" mkdir -p /var/lib/crewship/native-control/workspace /var/lib/crewship/native-control/output /var/lib/crewship/native-control/crew
native_root=$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/var/lib/crewship"}}{{.Source}}{{end}}{{end}}' "$prefix-a")
[ -n "$native_root" ] || { echo 'Native data source was not discovered' >&2; exit 1; }
native_mount_args=()
for target in workspace output crew; do
  native_mount_args+=(--mount "type=volume,dst=/$target,volume-driver=local,volume-opt=type=none,\"volume-opt=device=$native_root/native-control/$target\",\"volume-opt=o=bind,noexec,nosuid\"")
done
native_mounts=$(docker run --rm --name "$prefix-native-probe" --platform "$platform" \
  --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
  --user 1001:1001 "${native_mount_args[@]}" \
  "$crew_image" awk '$2=="/workspace" || $2=="/output" || $2=="/crew" {print $2 "=" $4}' /proc/mounts)
printf 'Native effective mount flags:\n%s\n' "$native_mounts"
probe "$prefix-a"
probe "$prefix-b"
# Restart, then full recreate reusing precisely the same volume names.
# Simulate a stale staged generation: the next provider must restage the exact
# distributed sidecar, and the in-container test compares its bytes.
docker exec "$prefix-a" sh -ec 'printf stale > /var/lib/crewship/runtime-smoke/.runtime/crewship-sidecar.next; mv /var/lib/crewship/runtime-smoke/.runtime/crewship-sidecar.next /var/lib/crewship/runtime-smoke/.runtime/crewship-sidecar'
docker restart "$prefix-a" >/dev/null
wait_server "$prefix-a"
probe "$prefix-a" 1
docker rm -f "$prefix-a" >/dev/null
start_server "$prefix-a"
probe "$prefix-a" 1
if [ -n "$previous" ]; then
  # Stop the canary agent before rolling back the server; retain its volumes.
  while IFS= read -r id; do [ -z "$id" ] || docker stop --time 10 "$id" >/dev/null; done < <(docker ps -q --filter "label=crewship.instance-id=$prefix-a")
  stop_server
  db_marker "$data_volume_a" check
  [ "$(identity "$data_volume_a")" = "$original_identity" ]
  # shellcheck disable=SC2016
  helper "$data_volume_a" 'test "$(cat /store/upgrade/marker)" = preserved; test "$(readlink /store/upgrade/reference)" = marker; test "$(stat -c "%a %u %g" /store/upgrade/marker)" = "600 1001 1001"'
  start_server "$prefix-a" "$previous"
  original_path_probe
  stop_server
  db_marker "$data_volume_a" check
  [ "$(identity "$data_volume_a")" = "$original_identity" ]
  [ "$(manifest "$prefix-a")" = "$before" ]
  echo "Cross-version upgrade/restart/recreate/rollback preserved database, HOME identity, data and backup: candidate=$image baseline=$previous"
  echo 'Rollback retained the original runtime path defect; it is not a runtime repair'
fi
echo "Containerized runtime verified: $image ($platform) source=$expected"

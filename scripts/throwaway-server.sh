#!/usr/bin/env bash
# throwaway-server.sh — a disposable Crewship server that cleans up after itself.
#
#   scripts/throwaway-server.sh run   NAME [--binary PATH] [--port N] -- CMD [ARGS...]
#   scripts/throwaway-server.sh start NAME [--binary PATH] [--port N]
#   scripts/throwaway-server.sh stop  NAME
#   scripts/throwaway-server.sh list
#
# `run` starts the server, runs CMD with CREWSHIP_SERVER/CREWSHIP_CONFIG
# pointing at it, and tears everything down when CMD ends — success, failure
# or Ctrl-C (trap). `start` leaves it running for interactive work; `stop`
# tears it down later.
#
# Why: a test server whose data directory is deleted while its crew
# containers live on leaves resources that no installation remembers (dozens
# of such containers and their volumes were found on the dev host). Every
# server started here gets its own data directory, port, container prefix and
# installation identity, and a manifest outside the data directory naming
# exactly what it owns. Teardown removes only those resources:
#   - containers labelled with this installation's crewship.instance-id or
#     named with this server's unique prefix,
#   - named volumes with that prefix and the anonymous volumes those
#     containers mounted from under this data directory.
# Nothing is pruned by pattern beyond that prefix. The data directory is
# deleted only after every resource is gone; on any failure it stays, with the
# manifest marked teardown_failed, so the owner evidence survives for a retry
# (`stop NAME` again) or for the host inventory. A SIGKILL or host crash skips
# the trap: the manifest and data directory remain and `stop NAME` finishes
# the job.
set -euo pipefail

STATE_ROOT="${CREWSHIP_THROWAWAY_STATE:-$HOME/.local/state/crewship-throwaway}"
PORT_RANGE_START=8110
PORT_RANGE_END=8199

die() { echo "throwaway-server: $*" >&2; exit 1; }
log() { echo "throwaway-server: $*" >&2; }

valid_name() { [[ "$1" =~ ^[a-z0-9][a-z0-9-]{0,30}$ ]]; }

dir_for() { echo "$STATE_ROOT/$1"; }

manifest_get() { # dir key
  python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get(sys.argv[2],""))' "$1/manifest.json" "$2"
}

manifest_set() { # dir key value
  python3 - "$1/manifest.json" "$2" "$3" <<'PY'
import json, sys
path, key, value = sys.argv[1:4]
with open(path) as f:
    data = json.load(f)
data[key] = value
with open(path + ".tmp", "w") as f:
    json.dump(data, f, indent=2)
import os
os.replace(path + ".tmp", path)
PY
}

free_port() {
  local p
  for ((p = PORT_RANGE_START; p <= PORT_RANGE_END; p++)); do
    if ! ss -ltn "sport = :$p" | grep -q LISTEN; then
      echo "$p"
      return 0
    fi
  done
  return 1
}

instance_id() { # data dir
  local f
  for f in "$1"/installations/*; do
    [[ -f "$f" && "$f" != *.db && "$f" != *.lock ]] || continue
    tr -d '[:space:]' <"$f"
    return 0
  done
  return 1
}

cmd_start() {
  local name="$1"; shift
  local binary="" port=""
  while (($#)); do
    case "$1" in
      --binary) binary="$2"; shift 2 ;;
      --port) port="$2"; shift 2 ;;
      *) die "unknown option $1" ;;
    esac
  done
  valid_name "$name" || die "name must be lowercase letters, digits and dashes (max 31)"
  local dir; dir="$(dir_for "$name")"
  [[ -e "$dir" ]] && die "$name already exists ($dir); stop it first"
  if [[ -z "$binary" ]]; then
    binary="$(command -v crewship || true)"
  fi
  [[ -x "$binary" ]] || die "no crewship binary (pass --binary PATH)"
  if [[ -z "$port" ]]; then
    port="$(free_port)" || die "no free port in $PORT_RANGE_START-$PORT_RANGE_END"
  fi
  local suffix; suffix="$(od -An -N2 -tx1 /dev/urandom | tr -d ' \n')"
  local prefix="crewship-tw-$name-$suffix"

  mkdir -p "$dir/data"
  chmod 700 "$dir"
  cat >"$dir/manifest.json" <<EOF
{
  "name": "$name",
  "state": "starting",
  "created_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "binary": "$binary",
  "port": "$port",
  "prefix": "$prefix",
  "data_dir": "$dir/data",
  "database": "$dir/data/crewship.db",
  "pid": "",
  "instance_id": ""
}
EOF
  (
    cd "$dir"
    env -i HOME="$HOME" PATH="$PATH" \
      CREWSHIP_DATA_DIR="$dir/data" CREWSHIP_HOST=127.0.0.1 CREWSHIP_PORT="$port" \
      CREWSHIP_CONTAINER_PREFIX="$prefix" \
      setsid "$binary" start >"$dir/server.log" 2>&1 &
    echo $! >"$dir/server.pid"
  )
  local pid; pid="$(cat "$dir/server.pid")"
  manifest_set "$dir" pid "$pid"

  local i
  for ((i = 0; i < 180; i++)); do
    if ! kill -0 "$pid" 2>/dev/null; then
      manifest_set "$dir" state "failed_to_start"
      die "server exited during start; see $dir/server.log"
    fi
    if curl -fs -o /dev/null "http://127.0.0.1:$port/"; then
      break
    fi
    sleep 1
  done
  ((i < 180)) || die "server did not answer on :$port within 180 s; see $dir/server.log"
  local iid; iid="$(instance_id "$dir/data" || true)"
  manifest_set "$dir" instance_id "$iid"
  manifest_set "$dir" state "running"
  log "$name running on http://127.0.0.1:$port (prefix $prefix, instance ${iid:-unknown})"
  echo "export CREWSHIP_SERVER=http://127.0.0.1:$port CREWSHIP_CONFIG=$dir/cli-config.yaml"
}

# owned_resources prints "container ID" and "volume NAME" lines for exactly
# what this server owns. Called with the server stopped. Any docker failure
# returns non-zero: an incomplete list must never be taken as "nothing left".
owned_resources() {
  local dir="$1" prefix iid data ids more id mounts vol device vols
  prefix="$(manifest_get "$dir" prefix)" || return 1
  iid="$(manifest_get "$dir" instance_id)" || return 1
  data="$(manifest_get "$dir" data_dir)" || return 1
  [[ -n "$prefix" && -n "$data" ]] || return 1
  ids="$(docker ps -aq --no-trunc --filter "name=^/$prefix-")" || return 1
  if [[ -n "$iid" ]]; then
    more="$(docker ps -aq --no-trunc --filter "label=crewship.instance-id=$iid")" || return 1
    ids+=$'\n'"$more"
  fi
  while read -r id; do
    [[ -n "$id" ]] || continue
    echo "container $id"
    # Anonymous volumes this container mounted from under our data directory.
    mounts="$(docker inspect --format '{{range .Mounts}}{{if eq .Type "volume"}}{{.Name}}{{"\n"}}{{end}}{{end}}' "$id")" || return 1
    for vol in $mounts; do
      device="$(docker volume inspect --format '{{index .Options "device"}}' "$vol" 2>/dev/null || true)"
      if [[ "$vol" == "$prefix-"* || "$device" == "$data"/* ]]; then
        echo "volume $vol"
      fi
    done
  done < <(sort -u <<<"$ids")
  vols="$(docker volume ls -q)" || return 1
  for vol in $vols; do
    [[ "$vol" == "$prefix-"* ]] && echo "volume $vol"
  done
  return 0
}

cmd_stop() {
  local name="$1"
  valid_name "$name" || die "invalid name"
  local dir; dir="$(dir_for "$name")"
  [[ -f "$dir/manifest.json" ]] || die "no throwaway server named $name"
  manifest_set "$dir" state "stopping"

  local pid; pid="$(manifest_get "$dir" pid)"
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    kill -TERM "$pid" 2>/dev/null || true
    local i
    for ((i = 0; i < 30; i++)); do kill -0 "$pid" 2>/dev/null || break; sleep 1; done
    if kill -0 "$pid" 2>/dev/null; then
      kill -KILL "$pid" 2>/dev/null || true
    fi
  fi
  if [[ -z "$(manifest_get "$dir" instance_id)" ]]; then
    manifest_set "$dir" instance_id "$(instance_id "$(manifest_get "$dir" data_dir)" || true)"
  fi

  local list
  if ! list="$(owned_resources "$dir")"; then
    manifest_set "$dir" state "teardown_failed"
    die "could not establish what $name owns (docker unavailable or manifest incomplete); data and manifest kept in $dir"
  fi
  list="$(sort -u <<<"$list")"
  printf '%s\n' "$list" >"$dir/resources.txt"
  local failed=0 kind ref
  # Containers before volumes: docker refuses to remove an attached volume.
  while read -r kind ref; do
    [[ "$kind" == container ]] || continue
    docker rm -f "$ref" >/dev/null 2>&1 || { log "could not remove container $ref"; failed=1; }
  done <<<"$list"
  while read -r kind ref; do
    [[ "$kind" == volume ]] || continue
    docker volume rm "$ref" >/dev/null 2>&1 || { log "could not remove volume $ref"; failed=1; }
  done <<<"$list"

  if ((failed)); then
    manifest_set "$dir" state "teardown_failed"
    die "teardown incomplete; data and manifest kept in $dir — run 'stop $name' again"
  fi
  rm -rf "$dir"
  log "$name removed ($(grep -c . <<<"$list" || true) resources)"
}

cmd_run() {
  local name="$1"; shift
  local opts=()
  while (($#)) && [[ "$1" != "--" ]]; do opts+=("$1"); shift; done
  [[ "${1:-}" == "--" ]] || die "run needs -- CMD"
  shift
  (($#)) || die "run needs a command after --"
  local env_line
  env_line="$(cmd_start "$name" "${opts[@]}")"
  # shellcheck disable=SC2064 # expand NAME now: the trap must stop this server.
  trap "cmd_stop '$name'" EXIT
  trap 'exit 130' INT TERM
  local port; port="$(manifest_get "$(dir_for "$name")" port)"
  CREWSHIP_SERVER="http://127.0.0.1:$port" CREWSHIP_CONFIG="$(dir_for "$name")/cli-config.yaml" "$@"
  : "$env_line"
}

cmd_list() {
  local m
  for m in "$STATE_ROOT"/*/manifest.json; do
    [[ -f "$m" ]] || continue
    python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d["name"], d["state"], d["port"], d["prefix"], d.get("instance_id",""))' "$m"
  done
}

main() {
  (($#)) || { sed -n '2,8p' "$0"; exit 2; }
  local sub="$1"; shift
  case "$sub" in
    start) (($#)) || die "start NAME"; cmd_start "$@" ;;
    stop) (($#)) || die "stop NAME"; cmd_stop "$1" ;;
    run) (($#)) || die "run NAME -- CMD"; cmd_run "$@" ;;
    list) cmd_list ;;
    *) die "unknown command $sub" ;;
  esac
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi

#!/usr/bin/env bash
# throwaway-server.sh — a disposable Crewship server that cleans up after itself.
#
#   scripts/throwaway-server.sh run   NAME [--binary PATH] [--port N] [--env NAME=VALUE]... [--page-projects [DIR]] -- CMD [ARGS...]
#   scripts/throwaway-server.sh start NAME [--binary PATH] [--port N] [--env NAME=VALUE]... [--page-projects [DIR]]
#   scripts/throwaway-server.sh stop  NAME
#   scripts/throwaway-server.sh list
#
# `--env` accepts only optional feature settings (see THROWAWAY_ENV_ALLOWLIST)
# and `--page-projects` configures Page project storage under this instance's
# own state dir, so teardown removes it with everything else.
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
# A cold start migrates an empty database; under load that has taken >180 s.
START_TIMEOUT="${CREWSHIP_THROWAWAY_START_TIMEOUT:-600}"
# Crew containers write into the data directory as the agent uid, so those
# files cannot be unlinked by us; a throwaway container removes them.
HELPER_IMAGE="${CREWSHIP_THROWAWAY_HELPER_IMAGE:-alpine:3}"

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

# Optional settings `--env` may pass through the `env -i` below. This is an
# ALLOWLIST, not a denylist: anything that names the server's identity, data,
# network or lifecycle (CREWSHIP_DATA_DIR/HOST/PORT/CONTAINER_PREFIX/CONFIG,
# DATABASE_URL, HOME, PATH, …) is refused, because overriding one of those
# makes the "throwaway" server run on live data while the manifest still
# names this dir — exactly the leak the teardown contract exists to prevent.
# Keep this list to optional feature configuration only.
THROWAWAY_ENV_ALLOWLIST=(
  CREWSHIP_PAGE_BUILD_IMAGE
  CREWSHIP_PAGE_RUNTIME_ORIGIN
  CREWSHIP_PAGE_RUNTIME_DEVELOPMENT_SAME_ORIGIN
  CREWSHIP_RESTRICTED_RUNTIME_IMAGE
  CREWSHIP_RESTRICTED_NATIVE_IMAGE
  CREWSHIP_RATELIMIT_DISABLED
  CREWSHIP_SKIP_SIDECAR
  CREWSHIP_ALLOW_SIGNUP
  CREWSHIP_NEXTJS_URL
)

cmd_start() {
  local name="$1"; shift
  local binary="" port="" page_projects=""
  local -a extra_env=()
  while (($#)); do
    case "$1" in
      --binary) binary="$2"; shift 2 ;;
      --port) port="$2"; shift 2 ;;
      # Pass one optional feature setting to the server process. The key
      # must be on the allowlist above (see its comment for why anything
      # else is refused). Repeatable; NAME=VALUE form only.
      --env)
        [[ "${2:-}" =~ ^[A-Za-z_][A-Za-z0-9_]*= ]] || die "--env expects NAME=VALUE, got '${2:-}'"
        local key="${2%%=*}"
        local ok=0
        local allowed
        for allowed in "${THROWAWAY_ENV_ALLOWLIST[@]}"; do
          [[ "$key" == "$allowed" ]] && { ok=1; break; }
        done
        [[ "$ok" -eq 1 ]] || die "--env: '$key' is not an optional feature setting; allowed: ${THROWAWAY_ENV_ALLOWLIST[*]}"
        extra_env+=("$2")
        shift 2
        ;;
      # Configure CREWSHIP_PAGE_PROJECTS_PATH under this throwaway's own
      # state dir, so the server gets real Page project storage AND teardown
      # provably removes it (config requires the path outside crew storage,
      # so it cannot live under data/; anywhere else would be leaked). The
      # optional argument is a single path component (default
      # "page-projects"); absolute paths and traversal are refused.
      --page-projects)
        page_projects="page-projects"
        if [[ "${2:-}" != "" && "$2" != --* ]]; then
          page_projects="$2"
          shift
        fi
        [[ "$page_projects" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,40}$ ]] \
          || die "--page-projects expects a simple directory name, got '$page_projects'"
        # Names this script itself keeps in the instance dir (data would put
        # the projects path ON the data dir, which config refuses at boot).
        case "$page_projects" in
          data|server.log|server.pid|manifest.json|cli-config.yaml)
            die "--page-projects: '$page_projects' is reserved for the instance's own files" ;;
        esac
        shift
        ;;
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
  if [[ -n "$page_projects" ]]; then
    # Owned by this instance: under $dir (so remove_state deletes it) but
    # outside $dir/data (config forbids the overlap with crew storage).
    mkdir -p "$dir/$page_projects"
    chmod 700 "$dir/$page_projects"
    extra_env+=("CREWSHIP_PAGE_PROJECTS_PATH=$dir/$page_projects")
  fi
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
  "page_projects_dir": "${page_projects:+$dir/$page_projects}",
  "pid": "",
  "instance_id": ""
}
EOF
  (
    cd "$dir"
    env -i HOME="$HOME" PATH="$PATH" \
      CREWSHIP_DATA_DIR="$dir/data" CREWSHIP_HOST=127.0.0.1 CREWSHIP_PORT="$port" \
      CREWSHIP_CONTAINER_PREFIX="$prefix" \
      ${extra_env[@]+"${extra_env[@]}"} \
      setsid "$binary" start >"$dir/server.log" 2>&1 &
    echo $! >"$dir/server.pid"
  )
  local pid; pid="$(cat "$dir/server.pid")"
  manifest_set "$dir" pid "$pid"

  local i
  for ((i = 0; i < START_TIMEOUT; i++)); do
    if ! kill -0 "$pid" 2>/dev/null; then
      manifest_set "$dir" state "failed_to_start"
      die "server exited during start; see $dir/server.log — 'stop $name' cleans up"
    fi
    # Any HTTP answer means it is up: / is 404 on a build without the web UI.
    if [[ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/healthz" || true)" != 000 ]]; then
      break
    fi
    sleep 1
  done
  if ((i >= START_TIMEOUT)); then
    # Never leave a server running that the caller believes failed.
    kill -TERM "$pid" 2>/dev/null || true
    manifest_set "$dir" state "failed_to_start"
    die "server did not answer on :$port within $START_TIMEOUT s; see $dir/server.log — 'stop $name' cleans up"
  fi
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
  if ! remove_state "$dir"; then
    manifest_set "$dir" state "teardown_failed"
    die "resources removed but $dir could not be deleted — run 'stop $name' again"
  fi
  log "$name removed ($(grep -c . <<<"$list" || true) resources)"
}

# remove_state deletes the data directory first and the manifest last, so a
# failure part-way leaves the manifest naming what is still there.
remove_state() {
  local dir="$1" data
  data="$(manifest_get "$dir" data_dir)" || return 1
  [[ "$data" == "$dir/data" ]] || return 1
  if [[ -e "$data" ]] && ! rm -rf "$data" 2>/dev/null; then
    docker run --rm --network none -v "$data:/d" "$HELPER_IMAGE" \
      sh -c 'find /d -mindepth 1 -delete' >/dev/null 2>&1 || return 1
    rm -rf "$data" || return 1
  fi
  rm -rf "$dir"
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

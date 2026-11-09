#!/usr/bin/env bash
# throwaway-server.sh — a disposable Crewship server that cleans up after itself.
#
#   scripts/throwaway-server.sh run   NAME [--binary PATH] [--port N] [--env NAME=VALUE]... [--page-projects [DIR]] [--documents] -- CMD [ARGS...]
#   scripts/throwaway-server.sh start NAME [--binary PATH] [--port N] [--env NAME=VALUE]... [--page-projects [DIR]] [--documents] [--owner-nonce TOKEN]
#   scripts/throwaway-server.sh stop  NAME
#   scripts/throwaway-server.sh restart NAME [--down-seconds N]
#   scripts/throwaway-server.sh list
#
# `--env` accepts only optional feature settings (see THROWAWAY_ENV_ALLOWLIST)
# and `--page-projects` configures Page project storage under this instance's
# own state dir, so teardown removes it with everything else.
# `--documents` creates private authority/service socket paths and a mode-0600
# service-token file under that state dir. Go receives paths only; CMD owns any
# Node child. `run` passes CREWSHIP_THROWAWAY_MANIFEST to CMD; its JSON fields
# documents_authority_socket, documents_service_socket and
# documents_service_token_file contain owned paths, never the secret value.
# Use a short absolute CREWSHIP_THROWAWAY_STATE when needed for Unix sockets.
#
# `run` starts the server, runs CMD with CREWSHIP_SERVER/CREWSHIP_CONFIG
# pointing at it, and tears everything down when CMD ends — success, failure
# or Ctrl-C (trap). `start` leaves it running for interactive work; `stop`
# tears it down later.
#
# `restart` stops only the server process (SIGTERM, SIGKILL after 30 s) and
# starts the same binary again on the same data directory, port, container
# prefix and settings. Nothing is reseeded and no container, volume or file is
# removed, so a recovery test sees exactly what a deploy-time restart leaves
# behind. `--down-seconds N` keeps the server stopped for N seconds first (for
# example past the 60 s work lease). It refuses an instance whose recorded
# installation identity no longer matches its data directory, and a port that
# another process took meanwhile (state restart_failed; restart or stop again).
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
  CREWSHIP_ALLOWED_ORIGINS
  CREWSHIP_CRAFT_NATIVE_ENABLED
  CREWSHIP_CRAFT_NATIVE_AGENTS_ENABLED
  CREWSHIP_CRAFT_NATIVE_EDITOR_BUNDLE_DIR
  CREWSHIP_CRAFT_NATIVE_WORD_ENGINE_REVISION
  CREWSHIP_CRAFT_NATIVE_WORD_CLI_PATH
  CREWSHIP_CRAFT_NATIVE_WORD_CLI_SHA256
  CREWSHIP_CRAFT_NATIVE_WORD_EDITOR_BUNDLE
  CREWSHIP_CRAFT_NATIVE_WORD_EDITOR_URL
  CREWSHIP_CRAFT_NATIVE_GRID_ENGINE_REVISION
  CREWSHIP_CRAFT_NATIVE_GRID_CLI_PATH
  CREWSHIP_CRAFT_NATIVE_GRID_CLI_SHA256
  CREWSHIP_CRAFT_NATIVE_GRID_EDITOR_BUNDLE
  CREWSHIP_CRAFT_NATIVE_GRID_EDITOR_URL
  CREWSHIP_ARTIFACT_DATA_RUNS_KEPT
  CREWSHIP_ARTIFACT_DATA_RUNS_ACTIVE
  CREWSHIP_ARTIFACT_DATA_RUN_KEY_TTL
  CREWSHIP_ARTIFACT_DATA_RUN_STALLED_AFTER
)

cmd_start() {
  local name="$1"; shift
  local binary="" port="" page_projects="" documents=0 owner_nonce=""
  local -a extra_env=()
  while (($#)); do
    case "$1" in
      --binary) binary="$2"; shift 2 ;;
      --port) port="$2"; shift 2 ;;
      --documents) documents=1; shift ;;
      # A caller-chosen token written into the manifest at claim time, so a
      # caller can later prove an instance is the one IT started.
      --owner-nonce)
        [[ "${2:-}" =~ ^[A-Za-z0-9-]{8,64}$ ]] || die "--owner-nonce expects 8-64 letters, digits or dashes"
        owner_nonce="$2"; shift 2 ;;
      # Pass one optional feature setting to the server process. The key
      # must be on the allowlist above (see its comment for why anything
      # else is refused). Repeatable; NAME=VALUE form only.
      --env)
        [[ "${2:-}" =~ ^[A-Za-z_][A-Za-z0-9_]*= ]] || die "--env expects NAME=VALUE, got '${2:-}'"
        # One line each: restart reads the saved settings back line by line.
        [[ "$2" != *$'\n'* ]] || die "--env values must be a single line"
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
          data|server.log|server.pid|server.env|manifest.json|cli-config.yaml)
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
  local documents_authority="" documents_service="" documents_token=""
  if ((documents)); then
    [[ "$page_projects" != documents ]] || die "--page-projects conflicts with the owned documents directory"
    documents_authority="$dir/documents/authority.sock"
    documents_service="$dir/documents/service.sock"
    documents_token="$dir/documents/service-token"
    python3 - "$documents_authority" "$documents_service" "$documents_token" <<'PYDOC' || die "invalid --documents paths"
import os, sys
for path in sys.argv[1:]:
    if not os.path.isabs(path) or os.path.normpath(path) != path or len(os.fsencode(path)) > 100:
        raise SystemExit('throwaway-server: --documents requires clean absolute owned paths <=100 bytes; use a shorter CREWSHIP_THROWAWAY_STATE')
PYDOC
  fi
  local suffix; suffix="$(od -An -N2 -tx1 /dev/urandom | tr -d ' \n')"
  local prefix="crewship-tw-$name-$suffix"

  # Atomic claim of the name: two concurrent starts cannot both own one dir.
  mkdir -p "$STATE_ROOT"
  mkdir "$dir" 2>/dev/null || die "$name already exists ($dir); stop it first"
  mkdir "$dir/data"
  chmod 700 "$dir"
  if [[ -n "$page_projects" ]]; then
    # Owned by this instance: under $dir (so remove_state deletes it) but
    # outside $dir/data (config forbids the overlap with crew storage).
    mkdir -p "$dir/$page_projects"
    chmod 700 "$dir/$page_projects"
    extra_env+=("CREWSHIP_PAGE_PROJECTS_PATH=$dir/$page_projects")
  fi
  if ((documents)); then
    mkdir "$dir/documents" || die "could not create private documents directory"
    chmod 700 "$dir/documents" || die "could not protect private documents directory"
    python3 - "$documents_token" <<'PYDOC' || die "could not create private documents token file"
import os, secrets, sys
fd = os.open(sys.argv[1], os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, 'w') as stream:
    stream.write(secrets.token_hex(32))
PYDOC
    extra_env+=("CREWSHIP_DOCUMENTS_ENABLED=true"
      "CREWSHIP_DOCUMENTS_AUTHORITY_SOCKET=$documents_authority"
      "CREWSHIP_DOCUMENTS_SERVICE_SOCKET=$documents_service"
      "CREWSHIP_DOCUMENTS_SERVICE_TOKEN_FILE=$documents_token")
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
  "documents_authority_socket": "$documents_authority",
  "documents_service_socket": "$documents_service",
  "documents_service_token_file": "$documents_token",
  "owner_nonce": "$owner_nonce",
  "pid": "",
  "instance_id": ""
}
EOF
  # The optional settings are kept beside the manifest (mode 0600) so that
  # `restart` gives the new process exactly what this one received. They are
  # allowlisted feature settings and owned paths, never secret values.
  local setting
  (umask 077; : >"$dir/server.env")
  for setting in ${extra_env[@]+"${extra_env[@]}"}; do
    printf '%s\n' "$setting" >>"$dir/server.env"
  done
  launch_server "$dir" failed_to_start
  local iid; iid="$(instance_id "$dir/data" || true)"
  manifest_set "$dir" instance_id "$iid"
  manifest_set "$dir" state "running"
  log "$name running on http://127.0.0.1:$port (prefix $prefix, instance ${iid:-unknown})"
  echo "export CREWSHIP_SERVER=http://127.0.0.1:$port CREWSHIP_CONFIG=$dir/cli-config.yaml"
}

# launch_server starts the recorded binary on the instance's own data
# directory, port, prefix and saved settings, then waits for any HTTP answer.
# On failure it records FAIL_STATE and exits; it never stops anything else.
launch_server() { # dir fail_state
  local dir="$1" fail_state="$2" name binary port prefix
  name="$(manifest_get "$dir" name)"
  binary="$(manifest_get "$dir" binary)"
  port="$(manifest_get "$dir" port)"
  prefix="$(manifest_get "$dir" prefix)"
  local -a saved_env=()
  if [[ -s "$dir/server.env" ]]; then
    mapfile -t saved_env <"$dir/server.env"
  fi
  (
    cd "$dir"
    env -i HOME="$HOME" PATH="$PATH" \
      CREWSHIP_DATA_DIR="$dir/data" CREWSHIP_HOST=127.0.0.1 CREWSHIP_PORT="$port" \
      CREWSHIP_CONTAINER_PREFIX="$prefix" \
      ${saved_env[@]+"${saved_env[@]}"} \
      setsid "$binary" start >>"$dir/server.log" 2>&1 &
    echo $! >"$dir/server.pid"
  )
  local pid; pid="$(cat "$dir/server.pid")"
  manifest_set "$dir" pid "$pid"
  # The kernel start time pins the pid to this process: a later restart or
  # stop never signals another process that inherited the number.
  manifest_set "$dir" pid_starttime "$(proc_starttime "$pid" || true)"

  local i
  for ((i = 0; i < START_TIMEOUT; i++)); do
    if ! kill -0 "$pid" 2>/dev/null; then
      manifest_set "$dir" state "$fail_state"
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
    manifest_set "$dir" state "$fail_state"
    die "server did not answer on :$port within $START_TIMEOUT s; see $dir/server.log — 'stop $name' cleans up"
  fi
}

# proc_starttime prints field 22 of /proc/PID/stat (clock ticks since boot).
proc_starttime() {
  python3 -c '
import sys
data = open("/proc/%s/stat" % sys.argv[1]).read()
print(data[data.rindex(")") + 2:].split()[19])' "$1" 2>/dev/null
}

# server_pid_ours reports whether PID is still this instance's server: same
# kernel start time as recorded at launch (when recorded) AND launched with
# this instance's data directory and container prefix. A recycled pid fails.
server_pid_ours() { # dir pid
  local dir="$1" pid="$2" recorded now data prefix
  [[ "$pid" =~ ^[0-9]+$ ]] && kill -0 "$pid" 2>/dev/null || return 1
  recorded="$(manifest_get "$dir" pid_starttime)"
  if [[ -n "$recorded" ]]; then
    now="$(proc_starttime "$pid")" || return 1
    [[ "$now" == "$recorded" ]] || return 1
  fi
  data="$(manifest_get "$dir" data_dir)"
  prefix="$(manifest_get "$dir" prefix)"
  [[ -n "$data" && -n "$prefix" ]] || return 1
  python3 - "$pid" "$data" "$prefix" <<'PYENV'
import sys
pid, data, prefix = sys.argv[1:4]
try:
    env = open("/proc/%s/environ" % pid, "rb").read().split(b"\0")
except OSError:
    sys.exit(1)
want = {("CREWSHIP_DATA_DIR=" + data).encode(), ("CREWSHIP_CONTAINER_PREFIX=" + prefix).encode()}
sys.exit(0 if want <= set(env) else 1)
PYENV
}

# stop_server_process ends only the recorded server process: SIGTERM, then
# SIGKILL after 30 s. Containers, volumes and data are left alone. A pid that
# no longer belongs to this instance's server is never signalled.
stop_server_process() { # dir
  local dir="$1" pid; pid="$(manifest_get "$1" pid)"
  [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null || return 0
  if ! server_pid_ours "$dir" "$pid"; then
    log "pid $pid is not this instance's server (exited, pid reused); not signalling it"
    return 0
  fi
  kill -TERM "$pid" 2>/dev/null || true
  local i
  for ((i = 0; i < 30; i++)); do server_pid_ours "$dir" "$pid" || break; sleep 1; done
  if server_pid_ours "$dir" "$pid"; then
    kill -KILL "$pid" 2>/dev/null || true
    for ((i = 0; i < 10; i++)); do server_pid_ours "$dir" "$pid" || break; sleep 1; done
  fi
}

port_listening() { ss -ltn "sport = :$1" | grep -q LISTEN; }

cmd_restart() {
  local name="$1"; shift
  local down=0
  while (($#)); do
    case "$1" in
      --down-seconds)
        [[ "${2:-}" =~ ^[0-9]{1,5}$ ]] || die "--down-seconds expects whole seconds, got '${2:-}'"
        down="$2"; shift 2 ;;
      *) die "unknown option $1" ;;
    esac
  done
  valid_name "$name" || die "invalid name"
  local dir; dir="$(dir_for "$name")"
  [[ -f "$dir/manifest.json" ]] || die "no throwaway server named $name"
  local state; state="$(manifest_get "$dir" state)"
  case "$state" in
    running|restart_failed) ;;
    *) die "$name is $state; only a running or restart_failed instance can be restarted" ;;
  esac
  local data recorded current
  data="$(manifest_get "$dir" data_dir)"
  [[ "$data" == "$dir/data" && -d "$data" ]] || die "$name has no owned data directory; refusing to restart"
  recorded="$(manifest_get "$dir" instance_id)"
  current="$(instance_id "$data" || true)"
  if [[ -n "$recorded" && "$current" != "$recorded" ]]; then
    die "$name: data directory identity '${current:-none}' does not match the recorded '$recorded'; not restarting"
  fi

  manifest_set "$dir" state "restarting"
  stop_server_process "$dir"
  local pid; pid="$(manifest_get "$dir" pid)"
  if [[ -n "$pid" ]] && server_pid_ours "$dir" "$pid"; then
    manifest_set "$dir" state "restart_failed"
    die "$name: server process $pid did not stop"
  fi
  log "$name server process stopped; data, containers and volumes kept"
  if ((down > 0)); then
    log "$name stays down for $down s"
    "${RESTART_SLEEP:-sleep}" "$down"
  fi
  local port; port="$(manifest_get "$dir" port)"
  if port_listening "$port"; then
    manifest_set "$dir" state "restart_failed"
    die "$name: port $port is held by another process; not starting a second server"
  fi
  launch_server "$dir" restart_failed
  current="$(instance_id "$data" || true)"
  if [[ -n "$recorded" && "$current" != "$recorded" ]]; then
    stop_server_process "$dir"
    manifest_set "$dir" state "restart_failed"
    die "$name: after restart the data directory names identity '${current:-none}', not '$recorded'"
  fi
  local count; count="$(manifest_get "$dir" restarts)"
  manifest_set "$dir" restarts "$(( ${count:-0} + 1 ))"
  manifest_set "$dir" restarted_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  manifest_set "$dir" state "running"
  log "$name restarted on http://127.0.0.1:$port (pid $(manifest_get "$dir" pid), instance ${current:-unknown})"
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

  stop_server_process "$dir"
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
  CREWSHIP_SERVER="http://127.0.0.1:$port" CREWSHIP_CONFIG="$(dir_for "$name")/cli-config.yaml" \
    CREWSHIP_THROWAWAY_MANIFEST="$(dir_for "$name")/manifest.json" "$@"
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
  (($#)) || { sed -n '2,9p' "$0"; exit 2; }
  local sub="$1"; shift
  case "$sub" in
    start) (($#)) || die "start NAME"; cmd_start "$@" ;;
    stop) (($#)) || die "stop NAME"; cmd_stop "$1" ;;
    run) (($#)) || die "run NAME -- CMD"; cmd_run "$@" ;;
    restart) (($#)) || die "restart NAME [--down-seconds N]"; cmd_restart "$@" ;;
    list) cmd_list ;;
    *) die "unknown command $sub" ;;
  esac
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi

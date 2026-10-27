#!/usr/bin/env bash
# throwaway-server.sh — a disposable Crewship server that cleans up after itself.
#
#   scripts/throwaway-server.sh run   NAME [--binary PATH] [--port N] [--env NAME=VALUE]... [--page-projects [DIR]] [--documents] -- CMD [ARGS...]
#   scripts/throwaway-server.sh start NAME [--binary PATH] [--port N] [--env NAME=VALUE]... [--page-projects [DIR]] [--documents]
#   scripts/throwaway-server.sh restart NAME [--crash]
#   scripts/throwaway-server.sh stop  NAME
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
# tears it down later. `restart` retains the same data, port and identity;
# `--crash` uses SIGKILL rather than graceful shutdown. Linux pidfds and /proc
# are required for safe signalling. Optional settings stay in a private file;
# the manifest records lifecycle events and restart_count, never their values.
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

# State is private, but retained files may be accidentally replaced or edited.
# Reject symlinks before any mutation, including at existing ancestor paths.
safe_paths() { # instance dir (may not yet exist)
  python3 - "$STATE_ROOT" "$1" <<'PYSAFE'
import os, pathlib, stat, sys
root, directory = map(pathlib.Path, sys.argv[1:])
if directory.parent != root:
    raise SystemExit('throwaway-server: instance escapes state root')
for path in (root, directory, directory / 'data', directory / 'documents'):
    if not path.is_absolute() or os.path.normpath(path) != str(path):
        raise SystemExit('throwaway-server: state paths must be clean and absolute')
    for component in (*reversed(path.parents), path):
        try:
            st = component.lstat()
        except FileNotFoundError:
            continue
        if not stat.S_ISDIR(st.st_mode):
            raise SystemExit('throwaway-server: state directories must not be symlinks or files')
    if path.exists():
        st = path.stat()
        if st.st_uid != os.getuid() or st.st_mode & 0o022:
            raise SystemExit('throwaway-server: state directory is not privately owned')
for name in ('manifest.json', 'launch.env', 'ownership.json', 'server.log', 'server.pid', 'cli-config.yaml', 'resources.txt', 'documents/service-token', 'data/crewship.db', 'data/crewship.db-wal', 'data/crewship.db-shm'):
    path = directory / name
    try:
        st = path.lstat()
    except FileNotFoundError:
        continue
    if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or st.st_nlink != 1 or st.st_mode & 0o022:
        raise SystemExit('throwaway-server: retained file is not an owned regular file')
    if name in ('launch.env', 'ownership.json', 'documents/service-token') and st.st_mode & 0o077:
        raise SystemExit('throwaway-server: retained settings must be private')
for name in ('authority.sock', 'service.sock'):
    path = directory / 'documents' / name
    try:
        st = path.lstat()
    except FileNotFoundError:
        continue
    if not stat.S_ISSOCK(st.st_mode) or st.st_uid != os.getuid():
        raise SystemExit('throwaway-server: document socket is not owned')
PYSAFE
}

manifest_get() { # dir key
  safe_paths "$1" || return 1
  python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get(sys.argv[2],""))' "$1/manifest.json" "$2"
}

manifest_set() { # dir key value
  safe_paths "$1" || return 1
  python3 - "$1/manifest.json" "$2" "$3" <<'PY'
import json, sys
path, key, value = sys.argv[1:4]
with open(path) as f:
    data = json.load(f)
data[key] = value
import os, tempfile
fd, temporary = tempfile.mkstemp(prefix='.manifest-', dir=os.path.dirname(path))
with os.fdopen(fd, 'w') as f:
    json.dump(data, f, indent=2)
os.replace(temporary, path)
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

instance_id() { # data dir; never follow an identity-file symlink
  safe_paths "${1%/data}" || return 1
  python3 - "$1/installations" <<'PYIID'
import os, pathlib, stat, sys
root = pathlib.Path(sys.argv[1])
try:
    st = root.lstat()
except FileNotFoundError:
    sys.exit(1)
if not stat.S_ISDIR(st.st_mode) or st.st_uid != os.getuid() or st.st_mode & 0o022:
    raise SystemExit('throwaway-server: untrusted installation directory')
for path in sorted(root.iterdir()):
    if path.name.endswith(('.db', '.lock')):
        continue
    st = path.lstat()
    if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or st.st_nlink != 1 or st.st_mode & 0o022:
        raise SystemExit('throwaway-server: untrusted installation identity file')
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd) as f:
        value = ''.join(f.read().split())
    if value:
        print(value)
        sys.exit(0)
sys.exit(1)
PYIID
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
  CREWSHIP_MOORING_ENABLED
)

# Serialize lifecycle operations without keeping the lock in the server process.
lock_instance() {
  valid_name "$1" || die "invalid name"
  safe_paths "$(dir_for "$1")" || die "untrusted state path"
  mkdir -p "$STATE_ROOT"
  # A directory lock needs no writable/truncating open. Keep its inode after
  # teardown so a waiting operation cannot accidentally lock another inode.
  local lock="$STATE_ROOT/.$1.lock"
  python3 - "$lock" <<'PYLOCK' || die "untrusted lifecycle lock"
import os, stat, sys
path = sys.argv[1]
try:
    os.mkdir(path, 0o700)
except FileExistsError:
    pass
st = os.lstat(path)
if not stat.S_ISDIR(st.st_mode) or st.st_uid != os.getuid() or st.st_mode & 0o077:
    raise SystemExit('throwaway-server: lock must be a private owned directory')
PYLOCK
  exec 9<"$lock"
  python3 - "$lock" <<'PYLOCK' || die "lifecycle lock changed"
import os, stat, sys
st, opened = os.lstat(sys.argv[1]), os.fstat(9)
if not stat.S_ISDIR(st.st_mode) or (st.st_dev, st.st_ino) != (opened.st_dev, opened.st_ino):
    raise SystemExit('throwaway-server: lifecycle lock changed')
PYLOCK
  flock -x 9
  safe_paths "$(dir_for "$1")" || die "state path changed"
}

# The manifest is observable/editable runtime status. Keep the original owned
# resource selectors separately, so a stale or edited status file cannot select
# another installation's resources, even after the process has already exited.
ownership() { # dir record|verify
  safe_paths "$1" || return 1
  local actual_iid; actual_iid="$(instance_id "$1/data" || true)"
  python3 - "$1" "$2" "$actual_iid" <<'PYOWN'
import json, os, pathlib, re, sys, tempfile
root, action, actual_iid = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3]
d = json.load(open(root / 'manifest.json'))
if d.get('name') != root.name or d.get('data_dir') != str(root / 'data') or not re.fullmatch(r'crewship-tw-' + re.escape(root.name) + r'-[0-9a-f]{4}', d.get('prefix', '')):
    raise SystemExit('throwaway-server: invalid owned resource selectors')
keys = ('name', 'binary', 'port', 'prefix', 'data_dir', 'instance_id', 'page_projects_dir',
        'documents_authority_socket', 'documents_service_socket', 'documents_service_token_file')
current = {k: d.get(k, '') for k in keys}
path = root / 'ownership.json'
if action == 'verify':
    if not path.exists() and (root / 'launch.env').exists():
        raise SystemExit('throwaway-server: retained ownership record is missing')
    if path.exists():
        recorded = json.load(open(path))
        established = recorded.get('instance_id', '') or current['instance_id']
        if established and actual_iid != established:
            raise SystemExit('throwaway-server: established installation identity changed or missing')
        if recorded != current:
            # Publishing a newly discovered ID touches two atomic JSON files.
            # Either rename may be interrupted. Recover only the empty -> known
            # transition, backed by the owned installation file; every other
            # selector and every established nonempty ID must still match.
            previous = recorded.get('instance_id', '')
            present = current['instance_id']
            same_selectors = all(recorded.get(k, '') == current[k] for k in keys if k != 'instance_id')
            initial_binding = bool(previous) != bool(present) and actual_iid == (previous or present)
            if not same_selectors or not initial_binding:
                raise SystemExit('throwaway-server: manifest ownership changed')
else:
    fd, temporary = tempfile.mkstemp(prefix='.ownership-', dir=root)
    with os.fdopen(fd, 'w') as f:
        json.dump(current, f)
    os.replace(temporary, path)
PYOWN
}

retained_env() { # dir: populate the caller's extra_env only after validation
  local dir="$1" setting key value expected
  safe_paths "$dir" || return 1
  mapfile -d '' -t extra_env <"$dir/launch.env"
  if [[ "${extra_env[0]:-}" == "" ]]; then extra_env=(); fi
  local -A seen=()
  for setting in "${extra_env[@]}"; do
    [[ "$setting" =~ ^[A-Za-z_][A-Za-z0-9_]*= ]] || die "invalid retained feature setting"
    key="${setting%%=*}"; value="${setting#*=}"
    [[ -z "${seen[$key]:-}" ]] || die "duplicate retained feature setting"
    seen[$key]=1
    local allowed ok=0
    for allowed in "${THROWAWAY_ENV_ALLOWLIST[@]}"; do
      [[ "$key" == "$allowed" ]] && ok=1
    done
    if ((ok)); then continue; fi
    case "$key" in
      CREWSHIP_PAGE_PROJECTS_PATH)
        expected="$(manifest_get "$dir" page_projects_dir)"
        [[ "$expected" == "$dir/"* && "${expected#"$dir/"}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,40}$ ]] || die "invalid owned project storage"
        case "${expected#"$dir/"}" in data|documents|launch.env|ownership.json|manifest.json|server.log|server.pid|cli-config.yaml) die "reserved owned project storage" ;; esac
        [[ -d "$expected" && ! -L "$expected" ]] || die "untrusted project storage" ;;
      CREWSHIP_DOCUMENTS_ENABLED)
        [[ "$(manifest_get "$dir" documents_service_socket)" == "$dir/documents/service.sock" ]] || die "documents were not enabled for this instance"
        expected=true ;;
      CREWSHIP_DOCUMENTS_AUTHORITY_SOCKET)
        expected="$dir/documents/authority.sock"
        [[ "$(manifest_get "$dir" documents_authority_socket)" == "$expected" ]] || die "documents authority was not configured" ;;
      CREWSHIP_DOCUMENTS_SERVICE_SOCKET)
        expected="$dir/documents/service.sock"
        [[ "$(manifest_get "$dir" documents_service_socket)" == "$expected" ]] || die "documents service was not configured" ;;
      CREWSHIP_DOCUMENTS_SERVICE_TOKEN_FILE)
        expected="$dir/documents/service-token"
        [[ "$(manifest_get "$dir" documents_service_token_file)" == "$expected" ]] || die "documents token was not configured" ;;
      *) die "retained environment contains a forbidden setting" ;;
    esac
    [[ "$value" == "$expected" ]] || die "retained setting escapes owned storage"
  done
}

lifecycle() { # dir event; atomic alongside the manifest's existing fields
  safe_paths "$1" || return 1
  python3 - "$1/manifest.json" "$2" <<'PYLIFE'
import datetime, json, os, sys
path, event = sys.argv[1:]
d = json.load(open(path))
d.setdefault('lifecycle', []).append(dict(event=event, at=datetime.datetime.now(datetime.timezone.utc).isoformat(), pid=d.get('pid', '')))
d.setdefault('restart_count', 0)
if event == 'restarted':
    d['restart_count'] = int(d.get('restart_count', 0)) + 1
import tempfile
fd, temporary = tempfile.mkstemp(prefix='.manifest-', dir=os.path.dirname(path))
with os.fdopen(fd, 'w') as f:
    json.dump(d, f, indent=2)
os.replace(temporary, path)
PYLIFE
}

# A pidfd pins the task across validation and signal delivery. Never fall back
# to kill(PID): the task may exit and the PID may be reused between syscalls.
server_process() { # dir record|check|verify|stop|crash
  safe_paths "$1" || return 1
  python3 - "$1/manifest.json" "$2" <<'PYPROC'
import json, os, pathlib, select, signal, sys
path, action = sys.argv[1:]
d = json.load(open(path))
if not d.get('pid'):
    if action in ('stop', 'crash'):
        sys.exit(0)
    raise SystemExit('throwaway-server: missing process identity')
pid = int(d['pid'])
try:
    fd = os.pidfd_open(pid)
except ProcessLookupError:
    if action in ('stop', 'crash', 'verify'):
        sys.exit(0)
    raise SystemExit('throwaway-server: server exited')
except (AttributeError, OSError):
    raise SystemExit('throwaway-server: pidfd unavailable; refusing to signal')
try:
    proc = pathlib.Path('/proc') / str(pid)
    stat = (proc / 'stat').read_text().rsplit(')', 1)[1].split()
    if stat[0] == 'Z':
        if action in ('stop', 'crash', 'verify'):
            sys.exit(0)
        raise SystemExit('throwaway-server: server exited')
    identity = dict(start_ticks=stat[19], boot_id=pathlib.Path('/proc/sys/kernel/random/boot_id').read_text().strip())
    if action == 'record':
        d['process_identity'] = identity
        import tempfile
        handle, temporary = tempfile.mkstemp(prefix='.manifest-', dir=os.path.dirname(path))
        with os.fdopen(handle, 'w') as f:
            json.dump(d, f, indent=2)
        os.replace(temporary, path)
        sys.exit(0)
    # Legacy manifests can be stopped by adopting the pinned task only after
    # verifying its current owned environment below. Corrupt identities never
    # fall back to this path, and legacy tasks cannot be restarted.
    legacy = 'process_identity' not in d and action == 'stop'
    if (not legacy and d.get('process_identity') != identity) or proc.stat().st_uid != os.getuid():
        raise SystemExit('throwaway-server: process identity mismatch; refusing to signal')
    env = dict(item.split(b'=', 1) for item in (proc / 'environ').read_bytes().split(b'\0') if b'=' in item)
    expected = {'CREWSHIP_DATA_DIR': d['data_dir'], 'CREWSHIP_CONTAINER_PREFIX': d['prefix'],
                'CREWSHIP_HOST': '127.0.0.1', 'CREWSHIP_PORT': str(d['port'])}
    if any(env.get(k.encode()) != v.encode() for k, v in expected.items()):
        raise SystemExit('throwaway-server: process ownership mismatch; refusing to signal')
    if action not in ('check', 'verify'):
        signal.pidfd_send_signal(fd, signal.SIGKILL if action == 'crash' else signal.SIGTERM)
        poll = select.poll()
        poll.register(fd, select.POLLIN)
        if not poll.poll(30000):
            signal.pidfd_send_signal(fd, signal.SIGKILL)
            if not poll.poll(5000):
                raise SystemExit('throwaway-server: process did not exit; state retained')
except FileNotFoundError:
    if action not in ('stop', 'crash', 'verify'):
        raise SystemExit('throwaway-server: server exited')
finally:
    os.close(fd)
PYPROC
}

cmd_start() (
  lock_instance "$1"
  local name="$1"; shift
  local binary="" port="" page_projects="" documents=0
  local -a extra_env=()
  while (($#)); do
    case "$1" in
      --binary) binary="$2"; shift 2 ;;
      --port) port="$2"; shift 2 ;;
      --documents) documents=1; shift ;;
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
          data|server.log|server.pid|manifest.json|cli-config.yaml|launch.env|ownership.json)
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

  mkdir -p "$dir/data"
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
  "pid": "",
  "instance_id": ""
}
EOF
  # Retained launch options may contain secrets. Keep them outside the public
  # manifest and never evaluate them as shell code.
  (umask 077; printf '%s\0' "${extra_env[@]}" >"$dir/launch.env")
  ownership "$dir" record || die "could not record ownership"
  launch_server "$dir"
)

launch_server() {
  local dir="$1" binary port prefix name
  binary="$(manifest_get "$dir" binary)"
  port="$(manifest_get "$dir" port)"
  prefix="$(manifest_get "$dir" prefix)"
  name="$(manifest_get "$dir" name)"
  local -a extra_env=()
  ownership "$dir" verify || die "untrusted retained ownership"
  retained_env "$dir" || die "untrusted retained environment"
  if ss -ltn "sport = :$port" | grep -q LISTEN; then
    die "retained port is already in use; state kept"
  fi
  manifest_set "$dir" state starting
  (
    exec 9>&-
    cd "$dir"
    env -i HOME="$HOME" PATH="$PATH" \
      ${extra_env[@]+"${extra_env[@]}"} \
      CREWSHIP_DATA_DIR="$dir/data" CREWSHIP_HOST=127.0.0.1 CREWSHIP_PORT="$port" \
      CREWSHIP_CONTAINER_PREFIX="$prefix" \
      setsid "$binary" start >>"$dir/server.log" 2>&1 &
    echo $! >"$dir/server.pid"
  )
  local pid; pid="$(cat "$dir/server.pid")"
  manifest_set "$dir" pid "$pid"
  server_process "$dir" record || die "could not record server identity; state kept"
  lifecycle "$dir" starting

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
    server_process "$dir" stop || die "could not safely stop server; state kept"
    manifest_set "$dir" state "failed_to_start"
    die "server did not answer on :$port within $START_TIMEOUT s; see $dir/server.log — 'stop $name' cleans up"
  fi
  server_process "$dir" check || die "could not verify server ownership; state kept"
  local iid previous_iid
  iid="$(instance_id "$dir/data" || true)"
  previous_iid="$(manifest_get "$dir" instance_id)"
  if [[ -n "$previous_iid" && "$iid" != "$previous_iid" ]]; then
    server_process "$dir" stop || true
    die "installation identity changed; state kept"
  fi
  manifest_set "$dir" instance_id "$iid"
  ownership "$dir" record || die "could not record installation identity"
  manifest_set "$dir" state "running"
  lifecycle "$dir" running
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

cmd_stop() (
  lock_instance "$1"
  local name="$1"
  valid_name "$name" || die "invalid name"
  local dir; dir="$(dir_for "$name")"
  [[ -f "$dir/manifest.json" ]] || die "no throwaway server named $name"
  ownership "$dir" verify || die "untrusted teardown ownership; data kept"
  manifest_set "$dir" state "stopping"

  server_process "$dir" stop || die "could not prove server ownership; data and manifest kept"
  lifecycle "$dir" stopped
  if [[ -z "$(manifest_get "$dir" instance_id)" ]]; then
    manifest_set "$dir" instance_id "$(instance_id "$(manifest_get "$dir" data_dir)" || true)"
  fi
  ownership "$dir" record || die "could not retain discovered installation identity"

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
)

# Restart never removes containers, volumes or data. Only stop performs teardown.
cmd_restart() (
  lock_instance "$1"
  local name="$1" mode=stop
  shift
  if [[ "${1:-}" == --crash ]]; then mode=crash; shift; fi
  (($# == 0)) || die "restart NAME [--crash]"
  local dir; dir="$(dir_for "$name")"
  [[ -f "$dir/manifest.json" && -f "$dir/launch.env" ]] || die "no restartable server named $name"
  [[ "$(manifest_get "$dir" data_dir)" == "$dir/data" ]] || die "invalid owned data directory"
  ownership "$dir" verify || die "untrusted restart ownership"
  local -a extra_env=()
  retained_env "$dir" || die "untrusted retained environment"
  server_process "$dir" verify || die "could not prove server identity; restart refused"
  manifest_set "$dir" state restarting
  if [[ "$mode" == crash ]]; then lifecycle "$dir" crashing; else lifecycle "$dir" restarting; fi
  server_process "$dir" "$mode" || die "could not safely stop server; state kept"
  lifecycle "$dir" stopped
  launch_server "$dir"
  lifecycle "$dir" restarted
)

# remove_state deletes the data directory first and the manifest last, so a
# failure part-way leaves the manifest naming what is still there.
remove_state() {
  safe_paths "$1" || return 1
  ownership "$1" verify || return 1
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
  (($#)) || { sed -n '2,8p' "$0"; exit 2; }
  local sub="$1"; shift
  case "$sub" in
    start) (($#)) || die "start NAME"; cmd_start "$@" ;;
    restart) (($#)) || die "restart NAME [--crash]"; cmd_restart "$@" ;;
    stop) (($#)) || die "stop NAME"; cmd_stop "$1" ;;
    run) (($#)) || die "run NAME -- CMD"; cmd_run "$@" ;;
    list) cmd_list ;;
    *) die "unknown command $sub" ;;
  esac
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi

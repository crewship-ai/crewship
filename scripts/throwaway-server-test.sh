#!/usr/bin/env bash
# throwaway-server-test.sh — teardown ownership rules of throwaway-server.sh,
# against a fake docker on PATH. The real start/stop cycle needs a daemon and
# a built binary and is exercised by hand (see the script header).
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'chmod -R u+w "$tmp" 2>/dev/null; rm -rf "$tmp"' EXIT
fail=0
check() { if [[ "$2" == "$3" ]]; then echo "ok   $1"; else echo "FAIL $1: got [$2] want [$3]"; fail=1; fi; }

# shellcheck source=throwaway-server.sh
source "$here/throwaway-server.sh"

# --- names ---
valid_name ok-name && r=yes || r=no; check "lowercase name accepted" "$r" yes
valid_name "../etc" && r=yes || r=no; check "path-like name refused" "$r" no
valid_name "Upper" && r=yes || r=no; check "uppercase refused" "$r" no

# --- fake docker ---
mkdir -p "$tmp/bin"
cat >"$tmp/bin/docker" <<'FAKE'
#!/usr/bin/env bash
echo "$*" >>"$FAKE_LOG"
[[ -n "${FAKE_DOWN:-}" ]] && { echo "Cannot connect to the Docker daemon" >&2; exit 1; }
case "$1 $2" in
  "ps -aq")
    case "$*" in
      *label=crewship.instance-id=iid-own*) echo c-labelled ;;
      *"name=^/crewship-tw-t-abcd-"*) echo c-named ;;
    esac ;;
  "inspect --format") case "$*" in *c-labelled*) printf 'anon-own\nanon-foreign\n' ;; esac ;;
  "volume inspect")
    case "$*" in
      *anon-own*) echo "$FAKE_DATA/crews/x" ;;
      *anon-foreign*) echo "/elsewhere/crews/x" ;;
    esac ;;
  "run --rm")
    # The helper container: delete everything under the mounted directory.
    for a in "$@"; do [[ "$a" == *:/d ]] && { chmod -R u+w "${a%:/d}"; find "${a%:/d}" -mindepth 1 -delete; }; done; true ;;
  "volume ls") printf 'crewship-tw-t-abcd-home-x\ncrewship-tw-t-abcdef-home-x\nunrelated\n' ;;
esac
FAKE
chmod +x "$tmp/bin/docker"
export PATH="$tmp/bin:$PATH" FAKE_LOG="$tmp/docker.log" FAKE_DATA="$tmp/state/t/data"
# shellcheck disable=SC2034 # read by the sourced script
STATE_ROOT="$tmp/state"
# Bound the healthz wait: a wedged fake server must not hang the suite.
export CREWSHIP_THROWAWAY_START_TIMEOUT=5
mkdir -p "$tmp/state/t/data"
cat >"$tmp/state/t/manifest.json" <<JSON
{"name":"t","prefix":"crewship-tw-t-abcd","instance_id":"iid-own","data_dir":"$tmp/state/t/data","pid":""}
JSON

got="$(owned_resources "$tmp/state/t" | sort -u | tr '\n' ' ')"
check "owned: labelled+named containers, own anon volume, own prefix volume only" \
  "$got" "container c-labelled container c-named volume anon-own volume crewship-tw-t-abcd-home-x "

# --- a manifest without a prefix never authorises removal ---
mkdir -p "$tmp/state/np/data"
echo '{"name":"np","prefix":"","instance_id":"","data_dir":"x","pid":""}' >"$tmp/state/np/manifest.json"
: >"$FAKE_LOG"
if (cmd_stop np) 2>/dev/null; then r=removed; else r=refused; fi
check "no prefix: teardown refused" "$r" refused
check "no prefix: no docker removal issued" "$(grep -cE '^(rm|volume rm)' "$FAKE_LOG" || true)" 0
check "no prefix: data kept" "$([[ -d "$tmp/state/np/data" ]] && echo kept)" kept

# --- docker unavailable: nothing is assumed gone ---
mkdir -p "$tmp/state/down/data"
echo "{\"name\":\"down\",\"prefix\":\"crewship-tw-down-1234\",\"instance_id\":\"iid\",\"data_dir\":\"$tmp/state/down/data\",\"pid\":\"\"}" >"$tmp/state/down/manifest.json"
if (FAKE_DOWN=1 cmd_stop down) 2>/dev/null; then r=removed; else r=refused; fi
check "docker down: teardown refused" "$r" refused
check "docker down: data kept" "$([[ -d "$tmp/state/down/data" ]] && echo kept)" kept
check "docker down: manifest marked" "$(manifest_get "$tmp/state/down" state)" teardown_failed

# --- data the crew container wrote as another uid ---
mkdir -p "$tmp/state/ro/data/output/crews/x/shared"
echo hi >"$tmp/state/ro/data/output/crews/x/shared/f"
chmod 555 "$tmp/state/ro/data/output/crews/x/shared" # unlink refused, like a foreign uid
echo "{\"name\":\"ro\",\"prefix\":\"crewship-tw-ro-1234\",\"instance_id\":\"\",\"data_dir\":\"$tmp/state/ro/data\",\"pid\":\"\"}" >"$tmp/state/ro/manifest.json"
: >"$FAKE_LOG"
if (cmd_stop ro) 2>"$tmp/ro.err"; then r=removed; else r=refused; cat "$tmp/ro.err" >&2; fi
check "foreign-owned data: teardown completes" "$r" removed
check "foreign-owned data: state directory gone" "$([[ -e "$tmp/state/ro" ]] && echo left || echo gone)" gone
if ((EUID != 0)); then
  check "foreign-owned data: removed through the helper container" "$(grep -c '^run --rm' "$FAKE_LOG" || true)" 1
fi

# --- a data_dir outside the state directory is never deleted ---
mkdir -p "$tmp/state/out/data" "$tmp/outside"
echo "{\"name\":\"out\",\"prefix\":\"crewship-tw-out-1234\",\"instance_id\":\"\",\"data_dir\":\"$tmp/outside\",\"pid\":\"\"}" >"$tmp/state/out/manifest.json"
if (cmd_stop out) 2>/dev/null; then r=removed; else r=refused; fi
check "foreign data_dir: refused" "$r" refused
check "foreign data_dir: untouched" "$([[ -d "$tmp/outside" ]] && echo kept)" kept
check "foreign data_dir: manifest marked" "$(manifest_get "$tmp/state/out" state)" teardown_failed

# --- --env allowlist: identity/data/network keys are refused (#1815 F4) ---
# A generic passthrough would let `--env CREWSHIP_DATA_DIR=~/.crewship` run
# the "throwaway" server on live data while the manifest names this dir.
for bad in CREWSHIP_DATA_DIR=$tmp/live CREWSHIP_HOST=0.0.0.0 CREWSHIP_PORT=9999 \
           CREWSHIP_CONTAINER_PREFIX=live-prefix CREWSHIP_CONFIG=/etc/x \
           CREWSHIP_BOLT_PATH=/x.db CREWSHIP_LOG_PATH=/var/log CREWSHIP_STORAGE_BASE_PATH=/srv \
           DATABASE_URL=file:/live.db HOME=/root PATH=/bin CREWSHIP_PAGE_PROJECTS_PATH=/outside/projects \
           CREWSHIP_DOCUMENTS_ENABLED=true CREWSHIP_DOCUMENTS_AUTHORITY_SOCKET=/outside/a \
           CREWSHIP_DOCUMENTS_SERVICE_SOCKET=/outside/s CREWSHIP_DOCUMENTS_SERVICE_TOKEN_FILE=/outside/token; do
  key="${bad%%=*}"
  if (cmd_start envreject --binary /bin/true --env "$bad") >/dev/null 2>"$tmp/envreject.err"; then r=started; else r=refused; fi
  check "--env refuses $key" "$r" refused
done
check "--env refusal names the allowlist" "$(grep -c 'not an optional feature setting' "$tmp/envreject.err" || true)" 1
if (cmd_start envreject --binary /bin/true --env "$(printf 'CREWSHIP_RATELIMIT_DISABLED=1\nCREWSHIP_DATA_DIR=/live')") >/dev/null 2>&1; then r=started; else r=refused; fi
check "--env refuses a multi-line value (restart replays settings by line)" "$r" refused
check "multi-line refusal creates no instance state" "$([[ -e "$tmp/state/envreject" ]] && echo created || echo absent)" absent
if (cmd_start envreject --binary /bin/true --env) >/dev/null 2>&1; then r=started; else r=refused; fi
check "--env without a value is refused, not a set -u crash" "$r" refused

# --- --env acceptance + --page-projects ownership, against a fake server ---
# The script starts the server under `env -i`, so nothing from this shell
# reaches it except what the script passes — the output paths are baked in.
FAKE_SERVER_ENV="$tmp/server.env" FAKE_SERVER_PID="$tmp/server.pid"
cat >"$tmp/bin/fake-crewship" <<FAKEBIN
#!/usr/bin/env bash
env | sort >"$FAKE_SERVER_ENV"
echo \$\$ >"$FAKE_SERVER_PID"
exec python3 -m http.server "\${CREWSHIP_PORT:-8199}" --bind 127.0.0.1 >/dev/null 2>&1
FAKEBIN
chmod +x "$tmp/bin/fake-crewship"
: >"$FAKE_SERVER_ENV"
FAKE_PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
# --- --documents uses private owned paths, never caller-supplied credentials ---
cat >"$tmp/check-documents.py" <<'PYDOC'
import json, os, re, stat, sys
from pathlib import Path
manifest = Path(os.environ['CREWSHIP_THROWAWAY_MANIFEST'])
data = json.loads(manifest.read_text())
root = manifest.parent / 'documents'
assert stat.S_IMODE(root.stat().st_mode) == 0o700
for key, name in [('documents_authority_socket', 'authority.sock'),
                  ('documents_service_socket', 'service.sock'),
                  ('documents_service_token_file', 'service-token')]:
    path = Path(data[key])
    assert path == root / name and path.is_absolute() and len(os.fsencode(path)) <= 100
secret_file = Path(data['documents_service_token_file'])
assert stat.S_ISREG(secret_file.lstat().st_mode)
assert stat.S_IMODE(secret_file.stat().st_mode) == 0o600
secret = secret_file.read_text()
assert re.fullmatch('[0-9a-f]{64}', secret)
for name in ['manifest.json', 'server.log']:
    assert secret not in (manifest.parent / name).read_text()
for path in sys.argv[1:]:
    assert secret not in Path(path).read_text()
print('private documents paths verified')
PYDOC
# shellcheck disable=SC2094 # inspect current diagnostics for secret leakage, not as command input
if (cmd_run docsok --binary "$tmp/bin/fake-crewship" --port "$FAKE_PORT" --documents -- \
    python3 "$tmp/check-documents.py" "$FAKE_SERVER_ENV" "$tmp/docs.out" "$tmp/docs.err") >"$tmp/docs.out" 2>"$tmp/docs.err"; then r=passed; else r=failed; fi
check "--documents run exposes only owned paths through manifest" "$r" passed
check "--documents internally enables Go authority" \
  "$(grep -c '^CREWSHIP_DOCUMENTS_ENABLED=true$' "$FAKE_SERVER_ENV" || true)" 1
for setting in AUTHORITY_SOCKET=authority.sock SERVICE_SOCKET=service.sock SERVICE_TOKEN_FILE=service-token; do
  check "--documents owns ${setting%%=*}" \
    "$(grep -c "^CREWSHIP_DOCUMENTS_${setting%%=*}=$tmp/state/docsok/documents/${setting#*=}$" "$FAKE_SERVER_ENV" || true)" 1
done
check "--documents teardown removes private state" "$([[ -e "$tmp/state/docsok" ]] && echo left || echo gone)" gone

# Native Grid profile settings must survive the clean environment without
# allowing callers to replace this server's identity or document credentials.
grid_profile=(
  CREWSHIP_CRAFT_NATIVE_GRID_ENGINE_REVISION=1111111111111111111111111111111111111111
  CREWSHIP_CRAFT_NATIVE_GRID_CLI_PATH=/qualified/gridcraft-cli
  CREWSHIP_CRAFT_NATIVE_GRID_CLI_SHA256=2222222222222222222222222222222222222222222222222222222222222222
  CREWSHIP_CRAFT_NATIVE_GRID_EDITOR_BUNDLE=3333333333333333333333333333333333333333333333333333333333333333
  CREWSHIP_CRAFT_NATIVE_GRID_EDITOR_URL=https://editor.invalid/gridcraft/index.html
)
grid_args=()
for setting in "${grid_profile[@]}"; do grid_args+=(--env "$setting"); done
if (cmd_start gridok --binary "$tmp/bin/fake-crewship" --port "$FAKE_PORT" "${grid_args[@]}") >"$tmp/grid-profile.log" 2>&1; then
  r=accepted
else
  r=refused
fi
check "native Grid profile is accepted" "$r" accepted
if [[ "$r" == accepted ]]; then
  for setting in "${grid_profile[@]}"; do
    check "native Grid ${setting%%=*} reaches the server process" "$(grep -cFx "$setting" "$FAKE_SERVER_ENV" || true)" 1
  done
  check "native Grid keeps its owned data directory" "$(grep -cFx "CREWSHIP_DATA_DIR=$tmp/state/gridok/data" "$FAKE_SERVER_ENV" || true)" 1
  cmd_stop gridok >/dev/null 2>&1
  check "native Grid profile server has no orphan" "$([[ -e "$tmp/state/gridok" ]] && echo left || echo gone)" gone
else
  cat "$tmp/grid-profile.log" >&2
fi

cmd_start envok --binary "$tmp/bin/fake-crewship" --port "$FAKE_PORT" \
  --env CREWSHIP_PAGE_BUILD_IMAGE=sha256:0000000000000000000000000000000000000000000000000000000000000000 \
  --env CREWSHIP_RATELIMIT_DISABLED=1 --env CREWSHIP_ALLOW_SIGNUP=true \
  --env "CREWSHIP_NEXTJS_URL=http://localhost:$FAKE_PORT" --page-projects >/dev/null 2>&1
check "fake server answered healthz (cmd_start returned)" "$(manifest_get "$tmp/state/envok" state)" running
check "allowlisted --env reaches the server process" \
  "$(grep -c '^CREWSHIP_PAGE_BUILD_IMAGE=sha256:0000' "$FAKE_SERVER_ENV")" 1
check "signup + public-URL feature settings reach the server process" \
  "$(grep -cE "^(CREWSHIP_ALLOW_SIGNUP=true|CREWSHIP_NEXTJS_URL=http://localhost:$FAKE_PORT)\$" "$FAKE_SERVER_ENV")" 2
check "fixed identity keys still reach the server process" \
  "$(grep -c "^CREWSHIP_DATA_DIR=$tmp/state/envok/data" "$FAKE_SERVER_ENV")" 1
check "--page-projects config lands under the instance dir" \
  "$(grep -c "^CREWSHIP_PAGE_PROJECTS_PATH=$tmp/state/envok/page-projects$" "$FAKE_SERVER_ENV")" 1
check "--page-projects dir exists outside data/" "$([[ -d "$tmp/state/envok/page-projects" && ! -e "$tmp/state/envok/data/page-projects" ]] && echo yes)" yes
check "without --documents, Go document settings are absent" \
  "$(grep -c '^CREWSHIP_DOCUMENTS_' "$FAKE_SERVER_ENV" || true)" 0
check "without --documents, no private document state is created" \
  "$([[ -e "$tmp/state/envok/documents" ]] && echo created || echo absent)" absent
check "without --documents, manifest socket path is empty" \
  "$(manifest_get "$tmp/state/envok" documents_service_socket)" ""
check "manifest records the owned projects dir" \
  "$(manifest_get "$tmp/state/envok" page_projects_dir)" "$tmp/state/envok/page-projects"

# teardown removes the projects dir with everything else it owns
cmd_stop envok >/dev/null 2>&1 || true
check "teardown removes the page-projects dir" "$([[ -e "$tmp/state/envok" ]] && echo left || echo gone)" gone
# The recorded pid IS the server (exec), so stop must have terminated it: the
# port must no longer answer. A leftover here is what leaked an orphan in #1815.
if (exec 3<>/dev/tcp/127.0.0.1/"$FAKE_PORT") 2>/dev/null; then r=still-listening; else r=closed; fi
check "teardown terminated the fake server (no orphan)" "$r" closed
if [[ -f "$FAKE_SERVER_PID" ]]; then kill "$(cat "$FAKE_SERVER_PID")" 2>/dev/null || true; fi

# --- --page-projects rejects escape attempts ---
for bad_pp in ../escape /abs/path "a/b" data server.log manifest.json cli-config.yaml server.pid; do
  if (cmd_start ppreject --binary /bin/true --page-projects "$bad_pp") >/dev/null 2>&1; then r=started; else r=refused; fi
  check "--page-projects refuses '$bad_pp'" "$r" refused
done

# Documents accept no caller-selected path, and cannot share project storage.
for bad_pp in documents documents/. documents/child ./documents; do
  if (cmd_start docreject --binary /bin/true --documents --page-projects "$bad_pp") >/dev/null 2>&1; then r=started; else r=refused; fi
  check "--documents refuses project collision '$bad_pp'" "$r" refused
done
if (cmd_start docreject --binary /bin/true --documents /outside) >/dev/null 2>&1; then r=started; else r=refused; fi
check "--documents refuses an external path argument" "$r" refused
long_root="$tmp/$(printf '%080d' 0)"
if (STATE_ROOT="$long_root" cmd_start docreject --binary /bin/true --documents --port 8199) >/dev/null 2>&1; then r=started; else r=refused; fi
check "--documents refuses paths over the byte limit before launch" "$r" refused
check "overlong paths create no instance state" "$([[ -e "$long_root/docreject" ]] && echo created || echo absent)" absent
if (STATE_ROOT="$tmp/state/../state" cmd_start docreject --binary /bin/true --documents --port 8199) >/dev/null 2>&1; then r=started; else r=refused; fi
check "--documents refuses unclean paths" "$r" refused
check "unclean paths create no instance state" "$([[ -e "$tmp/state/docreject" ]] && echo created || echo absent)" absent

# Native-agent launch needs one exact operator opt-in; no generic prefix.
if (cmd_start agentenv --binary "$tmp/bin/fake-crewship" --port "$FAKE_PORT" --env CREWSHIP_CRAFT_NATIVE_AGENTS_ENABLED=true) >/dev/null 2>"$tmp/agent-env.err"; then agent_started=yes; else agent_started=no; fi
check "native agent exact opt-in is accepted" "$agent_started" yes
if [[ "$agent_started" == yes ]]; then
  check "native agent exact opt-in reaches isolated child" "$(grep -Fxc 'CREWSHIP_CRAFT_NATIVE_AGENTS_ENABLED=true' "$FAKE_SERVER_ENV" || true)" 1
  cmd_stop agentenv >/dev/null 2>&1 || true
fi
for bad_agent in CREWSHIP_CRAFT_NATIVE_AGENTS_ACTOR_ID=forged CREWSHIP_CRAFT_NATIVE_AGENTS_ENABLED_EXTRA=true; do
  if (cmd_start agentbad --binary /bin/true --env "$bad_agent") >/dev/null 2>&1; then r=started; else r=refused; fi
  check "native agent prefix refuses ${bad_agent%%=*}" "$r" refused
done

# --- native qualification env: exactly seven operator bindings, no identity ---
# Transport fixture only: real native server startup still verifies CLI/bundles.
native_bindings=(
  CREWSHIP_CRAFT_NATIVE_ENABLED=true
  "CREWSHIP_CRAFT_NATIVE_EDITOR_BUNDLE_DIR=$tmp/qualified-native-bundles"
  CREWSHIP_CRAFT_NATIVE_WORD_ENGINE_REVISION=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  "CREWSHIP_CRAFT_NATIVE_WORD_CLI_PATH=$tmp/qualified-wordcraft"
  CREWSHIP_CRAFT_NATIVE_WORD_CLI_SHA256=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
  CREWSHIP_CRAFT_NATIVE_WORD_EDITOR_BUNDLE=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
  CREWSHIP_CRAFT_NATIVE_WORD_EDITOR_URL=/_editor/b/cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc/index.html
)
native_args=()
for binding in "${native_bindings[@]}"; do native_args+=(--env "$binding"); done
check "native operator binding fixture contains exactly seven names" "${#native_bindings[@]}" 7
if (cmd_start nativeenv --binary "$tmp/bin/fake-crewship" --port "$FAKE_PORT" "${native_args[@]}") >/dev/null 2>"$tmp/native-env.err"; then native_started=yes; else native_started=no; fi
check "native env allowlist accepts exactly seven qualified operator bindings" "$native_started" yes
if [[ "$native_started" == yes ]]; then
  for binding in "${native_bindings[@]}"; do
    check "native env exact value ${binding%%=*}" "$(grep -Fxc -- "$binding" "$FAKE_SERVER_ENV" || true)" 1
  done
  check "only seven native settings reached the isolated child" "$(grep -c '^CREWSHIP_CRAFT_NATIVE_' "$FAKE_SERVER_ENV" || true)" 7
  check "native feature passthrough kept owned data identity" "$(grep -Fxc -- "CREWSHIP_DATA_DIR=$tmp/state/nativeenv/data" "$FAKE_SERVER_ENV" || true)" 1
  cmd_stop nativeenv >/dev/null 2>&1 || true
  check "native fake server teardown removed only its own state" "$([[ -e "$tmp/state/nativeenv" ]] && echo left || echo gone)" gone
  if (exec 3<>/dev/tcp/127.0.0.1/"$FAKE_PORT") 2>/dev/null; then native_port=still-listening; else native_port=closed; fi
  check "native fake server teardown leaves no orphan" "$native_port" closed
  if [[ -f "$FAKE_SERVER_PID" ]]; then kill "$(cat "$FAKE_SERVER_PID")" 2>/dev/null || true; fi
fi
for bad_native in CREWSHIP_CRAFT_NATIVE_WORD_ACTOR_ID=forged CREWSHIP_CRAFT_NATIVE_WORD_SESSION_TOKEN=forged; do
  if (cmd_start nativebad --binary /bin/true --env "$bad_native") >/dev/null 2>&1; then r=started; else r=refused; fi
  check "native prefix does not admit arbitrary ${bad_native%%=*}" "$r" refused
 done

# --- restart: same data, port, prefix, identity and settings; nothing removed ---
# A recovery scenario restarts the server WITHOUT reseeding (#4824): only the
# server process may change. Containers, volumes and the data directory belong
# to the instance and must survive, and the settings it was started with must
# reach the new process too.
: >"$FAKE_SERVER_ENV"
if (cmd_start rs --binary "$tmp/bin/fake-crewship" --port "$FAKE_PORT" \
    --env CREWSHIP_RATELIMIT_DISABLED=1 --page-projects) >/dev/null 2>"$tmp/rs-start.err"; then r=started; else r=failed; cat "$tmp/rs-start.err" >&2; fi
check "restart fixture started" "$r" started
mkdir -p "$tmp/state/rs/data/installations"
echo iid-rs >"$tmp/state/rs/data/installations/self"
echo kept >"$tmp/state/rs/data/marker"
manifest_set "$tmp/state/rs" instance_id iid-rs
rs_pid_before="$(manifest_get "$tmp/state/rs" pid)"
rs_prefix="$(manifest_get "$tmp/state/rs" prefix)"
env_before="$(grep -v '^_=' "$FAKE_SERVER_ENV" | sort)"
: >"$FAKE_LOG"
: >"$FAKE_SERVER_ENV"
if (cmd_restart rs) >/dev/null 2>"$tmp/rs-restart.err"; then r=restarted; else r=failed; cat "$tmp/rs-restart.err" >&2; fi
check "restart succeeds" "$r" restarted
rs_pid_after="$(manifest_get "$tmp/state/rs" pid)"
check "restart runs a new server process" "$([[ -n "$rs_pid_after" && "$rs_pid_after" != "$rs_pid_before" ]] && echo new)" new
if kill -0 "$rs_pid_before" 2>/dev/null; then r=alive; else r=gone; fi
check "restart ended the previous server process" "$r" gone
check "restart keeps the state running" "$(manifest_get "$tmp/state/rs" state)" running
check "restart keeps the port" "$(manifest_get "$tmp/state/rs" port)" "$FAKE_PORT"
check "restart keeps the container prefix" "$(manifest_get "$tmp/state/rs" prefix)" "$rs_prefix"
check "restart keeps the installation identity" "$(manifest_get "$tmp/state/rs" instance_id)" iid-rs
check "restart keeps the data directory" "$(cat "$tmp/state/rs/data/marker" 2>/dev/null)" kept
check "restart passes the same environment to the new process" "$(grep -v '^_=' "$FAKE_SERVER_ENV" | sort)" "$env_before"
check "restart removes no container or volume" "$(grep -cE '^(rm|volume rm|run)' "$FAKE_LOG" || true)" 0
check "restart records the restart count" "$(manifest_get "$tmp/state/rs" restarts)" 1

# --down-seconds keeps the server stopped for that long before starting it.
# shellcheck disable=SC2317 # invoked indirectly through RESTART_SLEEP
fake_sleep() { echo "$1" >>"$tmp/slept"; }
: >"$tmp/slept"
if (RESTART_SLEEP=fake_sleep cmd_restart rs --down-seconds 75) >/dev/null 2>"$tmp/rs-down.err"; then r=restarted; else r=failed; cat "$tmp/rs-down.err" >&2; fi
check "restart --down-seconds succeeds" "$r" restarted
check "restart --down-seconds waits with the server stopped" "$(cat "$tmp/slept")" 75
check "restart --down-seconds counts a second restart" "$(manifest_get "$tmp/state/rs" restarts)" 2
for bad_down in -1 abc 1.5 ""; do
  if (cmd_restart rs --down-seconds "$bad_down") >/dev/null 2>&1; then r=accepted; else r=refused; fi
  check "restart refuses --down-seconds '$bad_down'" "$r" refused
done
check "refused restart options leave the server running" "$(manifest_get "$tmp/state/rs" state)" running

# A different installation identity after restart means a different data
# directory answered: never report it as the same instance.
rs_pid="$(manifest_get "$tmp/state/rs" pid)"
manifest_set "$tmp/state/rs" instance_id iid-other
if (cmd_restart rs) >/dev/null 2>&1; then r=accepted; else r=refused; fi
check "restart refuses an instance whose identity changed" "$r" refused
check "identity mismatch leaves the server process alone" "$(manifest_get "$tmp/state/rs" pid)" "$rs_pid"
if kill -0 "$rs_pid" 2>/dev/null; then r=alive; else r=gone; fi
check "identity mismatch does not stop the server" "$r" alive
manifest_set "$tmp/state/rs" instance_id iid-rs

# Only a running or restart_failed instance may be restarted.
for bad_state in stopping teardown_failed failed_to_start starting; do
  manifest_set "$tmp/state/rs" state "$bad_state"
  if (cmd_restart rs) >/dev/null 2>&1; then r=accepted; else r=refused; fi
  check "restart refuses state $bad_state" "$r" refused
done
manifest_set "$tmp/state/rs" state running
if (cmd_restart no-such-instance) >/dev/null 2>&1; then r=accepted; else r=refused; fi
check "restart refuses an unknown instance" "$r" refused
if (main restart) >/dev/null 2>&1; then r=accepted; else r=refused; fi
check "restart without a name is refused" "$r" refused

# The port must still be ours: another listener there is a different server.
rs_pid="$(manifest_get "$tmp/state/rs" pid)"
kill -TERM "$rs_pid" 2>/dev/null || true
for _ in $(seq 1 50); do kill -0 "$rs_pid" 2>/dev/null || break; sleep 0.1; done
python3 -m http.server "$FAKE_PORT" --bind 127.0.0.1 >/dev/null 2>&1 &
squatter=$!
for _ in $(seq 1 50); do (exec 3<>/dev/tcp/127.0.0.1/"$FAKE_PORT") 2>/dev/null && break; sleep 0.1; done
if (cmd_restart rs) >/dev/null 2>&1; then r=accepted; else r=refused; fi
check "restart refuses a port another process holds" "$r" refused
check "occupied port marks the manifest" "$(manifest_get "$tmp/state/rs" state)" restart_failed
kill "$squatter" 2>/dev/null || true
wait "$squatter" 2>/dev/null || true
for _ in $(seq 1 50); do (exec 3<>/dev/tcp/127.0.0.1/"$FAKE_PORT") 2>/dev/null || break; sleep 0.1; done
if (cmd_restart rs) >/dev/null 2>"$tmp/rs-retry.err"; then r=restarted; else r=failed; cat "$tmp/rs-retry.err" >&2; fi
check "restart_failed instance can be restarted once the port is free" "$r" restarted

cmd_stop rs >/dev/null 2>&1 || true
check "restarted instance tears down completely" "$([[ -e "$tmp/state/rs" ]] && echo left || echo gone)" gone
if (exec 3<>/dev/tcp/127.0.0.1/"$FAKE_PORT") 2>/dev/null; then r=still-listening; else r=closed; fi
check "restarted instance leaves no orphan server" "$r" closed
if [[ -f "$FAKE_SERVER_PID" ]]; then kill "$(cat "$FAKE_SERVER_PID")" 2>/dev/null || true; fi

# --- pid identity: restart/stop never signal a process that is not the server ---
# Review of #4880: a server that died leaves its pid in the manifest; once the
# number is reused, signalling it would kill an unrelated process.
: >"$FAKE_SERVER_ENV"
if (cmd_start fp --binary "$tmp/bin/fake-crewship" --port "$FAKE_PORT") >/dev/null 2>"$tmp/fp.err"; then r=started; else r=failed; cat "$tmp/fp.err" >&2; fi
check "pid-identity fixture started" "$r" started
check "start records the server's kernel start time" "$([[ "$(manifest_get "$tmp/state/fp" pid_starttime)" =~ ^[0-9]+$ ]] && echo yes)" yes
fp_pid="$(manifest_get "$tmp/state/fp" pid)"
server_pid_ours "$tmp/state/fp" "$fp_pid" && r=ours || r=foreign
check "the running server is recognised as this instance's" "$r" ours
# A recorded start time that differs means the pid now names another process.
real_start="$(manifest_get "$tmp/state/fp" pid_starttime)"
manifest_set "$tmp/state/fp" pid_starttime 1
server_pid_ours "$tmp/state/fp" "$fp_pid" && r=ours || r=foreign
check "a different kernel start time is not this instance's server" "$r" foreign
if (cmd_restart fp) >/dev/null 2>&1; then r=accepted; else r=refused; fi
check "restart with a mismatched start time does not take over the port" "$r" refused
if kill -0 "$fp_pid" 2>/dev/null; then r=alive; else r=killed; fi
check "restart never signals a pid whose start time differs" "$r" alive
manifest_set "$tmp/state/fp" pid_starttime "$real_start"
manifest_set "$tmp/state/fp" state running
kill -TERM "$fp_pid" 2>/dev/null || true
for _ in $(seq 1 50); do kill -0 "$fp_pid" 2>/dev/null || break; sleep 0.1; done
# A foreign process of the same user on the recorded pid: neither restart nor stop may touch it.
sleep 300 &
foreign=$!
manifest_set "$tmp/state/fp" pid "$foreign"
manifest_set "$tmp/state/fp" pid_starttime "$(proc_starttime "$foreign")"
server_pid_ours "$tmp/state/fp" "$foreign" && r=ours || r=foreign
check "a process without this instance's data dir and prefix is foreign" "$r" foreign
if (cmd_restart fp) >/dev/null 2>"$tmp/fp-restart.err"; then r=restarted; else r=failed; cat "$tmp/fp-restart.err" >&2; fi
check "restart over a foreign pid starts the server again" "$r" restarted
if kill -0 "$foreign" 2>/dev/null; then r=alive; else r=killed; fi
check "restart never signals a foreign process on the recorded pid" "$r" alive
fp_new="$(manifest_get "$tmp/state/fp" pid)"
manifest_set "$tmp/state/fp" pid "$foreign"
cmd_stop fp >/dev/null 2>&1 || true
if kill -0 "$foreign" 2>/dev/null; then r=alive; else r=killed; fi
check "stop never signals a foreign process on the recorded pid" "$r" alive
kill "$foreign" "$fp_new" 2>/dev/null || true
wait "$foreign" 2>/dev/null || true
for _ in $(seq 1 50); do (exec 3<>/dev/tcp/127.0.0.1/"$FAKE_PORT") 2>/dev/null || break; sleep 0.1; done

# --- start claims the name atomically; an existing instance is left alone ---
mkdir -p "$tmp/state/taken/data"
echo '{"name":"taken","state":"running","prefix":"crewship-tw-taken-0000","instance_id":"","data_dir":"x","pid":""}' >"$tmp/state/taken/manifest.json"
taken_before="$(cat "$tmp/state/taken/manifest.json")"
if (cmd_start taken --binary "$tmp/bin/fake-crewship" --port "$FAKE_PORT") >/dev/null 2>&1; then r=started; else r=refused; fi
check "start refuses an existing name" "$r" refused
check "refused start leaves the existing manifest unchanged" "$(cat "$tmp/state/taken/manifest.json")" "$taken_before"

# --- the recovery scenario never stops an instance it did not create ---------
# Review of #4880 (HIGH): ownership was claimed before `start`, so a name
# collision made teardown stop someone else's instance.
mkdir -p "$tmp/scen/bin"
printf '#!/bin/sh\nexit 0\n' >"$tmp/scen/bin/crewship"
cp "$tmp/scen/bin/crewship" "$tmp/scen/bin/crewship-sidecar"
chmod 0755 "$tmp/scen/bin/crewship" "$tmp/scen/bin/crewship-sidecar"
cat >"$tmp/bin/docker" <<'FAKE2'
#!/usr/bin/env bash
echo "$*" >>"$FAKE_LOG"
exit 0
FAKE2
chmod +x "$tmp/bin/docker"
: >"$FAKE_LOG"
if (CREWSHIP_THROWAWAY_STATE="$tmp/state" bash "$here/acceptance-chat-recovery-throwaway.sh" \
    --binary "$tmp/scen/bin/crewship" --evidence "$tmp/scen/ev1" --name taken) >/dev/null 2>&1; then r=ran; else r=refused; fi
check "scenario refuses a name that already exists" "$r" refused
check "scenario names the existing instance as the reason" "$(grep -c 'already exists in' "$tmp/scen/ev1/scenario.log" || true)" 1
check "scenario leaves the existing instance's manifest unchanged" "$(cat "$tmp/state/taken/manifest.json")" "$taken_before"
check "scenario leaves the existing instance's data" "$([[ -d "$tmp/state/taken/data" ]] && echo kept)" kept
check "scenario issued no docker removal for the existing instance" "$(grep -cE '^(rm|volume rm|run)' "$FAKE_LOG" || true)" 0
check "scenario teardown says it stopped nothing" "$(grep -c 'no instance owned by this run' "$tmp/scen/ev1/scenario.log" || true)" 1
check "scenario result line reports the real (non-zero) exit status" "$(grep -c 'result: 0 failed check(s), exit status 1' "$tmp/scen/ev1/scenario.log" || true)" 1
# A start that loses the name race (`already exists`) is not this run's either.
cat >"$tmp/scen/tw-collide.sh" <<'TWF'
#!/usr/bin/env bash
echo "$*" >>"$TW_CALLS"
if [[ "$1" == start ]]; then echo "throwaway-server: $2 already exists" >&2; exit 1; fi
exit 0
TWF
chmod +x "$tmp/scen/tw-collide.sh"
: >"$tmp/scen/tw-calls"
if (TW_CALLS="$tmp/scen/tw-calls" CHAT_RECOVERY_THROWAWAY_SCRIPT="$tmp/scen/tw-collide.sh" CREWSHIP_THROWAWAY_STATE="$tmp/state" \
    bash "$here/acceptance-chat-recovery-throwaway.sh" --binary "$tmp/scen/bin/crewship" --evidence "$tmp/scen/ev2" --name racer) >/dev/null 2>&1; then r=ran; else r=refused; fi
check "scenario aborts when start loses the name" "$r" refused
check "scenario reached start in the collision case" "$(grep -c '^start racer' "$tmp/scen/tw-calls" || true)" 1
check "scenario never stops an instance whose start collided" "$(grep -c '^stop' "$tmp/scen/tw-calls" || true)" 0
# Default names are unique per run.
n1="$(grep -o 'instance_name: .*' "$tmp/scen/ev2/run-info.txt" || true)"
(TW_CALLS=/dev/null CHAT_RECOVERY_THROWAWAY_SCRIPT="$tmp/scen/tw-collide.sh" CREWSHIP_THROWAWAY_STATE="$tmp/state" \
    bash "$here/acceptance-chat-recovery-throwaway.sh" --binary "$tmp/scen/bin/crewship" --evidence "$tmp/scen/ev3") >/dev/null 2>&1 || true
(TW_CALLS=/dev/null CHAT_RECOVERY_THROWAWAY_SCRIPT="$tmp/scen/tw-collide.sh" CREWSHIP_THROWAWAY_STATE="$tmp/state" \
    bash "$here/acceptance-chat-recovery-throwaway.sh" --binary "$tmp/scen/bin/crewship" --evidence "$tmp/scen/ev4") >/dev/null 2>&1 || true
d1="$(grep -o 'instance_name: .*' "$tmp/scen/ev3/run-info.txt" || true)"
d2="$(grep -o 'instance_name: .*' "$tmp/scen/ev4/run-info.txt" || true)"
check "explicit --name is recorded" "$n1" "instance_name: racer"
check "default instance names differ between runs" "$([[ -n "$d1" && "$d1" != "$d2" ]] && echo unique)" unique

exit "$fail"

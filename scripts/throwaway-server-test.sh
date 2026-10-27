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
check "foreign data_dir: manifest not mutated" "$(manifest_get "$tmp/state/out" state)" ""

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
if (cmd_start envreject --binary /bin/true --env) >/dev/null 2>&1; then r=started; else r=refused; fi
check "--env without a value is refused, not a set -u crash" "$r" refused

# --- --env acceptance + --page-projects ownership, against a fake server ---
# The script starts the server under `env -i`, so nothing from this shell
# reaches it except what the script passes — the output paths are baked in.
FAKE_SERVER_ENV="$tmp/server.env" FAKE_SERVER_PID="$tmp/server.pid"
cat >"$tmp/bin/fake-crewship" <<FAKEBIN
#!/usr/bin/env bash
echo accessed >"\${CREWSHIP_DATA_DIR}/startup-marker"
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

# Mooring is a feature opt-in; it must not replace the owned identity or paths.
if (cmd_start mooringok --binary "$tmp/bin/fake-crewship" --port "$FAKE_PORT" \
    --env CREWSHIP_MOORING_ENABLED=true) >"$tmp/mooring.out" 2>"$tmp/mooring.err"; then r=started; else r=refused; fi
check "Mooring feature opt-in starts an owned throwaway" "$r" started
if [[ "$r" == started ]]; then
  check "Mooring opt-in reaches the isolated server" \
    "$(grep -c '^CREWSHIP_MOORING_ENABLED=true$' "$FAKE_SERVER_ENV" || true)" 1
  check "Mooring opt-in keeps owned data path" \
    "$(grep -c "^CREWSHIP_DATA_DIR=$tmp/state/mooringok/data$" "$FAKE_SERVER_ENV" || true)" 1
  mooring_prefix="$(manifest_get "$tmp/state/mooringok" prefix)"
  echo committed-mooring-fixture >"$tmp/state/mooringok/data/crewship.db"
  for restart_mode in --crash graceful; do
    mooring_pid="$(manifest_get "$tmp/state/mooringok" pid)"
    restart_options=()
    [[ "$restart_mode" == --crash ]] && restart_options+=(--crash)
    if (main restart mooringok "${restart_options[@]}") >"$tmp/mooring-restart.out" 2>"$tmp/mooring-restart.err"; then r=restarted; else r=failed; fi
    check "Mooring $restart_mode restart succeeds" "$r" restarted
    check "Mooring opt-in reaches $restart_mode replacement process" \
      "$(grep -c '^CREWSHIP_MOORING_ENABLED=true$' "$FAKE_SERVER_ENV" || true)" 1
    check "Mooring $restart_mode restart changes process" \
      "$([[ "$(manifest_get "$tmp/state/mooringok" pid)" != "$mooring_pid" ]] && echo new || echo old)" new
    check "Mooring $restart_mode restart retains owned data path" \
      "$(grep -c "^CREWSHIP_DATA_DIR=$tmp/state/mooringok/data$" "$FAKE_SERVER_ENV" || true)" 1
    check "Mooring $restart_mode restart preserves data" \
      "$(cat "$tmp/state/mooringok/data/crewship.db")" committed-mooring-fixture
    check "Mooring $restart_mode restart preserves prefix" \
      "$(manifest_get "$tmp/state/mooringok" prefix)" "$mooring_prefix"
  done
  check "Mooring restarts are both recorded" "$(manifest_get "$tmp/state/mooringok" restart_count)" 2
  cmd_stop mooringok >/dev/null
fi

cmd_start envok --binary "$tmp/bin/fake-crewship" --port "$FAKE_PORT" \
  --env CREWSHIP_PAGE_BUILD_IMAGE=sha256:0000000000000000000000000000000000000000000000000000000000000000 \
  --env CREWSHIP_RATELIMIT_DISABLED=1 --env CREWSHIP_ALLOW_SIGNUP=true \
  --env CREWSHIP_PAGE_RUNTIME_ORIGIN=private-fixture-marker \
  --env "CREWSHIP_NEXTJS_URL=http://localhost:$FAKE_PORT" --page-projects >/dev/null 2>&1
check "fake server answered healthz (cmd_start returned)" "$(manifest_get "$tmp/state/envok" state)" running
check "allowlisted --env reaches the server process" \
  "$(grep -c '^CREWSHIP_PAGE_BUILD_IMAGE=sha256:0000' "$FAKE_SERVER_ENV")" 1
check "signup + public-URL feature settings reach the server process" \
  "$(grep -cE "^(CREWSHIP_ALLOW_SIGNUP=true|CREWSHIP_NEXTJS_URL=http://localhost:$FAKE_PORT)\$" "$FAKE_SERVER_ENV")" 2
check "Mooring remains absent without explicit opt-in" \
  "$(grep -c '^CREWSHIP_MOORING_ENABLED=' "$FAKE_SERVER_ENV" || true)" 0
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

# --- retained cold restart, including an abrupt crash, keeps the same identity ---
mkdir -p "$tmp/state/envok/data/installations"
echo iid-retained >"$tmp/state/envok/data/installations/default"
manifest_set "$tmp/state/envok" instance_id iid-retained
ownership "$tmp/state/envok" record
echo committed-fixture >"$tmp/state/envok/data/crewship.db"
old_pid="$(manifest_get "$tmp/state/envok" pid)"
old_prefix="$(manifest_get "$tmp/state/envok" prefix)"
: >"$FAKE_LOG"
if (main restart envok --crash) >"$tmp/restart.out" 2>"$tmp/restart.err"; then r=restarted; else r=failed; fi
check "abrupt restart succeeds" "$r" restarted
check "restart preserves committed data" "$(cat "$tmp/state/envok/data/crewship.db")" committed-fixture
check "restart preserves installation identity" "$(manifest_get "$tmp/state/envok" instance_id)" iid-retained
check "restart preserves prefix" "$(manifest_get "$tmp/state/envok" prefix)" "$old_prefix"
check "restart preserves port" "$(manifest_get "$tmp/state/envok" port)" "$FAKE_PORT"
check "restart count is recorded" "$(manifest_get "$tmp/state/envok" restart_count)" 1
check "restart launches a new process" "$([[ "$(manifest_get "$tmp/state/envok" pid)" != "$old_pid" ]] && echo new || echo old)" new
check "restart never removes docker resources" "$(grep -cE '^(rm|volume rm)' "$FAKE_LOG" || true)" 0
check "restart retains optional feature environment" "$(grep -c '^CREWSHIP_ALLOW_SIGNUP=true$' "$FAKE_SERVER_ENV")" 1
check "Mooring remains default-off through abrupt restart" "$(grep -c '^CREWSHIP_MOORING_ENABLED=' "$FAKE_SERVER_ENV" || true)" 0

check "retained launch settings are private" "$(stat -c %a "$tmp/state/envok/launch.env" 2>/dev/null || true)" 600
check "optional environment values never enter manifest or diagnostics" \
  "$(grep -l private-fixture-marker "$tmp/state/envok/manifest.json" "$tmp/restart.out" "$tmp/restart.err" | wc -l)" 0
if (main restart envok) >"$tmp/graceful.out" 2>"$tmp/graceful.err"; then r=restarted; else r=failed; fi
check "graceful retained restart succeeds" "$r" restarted
check "graceful restart increments count" "$(manifest_get "$tmp/state/envok" restart_count)" 2
check "Mooring remains default-off through graceful restart" "$(grep -c '^CREWSHIP_MOORING_ENABLED=' "$FAKE_SERVER_ENV" || true)" 0
check "lifecycle records crash and graceful restart" \
  "$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print([x["event"] for x in d.get("lifecycle", [])].count("crashing"), [x["event"] for x in d.get("lifecycle", [])].count("restarting"))' "$tmp/state/envok/manifest.json")" "1 1"
cp "$tmp/state/envok/manifest.json" "$tmp/original-manifest.json"
python3 - "$tmp/state/envok/manifest.json" <<'PYIDENTITY'
import json, sys
p = sys.argv[1]
d = json.load(open(p))
d.setdefault('process_identity', {})['start_ticks'] = '0'
with open(p, 'w') as f:
    json.dump(d, f)
PYIDENTITY
if (main restart envok --crash) >/dev/null 2>&1; then r=restarted; else r=refused; fi
check "reused PID identity refuses crash restart" "$r" refused
if (cmd_stop envok) >/dev/null 2>&1; then r=removed; else r=refused; fi
check "reused PID identity refuses teardown signalling" "$r" refused
check "refused signalling keeps data" "$(cat "$tmp/state/envok/data/crewship.db")" committed-fixture
if (exec 3<>/dev/tcp/127.0.0.1/"$FAKE_PORT") 2>/dev/null; then r=alive; else r=dead; fi
check "identity mismatch leaves original server alive" "$r" alive
cp "$tmp/original-manifest.json" "$tmp/state/envok/manifest.json"
manifest_set "$tmp/state/envok" prefix foreign-prefix
if (main restart envok --crash) >/dev/null 2>&1; then r=restarted; else r=refused; fi
check "owned environment mismatch refuses crash restart" "$r" refused
cp "$tmp/original-manifest.json" "$tmp/state/envok/manifest.json"

# Retained environment is untrusted input: reject overrides before stopping
# the original process or launching against a foreign fixture directory.
cp "$tmp/state/envok/launch.env" "$tmp/original-launch.env"
mkdir "$tmp/foreign-data"
printf 'CREWSHIP_DATA_DIR=%s\0' "$tmp/foreign-data" >"$tmp/state/envok/launch.env"
old_pid="$(manifest_get "$tmp/state/envok" pid)"
if (main restart envok --crash) >/dev/null 2>&1; then r=restarted; else r=refused; fi
check "retained identity override refused before launch" "$r" refused
check "retained identity override leaves original PID" "$(manifest_get "$tmp/state/envok" pid)" "$old_pid"
check "retained identity override never accesses foreign data" "$([[ -e "$tmp/foreign-data/startup-marker" ]] && echo touched || echo untouched)" untouched
cp "$tmp/original-launch.env" "$tmp/state/envok/launch.env"

for bad_key in CREWSHIP_HOST CREWSHIP_PORT CREWSHIP_CONTAINER_PREFIX PATH DATABASE_URL; do
  printf '%s=foreign-fixture\0' "$bad_key" >"$tmp/state/envok/launch.env"
  if (main restart envok --crash) >/dev/null 2>&1; then r=restarted; else r=refused; fi
  check "retained environment refuses $bad_key before stopping" "$r" refused
  check "forbidden retained setting preserves original PID" "$(manifest_get "$tmp/state/envok" pid)" "$old_pid"
done
printf 'CREWSHIP_ALLOW_SIGNUP=true\0CREWSHIP_ALLOW_SIGNUP=false\0' >"$tmp/state/envok/launch.env"
if (main restart envok --crash) >/dev/null 2>&1; then r=restarted; else r=refused; fi
check "duplicate retained feature setting refused" "$r" refused
cp "$tmp/original-launch.env" "$tmp/state/envok/launch.env"
for leaf in server.log server.pid launch.env ownership.json; do
  mv "$tmp/state/envok/$leaf" "$tmp/saved-leaf"
  ln -s "$tmp/foreign-lock-target-unused" "$tmp/state/envok/$leaf"
  if (main restart envok --crash) >/dev/null 2>&1; then r=restarted; else r=refused; fi
  check "symlink retained $leaf refuses restart" "$r" refused
  rm "$tmp/state/envok/$leaf"
  mv "$tmp/saved-leaf" "$tmp/state/envok/$leaf"
done
mv "$tmp/state/envok/data/crewship.db" "$tmp/saved-db"
ln -s "$tmp/foreign-data/foreign.db" "$tmp/state/envok/data/crewship.db"
if (main restart envok --crash) >/dev/null 2>&1; then r=restarted; else r=refused; fi
check "symlink database refuses restart" "$r" refused
rm "$tmp/state/envok/data/crewship.db"
mv "$tmp/saved-db" "$tmp/state/envok/data/crewship.db"

# Dead-process cleanup must not select resources from changed manifest IDs.
manifest_set "$tmp/state/envok" prefix foreign-prefix
: >"$FAKE_LOG"
if (cmd_stop envok) >/dev/null 2>&1; then r=removed; else r=refused; fi
check "changed ownership refuses cleanup" "$r" refused
check "changed ownership never selects docker resources" "$(wc -l <"$FAKE_LOG")" 0
cp "$tmp/original-manifest.json" "$tmp/state/envok/manifest.json"

# Locking and atomic updates never follow caller-controlled symlinks.
echo valuable-fixture >"$tmp/foreign-lock-target"
ln -s "$tmp/foreign-lock-target" "$STATE_ROOT/.lockprobe.lock"
if (main restart lockprobe) >/dev/null 2>&1; then r=started; else r=refused; fi
check "symlink lock target unchanged" "$(cat "$tmp/foreign-lock-target")" valuable-fixture
echo valuable-fixture >"$tmp/foreign-temp-target"
ln -s "$tmp/foreign-temp-target" "$tmp/state/envok/manifest.json.tmp"
lifecycle "$tmp/state/envok" probe
check "manifest staging never follows symlink" "$(cat "$tmp/foreign-temp-target")" valuable-fixture
mkdir -p "$tmp/foreign-instance/data"
echo valuable-fixture >"$tmp/foreign-instance/data/valuable"
ln -s "$tmp/foreign-instance" "$STATE_ROOT/linkprobe"
printf '{"name":"linkprobe","prefix":"crewship-tw-linkprobe-1234","instance_id":"","pid":"","data_dir":"%s/linkprobe/data"}\n' "$STATE_ROOT" >"$tmp/foreign-instance/manifest.json"
if (cmd_stop linkprobe) >/dev/null 2>&1; then r=removed; else r=refused; fi
check "symlink instance refuses teardown" "$r" refused
check "symlink instance foreign data unchanged" "$(cat "$tmp/foreign-instance/data/valuable" 2>/dev/null || true)" valuable-fixture
mv "$tmp/state/envok/data" "$tmp/state/envok/retained-data"
ln -s "$tmp/foreign-instance/data" "$tmp/state/envok/data"
if (main restart envok --crash) >/dev/null 2>&1; then r=started; else r=refused; fi
check "symlink data refuses restart" "$r" refused
rm "$tmp/state/envok/data"
mv "$tmp/state/envok/retained-data" "$tmp/state/envok/data"

# teardown removes the projects dir with everything else it owns
cmd_stop envok >/dev/null 2>&1 || true
check "teardown removes the page-projects dir" "$([[ -e "$tmp/state/envok" ]] && echo left || echo gone)" gone
# The recorded pid IS the server (exec), so stop must have terminated it: the
# port must no longer answer. A leftover here is what leaked an orphan in #1815.
if (exec 3<>/dev/tcp/127.0.0.1/"$FAKE_PORT") 2>/dev/null; then r=still-listening; else r=closed; fi
check "teardown terminated the fake server (no orphan)" "$r" closed
if [[ -f "$FAKE_SERVER_PID" ]]; then kill "$(cat "$FAKE_SERVER_PID")" 2>/dev/null || true; fi

# Live pre-change manifests can be adopted only after pidfd + owned env checks.
cmd_start legacy --binary "$tmp/bin/fake-crewship" --port "$FAKE_PORT" >/dev/null 2>&1
python3 - "$tmp/state/legacy/manifest.json" <<'PYLEGACY'
import json, sys
p = sys.argv[1]
d = json.load(open(p)); d.pop('process_identity', None)
with open(p, 'w') as f: json.dump(d, f)
PYLEGACY
if (cmd_stop legacy) >"$tmp/legacy.out" 2>"$tmp/legacy.err"; then r=removed; else r=refused; fi
check "legacy live server has safe cleanup path" "$r" removed
check "legacy cleanup removes owned state" "$([[ -e "$tmp/state/legacy" ]] && echo left || echo gone)" gone

# A run callback can restart through the public helper; the outer EXIT trap
# must still remove the new process and its retained state.
if (cmd_run restarttrap --binary "$tmp/bin/fake-crewship" --port "$FAKE_PORT" -- \
    env CREWSHIP_THROWAWAY_STATE="$STATE_ROOT" bash "$here/throwaway-server.sh" restart restarttrap --crash) >"$tmp/trap.out" 2>"$tmp/trap.err"; then r=passed; else r=failed; fi
check "run trap cleans up after callback restart" "$r" passed
check "run trap removes retained state" "$([[ -e "$tmp/state/restarttrap" ]] && echo left || echo gone)" gone
if (exec 3<>/dev/tcp/127.0.0.1/"$FAKE_PORT") 2>/dev/null; then r=listening; else r=closed; fi
check "run trap stops the replacement server" "$r" closed

# A server may create its installation identity before it ever answers HTTP.
# Cleanup must survive both the timeout and an interrupted metadata update.
cat >"$tmp/bin/identity-before-http" <<'FAILEDSERVER'
#!/usr/bin/env bash
mkdir -p "$CREWSHIP_DATA_DIR/installations"
printf iid-before-http >"$CREWSHIP_DATA_DIR/installations/default"
exec sleep 300
FAILEDSERVER
chmod +x "$tmp/bin/identity-before-http"
if (START_TIMEOUT=1 cmd_start failedstart --binary "$tmp/bin/identity-before-http" --port "$FAKE_PORT") >"$tmp/failedstart.out" 2>"$tmp/failedstart.err"; then r=started; else r=failed; fi
check "identity-before-HTTP fixture actually fails startup" "$r" failed
check "failed startup created owned installation identity" "$(cat "$tmp/state/failedstart/data/installations/default")" iid-before-http
if (cmd_stop failedstart) >"$tmp/failedstop.out" 2>"$tmp/failedstop.err"; then r=removed; else r=refused; fi
check "failed startup with installation identity remains cleanable" "$r" removed
check "failed startup cleanup removes retained state" "$([[ -e "$tmp/state/failedstart" ]] && echo left || echo gone)" gone

for window in manifest-first ownership-first; do
  name="interrupted-$window"
  dir="$STATE_ROOT/$name"
  mkdir -p "$dir/data/installations"
  printf '{"name":"%s","prefix":"crewship-tw-%s-1234","data_dir":"%s/data","pid":"","instance_id":""}\n' "$name" "$name" "$dir" >"$dir/manifest.json"
  (umask 077; : >"$dir/launch.env")
  ownership "$dir" record
  printf iid-interrupted >"$dir/data/installations/default"
  manifest_set "$dir" instance_id iid-interrupted
  if [[ "$window" == ownership-first ]]; then
    ownership "$dir" record
    manifest_set "$dir" instance_id ""
  fi
  if (FAKE_DOWN=1 cmd_stop "$name") >"$tmp/$window-down.out" 2>"$tmp/$window-down.err"; then r=removed; else r=refused; fi
  check "interrupted $window retains state when Docker unavailable" "$r" refused
  check "interrupted $window reaches retryable teardown failure" "$(manifest_get "$dir" state)" teardown_failed
  if (cmd_stop "$name") >"$tmp/$window-retry.out" 2>"$tmp/$window-retry.err"; then r=removed; else r=refused; fi
  check "interrupted $window retry completes cleanup" "$r" removed
  check "interrupted $window cleanup removes state" "$([[ -e "$dir" ]] && echo left || echo gone)" gone
done

# Recovery accepts only initial publication backed by the owned file, never
# two different established IDs or an ID merely injected into either JSON.
name=identity-mismatch
dir="$STATE_ROOT/$name"
mkdir -p "$dir/data/installations"
printf iid-original >"$dir/data/installations/default"
printf '{"name":"%s","prefix":"crewship-tw-%s-1234","data_dir":"%s/data","pid":"","instance_id":"iid-original"}\n' "$name" "$name" "$dir" >"$dir/manifest.json"
(umask 077; : >"$dir/launch.env")
ownership "$dir" record
manifest_set "$dir" instance_id iid-changed
printf iid-changed >"$dir/data/installations/default"
: >"$FAKE_LOG"
if (cmd_stop "$name") >/dev/null 2>&1; then r=removed; else r=refused; fi
check "two established installation IDs are never reconciled" "$r" refused
check "identity mismatch selects no Docker resources" "$(wc -l <"$FAKE_LOG")" 0
manifest_set "$dir" instance_id ""
if (cmd_stop "$name") >/dev/null 2>&1; then r=removed; else r=refused; fi
check "known snapshot with different owned file refuses adoption" "$r" refused
ownership "$dir" record
manifest_set "$dir" instance_id iid-original
if (cmd_stop "$name") >/dev/null 2>&1; then r=removed; else r=refused; fi
check "unknown snapshot requires owned file to match discovered ID" "$r" refused
printf iid-original >"$dir/data/installations/default"
ownership "$dir" record
cmd_stop "$name" >/dev/null 2>&1

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

exit "$fail"

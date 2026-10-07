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
           DATABASE_URL=file:/live.db HOME=/root PATH=/bin CREWSHIP_PAGE_PROJECTS_PATH=/outside/projects; do
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
env | sort >"$FAKE_SERVER_ENV"
echo \$\$ >"$FAKE_SERVER_PID"
exec python3 -m http.server "\${CREWSHIP_PORT:-8199}" --bind 127.0.0.1 >/dev/null 2>&1
FAKEBIN
chmod +x "$tmp/bin/fake-crewship"
: >"$FAKE_SERVER_ENV"
FAKE_PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
cmd_start envok --binary "$tmp/bin/fake-crewship" --port "$FAKE_PORT" \
  --env CREWSHIP_PAGE_BUILD_IMAGE=sha256:0000000000000000000000000000000000000000000000000000000000000000 \
  --env CREWSHIP_RATELIMIT_DISABLED=1 --env CREWSHIP_ALLOW_SIGNUP=true \
  --env CREWSHIP_NEXTJS_URL=http://localhost:$FAKE_PORT --page-projects >/dev/null 2>&1
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
check "manifest records the owned projects dir" \
  "$(manifest_get "$tmp/state/envok" page_projects_dir)" "$tmp/state/envok/page-projects"

# teardown removes the projects dir with everything else it owns
cmd_stop envok >/dev/null 2>&1 || true
check "teardown removes the page-projects dir" "$([[ -e "$tmp/state/envok" ]] && echo left || echo gone)" gone
# The recorded pid IS the server (exec), so stop must have terminated it: the
# port must no longer answer. A leftover here is what leaked an orphan in #1815.
if (exec 3<>/dev/tcp/127.0.0.1/"$FAKE_PORT") 2>/dev/null; then r=still-listening; else r=closed; fi
check "teardown terminated the fake server (no orphan)" "$r" closed
[[ -f "$FAKE_SERVER_PID" ]] && kill "$(cat "$FAKE_SERVER_PID")" 2>/dev/null || true

# --- --page-projects rejects escape attempts ---
for bad_pp in ../escape /abs/path "a/b" data server.log manifest.json cli-config.yaml server.pid; do
  if (cmd_start ppreject --binary /bin/true --page-projects "$bad_pp") >/dev/null 2>&1; then r=started; else r=refused; fi
  check "--page-projects refuses '$bad_pp'" "$r" refused
done

exit "$fail"

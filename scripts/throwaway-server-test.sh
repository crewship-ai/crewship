#!/usr/bin/env bash
# throwaway-server-test.sh — teardown ownership rules of throwaway-server.sh,
# against a fake docker on PATH. The real start/stop cycle needs a daemon and
# a built binary and is exercised by hand (see the script header).
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
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
  "volume ls") printf 'crewship-tw-t-abcd-home-x\ncrewship-tw-t-abcdef-home-x\nunrelated\n' ;;
esac
FAKE
chmod +x "$tmp/bin/docker"
export PATH="$tmp/bin:$PATH" FAKE_LOG="$tmp/docker.log" FAKE_DATA="$tmp/state/t/data"
# shellcheck disable=SC2034 # read by the sourced script
STATE_ROOT="$tmp/state"
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

exit "$fail"

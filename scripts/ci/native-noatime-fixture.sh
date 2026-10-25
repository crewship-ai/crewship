#!/usr/bin/env bash
# Ephemeral Linux acceptance host only. The caller mounts an owned noatime tmpfs.
# Run the production native canary with read-only synthetic Docker network files.
set -euo pipefail
[[ $# == 4 ]] || { echo 'usage: native-noatime-fixture.sh IMAGE FIXTURE OUTPUT red|green' >&2; exit 2; }
image=$1 fixture=$2 output=$3 expected=$4
[[ $fixture = /* && $output = /* && ! -e $output ]]
[[ $expected == red || $expected == green ]]
mkdir -- "$output"
actual_docker=$(command -v docker)
readonly actual_docker
for file in resolv.conf hosts hostname; do
  [[ -f $fixture/$file && ! -L $fixture/$file ]]
done
export NATIVE_FIXTURE_ROOT="$fixture" NATIVE_FIXTURE_DOCKER="$actual_docker"
cat > "$output/docker" <<'PY'
#!/usr/bin/env python3
import os, sys
args = sys.argv[1:]
if args and args[0] == 'create':
    mounts = []
    for name in ('resolv.conf', 'hosts', 'hostname'):
        mounts += ['--mount', 'type=bind,src=' + os.environ['NATIVE_FIXTURE_ROOT'] + '/' + name + ',dst=/etc/' + name + ',readonly']
    args = [args[0], *mounts, *args[1:]]
os.execv(os.environ['NATIVE_FIXTURE_DOCKER'], [os.environ['NATIVE_FIXTURE_DOCKER'], *args])
PY
chmod 0755 "$output/docker"
set +e
PATH="$output:$PATH" CREWSHIP_RESTRICTED_LIVE=1 CREWSHIP_RESTRICTED_NATIVE_IMAGE="$image" \
  go test -tags restrictedruntime_live ./internal/restricteddispatch \
  -run '^TestLiveNativeFrozenToolsAndDurableAccounting$' -count=1 -timeout 10m -json > "$output/go-test.jsonl" 2> "$output/stderr.log"
result=$?
set -e
python3 - "$output/go-test.jsonl" "$expected" "$result" <<'PY'
import json, sys
events = [json.loads(line) for line in open(sys.argv[1])]
name = 'TestLiveNativeFrozenToolsAndDurableAccounting'
actions = [e['Action'] for e in events if e.get('Test') == name]
assert actions.count('run') == 1 and 'skip' not in actions, actions
if sys.argv[2] == 'green':
    assert sys.argv[3] == '0' and actions.count('pass') == 1 and 'fail' not in actions, actions
else:
    output = ''.join(e.get('Output', '') for e in events)
    assert sys.argv[3] != '0' and actions.count('fail') == 1, actions
    assert 'remount "/newroot/etc/resolv.conf": Operation not permitted' in output, output
print('Production native canary: ' + sys.argv[2] + ' expectation proved (no SKIP)')
PY

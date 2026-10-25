#!/usr/bin/env bash
# Ephemeral Linux acceptance host only. The caller sets Docker network-file
# source mount flags. The production canary container arguments remain unchanged.
set -euo pipefail
[[ $# == 3 ]] || { echo 'usage: native-noatime-fixture.sh IMAGE OUTPUT red|green' >&2; exit 2; }
image=$1 output=$2 expected=$3
[[ $output = /* && ! -e $output ]]
[[ $expected == red || $expected == green ]]
mkdir -- "$output"
set +e
CREWSHIP_RESTRICTED_LIVE=1 CREWSHIP_RESTRICTED_NATIVE_IMAGE="$image" \
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

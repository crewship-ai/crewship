#!/usr/bin/env bash
# Deterministic regression only: no server, credentials or live harness runs.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/automation-loop-wait.sh"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT
POLL_TIMEOUT=5 POLL_INTERVAL=1 MAX_CHAIN_DEPTH=8
automation_loop_now() { cat "$WORKDIR/clock"; }
automation_loop_sleep() { echo "$(( $(automation_loop_now) + $1 ))" > "$WORKDIR/clock"; }
changes_count() {
  local tick
  tick="$(automation_loop_now)"
  if (( tick < 3 )); then echo 8
  elif [[ "$SCENARIO" == changing ]] && (( tick >= 23 )); then echo 10
  else echo 9; fi
}
cs() {
  [[ "$SCENARIO" != error ]] || return 1
  if [[ "$SCENARIO" == malformed ]]; then echo 'not JSON'; return; fi
  python3 - "$SCENARIO" "$(automation_loop_now)" "$@" <<'PY'
import json,sys
scenario,tick=sys.argv[1],int(sys.argv[2]); args=sys.argv[3:]
late=tick<3 or scenario=='timeout'
if args[:2]==['routine','records']:
    first=args[2]=='routine-a'
    row={'id':'run-a' if first else 'run-b','status':'completed','chain_depth':8 if first else 7,'chain_origin':'root'}
    if late or scenario=='active' or (scenario=='late-active' and tick>=23): row['status']='running'
    if scenario=='depth': row['chain_depth']=9
    print(json.dumps([] if scenario=='empty' else [row]))
elif args[0]=='journal':
    payload={'automation_id':'auto-a','routine_slug':'routine-a','origin_run_id':'run-a','chain_depth':9,'max_chain_depth':8}
    if scenario=='foreign': payload['automation_id']='other'
    if scenario=='foreign-routine': payload['routine_slug']='other'
    if scenario=='foreign-origin': payload['origin_run_id']='other'
    if scenario=='unreadable': payload['reason']='unreadable'; payload.pop('chain_depth')
    print(json.dumps([] if late else [{'payload':payload}]))
else: sys.exit('unexpected fake CLI invocation')
PY
}
reset() { SCENARIO="$1"; echo 0 > "$WORKDIR/clock"; }
args=(routine-a routine-b auto-a auto-b)
reset late
[[ "$(changes_count mission)" == 8 ]]
automation_loop_wait_for_cap "${args[@]}"
[[ "$(automation_loop_now)" == 3 ]] # The final depth transition arrived late.
[[ "$(changes_count mission)" == 9 ]]
automation_loop_quiet mission 9 "${args[@]}"
[[ "$SETTLED" == 9 && "$(automation_loop_now)" == 23 ]]
for scenario in foreign foreign-routine foreign-origin unreadable active depth malformed error timeout empty; do
  reset "$scenario"
  if automation_loop_wait_for_cap "${args[@]}"; then echo "accepted $scenario" >&2; exit 1; fi
  [[ "$(automation_loop_now)" == 5 ]] # Every unsuccessful wait is bounded.
done
for scenario in changing late-active; do
  reset "$scenario"
  automation_loop_wait_for_cap "${args[@]}"
  if automation_loop_quiet mission 9 "${args[@]}"; then echo "quiet accepted $scenario" >&2; exit 1; fi
done
echo 'PASS: late final transition, correlated terminal cap, bounded failures and quiet recheck'

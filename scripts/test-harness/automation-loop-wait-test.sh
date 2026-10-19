#!/usr/bin/env bash
# Deterministic regression only: no server, credentials or live harness runs.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/automation-loop-wait.sh"
budget_cli="$(declare -f automation_loop_cli 2>/dev/null || true)"
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
  if [[ "$SCENARIO" == slow-success ]]; then automation_loop_sleep 2; fi
  [[ "$SCENARIO" != error ]] || return 1
  if [[ "$SCENARIO" == malformed ]]; then echo 'not JSON'; return; fi
  python3 - "$SCENARIO" "$(automation_loop_now)" "$@" <<'PY'
import json,sys
scenario,tick=sys.argv[1],int(sys.argv[2]); args=sys.argv[3:]
late=(tick<3 or scenario=='timeout') and scenario!='slow-success'
if args[:2]==['routine','records']:
    first=args[2]=='routine-a'
    row={'id':'run-a' if first else 'run-b','pipeline_slug':args[2],'status':'completed','chain_depth':8 if first else 7,'chain_origin':'root'}
    if scenario=='foreign-record': row['pipeline_slug']='other'
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
# For deterministic reads, preserve fake CLI/clock injection after the bounded
# production subprocess wrapper is introduced. Its real budget is tested below.
automation_loop_cli() { shift; cs "$@"; }
reset slow-success
if automation_loop_wait_for_cap "${args[@]}"; then echo 'accepted successful poll after deadline' >&2; exit 1; fi
reset late
[[ "$(changes_count mission)" == 8 ]]
automation_loop_wait_for_cap "${args[@]}"
[[ "$(automation_loop_now)" == 3 ]] # The final depth transition arrived late.
[[ "$(changes_count mission)" == 9 ]]
automation_loop_quiet mission 9 "${args[@]}"
[[ "$SETTLED" == 9 && "$(automation_loop_now)" == 23 ]]
for scenario in foreign foreign-routine foreign-record foreign-origin unreadable active depth malformed error timeout empty; do
  reset "$scenario"
  if automation_loop_wait_for_cap "${args[@]}"; then echo "accepted $scenario" >&2; exit 1; fi
  [[ "$(automation_loop_now)" == 5 ]] # Every unsuccessful wait is bounded.
done
for scenario in changing late-active; do
  reset "$scenario"
  automation_loop_wait_for_cap "${args[@]}"
  if automation_loop_quiet mission 9 "${args[@]}"; then echo "quiet accepted $scenario" >&2; exit 1; fi
done
# A real stalled executable must be stopped by the INNER remaining budget;
# reaching the outer watchdog without writing a result is a regression.
[[ -n "$budget_cli" ]] || { echo 'missing bounded CLI wrapper' >&2; exit 1; }
printf '#!/usr/bin/env bash\nexec sleep 10\n' > "$WORKDIR/stalled-cli"
chmod +x "$WORKDIR/stalled-cli"
export CREWSHIP="$WORKDIR/stalled-cli" SERVER='http://fixture.invalid' WORKDIR
{
  printf '%s\n' "$budget_cli"
  printf 'automation_loop_now() { date +%%s; }\n_CS_ARGS=()\n'
  printf 'automation_loop_cli "$(( $(date +%%s) + 1 ))" routine records fixture\n'
  printf 'printf "inner-status=%%s\\n" "$?" > "$WORKDIR/budget-result"\n'
} > "$WORKDIR/budget-probe.sh"
timeout 3 bash "$WORKDIR/budget-probe.sh" || true
grep -Eq '^inner-status=(124|137)$' "$WORKDIR/budget-result"
echo 'PASS: late final transition, correlated terminal cap, bounded failures and quiet recheck'

#!/usr/bin/env bash
# Sourced helpers only. Production uses the real CLI; regression fixtures replace
# cs/clock/sleep without running a server or changing the runtime depth limit.
automation_loop_now() { date +%s; }
automation_loop_sleep() { sleep "$1"; }

automation_loop_cli() {
  local deadline="$1" remaining
  shift
  remaining=$(( deadline - $(automation_loop_now) ))
  (( remaining > 0 )) || return 1
  command -v timeout >/dev/null 2>&1 || return 1
  # Same executable, server and profile/workspace arguments as lib.sh's cs().
  # Bound the process group, including a stalled request; do not rely on the
  # client's independently configurable HTTP timeout or grant an extra grace.
  timeout --signal=KILL "${remaining}s" "$CREWSHIP" --server "$SERVER" ${_CS_ARGS[@]+"${_CS_ARGS[@]}"} "$@"
}

automation_loop_ready() {
  local deadline="${5:-$(( $(automation_loop_now) + POLL_TIMEOUT ))}"
  automation_loop_cli "$deadline" routine records "$1" --limit 500 -f json > "$WORKDIR/cap-a.json" || return 1
  automation_loop_cli "$deadline" routine records "$2" --limit 500 -f json > "$WORKDIR/cap-b.json" || return 1
  automation_loop_cli "$deadline" journal --type automation.depth_exceeded --lines 500 -f json > "$WORKDIR/cap-events.json" || return 1
  python3 - "$WORKDIR" "$1" "$2" "$3" "$4" "$MAX_CHAIN_DEPTH" <<'PY'
import json,sys
try:
    directory,a,b,aid,bid,limit=sys.argv[1:]; limit=int(limit)
    groups=[json.load(open(directory+'/'+name)) for name in ('cap-a.json','cap-b.json')]
    entries=json.load(open(directory+'/cap-events.json'))
    if not isinstance(entries,list): raise ValueError()
    rows=[]
    for group,slug in zip(groups,(a,b)):
        if not isinstance(group,list) or not group or len(group)>=500: raise ValueError()
        for row in group:
            if not isinstance(row,dict) or not isinstance(row.get('id'),str) or not row['id']: raise ValueError()
            if row.get('pipeline_slug')!=slug: raise ValueError()
            if row.get('status') not in ('completed','failed','cancelled'): raise ValueError()
            if type(row.get('chain_depth')) is not int or not 0<=row['chain_depth']<=limit: raise ValueError()
            if not isinstance(row.get('chain_origin'),str) or not row['chain_origin']: raise ValueError()
        rows+=group
    if max(row['chain_depth'] for row in rows)!=limit: raise ValueError()
    ids={row['id'] for row in rows}
    def matches(entry):
        p=entry.get('payload') if isinstance(entry,dict) else None
        return (isinstance(p,dict) and 'reason' not in p and
                (p.get('automation_id'),p.get('routine_slug')) in ((aid,a),(bid,b)) and
                p.get('origin_run_id') in ids and type(p.get('chain_depth')) is int and
                p['chain_depth']==limit+1 and type(p.get('max_chain_depth')) is int and p['max_chain_depth']==limit)
    if not any(matches(entry) for entry in entries): raise ValueError()
except (ValueError,TypeError,KeyError,OSError):
    sys.exit(1)
PY
  [[ "$?" == 0 ]] && (( $(automation_loop_now) < deadline ))
}

automation_loop_wait_for_cap() {
  local deadline now remaining pause
  [[ "$POLL_TIMEOUT" =~ ^[1-9][0-9]*$ && "$POLL_INTERVAL" =~ ^[1-9][0-9]*$ ]] || return 1
  deadline=$(( $(automation_loop_now) + POLL_TIMEOUT ))
  while true; do
    if automation_loop_ready "$@" "$deadline"; then
      # Even a successful read must complete BEFORE the original deadline.
      (( $(automation_loop_now) < deadline ))
      return
    fi
    now="$(automation_loop_now)"
    (( now < deadline )) || return 1
    remaining=$(( deadline - now )); pause="$POLL_INTERVAL"
    (( pause <= remaining )) || pause="$remaining"
    automation_loop_sleep "$pause"
  done
}

# Retain the original quiet-window assertion, now AFTER observed cap readiness.
# Recheck current records as well: a terminal snapshot cannot hide a later run.
automation_loop_quiet() {
  local mission="$1" baseline="$2"
  shift 2
  automation_loop_sleep 20
  SETTLED="$(changes_count "$mission")" || return 1
  [[ "$SETTLED" =~ ^[0-9]+$ && "$SETTLED" == "$baseline" ]] && automation_loop_ready "$@"
}

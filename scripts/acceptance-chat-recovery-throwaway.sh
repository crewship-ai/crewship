#!/usr/bin/env bash
# acceptance-chat-recovery-throwaway.sh — host chat recovery across a server
# restart WITHOUT reseeding, on one disposable server with real Docker (#4824).
#
#   CREWSHIP_THROWAWAY_STATE=/protected/dir \
#   scripts/acceptance-chat-recovery-throwaway.sh --binary /path/to/crewship --evidence DIR [--name NAME]
#
# The binary's directory must also hold crewship-sidecar (agent-executable).
# Every server comes from scripts/throwaway-server.sh and is torn down on exit.
#
# What it proves, from server-side evidence (CLI JSON, the server's own retained
# capture spool, host-side process listing) rather than agent text:
#
#   1. lease expiry while the server is down: three host chat turns are live
#      when the server process stops; after `restart --down-seconds` (past the
#      60 s work lease, same data directory) each turn's attempt ends with
#      "lease expired in runtime phase ..." and the turn is parked in
#      needs_reconciliation.
#   2. no premature settlement: while the turn's CLI process is still alive in
#      its crew container the turn stays needs_reconciliation and the agent gets
#      no second live turn. It settles only after the capture shows a confirmed
#      end — a natural exit ("exited"), or a forced end with the exec-bound
#      whole-tree stop receipt ("execution_cancelled" + stop-confirmed) — and
#      never before the last moment the runtime was seen alive.
#   3. capacity is released: once settled, nothing for the server is left in
#      starting, running or needs_reconciliation (the states that hold a
#      ServerTotal execution slot).
#   4. new work: the same agents accept a new chat turn afterwards; it is
#      admitted, its runtime starts, and it settles terminally.
#   S. standard restart (SIGTERM, start again immediately, lease not expired):
#      the same no-premature-settlement and release checks.
#
# No model provider is configured (offline demo seed): the property under test
# is the runtime lifecycle, not model output. Without a credential the Claude
# Code CLI keeps retrying for minutes and then exits 1 on its own; an agent
# timeout shorter than that makes the capture helper stop it at its deadline.
# Both ends are real, but neither run produces model text, so "completes" in
# (4) means "reaches a terminal state through the normal path". Each check
# whose precondition does not hold (for example the CLI exited before the
# server came back) FAILS instead of passing vacuously.
#
# Protected host capture is used only when every ancestor of the server's data
# directory is owned by root or the server user and not group/world-writable.
# The default throwaway state root under ~/.local/state is group-writable on
# crewship-dev, which silently selects the legacy transport; this script
# refuses such a root before starting anything.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
TW="$here/throwaway-server.sh"
binary="" evidence="" name="chat-recovery-$(date -u +%H%M%S)"
LEASE_DOWN="${CHAT_RECOVERY_DOWN_SECONDS:-75}"
DEADLINE_SECS="${CHAT_RECOVERY_DEADLINE_SECONDS:-150}"
LONG_SECS="${CHAT_RECOVERY_LONG_SECONDS:-900}"
SETTLE_WAIT="${CHAT_RECOVERY_SETTLE_WAIT:-600}"

while (($#)); do
  case "$1" in
    --binary) binary="$2"; shift 2 ;;
    --evidence) evidence="$2"; shift 2 ;;
    --name) name="$2"; shift 2 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
done
[[ -x "$binary" && -n "$evidence" ]] || { sed -n '4,7p' "$0" >&2; exit 2; }
binary="$(cd "$(dirname "$binary")" && pwd)/$(basename "$binary")"
STATE_ROOT="${CREWSHIP_THROWAWAY_STATE:-$HOME/.local/state/crewship-throwaway}"
export CREWSHIP_THROWAWAY_STATE="$STATE_ROOT"

umask 077
mkdir -p "$evidence"
chmod 700 "$evidence"
LOG="$evidence/scenario.log"
: >"$LOG"
fails=0
say() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" | tee -a "$LOG" >&2; }
pass() { say "PASS $*"; }
fail() { say "FAIL $*"; fails=$((fails + 1)); }
die() { say "ABORT $*"; exit 1; }

# --- preflight -------------------------------------------------------------
if ! command -v docker >/dev/null || ! docker info >/dev/null 2>&1; then die "docker unavailable"; fi
sidecar="$(dirname "$binary")/crewship-sidecar"
[[ -f "$sidecar" ]] || die "no crewship-sidecar next to $binary"
[[ "$(stat -c %a "$sidecar")" =~ [157]$ ]] || die "$sidecar is not executable by the agent uid (chmod 0755)"
mkdir -p "$STATE_ROOT"
python3 - "$STATE_ROOT" <<'PY' || die "state root $STATE_ROOT has a group/world-writable or foreign-owned ancestor: protected host capture would be disabled; set CREWSHIP_THROWAWAY_STATE to a private directory"
import os, stat, sys
path = os.path.realpath(sys.argv[1])
uid = os.geteuid()
while True:
    st = os.lstat(path)
    if st.st_uid not in (0, uid) or stat.S_ISLNK(st.st_mode):
        sys.exit(1)
    if st.st_mode & 0o022 and not (st.st_uid == 0 and st.st_mode & stat.S_ISVTX):
        sys.exit(1)
    if path == "/":
        break
    path = os.path.dirname(path)
PY

{
  echo "binary: $binary"
  echo "binary_sha256: $(sha256sum "$binary" | cut -d' ' -f1)"
  echo "sidecar_sha256: $(sha256sum "$sidecar" | cut -d' ' -f1)"
  echo "binary_version: $("$binary" --version 2>/dev/null | head -1)"
  echo "scenario_commit: $(git -C "$here/.." rev-parse HEAD 2>/dev/null || echo unknown)"
  echo "started_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "lease_down_seconds: $LEASE_DOWN deadline_seconds: $DEADLINE_SECS"
} >"$evidence/run-info.txt"

# --- throwaway lifecycle ---------------------------------------------------
started=0
teardown() {
  local rc=$?
  set +e
  if ((started)); then
    say "teardown: stop $name"
    collect_server_log
    "$TW" stop "$name" >>"$LOG" 2>&1 || say "teardown of $name FAILED; manifest kept for 'stop $name'"
    "$TW" list >"$evidence/throwaway-list-after.txt" 2>&1
    if grep -q "^$name " "$evidence/throwaway-list-after.txt"; then say "teardown: $name still listed"; else say "teardown: $name no longer listed"; fi
  fi
  say "result: $fails failed check(s), exit status $rc"
  if ((rc == 0 && fails > 0)); then rc=1; fi
  exit "$rc"
}
trap teardown EXIT
trap 'exit 130' INT TERM

dir="$STATE_ROOT/$name"
collect_server_log() {
  # Lifecycle lines only; secrets never reach the evidence directory.
  [[ -f "$dir/server.log" ]] || return 0
  grep -E 'lease|retained|reconcil|exec (agent|finished)|agent exited|host chat|shutting down|server (start|listen)|dispatch: recovered|capture|stop' "$dir/server.log" \
    | grep -viE 'token|secret|password|api_key|authorization' >"$evidence/server-lifecycle.log" || true
}

say "starting throwaway $name under $STATE_ROOT"
started=1
"$TW" start "$name" --binary "$binary" >>"$LOG" 2>&1 || die "throwaway start failed"
port="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["port"])' "$dir/manifest.json")"
prefix="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["prefix"])' "$dir/manifest.json")"
export CREWSHIP_SERVER="http://127.0.0.1:$port" CREWSHIP_CONFIG="$dir/cli-config.yaml"
cs() { "$binary" "$@"; }

say "seeding offline demo (no provider credential)"
cs seed --server "$CREWSHIP_SERVER" --offline-demo --skip-issues >"$evidence/seed.txt" 2>&1 || die "seed failed (see seed.txt)"
grep -viE 'password|token' "$evidence/seed.txt" >"$evidence/seed.redacted.txt" || true
rm -f "$evidence/seed.txt"

# agent -> crew slug for the agents this scenario drives.
crew_of() { cs agent get "$1" -f json | python3 -c 'import json,sys; d=json.load(sys.stdin); c=d.get("crew") or {}; print(c.get("slug") or d.get("crew_slug") or "")'; }
container_of() { docker ps --filter "name=^/$prefix-team-$1-" --format '{{.Names}}' | head -1; }
runtime_alive() { # crew slug -> 0 when a Claude Code process runs in its container
  local c; c="$(container_of "$1")"
  [[ -n "$c" ]] && docker top "$c" -o pid,args 2>/dev/null | grep -q 'claude --print'
}
work_json() { cs work get "$1" -f json; }
field() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1], {}, {"d": d}))' "$1"; }

# start_turn AGENT TAG: send a chat turn in the background and print the work id
# once its runtime is confirmed.
start_turn() {
  local agent="$1" tag="$2" since id state reason
  since="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  # The redirection belongs on the subshell: inherited by the background job,
  # the caller's $(...) pipe would stay open until the turn ends.
  (cs run "$agent" "Recovery probe $tag: reply with one word." --no-stream --timeout "$LONG_SECS" || true) >"$evidence/run-$tag.txt" 2>&1 </dev/null &
  for _ in $(seq 1 120); do
    id="$(cs work list --agent "$agent" --since "$since" -f json | python3 -c 'import json,sys; it=json.load(sys.stdin).get("items") or []; print(it[-1]["id"] if it else "")')"
    if [[ -n "$id" ]]; then
      state="$(work_json "$id" | field 'd["state"]')"
      reason="$(work_json "$id" | field 'd.get("state_reason","")')"
      if [[ "$state" == running && "$reason" == "runtime confirmed" ]]; then echo "$id"; return 0; fi
    fi
    sleep 2
  done
  return 1
}

run_id_of() { work_json "$1" | field 'd["domain_id"]'; }

# watch_until_terminal ID CREW TAG: poll the item and the runtime until the item
# is terminal; record when the runtime was last seen alive and fail if the item
# was ever terminal while the runtime was alive.
watch_until_terminal() {
  local id="$1" crew="$2" tag="$3" end=$((SECONDS + SETTLE_WAIT)) state alive seen_park=0
  : >"$evidence/watch-$tag.tsv"
  last_alive=""
  while ((SECONDS < end)); do
    state="$(work_json "$id" | field 'd["state"]')"
    if runtime_alive "$crew"; then alive=1; last_alive="$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"; else alive=0; fi
    printf '%s\t%s\t%s\n' "$(date -u +%H:%M:%S)" "$state" "$alive" >>"$evidence/watch-$tag.tsv"
    [[ "$state" == needs_reconciliation ]] && seen_park=1
    case "$state" in
      succeeded|failed|cancelled|expired)
        if ((alive)); then fail "[$tag] settled ($state) while its runtime was still alive"; fi
        echo "$seen_park" >"$evidence/.park-$tag"
        echo "$last_alive" >"$evidence/.alive-$tag"
        return 0 ;;
    esac
    sleep 3
  done
  fail "[$tag] not settled within ${SETTLE_WAIT}s (last state $state)"
  echo "$seen_park" >"$evidence/.park-$tag"
  echo "$last_alive" >"$evidence/.alive-$tag"
  return 1
}

spool_of() { echo "$dir/data/run-output/$1"; }

record_spool() { # tag run_id: names, receipts and the terminal record only, never output data
  local s; s="$(spool_of "$2")"
  {
    echo "# spool $s"; ls -l "$s" "$s/output" 2>&1 || true
    echo "# checkpoint"; cat "$s/output/checkpoint.json" 2>&1 || true; echo
    echo "# identity"; cat "$s/identity.json" 2>&1 || true; echo
    echo "# stop-requested"; cat "$s/stop-requested" 2>&1 || true; echo
    echo "# stop-confirmed"; cat "$s/stop-confirmed" 2>&1 || true; echo
    echo "# terminal record"; capture_record "$2" || true
  } >"$evidence/spool-$1.txt"
}

# capture_record RUN_ID prints the capture's terminal "exit" record as JSON.
capture_record() {
  python3 - "$(spool_of "$1")/output/events.jsonl" <<'PY'
import json, sys
found = None
def walk(v):
    global found
    if isinstance(v, dict):
        if v.get("kind") == "exit":
            found = {k: v.get(k) for k in ("sequence", "kind", "exit_code", "reason", "completed_at")}
        for x in v.values():
            walk(x)
for line in open(sys.argv[1]):
    try:
        walk(json.loads(line))
    except ValueError:
        pass
if found is None:
    sys.exit(1)
print(json.dumps(found))
PY
}

capture_reason() { capture_record "$1" 2>/dev/null | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["reason"], d["exit_code"])' 2>/dev/null || true; }

# assert_settled TAG ID RUN_ID CREW EXPECT_REASON
assert_settled() {
  local tag="$1" id="$2" run="$3" expect="$4" detail terminal last_alive reason
  work_json "$id" >"$evidence/work-$tag-final.json"
  detail="$(field 'd["state"]+" | "+d.get("state_reason","")' <"$evidence/work-$tag-final.json")"
  terminal="$(field 'd.get("terminal_at") or ""' <"$evidence/work-$tag-final.json")"
  record_spool "$tag" "$run"
  reason="$(capture_reason "$run")"
  last_alive="$(cat "$evidence/.alive-$tag" 2>/dev/null)"
  say "[$tag] final: $detail; terminal_at=$terminal; capture=$reason; runtime last seen alive=$last_alive"
  if [[ "$reason" == "$expect "* ]]; then pass "[$tag] capture recorded a confirmed end: $reason"; else fail "[$tag] capture reason '$reason', want '$expect'"; fi
  if [[ "$expect" == execution_cancelled ]]; then
    local exec_id
    exec_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["exec_id"])' "$(spool_of "$run")/identity.json" 2>/dev/null || true)"
    if [[ -n "$exec_id" ]] && grep -q "$exec_id" "$(spool_of "$run")/stop-confirmed" 2>/dev/null; then
      pass "[$tag] whole-tree stop receipt is bound to the launched exec"
    else
      fail "[$tag] no stop receipt for the launched exec"
    fi
  fi
  if [[ -n "$last_alive" && -n "$terminal" ]]; then
    if python3 - "$terminal" "$last_alive" <<'PY'; then pass "[$tag] settled at $terminal, after the runtime was last seen alive ($last_alive)"; else fail "[$tag] terminal_at $terminal precedes last-seen-alive $last_alive"; fi
import sys
from datetime import datetime
def t(s):
    s = s.rstrip("Z")
    if "." in s:
        a, b = s.split(".", 1); s = a + "." + b[:6]
    return datetime.fromisoformat(s)
sys.exit(0 if t(sys.argv[1]) >= t(sys.argv[2]) else 1)
PY
  fi
}

slot_holders() { # prints the number of slot-holding work items server-wide
  local n=0 s
  for s in starting running needs_reconciliation; do
    n=$((n + $(cs work list --state "$s" -f json | python3 -c 'import json,sys; print(len(json.load(sys.stdin).get("items") or []))')))
  done
  echo "$n"
}

# --- phase A: lease expiry while the server is down ------------------------
A_DEADLINE=riley A_NATURAL=casey B_STANDARD=jamie
cs agent update "$A_DEADLINE" --timeout "$DEADLINE_SECS" >>"$LOG" 2>&1 || die "agent update $A_DEADLINE"
cs agent update "$A_NATURAL" --timeout "$LONG_SECS" >>"$LOG" 2>&1 || die "agent update $A_NATURAL"
cs agent update "$B_STANDARD" --timeout "$LONG_SECS" >>"$LOG" 2>&1 || die "agent update $B_STANDARD"
crew_deadline="$(crew_of "$A_DEADLINE")" crew_natural="$(crew_of "$A_NATURAL")" crew_standard="$(crew_of "$B_STANDARD")"
[[ "$crew_deadline" != "$crew_natural" && "$crew_natural" != "$crew_standard" && "$crew_deadline" != "$crew_standard" ]] \
  || die "scenario agents must be in three different crews ($crew_deadline/$crew_natural/$crew_standard)"

say "phase A: starting turns (deadline=$A_DEADLINE/${DEADLINE_SECS}s, natural=$A_NATURAL)"
id_dl="$(start_turn "$A_DEADLINE" lease-deadline)" || die "lease-deadline turn never confirmed its runtime"
id_nat="$(start_turn "$A_NATURAL" lease-natural)" || die "lease-natural turn never confirmed its runtime"
run_dl="$(run_id_of "$id_dl")" run_nat="$(run_id_of "$id_nat")"
say "lease-deadline work=$id_dl run=$run_dl; lease-natural work=$id_nat run=$run_nat"
for pair in "lease-deadline:$run_dl" "lease-natural:$run_nat"; do
  if [[ -f "$(spool_of "${pair#*:}")/identity.json" ]]; then pass "[${pair%%:*}] protected host capture owns the execution"; else die "[${pair%%:*}] no protected capture spool: legacy transport in use"; fi
done
work_json "$id_dl" >"$evidence/work-lease-deadline-before.json"
work_json "$id_nat" >"$evidence/work-lease-natural-before.json"
slots_before="$(slot_holders)"
say "slot-holding work items before restart: $slots_before"

say "restart: SIGTERM, down ${LEASE_DOWN}s, same data dir (no reseed)"
"$TW" restart "$name" --down-seconds "$LEASE_DOWN" >>"$LOG" 2>&1 || die "restart failed"
python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print("restarts", d.get("restarts"), "restarted_at", d.get("restarted_at"), "instance_id", d.get("instance_id"))' "$dir/manifest.json" | tee -a "$LOG" >"$evidence/restart-1.txt"

for tag in lease-deadline lease-natural; do
  if [[ "$tag" == lease-deadline ]]; then id="$id_dl" crew="$crew_deadline"; else id="$id_nat" crew="$crew_natural"; fi
  parked=0
  for _ in $(seq 1 60); do
    if [[ "$(work_json "$id" | field 'd["state"]')" == needs_reconciliation ]]; then parked=1; break; fi
    sleep 2
  done
  work_json "$id" >"$evidence/work-$tag-parked.json"
  end_reason="$(field '(d.get("attempts") or [{}])[-1].get("end_reason","")' <"$evidence/work-$tag-parked.json")"
  if ((parked)) && [[ "$end_reason" == "lease expired in runtime phase"* ]]; then
    pass "[$tag] (1) lease expired across the restart: $end_reason"
  else
    fail "[$tag] (1) not parked by lease expiry (end_reason='$end_reason')"
  fi
  if runtime_alive "$crew"; then
    pass "[$tag] (2) runtime still alive after restart; turn is needs_reconciliation, not settled"
  else
    fail "[$tag] (2) precondition: runtime already gone when the server came back; premature-settlement check is vacuous"
  fi
done

# While parked with a live runtime, the agent must not get a second live turn.
since_probe="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
cs run "$A_NATURAL" "Recovery probe: second turn while parked." --no-stream --timeout 60 >"$evidence/run-second-while-parked.txt" 2>&1 || true
live_second="$(cs work list --agent "$A_NATURAL" --since "$since_probe" -f json | python3 -c 'import json,sys; print(sum(1 for i in json.load(sys.stdin).get("items") or [] if i["state"] in ("starting","running")))')"
nat_state="$(work_json "$id_nat" | field 'd["state"]')"
if [[ "$live_second" == 0 && "$nat_state" == needs_reconciliation ]] && runtime_alive "$crew_natural"; then
  pass "[lease-natural] (2) agent got no second live turn while its parked runtime runs ($(head -c 200 "$evidence/run-second-while-parked.txt" | tr '\n' ' '))"
else
  fail "[lease-natural] (2) second turn admitted ($live_second live) or state changed ($nat_state) while the runtime ran"
fi

watch_until_terminal "$id_dl" "$crew_deadline" lease-deadline || true
assert_settled lease-deadline "$id_dl" "$run_dl" execution_cancelled
watch_until_terminal "$id_nat" "$crew_natural" lease-natural || true
assert_settled lease-natural "$id_nat" "$run_nat" exited

slots="$(slot_holders)"
cs work list --open -f json >"$evidence/work-open-after-phase-a.json"
if [[ "$slots" == 0 ]]; then pass "(3) after phase A no work item holds an execution slot"; else fail "(3) $slots slot-holding item(s) remain after phase A"; fi

# --- phase S: standard restart (SIGTERM, start again at once) --------------
say "phase S: standard restart with a live turn on $B_STANDARD"
id_std="$(start_turn "$B_STANDARD" standard)" || die "standard turn never confirmed its runtime"
run_std="$(run_id_of "$id_std")"
if [[ -f "$(spool_of "$run_std")/identity.json" ]]; then pass "[standard] protected host capture owns the execution"; else die "[standard] legacy transport in use"; fi
"$TW" restart "$name" >>"$LOG" 2>&1 || die "standard restart failed"
python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print("restarts", d.get("restarts"), "restarted_at", d.get("restarted_at"))' "$dir/manifest.json" >"$evidence/restart-2.txt"
work_json "$id_std" >"$evidence/work-standard-after-restart.json"
std_state="$(field 'd["state"]' <"$evidence/work-standard-after-restart.json")"
if runtime_alive "$crew_standard"; then
  pass "[standard] runtime alive after restart; state $std_state"
else
  fail "[standard] precondition: runtime gone right after restart (state $std_state)"
fi
watch_until_terminal "$id_std" "$crew_standard" standard || true
assert_settled standard "$id_std" "$run_std" exited
if [[ "$(cat "$evidence/.park-standard")" == 1 ]]; then say "[standard] passed through needs_reconciliation"; else say "[standard] settled without needs_reconciliation"; fi
slots="$(slot_holders)"
if [[ "$slots" == 0 ]]; then pass "(3) after the standard restart no work item holds an execution slot"; else fail "(3) $slots slot-holding item(s) remain after the standard restart"; fi

# --- phase C: the server accepts and settles new work ----------------------
say "phase C: new turns on the recovered agents"
for agent in "$A_DEADLINE" "$A_NATURAL"; do
  if [[ "$agent" == "$A_DEADLINE" ]]; then crew="$crew_deadline"; else crew="$crew_natural"; fi
  cs agent update "$agent" --timeout "$DEADLINE_SECS" >>"$LOG" 2>&1 || true
  if id_new="$(start_turn "$agent" "new-$agent")"; then
    pass "(4) $agent admitted a new turn after recovery and its runtime started (work $id_new)"
    watch_until_terminal "$id_new" "$crew" "new-$agent" || true
    work_json "$id_new" >"$evidence/work-new-$agent-final.json"
    st="$(field 'd["state"]+" | "+d.get("state_reason","")' <"$evidence/work-new-$agent-final.json")"
    if [[ "$st" == succeeded* || "$st" == failed* ]]; then pass "(4) $agent new turn settled through the normal path: $st"; else fail "(4) $agent new turn ended $st"; fi
  else
    fail "(4) $agent did not admit a new turn after recovery"
  fi
done
slots="$(slot_holders)"
cs work list -f json >"$evidence/work-all-final.json"
if [[ "$slots" == 0 ]]; then pass "(3) at the end no work item holds an execution slot"; else fail "(3) $slots slot-holding item(s) remain at the end"; fi
rm -f "$evidence"/.park-* "$evidence"/.alive-*

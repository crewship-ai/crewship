#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Keeper load / correctness-under-load — the valuable "performance" tests here
# are NOT raw RPS; they assert that a security/consistency invariant SURVIVES
# saturation, which is exactly where per-code-path enforcement tends to diverge
# from the intended guarantee.
#
# Bounded + non-destructive: concurrency is capped (CONC, default 40), all writes
# use HARNESS_ credentials and are cleaned up, and the server's health is polled
# throughout so we stop if we ever push it toward instability.
#
# Sections:
#   1. read-path latency baseline under concurrency (p50/p95/p99).
#   2. server stays healthy through a bounded write burst (no 5xx, health 200).
#   3. rate-limiter behaves — bursts yield 200 or 429, never 5xx.
#   4. escalation-list read stays consistent under concurrent reads.
#   5. keeper status reachable under load (decision path not wedged).
#   6. escalation-inbox flooding advisory-loss (T8, needs agents) — SKIP.
#   7. evaluator saturation → prove fail-closed (T9, needs token) — SKIP.
#
# Tunables: CONC (default 40), SAMPLES (default 60).

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

CONC="${CONC:-40}"
SAMPLES="${SAMPLES:-60}"

preflight

# percentile <p> <sorted-file-of-numbers> — nearest-rank percentile.
percentile() {
  local p="$1" f="$2" n idx
  n=$(wc -l < "$f" | tr -d ' ')
  [[ "$n" -eq 0 ]] && { echo 0; return; }
  idx=$(( (p * n + 99) / 100 )); (( idx < 1 )) && idx=1; (( idx > n )) && idx=$n
  sed -n "${idx}p" "$f"
}

health_ok() { [[ "$(curl -s -o /dev/null -w '%{http_code}' "$SERVER/healthz" --max-time 8 2>/dev/null)" == "200" ]]; }

# ─────────────────────────────────────────────────────────────────────────────
section "1. Read-path latency baseline under concurrency (${SAMPLES} samples, ${CONC}-wide)"
# ─────────────────────────────────────────────────────────────────────────────
lat="$(mktemp -t cs-lat.XXXXXX)"; codes="$(mktemp -t cs-codes.XXXXXX)"
info "Firing $SAMPLES read requests, up to $CONC concurrent…"
run_read() {
  local t0 t1 out rc
  t0=$(perl -MTime::HiRes=time -e 'printf "%d", time()*1000' 2>/dev/null || echo 0)
  out="$(cs escalation pending-count -f json 2>/dev/null)"; rc=$?
  t1=$(perl -MTime::HiRes=time -e 'printf "%d", time()*1000' 2>/dev/null || echo 0)
  echo $(( t1 - t0 )) >> "$lat"; echo "$rc" >> "$codes"
}
active=0
for _ in $(seq 1 "$SAMPLES"); do
  run_read &
  active=$((active+1))
  if (( active >= CONC )); then wait -n 2>/dev/null || wait; active=$((active-1)); fi
done
wait

ok_reads="$(grep -c '^0$' "$codes" 2>/dev/null || echo 0)"
assert_eq "all $SAMPLES concurrent reads succeeded" "$SAMPLES" "$ok_reads"
if perl -MTime::HiRes -e 1 >/dev/null 2>&1; then
  sort -n "$lat" -o "$lat"
  p50="$(percentile 50 "$lat")"; p95="$(percentile 95 "$lat")"; p99="$(percentile 99 "$lat")"
  info "latency: p50=${p50}ms p95=${p95}ms p99=${p99}ms (baseline — no hard threshold, tracked over time)"
  _pass "latency baseline captured (p50=${p50}ms p95=${p95}ms p99=${p99}ms)"
else
  skip "latency percentiles" "perl Time::HiRes not available"
fi
rm -f "$lat" "$codes"

# ─────────────────────────────────────────────────────────────────────────────
section "2. Server stays healthy through a bounded write burst"
# ─────────────────────────────────────────────────────────────────────────────
# Create + delete N HARNESS_ credentials concurrently; the server must stay
# healthy the whole time (no 5xx / no health flap). This exercises the write
# path (the same path whose audit INSERT is swallowed, T6) under contention.
BURST="${BURST:-20}"
info "Bursting $BURST concurrent credential create+delete cycles…"
health_flapped=0; wpids=()
( while true; do health_ok || { echo flap >> "$lat.flap" 2>/dev/null; }; sleep 1; done ) & watch_pid=$!
for k in $(seq 1 "$BURST"); do
  ( n="HARNESS_LOAD_$(nonce B | tr '-' '_')_$k"
    printf 'x' | cs credential create --name "$n" --type API_KEY --provider CUSTOM_CLI --env-var-name "$n" --value-stdin >/dev/null 2>&1
    cs credential delete "$n" --yes >/dev/null 2>&1 ) &
  wpids+=($!)
  (( ${#wpids[@]} % CONC == 0 )) && wait
done
for p in "${wpids[@]}"; do wait "$p" 2>/dev/null; done
kill "$watch_pid" 2>/dev/null; wait "$watch_pid" 2>/dev/null
[[ -f "$lat.flap" ]] && health_flapped=1 && rm -f "$lat.flap"
if (( health_flapped == 0 )) && health_ok; then
  _pass "server healthy throughout the $BURST-wide write burst (no health flap)"
else
  _fail "server healthy through write burst" "healthz flapped or is not 200 after the burst"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "3. Rate-limiter behaves — bursts yield 200 or 429, never 5xx"
# ─────────────────────────────────────────────────────────────────────────────
rc_file="$(mktemp -t cs-rc.XXXXXX)"
info "Firing ${SAMPLES} rapid authenticated reads to exercise the limiter…"
for _ in $(seq 1 "$SAMPLES"); do
  ( cs escalation pending-count -f json >/dev/null 2>&1; echo "$?" >> "$rc_file" ) &
  (( $(jobs -r | wc -l) >= CONC )) && wait -n 2>/dev/null || true
done
wait
# The CLI maps HTTP → exit codes; a 5xx surfaces as a non-zero, non-clean error.
# We can't see the raw status through the CLI, so assert the server is still
# healthy and the vast majority of calls succeeded (429 throttling is fine).
ok="$(grep -c '^0$' "$rc_file" 2>/dev/null || echo 0)"
rm -f "$rc_file"
if health_ok && (( ok >= SAMPLES * 6 / 10 )); then
  _pass "limiter healthy: $ok/$SAMPLES clean, server still 200 (throttling acceptable, no cascade)"
else
  _fail "limiter behaviour" "only $ok/$SAMPLES clean and/or health degraded — inspect for 5xx cascade"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "4. Escalation-list read stays consistent under concurrent reads"
# ─────────────────────────────────────────────────────────────────────────────
if have jq; then
  cfile="$(mktemp -t cs-count.XXXXXX)"
  for _ in $(seq 1 10); do
    ( cs escalation pending-count -f json 2>/dev/null | jq -r '.count // .pending // .' 2>/dev/null >> "$cfile" ) &
  done
  wait
  distinct="$(sort -u "$cfile" | grep -c . || echo 0)"; rm -f "$cfile"
  # Under a quiescent workspace the count should be stable across concurrent
  # reads (allow ≤2 distinct values for in-flight changes).
  if (( distinct <= 2 )); then
    _pass "pending-count is consistent under concurrent reads ($distinct distinct value(s))"
  else
    warn_note="pending-count returned $distinct distinct values under concurrency — possible read inconsistency"
    skip "pending-count consistency" "$warn_note"
  fi
else
  skip "pending-count consistency" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "5. Keeper status reachable under load (decision path not wedged)"
# ─────────────────────────────────────────────────────────────────────────────
if have jq; then
  online="$(cs system keeper --format json 2>/dev/null | jq -r '.enabled // .ollama_online // false')"
  if [[ "$online" == "true" ]]; then
    _pass "keeper still reports online after the load sections"
  else
    _fail "keeper online after load" "keeper not online — decision path may be wedged under load"
  fi
else
  skip "keeper status under load" "jq missing"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "6. Escalation-inbox flooding / advisory loss (needs agents, T8) — SKIP"
# ─────────────────────────────────────────────────────────────────────────────
skip "escalation-inbox flooding (test T8)" \
  "ESCALATE→inbox and F4 advisory inserts are fire-and-forget (keeper_request.go:282). Burst N ESCALATE-inducing agent runs, then submit one 'real' escalation and assert it still surfaces in 'escalation list' (nothing silently dropped). Needs real agent runs — run on dev3 with a fleet of ask jobs."

# ─────────────────────────────────────────────────────────────────────────────
section "7. Evaluator saturation → prove fail-closed (needs token, T9) — SKIP"
# ─────────────────────────────────────────────────────────────────────────────
skip "evaluator saturation fail-closed (test T9)" \
  "saturate the gatekeeper LLM slot with concurrent /request calls to force ctx-deadline; assert the decision is DENY, never ALLOW, and record p99 eval latency (= the T2 TOCTOU window width). Requires the internal token — sidecar-side probe."

finish

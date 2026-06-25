#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Recipe determinism — the "Opus authors, Haiku runs, identical output" promise.
#
# Runs a deterministic seed recipe N times with the SAME input and asserts the
# canonical (@json) output is byte-identical every time. Also reports the perf
# envelope (p50/max latency, cost/run) so we have a real-world baseline.
#
#   ./test-determinism.sh                 # csv-to-json, 5 runs
#   RUNS=10 ROUTINE=normalize-dates ./test-determinism.sh
#
# Pure-transform recipes that end in an @json transform are the right targets:
#   csv-to-json · normalize-dates · extract-contacts · parse-log-line

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

ROUTINE="${ROUTINE:-csv-to-json}"
RUNS="${RUNS:-5}"

preflight

if ! have jq; then
  skip "determinism test" "requires jq to read run outputs"
  finish
fi

section "Determinism: '$ROUTINE' × $RUNS runs, same input"

declare -a outputs=() costs=() durations=()
for i in $(seq 1 "$RUNS"); do
  info "run $i/$RUNS…"
  if ! cs routine run "$ROUTINE" >/dev/null 2>&1; then
    _fail "run $i completed" "routine run exited non-zero"
    continue
  fi
  rec="$(cs routine records "$ROUTINE" --json --limit 1 2>/dev/null)"
  out="$(printf '%s' "$rec" | jq -r '.[0].output // empty')"
  cost="$(printf '%s' "$rec" | jq -r '.[0].cost_usd // 0')"
  dur="$(printf '%s' "$rec" | jq -r '.[0].duration_ms // 0')"
  outputs+=("$out"); costs+=("$cost"); durations+=("$dur")
done

# All outputs identical?
identical=1
first="${outputs[0]:-}"
for o in "${outputs[@]}"; do
  [[ "$o" == "$first" ]] || identical=0
done
if (( identical == 1 )) && [[ -n "$first" ]]; then
  _pass "$ROUTINE produced byte-identical output across all $RUNS runs"
  info "canonical output: $(printf '%s' "$first" | head -c 120)…"
else
  _fail "$ROUTINE deterministic across $RUNS runs" "outputs diverged — printing distinct variants:"
  printf '%s\n' "${outputs[@]}" | sort -u | sed 's/^/        ⟶ /' | head -n 10
fi

# Perf envelope (informational — not a pass/fail unless you wire targets).
if (( ${#durations[@]} > 0 )); then
  max_dur=$(printf '%s\n' "${durations[@]}" | sort -n | tail -1)
  med_dur=$(printf '%s\n' "${durations[@]}" | sort -n | awk '{a[NR]=$1} END{print a[int((NR+1)/2)]}')
  tot_cost=$(printf '%s\n' "${costs[@]}" | awk '{s+=$1} END{printf "%.5f", s}')
  section "Perf envelope (baseline)"
  info "latency  median=${med_dur}ms  max=${max_dur}ms   cost(total ${RUNS} runs)=\$${tot_cost}"
  # Optional hard gates — uncomment + set targets once SLAs are agreed:
  # (( max_dur <= ${MAX_LATENCY_MS:-8000} )) && _pass "p-max latency within ${MAX_LATENCY_MS:-8000}ms" \
  #   || _fail "p-max latency within budget" "max=${max_dur}ms"
fi

finish

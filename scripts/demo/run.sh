#!/usr/bin/env bash
# Demo use cases, one script each, run one at a time or all in order.
#
#   ./run.sh                       list the use cases and what each needs
#   ./run.sh memory-recall         run one (by slug or by number)
#   ./run.sh 3 5 pack-ci-watch     run several, in the order given
#   ./run.sh --all                 run every use case, keep going past a
#                                  failure, print one summary table
#   ./run.sh --all --needs none    only the ones that need no model and
#                                  no GitHub token (the token-zero half)
#
# Every use case is a self-contained uc-NN-<slug>.sh that sources lib.sh and
# answers with an exit code: 0 PASS, 1 FAIL, 3 SKIP (not runnable here, with
# the reason on its last line). Its header carries the metadata this runner
# lists:
#
#   # uc: <slug>
#   # title: <one line>
#   # needs: none | model | github | model,github
#   # minutes: <rough wall time>
#
# The environment is the harness's: CREWSHIP (binary), SERVER or
# CREWSHIP_SERVER or CREWSHIP_PROFILE (target), DEMO_UC_TIMEOUT (seconds per
# use case, default 1800).
#
# Every use case runs and EVERY result counts: the loop collects each exit
# status instead of ending with the last one — the same defect the harness's
# PR runner shipped with once (#1784) is not repeated here.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UC_TIMEOUT="${DEMO_UC_TIMEOUT:-1800}"

# ── Catalogue ───────────────────────────────────────────────────────────────
# Read from the scripts themselves so the list can never disagree with what
# would run. Sorted by file name: the NN prefix is the running order.
declare -a UC_FILES=() UC_SLUGS=() UC_TITLES=() UC_NEEDS=() UC_MINUTES=()
_header() { sed -nE "s/^# $2:[[:space:]]*(.*)$/\1/p" "$1" | head -1; }
for f in "$HERE"/uc-[0-9][0-9]-*.sh; do
  [[ -f "$f" ]] || continue
  UC_FILES+=("$f")
  UC_SLUGS+=("$(_header "$f" uc)")
  UC_TITLES+=("$(_header "$f" title)")
  UC_NEEDS+=("$(_header "$f" needs)")
  UC_MINUTES+=("$(_header "$f" minutes)")
done

if (( ${#UC_FILES[@]} == 0 )); then
  echo "run.sh: no uc-NN-*.sh in $HERE" >&2
  exit 2
fi

list() {
  printf '%-3s %-24s %-14s %-4s %s\n' '#' 'USE CASE' 'NEEDS' 'MIN' 'TITLE'
  local i
  for i in "${!UC_FILES[@]}"; do
    printf '%-3s %-24s %-14s %-4s %s\n' "$((i + 1))" "${UC_SLUGS[$i]}" "${UC_NEEDS[$i]}" "${UC_MINUTES[$i]}" "${UC_TITLES[$i]}"
  done
  printf '\nrun one:  %s <slug|#>      run all:  %s --all [--needs none|model|github]\n' "${0##*/}" "${0##*/}"
}

# resolve <slug-or-number> — index into the catalogue, or "" when unknown.
resolve() {
  local want="$1" i
  if [[ "$want" =~ ^[0-9]+$ ]]; then
    (( want >= 1 && want <= ${#UC_FILES[@]} )) && printf '%s' "$((want - 1))"
    return
  fi
  for i in "${!UC_SLUGS[@]}"; do
    [[ "${UC_SLUGS[$i]}" == "$want" ]] && { printf '%s' "$i"; return; }
  done
  # uc-05-approval-gate.sh or uc-05 also work — people copy file names.
  for i in "${!UC_FILES[@]}"; do
    case "${UC_FILES[$i]##*/}" in "$want"|"$want".sh|"$want"-*) printf '%s' "$i"; return ;; esac
  done
}

# needs_match <needs-of-uc> <filter> — the --needs filter: "none" keeps the
# use cases that need nothing; "model" / "github" keep the ones that need
# exactly that (a use case that needs both is kept by either).
needs_match() {
  local needs="$1" filter="$2"
  [[ -z "$filter" ]] && return 0
  case "$filter" in
    none) [[ "$needs" == "none" ]] ;;
    *)    [[ ",$needs," == *",$filter,"* ]] ;;
  esac
}

# ── Argument parsing ────────────────────────────────────────────────────────
ALL=0
NEEDS_FILTER=""
declare -a SELECTED=()
while (( $# > 0 )); do
  case "$1" in
    --all) ALL=1 ;;
    --needs) shift; NEEDS_FILTER="${1:-}" ;;
    --needs=*) NEEDS_FILTER="${1#--needs=}" ;;
    -h|--help) sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    --list) list; exit 0 ;;
    -*) echo "run.sh: unknown option $1" >&2; exit 2 ;;
    *)
      idx="$(resolve "$1")"
      if [[ -z "$idx" ]]; then
        echo "run.sh: unknown use case '$1'" >&2; echo >&2; list >&2; exit 2
      fi
      SELECTED+=("$idx") ;;
  esac
  shift
done

if (( ALL == 1 )); then
  SELECTED=()
  for i in "${!UC_FILES[@]}"; do
    needs_match "${UC_NEEDS[$i]}" "$NEEDS_FILTER" && SELECTED+=("$i")
  done
elif [[ -n "$NEEDS_FILTER" && ${#SELECTED[@]} -gt 0 ]]; then
  echo "run.sh: --needs only filters --all" >&2; exit 2
fi

if (( ${#SELECTED[@]} == 0 )); then
  list
  exit 0
fi

# ── Run ─────────────────────────────────────────────────────────────────────
declare -a R_SLUG=() R_RESULT=() R_SECS=() R_NOTE=()
overall=0
for idx in "${SELECTED[@]}"; do
  f="${UC_FILES[$idx]}"; slug="${UC_SLUGS[$idx]}"
  printf '\n\033[1m############ %s — %s ############\033[0m\n' "$slug" "${UC_TITLES[$idx]}"
  start=$(date +%s)
  # tee keeps the audience-facing output on the terminal and lets the runner
  # read the SKIP reason / summary line afterwards.
  log="$(mktemp -t cs-demo.XXXXXX)"
  timeout "$UC_TIMEOUT" bash "$f" 2>&1 | tee "$log"
  rc=${PIPESTATUS[0]}
  secs=$(( $(date +%s) - start ))
  note=""
  case "$rc" in
    0)
      # A suite that recorded nothing but skips did not demonstrate anything.
      if grep -qE 'passed: 0 .*failed: 0 .*skipped: [1-9]' "$log"; then
        result="SKIP"; note="every step skipped"
      else
        result="PASS"
      fi ;;
    3)   result="SKIP"; note="$(sed -nE 's/^[[:space:]]*SKIPPED: (.*)$/\1/p' "$log" | tail -1)" ;;
    124) result="FAIL"; note="timed out after ${UC_TIMEOUT}s"; overall=1 ;;
    *)   result="FAIL"; note="exit $rc: $(sed -n '/──────── summary ────────/,$p' "$log" | sed -nE 's/^[[:space:]]*- (.*)$/\1/p' | paste -sd ';' -)"; overall=1 ;;
  esac
  rm -f "$log"
  R_SLUG+=("$slug"); R_RESULT+=("$result"); R_SECS+=("$secs"); R_NOTE+=("$note")
done

printf '\n\033[1m################ DEMO SUMMARY ################\033[0m\n'
printf '%-24s %-6s %6s  %s\n' 'USE CASE' 'RESULT' 'TIME' 'NOTE'
for i in "${!R_SLUG[@]}"; do
  printf '%-24s %-6s %5ss  %s\n' "${R_SLUG[$i]}" "${R_RESULT[$i]}" "${R_SECS[$i]}" "${R_NOTE[$i]}"
done
printf '##############################################\n'
exit "$overall"

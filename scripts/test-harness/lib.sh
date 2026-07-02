#!/usr/bin/env bash
# shellcheck shell=bash
# Shared library for the Crewship CLI integration test harness.
#
# This is NOT a unit-test layer — it drives the *real* `crewship` CLI against a
# running dev server (per CLAUDE.md: all ops go through the local CLI, never a
# DB shell or hand-rolled curl). It validates RUNTIME behaviour that can't be
# unit-tested: agent memory recall, crew-shared memory, notifications landing
# after a routine run, recipe determinism, agent credential self-service.
#
# Source this from each test-*.sh. It provides:
#   - cs()              thin `crewship --server <SERVER>` wrapper
#   - ask_agent()       one-shot prompt → captured plain-text reply (fresh session)
#   - assert_*          assertion helpers that record pass/fail and KEEP GOING
#   - nonce()           a random token so recall can't be satisfied by chance
#   - finish()          print the summary and set the process exit code
#
# Deliberately NO `set -e`: a failed assertion must not abort the whole run.
set -uo pipefail

# ── Configuration (override via env) ────────────────────────────────────────
CREWSHIP="${CREWSHIP:-./crewship}"
# Resolve target server: explicit SERVER env wins, else CREWSHIP_SERVER (the
# per-clone dev target, e.g. dev1), else the local default.
SERVER="${SERVER:-${CREWSHIP_SERVER:-http://localhost:8081}}"
ASK_TIMEOUT="${ASK_TIMEOUT:-180}"   # seconds to wait for a single agent reply
POLL_TIMEOUT="${POLL_TIMEOUT:-120}" # seconds to wait for an async signal (notif, run)
POLL_INTERVAL="${POLL_INTERVAL:-3}" # seconds between polls

# ── Output / counters ───────────────────────────────────────────────────────
_PASS=0
_FAIL=0
_SKIP=0
declare -a _FAILED_NAMES=()

if [[ -t 1 ]]; then
  _C_GREEN=$'\033[32m'; _C_RED=$'\033[31m'; _C_YEL=$'\033[33m'
  _C_DIM=$'\033[2m'; _C_BOLD=$'\033[1m'; _C_OFF=$'\033[0m'
else
  _C_GREEN=""; _C_RED=""; _C_YEL=""; _C_DIM=""; _C_BOLD=""; _C_OFF=""
fi

section() { printf '\n%s== %s ==%s\n' "$_C_BOLD" "$*" "$_C_OFF"; }
info()    { printf '%s   %s%s\n' "$_C_DIM" "$*" "$_C_OFF"; }

_pass() { _PASS=$((_PASS+1)); printf '%s  ✓ PASS%s %s\n' "$_C_GREEN" "$_C_OFF" "$1"; }
_fail() { _FAIL=$((_FAIL+1)); _FAILED_NAMES+=("$1"); printf '%s  ✗ FAIL%s %s\n' "$_C_RED" "$_C_OFF" "$1";
          [[ -n "${2:-}" ]] && printf '%s        %s%s\n' "$_C_DIM" "$2" "$_C_OFF"; }
skip()  { _SKIP=$((_SKIP+1)); printf '%s  ⊘ SKIP%s %s%s\n' "$_C_YEL" "$_C_OFF" "$1" \
            "${2:+ ($2)}"; }

# ── CLI wrappers ────────────────────────────────────────────────────────────
cs() { "$CREWSHIP" --server "$SERVER" "$@"; }

# nonce <prefix> — emit a hard-to-guess token, e.g. FALCON-7F3A9C.
# Uppercased so models echo it back verbatim more reliably.
nonce() {
  local p="${1:-N}" rand
  rand=$(LC_ALL=C tr -dc 'A-Z0-9' </dev/urandom 2>/dev/null | head -c 6 || true)
  [[ -z "$rand" ]] && rand=$(date +%s | tail -c 6)  # fallback
  printf '%s-%s' "$p" "$rand"
}

# ask_agent <agent-slug> <prompt> — run a ONE-SHOT prompt (fresh session, no
# carried history) and echo the agent's plain-text reply on stdout. Empty
# string on transport failure (callers assert on content).
ask_agent() {
  local agent="$1" prompt="$2" out rc
  out="$(mktemp -t cs-reply.XXXXXX)"
  cs ask --agent "$agent" --quiet --no-stream --timeout "$ASK_TIMEOUT" \
        -p "$prompt" --save "$out" >/dev/null 2>&1
  rc=$?
  if [[ $rc -ne 0 && ! -s "$out" ]]; then
    rm -f "$out"; return 1
  fi
  cat "$out"
  rm -f "$out"
}

# ── Assertions (never abort; record pass/fail) ──────────────────────────────

# assert_contains <name> <haystack> <needle> — case-insensitive substring.
assert_contains() {
  local name="$1" hay="$2" needle="$3"
  if printf '%s' "$hay" | grep -qiF -- "$needle"; then
    _pass "$name"
  else
    _fail "$name" "expected to find «${needle}» in reply: $(printf '%s' "$hay" | head -c 160 | tr '\n' ' ')…"
  fi
}

# assert_not_contains <name> <haystack> <needle> — case-insensitive absence.
assert_not_contains() {
  local name="$1" hay="$2" needle="$3"
  if printf '%s' "$hay" | grep -qiF -- "$needle"; then
    _fail "$name" "did NOT expect «${needle}» but found it: $(printf '%s' "$hay" | head -c 160 | tr '\n' ' ')…"
  else
    _pass "$name"
  fi
}

# assert_eq <name> <expected> <actual>
assert_eq() {
  local name="$1" exp="$2" act="$3"
  if [[ "$exp" == "$act" ]]; then _pass "$name"; else _fail "$name" "expected «${exp}», got «${act}»"; fi
}

# assert_nonempty <name> <value>
assert_nonempty() {
  local name="$1" v="$2"
  if [[ -n "${v// /}" ]]; then _pass "$name"; else _fail "$name" "value was empty"; fi
}

# require_cmd <bin> — skip-guard for optional external tools (jq, gh).
have() { command -v "$1" >/dev/null 2>&1; }

# poll_until <name> <timeout-s> <cmd...> — run <cmd> every POLL_INTERVAL until
# it returns 0 or timeout. Records pass/fail. <cmd> is eval'd.
poll_until() {
  local name="$1" timeout="$2"; shift 2
  local cmd="$*" waited=0
  while (( waited < timeout )); do
    if eval "$cmd" >/dev/null 2>&1; then _pass "$name"; return 0; fi
    sleep "$POLL_INTERVAL"; waited=$((waited+POLL_INTERVAL))
  done
  _fail "$name" "condition not met within ${timeout}s: $cmd"
  return 1
}

# preflight — confirm the CLI is reachable and the workspace is seeded.
preflight() {
  section "Preflight: CLI ↔ $SERVER"
  if ! cs whoami >/dev/null 2>&1; then
    printf '%s  ✗ cannot reach %s as an authenticated user.%s\n' "$_C_RED" "$SERVER" "$_C_OFF"
    printf '   Run from a clone shell with CREWSHIP_SERVER set and `crewship login` done,\n'
    printf '   and make sure the workspace is seeded:\n'
    printf '     crewship seed --nuke --with-memory --with-users --wait-provision\n'
    exit 2
  fi
  info "whoami OK · agents: $(cs agent list 2>/dev/null | grep -c . || echo '?') · routines: $(cs routine list 2>/dev/null | grep -c . || echo '?')"
  if ! have jq; then info "jq not found — JSON assertions will fall back to grep (less precise)"; fi
}

# finish — print summary, set exit code (0 = all green / only skips).
finish() {
  printf '\n%s──────── summary ────────%s\n' "$_C_BOLD" "$_C_OFF"
  printf '  %spassed: %d%s   %sfailed: %d%s   %sskipped: %d%s\n' \
    "$_C_GREEN" "$_PASS" "$_C_OFF" "$_C_RED" "$_FAIL" "$_C_OFF" "$_C_YEL" "$_SKIP" "$_C_OFF"
  if (( _FAIL > 0 )); then
    printf '  %sfailures:%s\n' "$_C_RED" "$_C_OFF"
    printf '    - %s\n' "${_FAILED_NAMES[@]}"
    exit 1
  fi
  exit 0
}

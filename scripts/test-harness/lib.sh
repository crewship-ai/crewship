#!/usr/bin/env bash
# shellcheck shell=bash
# Shared library for the Crewship CLI integration test harness.
#
# This is NOT a unit-test layer — it drives the *real* `crewship` CLI against a
# running dev server (per CLAUDE.md: all ops go through the local CLI, never a
# DB shell or hand-rolled curl). It validates RUNTIME behaviour that can't be
# unit-tested: agent memory recall, crew-shared memory, a routine run's outcome
# being observable afterwards, recipe determinism, agent credential self-service.
#
# Source this from each test-*.sh. It provides:
#   - cs()              thin `crewship --server <SERVER>` wrapper
#   - ask_agent()       one-shot prompt → captured plain-text reply (fresh session)
#   - assert_*          assertion helpers that record pass/fail and KEEP GOING
#   - assert_ok()       run a command; record pass/fail on its exit status
#   - nonce()           a random token so recall can't be satisfied by chance
#   - finish()          print the summary and set the process exit code
#
# Deliberately NO `set -e`: a failed assertion must not abort the whole run.
set -uo pipefail

# ── Configuration (override via env) ────────────────────────────────────────
_HARNESS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Resolve the CLI binary. This used to default to the RELATIVE `./crewship`,
# which only resolves when a suite is launched from the repo root — while the
# README (correctly) says to run them from this directory. The miss surfaced as
# an auth failure telling the operator to `crewship seed --nuke`, i.e. a path
# bug that asked you to wipe the workspace (#1297). Anchor on the repo root
# instead, then fall back to whatever `crewship` the operator has on PATH.
_resolve_crewship() {
  local root
  root="$(git -C "$_HARNESS_DIR" rev-parse --show-toplevel 2>/dev/null)" || root=""
  # No git, or not a checkout (tarball, vendored copy): the harness still lives
  # two levels below the root, so derive it from our own location.
  [[ -z "$root" ]] && root="$(cd "$_HARNESS_DIR/../.." && pwd)"

  if [[ -x "$root/crewship" ]]; then
    printf '%s\n' "$root/crewship"
    return
  fi
  # Nothing built in the clone — an installed CLI is the next best thing. If
  # there is none either, still name the repo-root path: preflight reports it
  # and that is the path the operator is expected to build.
  command -v crewship 2>/dev/null || printf '%s\n' "$root/crewship"
}
CREWSHIP="${CREWSHIP:-$(_resolve_crewship)}"
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
_XFAIL=0
declare -a _FAILED_NAMES=()
declare -a _XFAIL_NAMES=()

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

# xfail <name> <reason> — an EXPECTED failure: a known-broken behaviour we
# deliberately do NOT let turn CI hard-red (usually an environment/build gap,
# not a code regression), but which must stay LOUD and visible instead of
# hiding inside the skip bucket. Printed bold-yellow with an explicit XFAIL
# label + reason; counted separately and re-listed in the summary. Reserve for
# cases tracked by an issue — put the issue ref in the reason.
xfail() { _XFAIL=$((_XFAIL+1)); _XFAIL_NAMES+=("$1"); \
          printf '%s%s  ✗ XFAIL (known broken)%s %s%s\n' "$_C_BOLD" "$_C_YEL" "$_C_OFF" "$1" \
            "${2:+ ($2)}"; }

# ── CLI wrappers ────────────────────────────────────────────────────────────
# The CLI refuses to send a token to a host the token was not issued for, so
# pointing SERVER at a slot whose credential lives under a DIFFERENT profile
# name fails with a mismatch error rather than a 401. Profile names do not have
# to match slot names (a `dev3` profile can point at dev1), so name the profile
# explicitly via CREWSHIP_PROFILE. CREWSHIP_WORKSPACE overrides a workspace id
# gone stale in that profile after a reseed. Both default to unset, in which
# case this is exactly the plain `--server` wrapper it has always been.
# ${arr[@]+"${arr[@]}"} — bash 3.2 (macOS) errors on an empty array under set -u.
_CS_ARGS=()
[[ -n "${CREWSHIP_PROFILE:-}" ]]   && _CS_ARGS+=(--profile "$CREWSHIP_PROFILE")
[[ -n "${CREWSHIP_WORKSPACE:-}" ]] && _CS_ARGS+=(--workspace "$CREWSHIP_WORKSPACE")
cs() { "$CREWSHIP" --server "$SERVER" ${_CS_ARGS[@]+"${_CS_ARGS[@]}"} "$@"; }

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
# string on transport failure with no saved reply (callers assert on content).
# Exit status is the captured `cs ask` status: a failed ask returns non-zero
# EVEN when a partial reply was saved, so callers that care about the run's
# outcome must check $?, not just whether stdout is nonempty.
ask_agent() {
  local agent="$1" prompt="$2" out rc
  track_agent "$agent"
  out="$(mktemp -t cs-reply.XXXXXX)"
  cs ask --agent "$agent" --quiet --no-stream --timeout "$ASK_TIMEOUT" \
        -p "$prompt" --save "$out" >/dev/null 2>&1
  rc=$?
  if [[ $rc -ne 0 && ! -s "$out" ]]; then
    rm -f "$out"; return 1
  fi
  cat "$out"
  rm -f "$out"
  return "$rc"
}

# ── Grounded work: a crew record the agent works from (#4873) ───────────────
#
# A bare chat prompt carrying a harness nonce reads to a careful agent like an
# unverified relay or memory-poisoning attempt, and the seeded personas decline
# it before the API under test is ever called (stage 70a74af, 2026-11-03). That
# refusal says nothing about the feature. A positive scenario therefore starts
# from what the product documents as legitimate work: an issue in the crew,
# delegated to the agent and started with `crewship issue start`
# (docs/cli/issue.mdx). The issue is a durable, traceable record of who asked
# for what; the run it starts is attributed to it (`crewship issue runs`).
# Negative scenarios keep the bare prompt on purpose — see each suite.
#
# Which run is the agent's own: a non-lead delegate gets a mission task
# (source=task). A LEAD delegate gets no task — `issue start` triggers lead
# planning (internal/api/issue_handler_workflow.go), dispatched with
# LeadPlanning=true so it runs with LEAD privileges and the crew sidecar
# (internal/orchestrator/mission_tasks_planning.go). Only that run can write
# CREW.md or /assign across a link (a task run is role AGENT,
# internal/api/assignments_run.go), and `issue runs` labels it
# source=delegation. So the run that counts is the one whose agent_slug is the
# delegate, with source task or delegation.

# State that must survive command substitution. Suites call these helpers as
# `x="$(issue_work …)"`, which runs them in a subshell, so an array appended
# there is lost (stage run 20261103T231949Z kept no issue evidence). Every
# tracked agent and issue is appended to a file in a private directory. Only a
# suite that keeps evidence needs it, so keep_failure_evidence creates it, in
# the suite's own shell, and only when HARNESS_DETAIL_DIR is set; finish and
# the preflight exits remove it. Only a directory this shell created is used
# or removed: a value inherited from the environment is dropped, never deleted
# (a subshell keeps $$ of its parent, so command substitutions still match).
[[ "${_HARNESS_STATE_OWNER:-}" == "$$" ]] || _HARNESS_STATE=""
_state_init() {
  [[ -n "$_HARNESS_STATE" && -d "$_HARNESS_STATE" ]] && return 0
  _HARNESS_STATE="$(umask 077; mktemp -d "${TMPDIR:-/tmp}/harness-state.XXXXXX" 2>/dev/null)" && _HARNESS_STATE_OWNER=$$ || {
    _HARNESS_STATE=""
    info "cannot create a harness state directory under ${TMPDIR:-/tmp}; failure evidence will not list issues or agents"
  }
  return 0
}
_state_cleanup() {
  [[ -n "$_HARNESS_STATE" && "${_HARNESS_STATE_OWNER:-}" == "$$" ]] && rm -rf "$_HARNESS_STATE"
  _HARNESS_STATE=""; return 0
}
_state_add() { # <file> <value> — append once
  [[ -n "$_HARNESS_STATE" && -n "${2:-}" ]] || return 0
  grep -qxF -- "$2" "$_HARNESS_STATE/$1" 2>/dev/null || printf '%s\n' "$2" >>"$_HARNESS_STATE/$1"
}
_state_list() { [[ -n "$_HARNESS_STATE" ]] && cat "$_HARNESS_STATE/$1" 2>/dev/null; return 0; }

# issue_dispatch <crew> <agent> <title> <description> — create a TODO issue
# delegated to <agent> and start it. Prints the identifier; returns 1 when the
# issue could not be created or started (prints "<ident> START_FAILED").
issue_dispatch() {
  local crew="$1" agent="$2" title="$3" desc="$4" created ident
  track_agent "$agent"
  created="$(cs issue create --crew "$crew" --delegate "$agent" --status TODO \
    --title "$title" --description "$desc" --format json 2>/dev/null)" || return 1
  ident="$(printf '%s' "$created" | jq -r '.identifier // .id // empty' 2>/dev/null)"
  [[ -n "$ident" ]] || return 1
  _state_add issues "$ident"
  cs issue start "$ident" >/dev/null 2>&1 || { printf '%s START_FAILED\n' "$ident"; return 1; }
  printf '%s\n' "$ident"
}

# issue_run_status <ident> <agent> — the terminal status of <agent>'s own run on
# the issue (task or lead-planning delegation), empty while it is not over.
issue_run_status() {
  cs issue runs "$1" --format json 2>/dev/null | jq -r --arg a "$2" '
    [ .[] | select(.agent_slug == $a and ((.source // "task") == "task" or .source == "delegation")) ]
    | map(select(.status == "COMPLETED" or .status == "FAILED" or .status == "CANCELLED"))
    | (.[0].status // empty)' 2>/dev/null
}

# issue_work <crew> <agent> <title> <description> [timeout-s] — dispatch, then
# wait for the delegate's own run to end. Prints "<ident> <STATUS>"; returns 0
# when it ended, 1 when the issue could not be created or started, 2 on timeout.
issue_work() {
  local agent="$2" timeout="${5:-$ASK_TIMEOUT}" ident waited=0 status
  ident="$(issue_dispatch "$1" "$2" "$3" "$4")" || { printf '%s\n' "$ident"; return 1; }
  while (( waited < timeout )); do
    status="$(issue_run_status "$ident" "$agent")"
    if [[ -n "$status" ]]; then printf '%s %s\n' "$ident" "$status"; return 0; fi
    sleep "$POLL_INTERVAL"; waited=$((waited+POLL_INTERVAL))
  done
  printf '%s TIMEOUT\n' "$ident"
  return 2
}

# ── Evidence that survives the reseed (#4874) ───────────────────────────────
#
# The stage gauntlet reseeds right after e2e, which deletes every chat the
# suites drove: a failing scenario kept only its verdict and lost the agent's
# side of the story. When HARNESS_DETAIL_DIR is set (the e2e scan creates it
# 0700 inside the private run directory, which the report retention prunes),
# a suite that FAILS writes the transcripts of the agents it drove — chats
# started since the suite began — plus the issues it opened and their runs.
# Files are created 0600 under umask 077. Values registered with
# register_secret are replaced before anything is written; agent output is
# already scrubbed by the server. Nothing is written when the suite passes or
# the variable is unset.

_HARNESS_SUITE="$(basename "${BASH_SOURCE[1]:-$0}" .sh)"
_HARNESS_T0="$(date -u +%Y-%m-%dT%H:%M:%S)"  # no zone suffix: compares below any fractional/Z form of the same second

declare -a _HARNESS_SECRETS=()

track_agent() { _state_add agents "$1"; }

# register_secret <value> — never let <value> reach an evidence file.
register_secret() { [[ -n "${1:-}" ]] && _HARNESS_SECRETS+=("$1"); return 0; }

_redact() {
  local i=0 v
  if (( ${#_HARNESS_SECRETS[@]} == 0 )); then cat; return; fi
  for v in "${_HARNESS_SECRETS[@]}"; do i=$((i+1)); export "_HARNESS_SECRET_$i=$v"; done
  # One left-to-right pass: at the earliest match the longest value wins, and
  # the scan resumes after it, so a value that is a prefix of another, or a
  # substring of "[REDACTED]", can neither leak a tail nor loop.
  awk -v n="$i" 'BEGIN { for (k = 1; k <= n; k++) s[k] = ENVIRON["_HARNESS_SECRET_" k] }
    { line = $0; out = ""
      while (line != "") {
        best = 0; bp = 0
        for (k = 1; k <= n; k++) if (s[k] != "") {
          p = index(line, s[k])
          if (p > 0 && (bp == 0 || p < bp || (p == bp && length(s[k]) > length(s[best])))) { bp = p; best = k }
        }
        if (!best) { out = out line; break }
        out = out substr(line, 1, bp - 1) "[REDACTED]"; line = substr(line, bp + length(s[best]))
      }
      print out }'
  for ((v = 1; v <= i; v++)); do unset "_HARNESS_SECRET_$v"; done
}

# keep_failure_evidence — opt a suite in to preserve_evidence. Opt-in, not a
# default: suites that handle real tokens (test-secretless-github.sh,
# test-realworld-github.sh) keep them in their own arrays, and a failing run of
# exactly those suites is when a token may sit in a transcript.
_HARNESS_KEEP_EVIDENCE=0
keep_failure_evidence() { _HARNESS_KEEP_EVIDENCE=1; [[ -z "${HARNESS_DETAIL_DIR:-}" ]] || _state_init; }

preserve_evidence() {
  local root="${HARNESS_DETAIL_DIR:-}" dest a list id ident limit="${HARNESS_DETAIL_CHATS:-12}"
  (( _HARNESS_KEEP_EVIDENCE == 1 )) || return 0
  [[ -n "$root" && -d "$root" ]] || return 0
  dest="$root/$_HARNESS_SUITE"
  (
    umask 077
    mkdir -p "$dest" || exit 0
    {
      printf 'suite: %s\nstarted: %s\nserver: %s\nfailed:\n' "$_HARNESS_SUITE" "$_HARNESS_T0" "$SERVER"
      printf '  - %s\n' ${_FAILED_NAMES[@]+"${_FAILED_NAMES[@]}"}
    } | _redact >"$dest/failures.txt"
    have jq || exit 0
    for a in $(_state_list agents); do
      [[ "$a" =~ ^[A-Za-z0-9_-]+$ ]] || continue
      list="$(cs chat list "$a" --limit "$limit" --format json 2>/dev/null)" || continue
      printf '%s\n' "$list" | _redact >"$dest/chats-$a.json"
      for id in $(printf '%s' "$list" | jq -r --arg t0 "$_HARNESS_T0" \
          '.[] | select((.created_at // "" | sub(" "; "T")) >= $t0) | .id' 2>/dev/null); do
        [[ "$id" =~ ^[A-Za-z0-9_-]+$ ]] || continue
        cs chat "$id" --parts --no-markdown 2>&1 | _redact >"$dest/chat-$a-$id.txt"
      done
    done
    for ident in $(_state_list issues); do
      [[ "$ident" =~ ^[A-Za-z0-9_-]+$ ]] || continue
      { cs issue get "$ident" --format json; cs issue runs "$ident" --format json
        cs issue sessions "$ident" --format json; } 2>&1 \
        | _redact >"$dest/issue-$ident.json"
      # An issue run's transcript is not a chat (its chat shows "No
      # messages"); the run's own record is its journal.
      for id in $(cs issue runs "$ident" --format json 2>/dev/null | jq -r '.[] | (.run_id // .trace_id // empty)' 2>/dev/null); do
        [[ "$id" =~ ^[A-Za-z0-9_-]+$ ]] || continue
        cs journal --run-id "$id" --lines 500 --exclude-type container.metrics --format json 2>&1 | _redact >"$dest/issue-$ident-run-$id.journal.json"
      done
    done
  )
  info "evidence for the failed checks: $dest"
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

# assert_ok <name> <cmd...> — run the command, record pass/fail on its EXIT
# STATUS, and keep going. Output is captured and only shown on failure, so a
# passing run stays readable.
#
# The gap this fills: a suite that runs `cs foo >/dev/null` without checking
# `$?` discards the result entirely, and because lib.sh deliberately has no
# `set -e`, a documented command can fail while later assertions still pass off
# pre-existing state (#1771 review).
assert_ok() {
  local name="$1"; shift
  local out status
  out="$("$@" 2>&1)"; status=$?
  if (( status == 0 )); then
    _pass "$name"
  else
    _fail "$name" "exit $status: $(printf '%s' "$out" | tail -c 200 | tr '\n' ' ')"
  fi
  return 0
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

# agent_has_no_open_work <agent-slug> — 0 when the agent holds no open work of
# any capacity class. Issue runs are background work, not chat, so a lead still
# running an issue (or a run parked on a credential, which waits up to 5
# minutes per docs/guides/credentials.mdx) blocks its next turn although it
# holds no open chat.
agent_has_no_open_work() {
  cs work list --open --agent "$1" --format json 2>/dev/null \
    | jq -e '(.items // []) | length == 0' >/dev/null
}

# wait_agent_free <agent-slug> [timeout-s] — wait until the agent holds no open
# work of any class (chat included); fails the check after the timeout.
wait_agent_free() {
  local agent="$1" timeout="${2:-360}" waited=0
  have jq || return 0
  while (( waited < timeout )); do
    agent_has_no_open_work "$agent" && return 0
    sleep "$POLL_INTERVAL"; waited=$((waited+POLL_INTERVAL))
  done
  _fail "$agent frees its previous work" "still open after ${timeout}s: $(cs work list --open --agent "$agent" --format json 2>/dev/null | jq -c '[(.items // [])[] | {id, class, state, state_reason}]' 2>/dev/null | head -c 300)"
  return 1
}

# preflight — confirm the CLI is reachable and the workspace is seeded.
#
# The two failure modes are reported separately on purpose: "no binary" is a
# build/path problem and must never send the operator to a destructive reseed.
preflight() {
  section "Preflight: CLI ↔ $SERVER"
  if ! "$CREWSHIP" version >/dev/null 2>&1; then
    printf '%s  ✗ crewship binary not found at %s%s\n' "$_C_RED" "$CREWSHIP" "$_C_OFF"
    printf '   Build it from the repo root:\n'
    printf '     go build -o crewship ./cmd/crewship\n'
    printf '   or install the CLI on PATH, or point the harness at one:\n'
    printf '     CREWSHIP=/path/to/crewship %s\n' "$(basename "${BASH_SOURCE[1]:-run-all.sh}")"
    _state_cleanup
    exit 2
  fi
  if ! cs whoami >/dev/null 2>&1; then
    printf '%s  ✗ cannot reach %s as an authenticated user.%s\n' "$_C_RED" "$SERVER" "$_C_OFF"
    printf '   Run from a clone shell with CREWSHIP_SERVER set and a non-interactive login done:\n'
    printf '     crewship login --email <email> --password-stdin <<< "$PASSWORD"\n'
    printf '   and make sure the workspace is seeded:\n'
    printf '     crewship seed --nuke --with-memory --with-users --wait-provision\n'
    _state_cleanup
    exit 2
  fi
  info "whoami OK · agents: $(cs agent list 2>/dev/null | grep -c . || echo '?') · routines: $(cs routine list 2>/dev/null | grep -c . || echo '?')"
  if ! have jq; then info "jq not found — JSON assertions will fall back to grep (less precise)"; fi
}

# finish — print summary, set exit code (0 = all green / only skips).
finish() {
  printf '\n%s──────── summary ────────%s\n' "$_C_BOLD" "$_C_OFF"
  printf '  %spassed: %d%s   %sfailed: %d%s   %sskipped: %d%s   %sxfail: %d%s\n' \
    "$_C_GREEN" "$_PASS" "$_C_OFF" "$_C_RED" "$_FAIL" "$_C_OFF" \
    "$_C_YEL" "$_SKIP" "$_C_OFF" "$_C_BOLD$_C_YEL" "$_XFAIL" "$_C_OFF"
  if (( _XFAIL > 0 )); then
    printf '  %sknown-broken (xfail):%s\n' "$_C_BOLD$_C_YEL" "$_C_OFF"
    printf '    - %s\n' "${_XFAIL_NAMES[@]}"
  fi
  if (( _FAIL > 0 )); then
    printf '  %sfailures:%s\n' "$_C_RED" "$_C_OFF"
    printf '    - %s\n' "${_FAILED_NAMES[@]}"
    preserve_evidence
    _state_cleanup
    exit 1
  fi
  _state_cleanup
  exit 0
}

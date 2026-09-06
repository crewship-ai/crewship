#!/usr/bin/env bash
# shellcheck shell=bash
# Demo use-case library — the thin layer on top of the CLI harness that turns
# a suite into a DEMO: narrated steps, the command output shown to the
# audience rather than hidden, a "look here in the UI" line after each
# observable effect, and a whole-use-case SKIP when the environment lacks
# what the use case needs (a model, a GitHub token).
#
# Every uc-*.sh sources this file. It provides, on top of test-harness/lib.sh
# (cs, ask_agent, assert_*, poll_until, nonce, preflight, finish):
#
#   demo_step <title>              a numbered, bold section header
#   demo_say <text>                one line of narration
#   demo_show <name> <cmd...>      run the command, SHOW its output, record
#                                  pass/fail on its exit status, keep going
#   demo_ui <what> <path>          where to look in the web UI right now
#   demo_need model|github         0 when the need is met; otherwise a reason
#                                  on stdout and 1 — callers demo_skip_all
#   demo_skip_all <reason>         the whole use case is not runnable here:
#                                  print why, run cleanups, exit 3 (= SKIP)
#   demo_cleanup <cmd...>          register a command to run on exit, always
#   run_routine <slug> [args...]   `routine run --wait`; sets RUN_ID/RUN_STATUS
#   run_status <slug> <run_id>     terminal status of one run from the journal
#
# Exit codes are the contract run.sh reads: 0 PASS, 1 FAIL, 3 SKIP.
#
# No `set -e`, same as the harness: a failed step is a red line in the
# summary, not an abort — the audience still sees the steps after it.
set -uo pipefail

_DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../test-harness/lib.sh
source "$_DEMO_DIR/../test-harness/lib.sh"

# ── Configuration (override via env) ────────────────────────────────────────
# The web UI the audience has open. Derived from SERVER unless told otherwise
# (a dev slot behind Caddy serves the UI on a different host than the API).
DEMO_WEB_URL="${DEMO_WEB_URL:-${SERVER%/}}"
# `routine run --wait` bound per use case. Agent steps take minutes; the
# report routines of the packs can take ten.
RUN_WAIT="${RUN_WAIT:-15m}"

# ── Narration ───────────────────────────────────────────────────────────────
_DEMO_STEP=0
demo_step() { _DEMO_STEP=$((_DEMO_STEP + 1)); section "$_DEMO_STEP. $*"; }
demo_say()  { printf '%s   %s%s\n' "$_C_DIM" "$*" "$_C_OFF"; }
demo_ui()   { printf '%s   👀 %s: %s%s%s\n' "$_C_BOLD" "$1" "$DEMO_WEB_URL" "$2" "$_C_OFF"; }

# demo_show <name> <cmd...> — the audience-facing counterpart of assert_ok:
# the command's output is printed (indented), and the exit status is recorded
# as PASS/FAIL under <name>. Output is also kept in DEMO_OUT for assertions.
DEMO_OUT=""
demo_show() {
  local name="$1"; shift
  local status
  printf '%s   $ %s%s\n' "$_C_DIM" "$*" "$_C_OFF"
  DEMO_OUT="$("$@" 2>&1)"; status=$?
  if [[ -n "$DEMO_OUT" ]]; then
    printf '%s\n' "$DEMO_OUT" | sed 's/^/     /'
  fi
  if (( status == 0 )); then
    _pass "$name"
  else
    _fail "$name" "exit $status"
  fi
  return 0
}

# ── Cleanup ─────────────────────────────────────────────────────────────────
declare -a _DEMO_CLEANUPS=()
demo_cleanup() { _DEMO_CLEANUPS+=("$*"); }
_demo_run_cleanups() {
  local c
  for c in ${_DEMO_CLEANUPS[@]+"${_DEMO_CLEANUPS[@]}"}; do
    eval "$c" >/dev/null 2>&1 || true
  done
}
trap _demo_run_cleanups EXIT

# ── Needs ───────────────────────────────────────────────────────────────────
# provider_state — "active", "none", "inactive:<names and statuses>", or ""
# when it cannot be read. Same probe as test-run-stream.sh: a credential of a
# model provider that is ACTIVE. An EXPIRED key is a different shape from a
# missing one and must be named, not reported as "no provider".
PROVIDER_RE='ANTHROPIC|OPENAI|GOOGLE|GEMINI|AZURE|OPENROUTER|MISTRAL|OLLAMA|BEDROCK|VERTEX'
provider_state() {
  have jq || { printf ''; return; }
  cs credential list --format json 2>/dev/null \
    | jq -r --arg re "$PROVIDER_RE" \
        '[.[]? | select((.provider // "") | test($re))] as $p
         | if ($p | length) == 0 then "none"
           elif ([$p[] | select((.status // "") == "ACTIVE")] | length) > 0 then "active"
           else "inactive:" + ([$p[] | "\(.name // .id) is \(.status // "?")"] | join(", "))
           end' 2>/dev/null
}

# github_state — "active" when a GitHub token an agent could use exists:
# SEED_GITHUB_TOKEN in this shell, or an ACTIVE GITHUB credential that at
# least one agent has, or a crew-scoped pack credential (github-<pack>).
github_state() {
  [[ -n "${SEED_GITHUB_TOKEN:-}" ]] && { printf 'active'; return; }
  have jq || { printf ''; return; }
  cs credential list --format json 2>/dev/null \
    | jq -r '[.[]? | select((.provider // "") == "GITHUB" and (.status // "") == "ACTIVE"
                            and (((._count_agent_credentials // 0) > 0)
                                 or ((.name // "") | startswith("github-ci-watch"))
                                 or ((.name // "") | startswith("github-docs-drift"))))]
             | if length > 0 then "active" else "none" end' 2>/dev/null
}

# demo_need <model|github> — 0 when met. Otherwise the reason is printed on
# stdout and 1 is returned, so the caller can `demo_skip_all "$(demo_need model)"`
# or branch. Unknown state ("" — no jq) counts as met: better a real attempt
# that reports what happened than a skip on a guess.
demo_need() {
  local state
  case "$1" in
    model)
      state="$(provider_state)"
      case "$state" in
        none)       printf 'no model-provider credential in this workspace'; return 1 ;;
        inactive:*) printf 'no ACTIVE model-provider credential: %s' "${state#inactive:}"; return 1 ;;
      esac ;;
    github)
      state="$(github_state)"
      case "$state" in
        none) printf 'no GitHub token: set SEED_GITHUB_TOKEN and re-run `crewship seed`, or bind a GITHUB credential'; return 1 ;;
      esac ;;
    *) printf 'unknown need %s' "$1"; return 1 ;;
  esac
  return 0
}

# demo_skip_all <reason> — the use case cannot run here. Exit 3 so run.sh
# shows SKIP, not PASS: a green row for something that never ran is exactly
# the lie these scripts exist to avoid.
demo_skip_all() {
  skip "use case not runnable here" "$1"
  printf '\n%s──────── summary ────────%s\n  %sSKIPPED%s: %s\n' "$_C_BOLD" "$_C_OFF" "$_C_YEL" "$_C_OFF" "$1"
  exit 3
}

# ── Routines ────────────────────────────────────────────────────────────────
# shellcheck disable=SC2034  # set here, read by the use cases
RUN_ID=""
RUN_STATUS=""

# run_routine <slug> [routine run flags...] — invoke and wait. The receipt's
# first line is `Run <id>: <STATUS> (…)`; both are parsed into RUN_ID and
# RUN_STATUS. The whole output is shown to the audience. Returns the CLI's
# exit status.
run_routine() {
  local slug="$1"; shift
  local out status
  printf '%s   $ crewship routine run %s --wait %s%s\n' "$_C_DIM" "$slug" "$*" "$_C_OFF"
  out="$(cs routine run "$slug" --wait --wait-timeout "$RUN_WAIT" "$@" 2>&1)"; status=$?
  printf '%s\n' "$out" | sed 's/^/     /'
  # shellcheck disable=SC2034
  RUN_ID="$(printf '%s\n' "$out" | sed -nE 's/^Run (run_[A-Za-z0-9]+): .*/\1/p' | head -1)"
  # shellcheck disable=SC2034
  RUN_STATUS="$(printf '%s\n' "$out" | sed -nE 's/^Run run_[A-Za-z0-9]+: ([A-Z_]+).*/\1/p' | head -1)"
  DEMO_OUT="$out"
  return $status
}

# run_receipt <slug> [flags...] — invoke WITHOUT waiting (a run that will park
# on a human), parse the receipt the same way.
run_receipt() {
  local slug="$1"; shift
  local out status
  printf '%s   $ crewship routine run %s %s%s\n' "$_C_DIM" "$slug" "$*" "$_C_OFF"
  out="$(cs routine run "$slug" "$@" 2>&1)"; status=$?
  printf '%s\n' "$out" | sed 's/^/     /'
  # shellcheck disable=SC2034
  RUN_ID="$(printf '%s\n' "$out" | sed -nE 's/^Run (run_[A-Za-z0-9]+): .*/\1/p' | head -1)"
  # shellcheck disable=SC2034
  RUN_STATUS="$(printf '%s\n' "$out" | sed -nE 's/^Run run_[A-Za-z0-9]+: ([A-Z_]+).*/\1/p' | head -1)"
  DEMO_OUT="$out"
  return $status
}

# run_status <slug> <run_id> — "completed" / "failed" / "cancelled" from the
# routine's journal, or "" while the run is still going. Needs jq.
run_status() {
  local slug="$1" id="$2"
  have jq || { printf ''; return; }
  cs routine runs "$slug" --limit 100 --format json 2>/dev/null \
    | jq -r --arg id "$id" \
        '[.[]? | select(.run_id == $id and ((.entry_type // "") | test("pipeline\\.run\\.(completed|failed|cancelled)")))]
         | first | (.entry_type // "") | sub("pipeline\\.run\\."; "")' 2>/dev/null
}

# inbox_has <needle> — an inbox item (any state) whose id, source or title
# contains the needle. Case-insensitive; needs no jq.
inbox_has() {
  cs inbox list --state all --limit 200 --format json 2>/dev/null | grep -qiF -- "$1"
}

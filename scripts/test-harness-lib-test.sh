#!/usr/bin/env bash
# test-harness-lib-test.sh — unit tests for the binary resolution and preflight
# diagnosis in scripts/test-harness/lib.sh.
#
# Why this file exists (#1297): lib.sh defaulted to the RELATIVE `./crewship`,
# but the harness README tells you to run the suites from
# `scripts/test-harness/`, where no such file exists. Every suite then died in
# preflight with a message blaming auth and telling the operator to run the
# destructive `crewship seed --nuke` — a data-loss trap triggered by a path bug.
# Nothing could catch that, because the harness only runs by hand against a live
# server. These checks run the resolution and the two preflight failure modes
# against synthetic trees, with no server involved.
#
# Usage: bash scripts/test-harness-lib-test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB="$SCRIPT_DIR/test-harness/lib.sh"

FAILURES=0
pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1"; printf '       %s\n' "${2:-}"; FAILURES=$((FAILURES + 1)); }

expect_eq() { # <name> <want> <got>
  if [ "$2" = "$3" ]; then pass "$1"; else fail "$1" "want «$2» got «$3»"; fi
}
expect_contains() { # <name> <haystack> <needle>
  case "$2" in *"$3"*) pass "$1";; *) fail "$1" "expected «$3» in: $2";; esac
}
expect_not_contains() { # <name> <haystack> <needle>
  case "$2" in *"$3"*) fail "$1" "did NOT expect «$3» in: $2";; *) pass "$1";; esac
}

# fake_tree <as-git-repo:0|1> — build root/scripts/test-harness/lib.sh, echo root.
fake_tree() {
  local root
  # -P: macOS hands out /var/... (a symlink to /private/var), and `git
  # rev-parse` answers with the physical path. Compare like for like.
  root="$(cd "$(mktemp -d -t cs-harness.XXXXXX)" && pwd -P)"
  mkdir -p "$root/scripts/test-harness"
  cp "$LIB" "$root/scripts/test-harness/lib.sh"
  if [ "$1" = "1" ]; then
    git -C "$root" init -q 2>/dev/null
  fi
  printf '%s\n' "$root"
}

# fake_cli <path> <whoami-exit> — a stub `crewship` that always answers
# `version` (so it counts as a working binary) and exits N on anything else.
fake_cli() {
  cat >"$1" <<EOF
#!/usr/bin/env bash
for a in "\$@"; do [ "\$a" = "version" ] && exit 0; done
exit $2
EOF
  chmod +x "$1"
}

# resolve <root> [extra-env...] — source lib.sh from the harness dir (the way
# the README says to run it) and print the resolved CREWSHIP. Trailing
# assignments land after the `-u`s, so a caller can re-set CREWSHIP.
resolve() {
  local root="$1"; shift
  # shellcheck disable=SC2016  # the inner shell expands these, not this one
  env -u CREWSHIP -u CREWSHIP_SERVER -u SERVER "$@" \
    bash -c 'cd "$1/scripts/test-harness" && . ./lib.sh && printf "%s\n" "$CREWSHIP"' _ "$root"
}

# run_preflight <root> — source lib.sh and run preflight, merging stderr.
run_preflight() {
  local root="$1"; shift
  # shellcheck disable=SC2016  # the inner shell expands these, not this one
  env -u CREWSHIP -u CREWSHIP_SERVER -u SERVER "$@" \
    bash -c 'cd "$1/scripts/test-harness" && . ./lib.sh && preflight' _ "$root" 2>&1
}

echo "== binary resolution =="

root="$(fake_tree 1)"
fake_cli "$root/crewship" 0
expect_eq "git clone, repo-root binary -> absolute repo-root path" \
  "$root/crewship" "$(resolve "$root")"

root_nogit="$(fake_tree 0)"
fake_cli "$root_nogit/crewship" 0
expect_eq "no git (tarball / git missing) -> still the repo-root path" \
  "$root_nogit/crewship" "$(resolve "$root_nogit" PATH=/usr/bin:/bin)"

# No repo-root binary, but one installed on PATH: that is the operator's CLI.
root_path="$(fake_tree 1)"
bindir="$(cd "$(mktemp -d -t cs-bin.XXXXXX)" && pwd -P)"
fake_cli "$bindir/crewship" 0
expect_eq "no repo-root binary, crewship on PATH -> the PATH binary" \
  "$bindir/crewship" "$(resolve "$root_path" "PATH=$bindir:$PATH")"

# Nothing anywhere: still name the repo-root path, so the error is actionable.
root_none="$(fake_tree 1)"
expect_eq "no binary anywhere -> repo-root path (for the error message)" \
  "$root_none/crewship" "$(resolve "$root_none" PATH=/usr/bin:/bin)"

root_env="$(fake_tree 1)"
fake_cli "$root_env/crewship" 0
expect_eq "explicit CREWSHIP wins over resolution" \
  "/opt/custom/crewship" \
  "$(resolve "$root_env" CREWSHIP=/opt/custom/crewship)"

echo
echo "== preflight diagnosis =="

# Missing binary: its own message, and it must NOT push the destructive reseed.
out="$(run_preflight "$root_none" PATH=/usr/bin:/bin)"
expect_contains "missing binary -> says the binary was not found" "$out" "not found"
expect_contains "missing binary -> names the path it looked at" "$out" "$root_none/crewship"
expect_not_contains "missing binary -> does NOT suggest seed" "$out" "seed"
expect_not_contains "missing binary -> does NOT suggest --nuke" "$out" "--nuke"

# Binary runs, server rejects us: THAT is when reseed guidance is warranted.
root_auth="$(fake_tree 1)"
fake_cli "$root_auth/crewship" 1
out="$(run_preflight "$root_auth")"
expect_contains "auth failure -> points at login/seed" "$out" "seed --nuke"
expect_not_contains "auth failure -> does NOT claim the binary is missing" "$out" "not found"

echo
echo "== CREDENTIAL escalation deny (#4858) =="

# The server refuses ANY resolution text on a CREDENTIAL escalation, for reject
# as well as approve (#2376, TestResolveEscalation_Credential_RefusesText).
# keeper-audit's deny once sent a reason and failed on stage the first time the
# section got that far. This stub enforces the same rule and records the call.
deny_cli() {
  cat >"$1" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${DENY_LOG:?}"
for a in "$@"; do [ "$a" = version ] && exit 0; done
case " $* " in
  *" escalation resolve "*" --resolution "*)
    echo 'Error: a CREDENTIAL escalation takes no resolution text — supply the value with POST /api/v1/escalations/{id}/supply' >&2
    exit 1 ;;
  *" escalation resolve esc-deny --action reject "*) exit 0 ;;
esac
echo "Error: escalation not found" >&2
exit 1
EOF
  chmod +x "$1"
}

deny_dir="$(mktemp -d -t cs-deny.XXXXXX)"
deny_cli "$deny_dir/crewship"
: >"$deny_dir/log"
deny() { # <id> — run the real helper against the stub; print "rc|stdout"
  # shellcheck disable=SC2016  # the inner shell expands these, not this one
  env -u CREWSHIP_SERVER -u SERVER CREWSHIP="$deny_dir/crewship" DENY_LOG="$deny_dir/log" \
    bash -c '. "$1/test-harness/lib.sh" && . "$1/test-harness/owned-escalation.sh" && out="$(deny_credential_escalation "$2")"; rc=$?; printf "%s|%s" "$rc" "$out"' _ "$SCRIPT_DIR" "$1"
}
expect_eq "deny of the exact escalation succeeds with no resolution text" "0|" "$(deny esc-deny)"
# Only meaningful once the stub was really called.
expect_contains "deny reached the CLI" "$(cat "$deny_dir/log")" "escalation resolve esc-deny"
expect_not_contains "deny sends no --resolution" "$(cat "$deny_dir/log")" "--resolution"
expect_contains "deny sends --action reject for that id" "$(cat "$deny_dir/log")" "escalation resolve esc-deny --action reject"
expect_contains "a failed deny surfaces the CLI error" "$(deny esc-other)" "1|Error: escalation not found"
if grep -nE 'escalation resolve[^#]*--resolution' "$SCRIPT_DIR/test-harness/test-keeper-audit-integrity.sh" >/dev/null; then
  fail "keeper-audit resolves no escalation with resolution text" "$(grep -nE 'escalation resolve[^#]*--resolution' "$SCRIPT_DIR/test-harness/test-keeper-audit-integrity.sh")"
else
  pass "keeper-audit resolves no escalation with resolution text"
fi
rm -rf "$deny_dir"

echo
echo "== README prereqs =="

# The prereqs say an installed CLI on PATH is enough, so the commands they hand
# the operator must work for that operator too. A hardcoded `./crewship` in the
# seed block is exactly the breakage this file's fix removed from lib.sh.
README="$SCRIPT_DIR/test-harness/README.md"
readme_cmds="$(grep -nE '^[[:space:]]*\./crewship ' "$README" || true)"
expect_eq "README hands out no bare ./crewship command line" "" "$readme_cmds"
expect_contains "README seeds with the PATH form" \
  "$(grep -E '^[[:space:]]*crewship seed ' "$README" || true)" "crewship seed --nuke"
expect_contains "README still names the locally-built ./crewship form" \
  "$(cat "$README")" "./crewship"

echo
echo "== crew-links evidence (#4855) =="

# test-crew-links.sh once passed when alex REFUSED the delegation but quoted the
# requested "<tag>-OK" string (stage ff4c164, 2026-11-03): the prompt carries
# the tag, so the reply text cannot prove a crossing. Drive the real suite
# against a stub CLI whose `activity` feed is the only thing that changes.
# XLINK_MODE: refuse-quoting (no artifact, reply quotes the tag), landed
# (tag-correlated assignment for morgan; the reply never mentions the tag),
# leaky (an assignment lands even with the link removed) or feed-down
# (`activity` itself fails).
xlink_cli() {
  cat >"$1" <<'EOF'
#!/usr/bin/env bash
state="${XLINK_STATE:?}"
args=" $* "
prompt="" save="" prev="" desc=""
for a in "$@"; do
  [ "$prev" = "-p" ] && prompt="$a"
  [ "$prev" = "--save" ] && save="$a"
  [ "$prev" = "--description" ] && desc="$a"
  prev="$a"
done
case "$args" in
  *" crew connect "*) rm -f "$state/unlinked"; exit 0 ;;
  *" crew disconnect "*) : >"$state/unlinked"; exit 0 ;;
  *" version "*|*" whoami "*) exit 0 ;;
  *" agent list "*|*" routine list "*) echo row; exit 0 ;;
  *" crew connections "*)
    [ -e "$state/unlinked" ] || echo "link cc_stub engineering ops bidirectional"; exit 0 ;;
  *" chat list "*) echo "[]"; exit 0 ;;
  *" work list "*) echo '{"items":[]}'; exit 0 ;;
  *" activity "*)
    [ "$XLINK_MODE" = feed-down ] && exit 1
    [ -s "$state/landed" ] && cat "$state/landed"; exit 0 ;;
  *" issue create "*)
    n=$(( $(cat "$state/n" 2>/dev/null || echo 0) + 1 )); echo "$n" >"$state/n"
    printf '%s' "$desc" >"$state/issue-ENG-$n"
    printf '{"id":"iss_%s","identifier":"ENG-%s"}\n' "$n" "$n"; exit 0 ;;
  *" issue start "*)
    id="${args##* issue start }"; id="${id%% *}"
    tag="$(grep -oE 'XLINK(NEG)?-[A-Z0-9]+' "$state/issue-$id" | head -1)"
    if { [ "$XLINK_MODE" = landed ] && [ ! -e "$state/unlinked" ]; } || [ "$XLINK_MODE" = leaky ]; then
      printf '{"id":"a-%s","to_slug":"morgan","entry_type":"assignment.created","summary":"assigned alex → Morgan: Confirm on-call readiness for release %s"}\n' "$tag" "$tag" >>"$state/landed"
      printf '{"id":"c-%s","to_slug":"morgan","entry_type":"assignment.completed","summary":"morgan completed: Confirm on-call readiness for release %s"}\n' "$tag" "$tag" >>"$state/landed"
      printf 'Morgan confirmed.' >"$state/result-$id"
    elif [ -e "$state/unlinked" ]; then
      printf 'Assignment refused: crews are not connected — create a crew connection first' >"$state/result-$id"
    else
      printf "I could not get Morgan to reply %s-OK." "$tag" >"$state/result-$id"
    fi
    exit 0 ;;
  *" issue runs "*)
    id="${args##* issue runs }"; id="${id%% *}"
    st=COMPLETED
    [ "$XLINK_MODE" = neg-hangs ] && grep -q XLINKNEG "$state/issue-$id" && st=RUNNING
    jq -nc --arg r "$(cat "$state/result-$id" 2>/dev/null)" --arg st "$st" '[{status:$st,source:"delegation",agent_slug:"alex",result_summary:$r}]'
    exit 0 ;;
  *" issue comments "*|*" issue get "*) exit 0 ;;
  *" ask "*)
    printf 'ops\n' >"$save"; exit 0 ;;
esac
exit 0
EOF
  chmod +x "$1"
}

run_crew_links() { # <mode> — print the suite's output (colour-free, stdout not a tty)
  local dir; dir="$(mktemp -d -t cs-xlink.XXXXXX)"
  xlink_cli "$dir/crewship"
  env -u CREWSHIP_SERVER -u SERVER CREWSHIP="$dir/crewship" XLINK_STATE="$dir" XLINK_MODE="$1" ASK_TIMEOUT=3 POLL_INTERVAL=1 \
    bash "$SCRIPT_DIR/test-harness/test-crew-links.sh" 2>&1
  rm -rf "$dir"
}

if command -v jq >/dev/null 2>&1; then
  out="$(run_crew_links refuse-quoting)"
  expect_contains "quoted tag without an artifact -> section 3 FAILS" "$out" "FAIL cross-crew delegation over a live link"
  expect_contains "quoted tag without an artifact -> says the echo is not evidence" "$out" "is not evidence"
  expect_not_contains "quoted tag without an artifact -> suite is not green" "$out" "failed: 0"

  out="$(run_crew_links landed)"
  expect_contains "tag-correlated assignment -> section 3 PASSES" "$out" "PASS delegation crossed the link"
  expect_contains "tag-correlated completion -> result PASSES" "$out" "PASS the cross-crew assignment completed with a result"
  expect_contains "landed run -> the lead's own (lead-planning) run is the one awaited" "$out" "PASS the lead's unlinked hand-off issue ran to an end"
  expect_not_contains "landed run -> the agent's refusal text is never a PASS" "$out" "PASS the unlinked hand-off was attempted"
  expect_contains "landed run -> unlinked half PASSES" "$out" "PASS an unlinked crew cannot be delegated to"
  expect_contains "landed run -> suite is green" "$out" "failed: 0"

  out="$(run_crew_links leaky)"
  expect_contains "assignment lands with no link -> unlinked half FAILS" "$out" "FAIL an unlinked crew cannot be delegated to"

  out="$(run_crew_links neg-hangs)"
  expect_contains "unlinked hand-off run never ends -> FAILS, not a vacuous pass" "$out" "FAIL the lead's unlinked hand-off issue ran to an end"
  expect_contains "unlinked hand-off run never ends -> refusal unproven" "$out" "FAIL an unlinked crew cannot be delegated to"
  expect_contains "unlinked hand-off run never ends -> no-work check unproven" "$out" "FAIL no work reaches an unlinked crew"

  out="$(run_crew_links feed-down)"
  expect_contains "unreadable activity feed -> section 3 names the feed" "$out" "could not be read, so there is no evidence"
  expect_contains "unreadable activity feed -> unlinked half FAILS" "$out" "FAIL an unlinked crew cannot be delegated to"
  expect_not_contains "unreadable activity feed -> no echo blame" "$out" "is not evidence"
else
  echo "  skip crew-links evidence checks (jq not installed)"
fi

echo
echo "== grounded issue work and failure evidence (#4873, #4874) =="

# A stub CLI that records every call, answers the issue lifecycle, and serves
# one chat created after the suite started plus one created before it.
ev_cli() {
  cat >"$1" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${EV_LOG:?}"
args=" $* "
case "$args" in
  *" version "*|*" whoami "*) exit 0 ;;
  *" issue create "*) echo '{"id":"iss_1","identifier":"OPS-7"}'; exit 0 ;;
  *" issue start "*) [ "${EV_START:-ok}" = ok ] || exit 1; exit 0 ;;
  *" issue runs "*)
    case "${EV_RUN:-done}" in
      done) echo '[{"status":"FAILED","source":"delegation","agent_slug":"morgan"}]' ;;
      task) echo '[{"status":"COMPLETED","source":"task","agent_slug":"morgan"}]' ;;
      other) echo '[{"status":"COMPLETED","source":"task","agent_slug":"sam"},{"status":"COMPLETED","source":"mention","agent_slug":"morgan"}]' ;;
      *) echo '[{"status":"RUNNING","source":"delegation","agent_slug":"morgan"}]' ;;
    esac; exit 0 ;;
  *" issue get "*) echo '{"identifier":"OPS-7","description":"needs token keeper-gated-secret-value"}'; exit 0 ;;
  *" chat list "*) printf '[{"id":"chat_new","created_at":"2999-01-01T00:00:00.000Z"},{"id":"chat_old","created_at":"2000-01-01T00:00:00.000Z"}]\n'; exit 0 ;;
  *" chat chat_new "*) echo "assistant: I used keeper-gated-secret-value"; exit 0 ;;
  *" chat chat_old "*) echo "old"; exit 0 ;;
esac
exit 0
EOF
  chmod +x "$1"
}
ev_dir="$(mktemp -d -t cs-ev.XXXXXX)"; ev_cli "$ev_dir/crewship"; : >"$ev_dir/log"
ev_run() { # <script-body> — source lib.sh + owned-escalation.sh in a clean env and run <body>
  # shellcheck disable=SC2016
  env -u CREWSHIP_SERVER -u SERVER CREWSHIP="$ev_dir/crewship" EV_LOG="$ev_dir/log" POLL_INTERVAL=1 "$@"
}
out="$(ev_run bash -c '. "$1/test-harness/lib.sh"; . "$1/test-harness/owned-escalation.sh"; request_credential_via_issue ops morgan HARNESS_X 5; echo "rc=$?"' _ "$SCRIPT_DIR")"
expect_contains "grounded credential ask reports the issue and its run status" "$out" "OPS-7 FAILED"
expect_contains "grounded credential ask returns 0 once the run ended" "$out" "rc=0"
expect_contains "the issue is delegated to the agent in its crew" "$(cat "$ev_dir/log")" "issue create --crew ops --delegate morgan --status TODO"
expect_contains "the issue description names the credential and asks for an escalation" "$(cat "$ev_dir/log")" "metadata.name is exactly HARNESS_X"
expect_contains "the issue is started" "$(cat "$ev_dir/log")" "issue start OPS-7"
out="$(ev_run env EV_START=fail bash -c '. "$1/test-harness/lib.sh"; issue_work ops morgan t d 5; echo "rc=$?"' _ "$SCRIPT_DIR")"
expect_contains "a refused start is reported, not waited on" "$out" "OPS-7 START_FAILED"
expect_contains "a refused start returns 1" "$out" "rc=1"
out="$(ev_run env EV_RUN=running bash -c '. "$1/test-harness/lib.sh"; issue_work ops morgan t d 2; echo "rc=$?"' _ "$SCRIPT_DIR")"
expect_contains "a run that never ends times out with rc 2" "$out" "rc=2"
out="$(ev_run env EV_RUN=task bash -c '. "$1/test-harness/lib.sh"; issue_work ops morgan t d 5; echo "rc=$?"' _ "$SCRIPT_DIR")"
expect_contains "a delegate's own task run (non-lead) is awaited" "$out" "rc=0"
out="$(ev_run env EV_RUN=other bash -c '. "$1/test-harness/lib.sh"; issue_work ops morgan t d 2; echo "rc=$?"' _ "$SCRIPT_DIR")"
expect_contains "another agent's run or a mention run never stands in for the delegate's" "$out" "rc=2"

det="$ev_dir/detail"; mkdir -m 700 "$det"
# shellcheck disable=SC2016
ev_run env HARNESS_DETAIL_DIR="$det" bash -c '. "$1/test-harness/lib.sh"; keep_failure_evidence; register_secret keeper-gated-secret-value; issue_work ops morgan t d 5 >/dev/null; _fail "probe check"; finish' _ "$SCRIPT_DIR" >/dev/null 2>&1
suite_dir="$det/bash"
[ -d "$det" ] && suite_dir="$(find "$det" -mindepth 1 -maxdepth 1 -type d | head -1)"
expect_eq "a failing suite writes its failures list" "1" "$( [ -f "$suite_dir/failures.txt" ] && echo 1 || echo 0)"
expect_contains "the failures list names the failed check" "$(cat "$suite_dir/failures.txt" 2>/dev/null)" "probe check"
expect_eq "the chat started during the suite is kept" "1" "$( [ -f "$suite_dir/chat-morgan-chat_new.txt" ] && echo 1 || echo 0)"
expect_eq "a chat older than the suite is not dumped" "0" "$( [ -f "$suite_dir/chat-morgan-chat_old.txt" ] && echo 1 || echo 0)"
expect_contains "the issue and its runs are kept" "$(cat "$suite_dir/issue-OPS-7.json" 2>/dev/null)" "\"status\":\"FAILED\""
expect_not_contains "registered values never reach evidence (chat)" "$(cat "$suite_dir"/chat-* 2>/dev/null)" "keeper-gated-secret-value"
expect_not_contains "registered values never reach evidence (issue)" "$(cat "$suite_dir"/issue-* 2>/dev/null)" "keeper-gated-secret-value"
expect_contains "a redacted value is marked" "$(cat "$suite_dir"/chat-* 2>/dev/null)" "[REDACTED]"
expect_eq "evidence files are owner-only" "" "$(find "$suite_dir" -type f ! -perm 600 2>/dev/null)"
pass_det="$ev_dir/pass"; mkdir -m 700 "$pass_det"
# shellcheck disable=SC2016
ev_run env HARNESS_DETAIL_DIR="$pass_det" bash -c '. "$1/test-harness/lib.sh"; keep_failure_evidence; issue_work ops morgan t d 5 >/dev/null; _pass ok; finish' _ "$SCRIPT_DIR" >/dev/null 2>&1
expect_eq "a passing suite writes no evidence" "" "$(find "$pass_det" -mindepth 1 2>/dev/null)"
: >"$ev_dir/log"
# shellcheck disable=SC2016
ev_run bash -c '. "$1/test-harness/lib.sh"; keep_failure_evidence; issue_work ops morgan t d 5 >/dev/null; _fail x; finish' _ "$SCRIPT_DIR" >/dev/null 2>&1
expect_not_contains "without HARNESS_DETAIL_DIR no transcript is fetched" "$(cat "$ev_dir/log")" "chat chat_new"
optin_det="$ev_dir/optin"; mkdir -m 700 "$optin_det"
# shellcheck disable=SC2016
ev_run env HARNESS_DETAIL_DIR="$optin_det" bash -c '. "$1/test-harness/lib.sh"; issue_work ops morgan t d 5 >/dev/null; _fail x; finish' _ "$SCRIPT_DIR" >/dev/null 2>&1
expect_eq "a suite that did not opt in keeps nothing, even failing" "" "$(find "$optin_det" -mindepth 1 2>/dev/null)"
# shellcheck disable=SC2016
red="$(timeout 10 bash -c '. "$1/test-harness/lib.sh"; register_secret tok; register_secret token-abc; register_secret RED; printf "a token-abc b tok c [RED]\n" | _redact' _ "$SCRIPT_DIR")"
expect_eq "redaction: longest value wins, a value inside [REDACTED] cannot loop" "a [REDACTED] b [REDACTED] c [[REDACTED]]" "$red"
rm -rf "$ev_dir"

echo
if [ "$FAILURES" -eq 0 ]; then
  echo "all test-harness lib checks passed"
else
  echo "$FAILURES check(s) FAILED"
fi
exit $(( FAILURES > 0 ? 1 : 0 ))

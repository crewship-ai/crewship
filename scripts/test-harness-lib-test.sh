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
echo "== keeper ledger request selection (#5083) =="
if command -v jq >/dev/null 2>&1; then
  KEEPER_ROWS='[
    {"id":"bhv","request_type":"behavior","decision":"ESCALATE"},
    {"id":"dup","request_type":"execute","decision":"DUPLICATE_SUPPRESSED"},
    {"id":"open","request_type":"execute","decision":"PENDING"},
    {"id":"exe","request_type":"execute","decision":"DENY"},
    {"id":"acc","request_type":"access","decision":"ALLOW"}]'
  pick() { # <function> <rows>
    bash -c '. "$1/test-harness/keeper-ledger.sh" && "$2" "$3"' _ "$SCRIPT_DIR" "$1" "$2" 2>/dev/null
  }
  expect_eq "a raised request skips newer one-shot rows" "exe" "$(pick keeper_pick_raised_request "$KEEPER_ROWS")"
  expect_eq "the newest one-shot row is a verdict" "bhv" "$(pick keeper_pick_one_shot_request "$KEEPER_ROWS")"
  expect_eq "a suppression counts as one-shot" "dup" "$(pick keeper_pick_one_shot_request '[{"id":"dup","request_type":"execute","decision":"DUPLICATE_SUPPRESSED"}]')"
  expect_eq "only one-shot rows leave no raised request" "" "$(pick keeper_pick_raised_request '[{"id":"bhv","request_type":"behavior","decision":"ESCALATE"}]')"
  expect_eq "a still-pending row is neither" "" "$(pick keeper_pick_raised_request '[{"id":"open","request_type":"access","decision":"PENDING"}]')$(pick keeper_pick_one_shot_request '[{"id":"open","request_type":"access","decision":"PENDING"}]')"
else
  pass "keeper ledger selection skipped (jq missing)"
fi

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
  *" crew get engineering "*) echo '{"id":"eng-id"}'; exit 0 ;;
  *" crew get ops "*) echo '{"id":"ops-id"}'; exit 0 ;;
  *" agent get alex "*) echo '{"id":"alex-id"}'; exit 0 ;;
  *" journal --agent alex "*)
    printf '%s\n' journal >>"$state/request-categories"
    if [ -s "$state/refused" ]; then jq -sc . "$state/refused"; else echo '[]'; fi; exit 0 ;;
  *" journal --run-id "*)
    printf '%s\n' journal >>"$state/request-categories"
    id="${args##* journal --run-id }"; id="${id%% *}"
    if [ -s "$state/started" ]; then jq -sc --arg run "$id" 'map(select(.trace_id == $run))' "$state/started"; else echo '[]'; fi; exit 0 ;;
  *" chat list "*)
    if [ -s "$state/chats" ]; then jq -sc . "$state/chats"; else echo '[{"id":"chat-old","created_at":"2026-01-01T00:00:00Z"}]'; fi; exit 0 ;;
  *" work list "*)
    [ "${XLINK_BUSY:-}" = 1 ] && { echo '{"items":[{"id":"busy"}]}'; exit 0; }
    echo '{"items":[]}'; exit 0 ;;
  *" activity "*)
    printf '%s\n' activity >>"$state/request-categories"
    [ "$XLINK_MODE" = feed-down ] && exit 1
    [ -s "$state/landed" ] && cat "$state/landed"; exit 0 ;;
  *" issue create "*)
    printf '%s\n' issue-create >>"$state/request-categories"
    n=$(( $(cat "$state/n" 2>/dev/null || echo 0) + 1 )); echo "$n" >"$state/n"
    printf '%s' "$desc" >"$state/issue-ENG-$n"
    if [[ "$args" == *"--crew ops"* ]]; then
      printf '%s' "$desc" | grep -oE 'VERIFIED-[A-Z0-9]+' >"$state/verification-code"
    fi
    printf '{"id":"iss_%s","identifier":"ENG-%s"}\n' "$n" "$n"; exit 0 ;;
  *" issue start "*)
    printf '%s\n' issue-start >>"$state/request-categories"
    id="${args##* issue start }"; id="${id%% *}"
    tag="$(grep -oE 'XLINK(NEG)?-[A-Z0-9]+' "$state/issue-$id" | head -1)"
    if { [[ "$XLINK_MODE" == landed* ]] && [ ! -e "$state/unlinked" ]; } || [ "$XLINK_MODE" = leaky ]; then
      jq -nc --arg tag "$tag" --arg mode "$XLINK_MODE" '{id:("a-"+$tag),to_slug:"morgan",entry_type:"assignment.created",payload:{assignment_id:("asn-"+$tag),task:("Read actual record "+$tag)}}
        | if $mode == "landed-no-created-receipt-id" then del(.id) else . end' >>"$state/landed"
      if [ "$XLINK_MODE" != landed-no-completion ]; then
        jq -nc --arg tag "$tag" '{id:("c-"+$tag),to_slug:"morgan",entry_type:"assignment.completed",payload:{assignment_id:("asn-"+$tag),outcome:"SUCCEEDED"}}' >>"$state/landed"
      fi
      printf '%s' "asn-$tag" >"$state/assignment-$id"
      # The real list exposes only 200 characters plus an ellipsis. Keep the
      # independent code past that boundary; only the full-result route has it.
      printf '%0250d Morgan read the record: ' 0 >"$state/result-$id"
      if [ "$XLINK_MODE" != landed-no-code ]; then
        cat "$state/verification-code" >>"$state/result-$id"
      fi
    elif [ -e "$state/unlinked" ]; then
      printf 'Assignment refused: crews are not connected — create a crew connection first' >"$state/result-$id"
    else
      printf "I could not get Morgan to reply %s-OK." "$tag" >"$state/result-$id"
    fi
    exit 0 ;;
  *" issue runs "*)
    printf '%s\n' issue-runs >>"$state/request-categories"
    id="${args##* issue runs }"; id="${id%% *}"
    st=COMPLETED
    [ "$XLINK_MODE" = neg-hangs ] && grep -q XLINKNEG "$state/issue-$id" && st=RUNNING
    [ "$XLINK_MODE" = neg-failed ] && grep -q XLINKNEG "$state/issue-$id" && st=FAILED
    assignment="$(cat "$state/assignment-$id" 2>/dev/null)"
    [ "$XLINK_MODE" = landed-wrong-id ] && assignment="unrelated-$assignment"
    jq -nc --arg r "$(cat "$state/result-$id" 2>/dev/null)" --arg st "$st" --arg id "$id" --arg assignment "$assignment" '
      ($r | if length > 200 then .[:200]+"…" else . end) as $preview
      | [{id:("source-assignment-"+$id),status:$st,source:"delegation",agent_slug:"alex",agent_id:"alex-id",run_id:("run-"+$id),result_summary:$preview}]
      + (if ($r|contains("Morgan read the record:")) then [{id:$assignment,run_id:("morgan-journal-"+$id),status:"COMPLETED",source:"delegation",agent_id:"morgan-id",agent_slug:"morgan",result_summary:$preview}] else [] end)'
    exit 0 ;;
  *" issue result "*)
    printf '%s\n' issue-result >>"$state/request-categories"
    id="${args##* issue result }"; assignment="${id#* }"; assignment="${assignment%% *}"; id="${id%% *}"
    [ "$XLINK_MODE" != landed-no-full ] || exit 1
    [ -s "$state/assignment-$id" ] || exit 1
    [ "$assignment" = "$(cat "$state/assignment-$id")" ] || exit 1
    jq -nc --arg r "$(cat "$state/result-$id")" '{status:"COMPLETED",outcome:"SUCCEEDED",result_summary:$r}'
    exit 0 ;;
  *" issue comments "*|*" issue get "*) exit 0 ;;
  *" ask "*)
    if [[ "$prompt" == *"9119/assign"* ]]; then
      # The direct owner turn of section 4: one exact /assign while unlinked.
      printf '%s\n' ask-assign >>"$state/request-categories"
      case "$XLINK_MODE" in neg-hangs|neg-failed) exit 1 ;; esac
      task="${prompt#*\"task\":\"}"; task="${task%%\"*}"
      tag="$(printf '%s' "$task" | grep -oE 'XLINKNEG-[A-Z0-9]+' | head -1)"
      # The real feed strips chat_id from a human-authorized run.started.
      jq -nc '{id:"run-start:chat-run-neg",entry_type:"run.started",trace_id:"chat-run-neg",agent_id:"alex-id",payload:{agent_slug:"alex"}}' >>"$state/started"
      [ -s "$state/chats" ] || jq -nc '{id:"chat-old",created_at:"2026-01-01T00:00:00Z"}' >"$state/chats"
      [ "$XLINK_MODE" = chat-unlisted ] || jq -nc '{id:"chat-neg",created_at:"2026-11-07T00:00:00Z"}' >>"$state/chats"
      if [ "$XLINK_MODE" = leaky ]; then
        jq -nc --arg tag "$tag" '{id:("a-"+$tag),to_slug:"morgan",entry_type:"assignment.created",payload:{assignment_id:("asn-"+$tag),task:("Read actual record "+$tag)}}' >>"$state/landed"
      elif [ -e "$state/unlinked" ] && [ "$XLINK_MODE" != no-attempt ]; then
        jq -nc --arg sha "$(printf '%s' "$task" | sha256sum | cut -d' ' -f1)" --arg mode "$XLINK_MODE" '{id:"refused-chat-run-neg",actor_type:"agent",entry_type:"assignment.refused",trace_id:"chat-run-neg",actor_id:"alex-id",agent_id:"alex-id",crew_id:"eng-id",refs:{chat_id:"chat-neg"},payload:{reason:"crews_not_connected",http_status:403,source_crew_id:"eng-id",target_crew_id:"ops-id",target_slug:"morgan",task_sha256:$sha}}
          | if $mode == "landed-no-refusal-receipt-id" then del(.id) else . end' >>"$state/refused"
      fi
      printf '{"error":"crews are not connected — create a crew connection first"}\n' >"$save"
      exit 0
    fi
    printf 'ops\n' >"$save"
    [ "${XLINK_ASK:-}" = partial ] && exit 1
    exit 0 ;;
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
  local suite_rc=$?
  echo "fixture: suite-exit:$suite_rc"
  if [ -n "${XLINK_REQUEST_TRACE:-}" ]; then cp "$dir/request-categories" "$XLINK_REQUEST_TRACE"; fi
  if [ "$1" = landed ]; then
    local file id assignment preview full code
    for file in "$dir"/assignment-*; do
      id="${file##*/assignment-}"; assignment="$(cat "$file")"; code="$(cat "$dir/verification-code")"
      preview="$(XLINK_STATE="$dir" XLINK_MODE=landed "$dir/crewship" issue runs "$id" --format json)"
      full="$(XLINK_STATE="$dir" XLINK_MODE=landed "$dir/crewship" issue result "$id" "$assignment" --format json)"
      if printf '%s\n' "$preview" | jq -e --arg code "$code" --arg assignment "$assignment" '
        any(.[]; .id == $assignment and .run_id != $assignment and
          (.result_summary|length) == 201 and (.result_summary|contains($code)|not))' >/dev/null &&
        printf '%s\n' "$full" | jq -e --arg code "$code" '
          (.result_summary|length) > 250 and (.result_summary|contains($code))' >/dev/null; then
        echo "fixture: bounded preview and full matched result verified"
      fi
      if ! XLINK_STATE="$dir" XLINK_MODE=landed "$dir/crewship" issue result "$id" "morgan-journal-$id" --format json >/dev/null 2>&1; then
        echo "fixture: journal identity is rejected by full result"
      fi
    done
  fi
  rm -rf "$dir"
}

if command -v jq >/dev/null 2>&1; then
  out="$(run_crew_links refuse-quoting)"
  expect_contains "quoted tag without an artifact -> section 3 FAILS" "$out" "FAIL cross-crew delegation over a live link"
  expect_contains "quoted tag without an artifact -> says the echo is not evidence" "$out" "is not evidence"
  expect_not_contains "quoted tag without an artifact -> suite is not green" "$out" "failed: 0"

  request_trace="$(mktemp -t cs-xlink-requests.XXXXXX)"
  receipt_trace="$(mktemp -t cs-xlink-receipt-requests.XXXXXX)"
  out="$(XLINK_REQUEST_TRACE="$request_trace" run_crew_links landed)"
  expect_contains "tag-correlated assignment -> section 3 PASSES" "$out" "PASS delegation crossed the link"
  expect_contains "tag-correlated completion -> result PASSES" "$out" "PASS the cross-crew assignment completed with a result"
  expect_contains "landed run -> the lead's direct owner turn ended" "$out" "PASS the lead's unlinked hand-off turn ran to an end"
  expect_not_contains "landed run -> the agent's refusal text is never a PASS" "$out" "PASS the unlinked hand-off was attempted"
  expect_contains "landed run -> unlinked half PASSES" "$out" "PASS an unlinked crew cannot be delegated to"
  expect_contains "landed run -> suite is green" "$out" "failed: 0"
  expect_contains "landed fixture -> code is beyond the 201-character preview" "$out" "fixture: bounded preview and full matched result verified"
  expect_contains "landed fixture -> unknown journal identity cannot read the result" "$out" "fixture: journal identity is rejected by full result"

  private_receipt_dir="$(umask 077; mktemp -d -t cs-xlink-private.XXXXXX)"
  retained_out="$(HARNESS_DETAIL_DIR="$private_receipt_dir" INGEST_SHA="" XLINK_REQUEST_TRACE="$receipt_trace" run_crew_links landed)"
  expect_contains "receipt failure keeps functional completion observation" "$retained_out" "PASS the cross-crew assignment completed with a result"
  expect_contains "receipt failure keeps actual server refusal observation" "$retained_out" "PASS an unlinked crew cannot be delegated to"
  expect_contains "configured receipt failure fails suite" "$retained_out" "fixture: suite-exit:1"
  expect_contains "configured receipt failure is named separately" "$retained_out" "FAIL crew-link success evidence is durably retained"
  expect_eq "retention makes no extra API/model requests" "$(cat "$request_trace")" "$(cat "$receipt_trace")"
  expect_eq "no success receipt after missing source binding" "0" "$(find "$private_receipt_dir" -name success-receipt.v1.json | wc -l | tr -d ' ')"
  rm -rf "$private_receipt_dir"
  # Both modes still satisfy the unchanged functional predicates. Only
  # metadata needed for the private receipt is absent, forcing rc2 after a
  # successful match rather than the final writer's missing-binding path.
  for mode in landed-no-created-receipt-id landed-no-refusal-receipt-id; do
    private_receipt_dir="$(umask 077; mktemp -d -t cs-xlink-private.XXXXXX)"
    projection_baseline="$(HARNESS_DETAIL_DIR="" XLINK_REQUEST_TRACE="$request_trace" run_crew_links "$mode")"
    projection_out="$(HARNESS_DETAIL_DIR="$private_receipt_dir" INGEST_SHA="" XLINK_REQUEST_TRACE="$receipt_trace" run_crew_links "$mode")"
    expect_contains "$mode: original predicate-only suite exits zero" "$projection_baseline" "fixture: suite-exit:0"
    expect_contains "$mode: original nine functional checks pass" "$projection_baseline" "passed: 9"
    expect_contains "$mode: configured nine functional checks still pass" "$projection_out" "passed: 9"
    expect_contains "$mode: full result predicate remains true" "$projection_out" "PASS the cross-crew assignment completed with a result"
    expect_contains "$mode: actual 403 predicate remains true" "$projection_out" "PASS an unlinked crew cannot be delegated to"
    expect_contains "$mode: rc2 is a distinct retention failure" "$projection_out" "a passing observation could not be projected"
    expect_contains "$mode: configured gate actually exits one" "$projection_out" "fixture: suite-exit:1"
    expect_eq "$mode: rc2 stops without extra API/model/poll requests" "$(cat "$request_trace")" "$(cat "$receipt_trace")"
    expect_not_contains "$mode: no raw verification code in output" "$projection_out" "VERIFIED-"
    expect_not_contains "$mode: no full model result in output" "$projection_out" "Morgan read the record"
    expect_eq "$mode: no forged success receipt" "0" "$(find "$private_receipt_dir" -name success-receipt.v1.json | wc -l | tr -d ' ')"
    rm -rf "$private_receipt_dir"
  done
  rm -f "$request_trace" "$receipt_trace"

  # A committed, owned source fixture exercises the actual suite -> projector ->
  # writer -> finish cleanup, with no live server or model and no source override.
  receipt_fixture="$(umask 077; mktemp -d -t cs-xlink-source.XXXXXX)"
  mkdir -p "$receipt_fixture/repo/scripts/test-harness" "$receipt_fixture/state" "$receipt_fixture/tmp"
  mkdir -m 700 "$receipt_fixture/private"
  for file in run-all.sh lib.sh test-crew-links.sh crew-links-evidence.sh crew-links-receipt.py; do
    cp "$SCRIPT_DIR/test-harness/$file" "$receipt_fixture/repo/scripts/test-harness/$file"
  done
  git -C "$receipt_fixture/repo" init -q
  git -C "$receipt_fixture/repo" add scripts
  git -C "$receipt_fixture/repo" -c user.name=Fixture -c user.email=fixture@example.invalid commit -qm 'test: synthetic receipt source'
  receipt_sha="$(git -C "$receipt_fixture/repo" rev-parse HEAD)"
  xlink_cli "$receipt_fixture/crewship"
  receipt_out="$(env -u CREWSHIP_SERVER -u SERVER CREWSHIP="$receipt_fixture/crewship" \
    XLINK_STATE="$receipt_fixture/state" XLINK_MODE=landed ASK_TIMEOUT=3 POLL_INTERVAL=1 \
    TMPDIR="$receipt_fixture/tmp" HARNESS_DETAIL_DIR="$receipt_fixture/private" \
    INGEST_SHA="$receipt_sha" SCAN_RUN_ID=fixture-scan STAGE_RUN_ID=fixture-scan CREWSHIP_WORKSPACE=fixture-workspace \
    bash "$receipt_fixture/repo/scripts/test-harness/test-crew-links.sh" 2>&1)"
  receipt_rc=$?
  receipt_file="$receipt_fixture/private/test-crew-links/success-receipt.v1.json"
  expect_eq "configured success receipt suite actually exits zero" 0 "$receipt_rc"
  expect_contains "configured success keeps nine functional assertions" "$receipt_out" 'passed: 9'
  if jq -e --arg sha "$receipt_sha" '.complete == true and .source.sha == $sha and .assertions.passed == 9 and
      .positive.assignment_id != .positive.journal_run_id and .positive.expected_code_present == true and
      .negative.refusal.http_status == 403 and .negative.refusal.trace_id == .negative.source_run_id and
      .negative.refusal.actor_id == .negative.source_actor_id and .negative.chat_id == "chat-neg" and
      .negative.run_started_entry_id == "run-start:chat-run-neg" and .schema_version == 2 and .topology.link_restored == true' "$receipt_file" >/dev/null 2>&1; then
    pass "success receipt retains actual full-result/refusal joins before cleanup"
  else fail "success receipt retains actual full-result/refusal joins before cleanup"; fi
  code="$(cat "$receipt_fixture/state/verification-code")"
  if grep -qF "$code" "$receipt_file" 2>/dev/null || grep -qF 'Morgan read the record' "$receipt_file" 2>/dev/null; then
    fail "success receipt excludes raw code and model text"
  else pass "success receipt excludes raw code and model text"; fi
  expect_eq "success receipt survives volatile-state cleanup" "" "$(find "$receipt_fixture/tmp" -name 'harness-state.*')"
  rm -rf "$receipt_fixture"

  for mode in landed-wrong-id landed-no-completion landed-no-code landed-no-full; do
    out="$(run_crew_links "$mode")"
    expect_contains "$mode -> full matched completion is required" "$out" "FAIL the cross-crew assignment completed with a result"
    expect_not_contains "$mode -> suite cannot be green" "$out" "failed: 0"
  done

  out="$(run_crew_links no-attempt)"
  expect_contains "completed model refusal without server call -> unlinked half FAILS" "$out" "FAIL an unlinked crew cannot be delegated to"
  expect_contains "model refusal without server call -> receipt missing" "$out" "no server refusal receipt"
  expect_contains "model refusal without server call -> no-work unproven" "$out" "FAIL no work reaches an unlinked crew"

  out="$(run_crew_links chat-unlisted)"
  expect_contains "receipt in a chat this turn did not create -> unlinked half FAILS" "$out" "FAIL an unlinked crew cannot be delegated to"
  expect_contains "receipt in an unidentified chat -> says the chat is unknown" "$out" "chat could not be identified"

  out="$(run_crew_links leaky)"
  expect_contains "assignment lands with no link -> unlinked half FAILS" "$out" "FAIL an unlinked crew cannot be delegated to"

  out="$(run_crew_links neg-hangs)"
  expect_contains "unlinked hand-off turn never ends -> FAILS, not a vacuous pass" "$out" "FAIL the lead's unlinked hand-off turn ran to an end"
  expect_contains "unlinked hand-off turn never ends -> refusal unproven" "$out" "FAIL an unlinked crew cannot be delegated to"
  expect_contains "unlinked hand-off turn never ends -> no-work check unproven" "$out" "FAIL no work reaches an unlinked crew"

  out="$(run_crew_links neg-failed)"
  expect_contains "unlinked hand-off turn failed at once -> not a pass" "$out" "FAIL an unlinked crew cannot be delegated to"
  expect_contains "unlinked hand-off turn failed at once -> no-work check unproven" "$out" "FAIL no work reaches an unlinked crew"

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
      done) echo '[{"status":"FAILED","source":"delegation","agent_slug":"morgan","run_id":"run_9"}]' ;;
      task) echo '[{"status":"COMPLETED","source":"task","agent_slug":"morgan"}]' ;;
      other) echo '[{"status":"COMPLETED","source":"task","agent_slug":"sam"},{"status":"COMPLETED","source":"mention","agent_slug":"morgan"}]' ;;
      *) echo '[{"status":"RUNNING","source":"delegation","agent_slug":"morgan"}]' ;;
    esac; exit 0 ;;
  *" issue get "*) echo '{"identifier":"OPS-7","description":"needs token keeper-gated-secret-value"}'; exit 0 ;;
  *" journal --run-id run_9 "*) echo '{"entries":[{"kind":"tool","text":"read keeper-gated-secret-value"}]}'; exit 0 ;;
  *" escalation list "*)
    if [ -n "${EV_ESC:-}" ]; then
      echo '[{"id":"esc_1","type":"CREDENTIAL","status":"PENDING","from_slug":"morgan","metadata":"{\"name\":\"HARNESS_X\"}"}]'
    else echo '[]'; fi; exit 0 ;;
  *" work list "*)
    # Busy with background (issue) work only: a chat-only query sees nothing.
    if [ "${EV_WORK:-free}" = busy ] && [[ "$args" != *" --class chat "* ]]; then echo '{"items":[{"id":"w1","class":"background","state":"running"}]}'; else echo '{"items":[]}'; fi; exit 0 ;;
  *" ask "*)
    if [ "${EV_ASK:-ok}" = partial ]; then
      save=""; prev=""
      for a in "$@"; do [ "$prev" = --save ] && save="$a"; prev="$a"; done
      echo "ops partial" >"$save"; echo "Error: stream interrupted" >&2; exit 1
    fi
    if [ "${EV_ASK:-ok}" = 429 ]; then echo "Error: server returned 429: agent is at capacity" >&2; exit 1; fi
    save=""; prev=""
    for a in "$@"; do [ "$prev" = --save ] && save="$a"; prev="$a"; done
    [ -n "$save" ] && echo "pong" >"$save"; exit 0 ;;
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
  env -u CREWSHIP_SERVER -u SERVER TMPDIR="$ev_dir" CREWSHIP="$ev_dir/crewship" EV_LOG="$ev_dir/log" POLL_INTERVAL=1 "$@"
}
out="$(ev_run bash -c '. "$1/test-harness/lib.sh"; . "$1/test-harness/owned-escalation.sh"; request_credential_via_issue ops morgan HARNESS_X 5; echo "rc=$?"' _ "$SCRIPT_DIR")"
expect_contains "grounded credential ask reports the issue and its run status" "$out" "OPS-7 FAILED"
expect_contains "grounded credential ask returns 0 once the run ended" "$out" "rc=0"
expect_contains "the issue is delegated to the agent in its crew" "$(cat "$ev_dir/log")" "issue create --crew ops --delegate morgan --status TODO"
expect_contains "the issue description names the credential and asks for an escalation" "$(cat "$ev_dir/log")" "metadata.name is exactly HARNESS_X"
expect_contains "the issue is started" "$(cat "$ev_dir/log")" "issue start OPS-7"
out="$(ev_run env EV_RUN=running EV_ESC=1 bash -c '. "$1/test-harness/lib.sh"; . "$1/test-harness/owned-escalation.sh"; request_credential_via_issue ops morgan HARNESS_X 5; echo "rc=$?"' _ "$SCRIPT_DIR")"
expect_contains "a run parked on its escalation counts as the request made" "$out" "OPS-7 ESCALATED"
expect_contains "a parked escalation returns 0 without waiting for the run" "$out" "rc=0"
out="$(ev_run env EV_RUN=running bash -c '. "$1/test-harness/lib.sh"; . "$1/test-harness/owned-escalation.sh"; request_credential_via_issue ops morgan HARNESS_X 2; echo "rc=$?"' _ "$SCRIPT_DIR")"
expect_contains "no escalation and no end -> TIMEOUT, rc 2" "$out" "OPS-7 TIMEOUT"
out="$(ev_run env EV_WORK=busy bash -c '. "$1/test-harness/lib.sh"; wait_agent_free morgan 2; echo "rc=$?"' _ "$SCRIPT_DIR")"
expect_contains "an agent holding background work is not free" "$out" "rc=1"
expect_contains "the open work is named in the failure" "$out" "background"
out="$(ev_run env EV_ASK=429 bash -c '. "$1/test-harness/lib.sh"; r="$(ask_agent alex hi 2>"$TMPDIR/ask.err")"; echo "rc=$? reply=[$r]"; cat "$TMPDIR/ask.err"' _ "$SCRIPT_DIR")"
expect_contains "an injected 429 ask returns non-zero, not an empty success" "$out" "rc=1 reply=[]"
expect_contains "an injected 429 ask names the capacity refusal" "$out" "ASK REFUSED for alex"
out="$(ev_run env EV_ASK=partial bash -c '. "$1/test-harness/lib.sh"; r="$(ask_agent alex hi 2>/dev/null)"; echo "rc=$? reply=[$r]"' _ "$SCRIPT_DIR")"
expect_contains "a nonzero ask with partial stdout keeps its nonzero status" "$out" "rc=1 reply=[ops partial]"
out="$(ev_run bash -c '. "$1/test-harness/lib.sh"; r="$(ask_agent alex hi 2>&1)"; echo "rc=$? reply=[$r]"' _ "$SCRIPT_DIR")"
expect_contains "a successful ask still returns only the reply" "$out" "rc=0 reply=[pong]"
out="$(ev_run bash -c '. "$1/test-harness/lib.sh"; wait_agent_free morgan 2; echo "rc=$?"' _ "$SCRIPT_DIR")"
expect_contains "an agent with no open work is free" "$out" "rc=0"
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
expect_contains "each issue run's journal is kept" "$(cat "$suite_dir/issue-OPS-7-run-run_9.journal.json" 2>/dev/null)" "\"kind\":\"tool\""
expect_not_contains "registered values never reach evidence (journal)" "$(cat "$suite_dir"/*.journal.json 2>/dev/null)" "keeper-gated-secret-value"
sub_det="$ev_dir/subshell"; mkdir -m 700 "$sub_det"
# The suites call issue_work inside $(...); tracking must survive the subshell.
# shellcheck disable=SC2016
ev_run env HARNESS_DETAIL_DIR="$sub_det" bash -c '. "$1/test-harness/lib.sh"; keep_failure_evidence; w="$(issue_work ops morgan t d 5)"; _fail "probe $w"; finish' _ "$SCRIPT_DIR" >/dev/null 2>&1
expect_eq "an issue started inside a command substitution is still in the evidence" "1" "$(find "$sub_det" -name 'issue-OPS-7.json' | wc -l | tr -d ' ')"
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
foreign="$ev_dir/foreign"; mkdir "$foreign"; : >"$foreign/keep"
# shellcheck disable=SC2016
ev_run env _HARNESS_STATE="$foreign" _HARNESS_STATE_OWNER=1 HARNESS_DETAIL_DIR="$pass_det" bash -c '. "$1/test-harness/lib.sh"; keep_failure_evidence; _pass ok; finish' _ "$SCRIPT_DIR" >/dev/null 2>&1
expect_eq "an inherited state path is never deleted" "1" "$( [ -f "$foreign/keep" ] && echo 1 || echo 0)"
expect_eq "finish removes the harness state directory" "" "$(find "$ev_dir" -maxdepth 1 -name 'harness-state.*' 2>/dev/null)"
rm -rf "$ev_dir"

if bash "$SCRIPT_DIR/test-harness/crew-links-evidence-test.sh"; then
  pass "cross-crew completion and refusal evidence rejects false positives"
else
  fail "cross-crew completion and refusal evidence rejects false positives"
fi

# ── Delegation evidence: the real suite against a stub CLI ───────────────────
# No path without a tagged assignment may pass or skip: run-all.sh and the
# nightly job read only the exit status, so a skip here is a false green.
deleg_cli() {
  cat >"$1" <<'EOF'
#!/usr/bin/env bash
state="${DELEG_STATE:?}"
args=" $* "
desc="" prev=""
for a in "$@"; do [ "$prev" = "--description" ] && desc="$a"; prev="$a"; done
created() { printf '{"from_slug":"%s","to_slug":"%s","entry_type":"assignment.created","payload":{"task":"%s","assignment_id":"%s"}}\n' "${DELEG_FROM:-alex}" "$1" "$2" "$3"; }
done_row() { printf '{"to_slug":"sam","entry_type":"assignment.completed","payload":{"outcome":"%s","assignment_id":"%s"}}\n' "$1" "$2"; }
case "$args" in
  *" version "*|*" whoami "*) exit 0 ;;
  *" agent list "*|*" routine list "*) echo row; exit 0 ;;
  *" work list "*)
    [ "$DELEG_MODE" = busy ] && { echo '{"items":[{"id":"busy"}]}'; exit 0; }
    echo '{"items":[]}'; exit 0 ;;
  *" issue create "*)
    [ "$DELEG_MODE" = no-dispatch ] && exit 1
    n=$(( $(cat "$state/n" 2>/dev/null || echo 0) + 1 )); echo "$n" >"$state/n"
    printf '%s' "$desc" | grep -oE 'DELEG-[A-Z0-9]+' | head -1 >"$state/tag"
    printf '{"id":"iss_%s","identifier":"ENG-%s"}\n' "$n" "$n"; exit 0 ;;
  *" issue start "*) exit 0 ;;
  *" issue runs "*)
    [ "$DELEG_MODE" = run-timeout ] && { echo '[]'; exit 0; }
    status=COMPLETED; [ "$DELEG_MODE" = run-failed ] && status=FAILED
    echo '[{"agent_slug":"alex","source":"delegation","status":"'"$status"'","result_summary":"tag quoted"}]'; exit 0 ;;
  *" activity "*)
    tag="$(cat "$state/tag" 2>/dev/null)"; n="$(cat "$state/n" 2>/dev/null || echo 0)"
    reads=$(( $(cat "$state/reads" 2>/dev/null || echo 0) + 1 )); echo "$reads" >"$state/reads"
    case "$DELEG_MODE" in
      proven) created sam "do $tag" a1; done_row SUCCEEDED a1 ;;
      second-try) if [ "$n" -ge 2 ]; then created sam "do $tag" a1; done_row SUCCEEDED a1; fi ;;
      no-row) ;;
      other-task) created sam "do DELEG-OTHER" a1; done_row SUCCEEDED a1 ;;
      other-agent) created robin "do $tag" a1; done_row SUCCEEDED a1 ;;
      other-sender) DELEG_FROM=morgan; created sam "do $tag" a1; done_row SUCCEEDED a1 ;;
      no-completion) created sam "do $tag" a1 ;;
      other-completion) created sam "do $tag" a1; done_row SUCCEEDED a2 ;;
      failed-completion) created sam "do $tag" a1; done_row FAILED a1 ;;
      no-change-completion) created sam "do $tag" a1; done_row NO_CHANGE a1 ;;
      work-created-completion) created sam "do $tag" a1; done_row WORK_CREATED a1 ;;
      feed-down) exit 1 ;;
      completion-feed-down) [ "$reads" -ge 2 ] && exit 1; created sam "do $tag" a1 ;;
      bad-row) created sam "do $tag" a1; done_row SUCCEEDED a1
        printf '{"to_slug":"sam","entry_type":"assignment.completed","payload":"oops"}\n' ;;
    esac
    exit 0 ;;
esac
exit 0
EOF
  chmod +x "$1"
}

run_delegation() { # <mode> [PATH override] — suite output, then its exit status
  local dir; dir="$(mktemp -d -t cs-deleg.XXXXXX)"
  deleg_cli "$dir/crewship"
  env -u CREWSHIP_SERVER -u SERVER CREWSHIP="$dir/crewship" DELEG_STATE="$dir" DELEG_MODE="$1" ASK_TIMEOUT=2 POLL_INTERVAL=1 DELEG_FREE_TIMEOUT=1 \
    ${2:+PATH="$2"} bash "$SCRIPT_DIR/test-harness/test-delegation.sh" 2>&1
  echo "fixture: suite-exit:$?"
  rm -rf "$dir"
}

deleg_expect_fail() { # <mode> <check name> [PATH override]
  local out; out="$(run_delegation "$1" "${3:-}")"
  expect_contains "delegation $1 -> FAIL $2" "$out" "FAIL $2"
  expect_not_contains "delegation $1 -> no skip stands in for proof" "$out" "SKIP lead→peer"
  expect_contains "delegation $1 -> suite exits non-zero" "$out" "fixture: suite-exit:1"
}

if command -v jq >/dev/null 2>&1; then
  out="$(run_delegation proven)"
  expect_contains "delegation proven -> reached sam" "$out" "PASS alex's delegation reached sam"
  expect_contains "delegation proven -> completed" "$out" "PASS the delegated assignment to sam completed"
  expect_not_contains "delegation proven -> no peer-chat check" "$out" "peer chat"
  expect_contains "delegation proven -> suite exits zero" "$out" "fixture: suite-exit:0"
  out="$(run_delegation second-try)"
  expect_contains "delegation on the retry -> reached sam" "$out" "PASS alex's delegation reached sam"
  expect_contains "delegation on the retry -> completed" "$out" "PASS the delegated assignment to sam completed"
  expect_contains "delegation on the retry -> suite exits zero" "$out" "fixture: suite-exit:0"
  for mode in no-row other-task other-agent other-sender feed-down no-dispatch run-failed run-timeout; do
    deleg_expect_fail "$mode" "lead→peer delegation"
  done
  for mode in no-completion other-completion failed-completion no-change-completion work-created-completion completion-feed-down bad-row; do
    deleg_expect_fail "$mode" "the delegated assignment to sam completed"
  done
  deleg_expect_fail busy "alex frees its previous work"
  out="$(run_delegation completion-feed-down)"
  expect_contains "feed outage while polling -> named, not a timeout" "$out" "could not be read while waiting"
  out="$(run_delegation other-task)"
  expect_not_contains "another task's row is not proof" "$out" "PASS alex's delegation"
  out="$(run_delegation bad-row)"
  expect_not_contains "a parser failure leaves no matching id" "$out" "PASS the delegated assignment"

  # jq is the only tool removed, so the outcome is the suite's, not the shell's.
  nojq="$(mktemp -d -t cs-nojq.XXXXXX)"
  for tool in bash cat cut date dirname basename env grep head mktemp rm sed sleep sort tail tr wc; do
    ln -s "$(command -v "$tool")" "$nojq/$tool"
  done
  deleg_expect_fail proven "lead→peer delegation" "$nojq"
  rm -rf "$nojq"

  out="$(XLINK_ASK=partial run_crew_links landed)"
  expect_contains "partial failed ask -> FAIL" "$out" "FAIL alex answers the connections ask"
  expect_not_contains "partial failed ask -> ops is not read" "$out" "PASS alex sees ops"
  out="$(XLINK_BUSY=1 XLINK_FREE_TIMEOUT=1 run_crew_links landed)"
  expect_contains "busy lead -> FAIL before the ask" "$out" "FAIL alex frees its previous work"
  expect_not_contains "busy lead -> the ask is not sent" "$out" "alex sees ops"
fi

echo
if [ "$FAILURES" -eq 0 ]; then
  echo "all test-harness lib checks passed"
else
  echo "$FAILURES check(s) FAILED"
fi
exit $(( FAILURES > 0 ? 1 : 0 ))

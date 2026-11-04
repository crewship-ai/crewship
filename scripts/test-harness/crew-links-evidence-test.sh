#!/usr/bin/env bash
# No server/model: drive the same predicates with boundary-shaped CLI records.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=crew-links-evidence.sh
source "$HERE/crew-links-evidence.sh"
mock_feed=''
mock_runs='[]'
mock_entries='[]'
mock_results='{}'
fail_full_read=0
fail_read=0
cs() {
  (( fail_read == 0 )) || return 1
  case "$1 $2" in
    'activity --since') printf '%s\n' "$mock_feed" ;;
    'issue runs') printf '%s\n' "$mock_runs" ;;
    'issue result')
      (( fail_full_read == 0 )) || return 1
      [[ "$3" == ENG-1 ]] || return 1
      printf '%s\n' "$mock_results" | jq -ec --arg id "$4" '.[$id] // empty'
      ;;
    'journal --run-id') printf '%s\n' "$mock_entries" ;;
    *) return 1 ;;
  esac
}
expect_yes() { "$@" || { echo "expected evidence: $*" >&2; exit 1; }; }
expect_no() { if "$@"; then echo "false evidence: $*" >&2; exit 1; fi; }
created='{"to_slug":"morgan","entry_type":"assignment.created","payload":{"assignment_id":"expected","task":"Read record XLINK-1"}}'
completed='{"to_slug":"morgan","entry_type":"assignment.completed","payload":{"assignment_id":"expected","outcome":"SUCCEEDED"}}'
mock_feed="$created"
expect_no xlink_assignment_completed XLINK-1
mock_feed="$created
$completed"
expect_yes xlink_assignment_completed XLINK-1
for outcome in NO_CHANGE WORK_CREATED; do
  mock_feed="$created
${completed/SUCCEEDED/$outcome}"
  expect_yes xlink_assignment_completed XLINK-1
done
expect_no xlink_assignment_completed XLINK-other
mock_feed="$created
${completed/expected/unrelated}"
expect_no xlink_assignment_completed XLINK-1
mock_feed="$created
${completed/SUCCEEDED/PARTIAL}"
expect_no xlink_assignment_completed XLINK-1
# A real list preview is bounded; the independent code occurs after its end.
# Journal run identity differs from the assignment ID accepted by issue result.
mock_feed="$created
$completed"
printf -v long_prefix '%0250d' 0
mock_results="$(jq -nc --arg result "$long_prefix Read actual OPS-1: VERIFIED-1" '
  {expected:{status:"COMPLETED",outcome:"SUCCEEDED",result_summary:$result},
   unrelated:{status:"COMPLETED",outcome:"SUCCEEDED",result_summary:$result}}')"
mock_runs='[{"id":"expected","agent_slug":"alex","status":"COMPLETED","result_summary":"VERIFIED-1"}]'
expect_no xlink_result_reported ENG-1 VERIFIED-1 XLINK-1
mock_runs='[{"id":"expected","agent_slug":"morgan","status":"FAILED","result_summary":"VERIFIED-1"}]'
expect_no xlink_result_reported ENG-1 VERIFIED-1 XLINK-1
matching_runs="$(jq -nc --arg preview "${long_prefix:0:200}…" '
  [{id:"expected",run_id:"journal-run",agent_slug:"morgan",status:"COMPLETED",result_summary:$preview}]')"
mock_runs="$matching_runs"
expect_yes xlink_result_reported ENG-1 VERIFIED-1 XLINK-1
expect_no xlink_result_reported ENG-1 VERIFIED-other XLINK-1
expect_no xlink_result_reported ENG-1 VERIFIED-1 XLINK-other
# Even a successful Morgan result with the code must belong to THIS tagged assignment.
mock_runs='[{"id":"unrelated","run_id":"expected","agent_slug":"morgan","status":"COMPLETED","result_summary":"VERIFIED-1"}]'
expect_no xlink_result_reported ENG-1 VERIFIED-1 XLINK-1
mock_runs="$matching_runs"
good_results="$mock_results"
mock_results="$(printf '%s\n' "$good_results" | jq '.expected.status="FAILED"')"
expect_no xlink_result_reported ENG-1 VERIFIED-1 XLINK-1
mock_results="$(printf '%s\n' "$good_results" | jq '.expected.outcome="PARTIAL"')"
expect_no xlink_result_reported ENG-1 VERIFIED-1 XLINK-1
mock_results="$(printf '%s\n' "$good_results" | jq '.expected.result_summary=""')"
expect_no xlink_result_reported ENG-1 VERIFIED-1 XLINK-1
mock_results='{}'
expect_no xlink_result_reported ENG-1 VERIFIED-1 XLINK-1
mock_results="$good_results"
fail_full_read=1
expect_no xlink_result_reported ENG-1 VERIFIED-1 XLINK-1
fail_full_read=0
for outcome in NO_CHANGE WORK_CREATED; do
  mock_feed="$created
${completed/SUCCEEDED/$outcome}"
  mock_results="$(printf '%s\n' "$good_results" | jq --arg outcome "$outcome" '.expected.outcome=$outcome')"
  expect_yes xlink_result_reported ENG-1 VERIFIED-1 XLINK-1
done
mock_feed="$created
$completed"
mock_results="$good_results"
mock_runs='[{"agent_slug":"alex","agent_id":"actor","run_id":"source-run","status":"COMPLETED","result_summary":"crews are not connected"}]'
expect_no xlink_refusal_seen ENG-1 eng ops
receipt='{"entry_type":"assignment.refused","trace_id":"source-run","actor_id":"actor","agent_id":"actor","crew_id":"eng","payload":{"reason":"crews_not_connected","http_status":403,"source_crew_id":"eng","target_crew_id":"ops","target_slug":"morgan"}}'
mock_entries="[$receipt]"
expect_yes xlink_refusal_seen ENG-1 eng ops
for mutation in 'source-run/other-run' 'actor/other-actor' 'crews_not_connected/different_reason' '403/400' 'ops/other-crew' 'morgan/other-target'; do
  mock_entries="[$(printf '%s' "$receipt" | sed "s|${mutation%/*}|${mutation#*/}|g")]"
  expect_no xlink_refusal_seen ENG-1 eng ops
done
mock_entries="[$receipt]"
fail_read=1
expect_no xlink_refusal_seen ENG-1 eng ops
expect_no xlink_assignment_completed XLINK-1
expect_no xlink_result_reported ENG-1 VERIFIED-1 XLINK-1
printf 'crew-links evidence: completion, result and refusal boundaries passed\n'

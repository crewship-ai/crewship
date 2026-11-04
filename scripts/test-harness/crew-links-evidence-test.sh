#!/usr/bin/env bash
# No server/model: drive the same predicates with boundary-shaped CLI records.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=crew-links-evidence.sh
source "$HERE/crew-links-evidence.sh"
mock_feed=''
mock_runs='[]'
mock_entries='[]'
fail_read=0
cs() {
  (( fail_read == 0 )) || return 1
  case "$1 $2" in
    'activity --since') printf '%s\n' "$mock_feed" ;;
    'issue runs') printf '%s\n' "$mock_runs" ;;
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
mock_runs='[{"agent_slug":"alex","status":"COMPLETED","result_summary":"VERIFIED-1"}]'
expect_no xlink_result_reported ENG-1 VERIFIED-1
mock_runs='[{"agent_slug":"morgan","status":"FAILED","result_summary":"VERIFIED-1"}]'
expect_no xlink_result_reported ENG-1 VERIFIED-1
mock_runs='[{"agent_slug":"morgan","status":"COMPLETED","result_summary":"Read actual OPS-1: VERIFIED-1"}]'
expect_yes xlink_result_reported ENG-1 VERIFIED-1
expect_no xlink_result_reported ENG-1 VERIFIED-other
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
expect_no xlink_result_reported ENG-1 VERIFIED-1
printf 'crew-links evidence: completion, result and refusal boundaries passed\n'

#!/usr/bin/env bash
# Evidence predicates shared by the live crew-link suite and its fixture test.
# Projection stdout is captured by the caller; boolean wrappers print nothing.
_XLINK_EVIDENCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

xlink_completion_candidates() { # <tag> -> safe activity identities, no task text
  local feed
  feed="$(cs activity --since 30m --lines 200 --export ndjson 2>/dev/null)" || return 1
  printf '%s\n' "$feed" | jq -sc --arg tag "$1" '
    [ .[] | select(.to_slug == "morgan" and .entry_type == "assignment.created")
      | select((.payload.task // "") | contains($tag))
      | select(.payload.assignment_id | type == "string" and length > 0) ] as $created
    | [ .[] | select(.entry_type == "assignment.completed")
      | select(.payload.outcome | . == "SUCCEEDED" or . == "NO_CHANGE" or . == "WORK_CREATED") ] as $completed
    | [ $created[] as $c | $completed[]
      | select(.payload.assignment_id == $c.payload.assignment_id)
      | {assignment_id:.payload.assignment_id, outcome:.payload.outcome,
         created_activity_id:$c.id, completed_activity_id:.id,
         target_agent_id:$c.payload.target_id} ] | unique_by(.assignment_id)' 2>/dev/null
}

xlink_completed_assignment_ids() { # <tag> -> assignment IDs, never journal run IDs
  local candidates
  candidates="$(xlink_completion_candidates "$1")" || return 1
  printf '%s\n' "$candidates" | jq -c 'map(.assignment_id) | unique' 2>/dev/null
}

xlink_assignment_completed() { # <tag>
  local ids
  ids="$(xlink_completed_assignment_ids "$1")" || return 1
  printf '%s\n' "$ids" | jq -e 'length > 0' >/dev/null 2>&1
}

# 0: predicate passed (safe projection when configured); 1: not matched;
# 2: predicate passed but its mandatory receipt could not be projected.
# Raw model output travels only through private shell memory and a pipe.
xlink_result_evidence() { # <issue> <record-code> <tag>
  local runs completed candidates assignment full projected rc
  completed="$(xlink_completion_candidates "$3")" || return 1
  runs="$(cs issue runs "$1" --format json 2>/dev/null)" || return 1
  candidates="$(printf '%s\n' "$runs" | jq -r --argjson completed "$completed" '
    .[] | select(.agent_slug == "morgan" and .status == "COMPLETED")
    | .id | select(type == "string" and length > 0)
    | select(. as $id | $completed | map(.assignment_id) | index($id))' 2>/dev/null)" || return 1
  while IFS= read -r assignment; do
    [[ -n "$assignment" ]] || continue
    # Full result is keyed by assignment .id; the distinct .run_id would 404.
    full="$(cs issue result "$1" "$assignment" --format json 2>/dev/null)" || continue
    # Keep the functional predicate unchanged; receipt errors are separate.
    if ! printf '%s\n' "$full" | jq -e --arg code "$2" '
      .status == "COMPLETED" and
      (.outcome | . == "SUCCEEDED" or . == "NO_CHANGE" or . == "WORK_CREATED") and
      ((.result_summary // "") | contains($code))' >/dev/null 2>&1; then continue; fi
    [[ -n "${HARNESS_DETAIL_DIR:-}" ]] || return 0
    command -v python3 >/dev/null 2>&1 || return 2
    projected="$(printf '%s\0' "$1" "$assignment" "$2" "$3" "$completed" "$runs" "$full" |
      python3 "$_XLINK_EVIDENCE_DIR/crew-links-receipt.py" positive)"
    rc=$?
    if (( rc == 0 )); then printf '%s\n' "$projected"; return 0; fi
    return 2  # The predicate passed; only its receipt projection failed.
  done <<< "$candidates"
  return 1
}

xlink_result_reported() {
  local HARNESS_DETAIL_DIR=""  # Compatibility predicate never writes/depends on retention.
  xlink_result_evidence "$@" >/dev/null
}

xlink_refusal_evidence() { # <issue> <source-crew-id> <target-crew-id>
  local runs source run actor entries row projected rc
  runs="$(cs issue runs "$1" --format json 2>/dev/null)" || return 1
  source="$(printf '%s\n' "$runs" | jq -c '.[] | select(.agent_slug == "alex")' 2>/dev/null)" || return 1
  while IFS= read -r row; do
    [[ -n "$row" ]] || continue
    run="$(printf '%s\n' "$row" | jq -r '.run_id // .trace_id // empty')"
    actor="$(printf '%s\n' "$row" | jq -r '.agent_id // empty')"
    [[ -n "$run" && -n "$actor" ]] || continue
    entries="$(cs journal --run-id "$run" --type assignment.refused --lines 100 --format json 2>/dev/null)" || return 1
    if ! printf '%s\n' "$entries" | jq -e --arg run "$run" --arg actor "$actor" --arg source "$2" --arg target "$3" '
      any(.[]; .entry_type == "assignment.refused" and .trace_id == $run and
        .actor_id == $actor and .agent_id == $actor and .crew_id == $source and
        .payload.reason == "crews_not_connected" and .payload.http_status == 403 and
        .payload.source_crew_id == $source and .payload.target_crew_id == $target and
        .payload.target_slug == "morgan")' >/dev/null 2>&1; then continue; fi
    [[ -n "${HARNESS_DETAIL_DIR:-}" ]] || return 0
    command -v python3 >/dev/null 2>&1 || return 2
    projected="$(printf '%s\0' "$1" "$2" "$3" "$row" "$entries" |
      python3 "$_XLINK_EVIDENCE_DIR/crew-links-receipt.py" refusal)"
    rc=$?
    if (( rc == 0 )); then printf '%s\n' "$projected"; return 0; fi
    return 2  # Actual server refusal passed; retention projection failed.
  done <<< "$source"
  return 1
}

xlink_refusal_seen() {
  local HARNESS_DETAIL_DIR=""
  xlink_refusal_evidence "$@" >/dev/null
}

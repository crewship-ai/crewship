#!/usr/bin/env bash
# Evidence predicates shared by the live crew-link suite and its fixture test.
# The caller supplies cs() and jq. Read failures never count as a refusal.

xlink_completed_assignment_ids() { # <tag> -> JSON array, not journal run IDs
  local feed
  feed="$(cs activity --since 30m --lines 200 --export ndjson 2>/dev/null)" || return 1
  printf '%s\n' "$feed" | jq -sc --arg tag "$1" '
    [ .[] | select(.to_slug == "morgan" and .entry_type == "assignment.created")
      | select((.payload.task // "") | contains($tag))
      | .payload.assignment_id | select(type == "string" and length > 0) ] as $ids
    | [ .[] | select(.entry_type == "assignment.completed")
      | select(.payload.outcome | . == "SUCCEEDED" or . == "NO_CHANGE" or . == "WORK_CREATED")
      | .payload.assignment_id | select(type == "string" and length > 0)
      | select(. as $id | $ids | index($id)) ] | unique' 2>/dev/null
}

xlink_assignment_completed() { # <tag>
  local ids
  ids="$(xlink_completed_assignment_ids "$1")" || return 1
  printf '%s\n' "$ids" | jq -e 'length > 0' >/dev/null 2>&1
}

xlink_result_reported() { # <issue> <record-code> <tag>
  local runs completed candidates assignment full
  completed="$(xlink_completed_assignment_ids "$3")" || return 1
  runs="$(cs issue runs "$1" --format json 2>/dev/null)" || return 1
  candidates="$(printf '%s\n' "$runs" | jq -r --argjson completed "$completed" '
    .[] | select(.agent_slug == "morgan" and .status == "COMPLETED")
    | .id | select(type == "string" and length > 0)
    | select(. as $id | $completed | index($id))' 2>/dev/null)" || return 1
  while IFS= read -r assignment; do
    [[ -n "$assignment" ]] || continue
    # The list preview is deliberately bounded. The full-result endpoint is
    # keyed by assignment .id, not .run_id (the distinct journal identity).
    full="$(cs issue result "$1" "$assignment" --format json 2>/dev/null)" || continue
    if printf '%s\n' "$full" | jq -e --arg code "$2" '
      .status == "COMPLETED" and
      (.outcome | . == "SUCCEEDED" or . == "NO_CHANGE" or . == "WORK_CREATED") and
      ((.result_summary // "") | contains($code))' >/dev/null 2>&1; then return 0; fi
  done <<< "$candidates"
  return 1
}

xlink_refusal_seen() { # <issue> <source-crew-id> <target-crew-id>
  local runs source run actor entries
  runs="$(cs issue runs "$1" --format json 2>/dev/null)" || return 1
  source="$(printf '%s\n' "$runs" | jq -r '.[] | select(.agent_slug == "alex")
    | [(.run_id // .trace_id // ""), (.agent_id // "")] | @tsv' 2>/dev/null)" || return 1
  while IFS=$'\t' read -r run actor; do
    [[ -n "$run" && -n "$actor" ]] || continue
    entries="$(cs journal --run-id "$run" --type assignment.refused --lines 100 --format json 2>/dev/null)" || return 1
    if printf '%s\n' "$entries" | jq -e --arg run "$run" --arg actor "$actor" --arg source "$2" --arg target "$3" '
      any(.[]; .entry_type == "assignment.refused" and .trace_id == $run and
        .actor_id == $actor and .agent_id == $actor and .crew_id == $source and
        .payload.reason == "crews_not_connected" and .payload.http_status == 403 and
        .payload.source_crew_id == $source and .payload.target_crew_id == $target and
        .payload.target_slug == "morgan")' >/dev/null 2>&1; then return 0; fi
  done <<< "$source"
  return 1
}

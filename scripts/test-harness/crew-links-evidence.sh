#!/usr/bin/env bash
# Evidence predicates shared by the live crew-link suite and its fixture test.
# The caller supplies cs() and jq. Read failures never count as a refusal.

xlink_assignment_completed() { # <tag>
  local feed
  feed="$(cs activity --since 30m --lines 200 --export ndjson 2>/dev/null)" || return 1
  printf '%s\n' "$feed" | jq -se --arg tag "$1" '
    [ .[] | select(.to_slug == "morgan" and .entry_type == "assignment.created")
      | select((.payload.task // "") | contains($tag))
      | .payload.assignment_id | select(type == "string" and length > 0) ] as $ids
    | any(.[]; .entry_type == "assignment.completed" and
        (.payload.outcome | . == "SUCCEEDED" or . == "NO_CHANGE" or . == "WORK_CREATED") and
        (.payload.assignment_id as $id | $ids | index($id)) != null)' >/dev/null 2>&1
}

xlink_result_reported() { # <issue> <record-code>
  local runs
  runs="$(cs issue runs "$1" --format json 2>/dev/null)" || return 1
  printf '%s\n' "$runs" | jq -e --arg code "$2" '
    any(.[]; .agent_slug == "morgan" and .status == "COMPLETED" and
      ((.result_summary // "") | contains($code)))' >/dev/null 2>&1
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

import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"

// The last fire of a schedule (`last_status`) in the reader's words, as of
// that fire: the schedule is not updated when a queued start later runs,
// expires or is cancelled, so every label is in the past tense.
//
// Capacity outcomes are admission results, not failures: the first
// occurrence that meets a full routine waits in the queue (QUEUED), and while
// it waits every later occurrence is recorded as SKIPPED with
// last_missed_count raised. After scheduler downtime a catch-up backlog also
// lands in last_missed_count (catch-up `skip`, or an inactive routine with a
// backlog), and nothing in the schedule tells that apart from a capacity skip
// (#4490), so a counted SKIPPED names both causes.
//
// An uncounted SKIPPED is an inactive routine or, for an active one with
// `concurrency_policy: skip`, a tick dropped because the previous run was
// still going. The routine's lifecycle now is the only hint: an inactive
// routine was inactive then, while an active one may have been either.
// Other statuses keep the server's word.
export function scheduleLastStatusLabel(
  s: Pick<PipelineSchedule, "last_status" | "last_missed_count">,
  lifecycle: "active" | "proposed" | "disabled",
): string | undefined {
  const status = s.last_status
  if (!status) return undefined
  if (status === "QUEUED") return "queued for capacity"
  if (status === "SKIPPED") {
    if ((s.last_missed_count ?? 0) > 0) return "skipped: capacity or catch-up"
    return lifecycle === "active" ? "skipped: previous run busy or routine inactive" : "skipped: routine inactive"
  }
  return status
}

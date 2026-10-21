import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"

// The last fire of a schedule (`last_status`) in the reader's words. Capacity
// outcomes are admission results, not failures: the first occurrence that
// meets a full routine waits in the queue (QUEUED), and while it waits every
// later occurrence is recorded as SKIPPED with last_missed_count raised. An
// inactive routine is also SKIPPED, with no occurrence counted as missed.
// Other statuses keep the server's word, as the tab showed them before.
export function scheduleLastStatusLabel(
  s: Pick<PipelineSchedule, "last_status" | "last_missed_count">,
): string | undefined {
  const status = s.last_status
  if (!status) return undefined
  if (status === "QUEUED") return "Waiting for capacity"
  if (status === "SKIPPED") {
    return (s.last_missed_count ?? 0) > 0 ? "Skipped – a run is already waiting" : "Routine inactive"
  }
  return status
}

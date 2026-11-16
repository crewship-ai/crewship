import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"

const SKIP_LABELS: Record<string, string> = {
  capacity: "skipped: capacity",
  catchup: "skipped: catch-up",
  inactive: "skipped: routine inactive",
  busy: "skipped: previous run busy",
}

// Last-fire telemetry describes admission then, not whether work is waiting
// now. Neither today's routine lifecycle nor a missed-count implies a cause.
// Older servers/rows and future reason values retain a neutral fallback.
export function scheduleLastStatusLabel(
  s: Pick<PipelineSchedule, "last_status" | "last_skip_reason" | "last_missed_count">,
): string | undefined {
  const status = s.last_status
  if (!status) return undefined
  if (status === "QUEUED") return "queued for capacity"
  if (status === "SKIPPED") {
    return s.last_skip_reason && Object.hasOwn(SKIP_LABELS, s.last_skip_reason)
      ? SKIP_LABELS[s.last_skip_reason]
      : "skipped: reason unavailable"
  }
  return status
}

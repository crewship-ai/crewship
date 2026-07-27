import Link from "next/link"
import { Calendar, CircleDot, ScrollText, Webhook } from "lucide-react"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"

// SourcePill renders a chip linking a run back to its trigger — an issue
// identifier (clickable to /issues), a schedule, a webhook, a parent pipeline,
// or a manual run. The user's mental model is "this happened because X"; the
// pill is the X.
//
// Single source of truth for run provenance, shared by the /activity RunsView
// AND the header Activity dropdown rows (LiveRunRow / RecentRunRow) so a run
// never appears in the Activity Bar without saying where it came from (#1418
// follow-up: "I don't know what it came from").
export function SourcePill({ run }: { run: PipelineRun }) {
  if (run.triggered_via === "issue" && run.issue_identifier) {
    return (
      <Link
        href={`/issues/${encodeURIComponent(run.issue_identifier)}`}
        onClick={(e) => e.stopPropagation()}
        className="rounded bg-primary/15 px-1.5 py-0.5 text-[10px] font-medium text-primary hover:bg-primary/25"
      >
        <CircleDot className="mr-1 inline h-2.5 w-2.5" />
        {run.issue_identifier}
      </Link>
    )
  }
  if (run.triggered_via === "schedule") {
    return (
      <span className="rounded bg-purple/15 px-1.5 py-0.5 text-[10px] font-medium text-purple">
        <Calendar className="mr-1 inline h-2.5 w-2.5" />
        schedule
      </span>
    )
  }
  if (run.triggered_via === "webhook") {
    return (
      <span className="rounded bg-warn/15 px-1.5 py-0.5 text-[10px] font-medium text-warn">
        <Webhook className="mr-1 inline h-2.5 w-2.5" />
        webhook
      </span>
    )
  }
  if (run.triggered_via === "call_pipeline") {
    return (
      <span className="rounded bg-white/[0.08] px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground">
        <ScrollText className="mr-1 inline h-2.5 w-2.5" />
        sub-run
      </span>
    )
  }
  return (
    <span className="rounded bg-white/[0.06] px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground">
      manual
    </span>
  )
}

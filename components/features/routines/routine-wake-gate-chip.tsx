"use client"

import { Radio } from "lucide-react"
import { Badge } from "@/components/ui/badge"

// WakeGateChip — surfaces a schedule's wake gate (v115): the schedule
// runs a token-zero probe routine (`wake_pipeline_id`) first, and only
// invokes the real target routine when the probe signals "wake". Renders
// nothing when the schedule has no wake gate wired.
export function WakeGateChip({ wakePipelineSlug }: { wakePipelineSlug?: string }) {
  if (!wakePipelineSlug) return null
  return (
    <Badge
      variant="outline"
      className="gap-1 border-violet-500/30 bg-violet-500/10 px-1.5 py-0 text-[10px] font-medium text-violet-400"
      title="Wake gate: runs a token-zero probe first; the agent only wakes when the gate opens."
    >
      <Radio className="h-2.5 w-2.5" aria-hidden />
      {`Wake gate: ${wakePipelineSlug}`}
    </Badge>
  )
}

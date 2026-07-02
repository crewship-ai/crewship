"use client"

import { SlidersHorizontal } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import type { PipelineStepOverride } from "@/hooks/use-pipeline-step-overrides"

// StepOverrideChip — surfaces the v121 runtime override layer: an
// operator can pin a prompt or model tier for a single step without
// bumping the routine version (internal/api/pipeline_step_overrides.go).
// Renders nothing when the step has no override.
export function StepOverrideChip({ override }: { override?: PipelineStepOverride }) {
  if (!override || (!override.model_override && !override.prompt)) return null
  const label = override.model_override ? `model: ${override.model_override}` : "prompt override"
  const details: string[] = []
  if (override.model_override) details.push(`model overridden to "${override.model_override}"`)
  if (override.prompt) details.push("prompt overridden for this step")
  return (
    <Badge
      variant="outline"
      className="gap-1 border-amber-500/30 bg-amber-500/10 px-1.5 py-0 text-[9.5px] font-medium text-amber-400"
      title={`Step override — ${details.join(" · ")}. Applied at run start, over the versioned routine definition.`}
    >
      <SlidersHorizontal className="h-2.5 w-2.5" aria-hidden />
      {label}
    </Badge>
  )
}

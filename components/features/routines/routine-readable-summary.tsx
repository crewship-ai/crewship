"use client"

import { Clock, Puzzle } from "lucide-react"
import { cn } from "@/lib/utils"
import { integrationLabel } from "@/lib/integration-labels"
import { describeRoutine, type ReadableStep } from "@/lib/routine-readable"

// RoutineReadableSummary — renders a routine's DSL as a plain-language
// step list (trigger + one row per step), so a user understands what a
// routine does without reading raw JSON. Backed by the pure
// lib/routine-readable renderer; shared by the Overview tab and the
// describe-first authoring previews.

const STEP_TONE: Record<ReadableStep["kind"], string> = {
  trigger: "bg-amber-500/15 text-amber-400",
  agent_run: "bg-violet-500/15 text-violet-300",
  http: "bg-cyan-500/15 text-cyan-300",
  transform: "bg-blue-500/15 text-blue-300",
  wait: "bg-amber-500/15 text-amber-300",
  code: "bg-emerald-500/15 text-emerald-300",
  call_pipeline: "bg-fuchsia-500/15 text-fuchsia-300",
  unknown: "bg-white/[0.06] text-muted-foreground",
}

export function RoutineReadableSummary({
  definition,
  className,
}: {
  definition: unknown
  className?: string
}) {
  const r = describeRoutine(definition)

  return (
    <div className={cn("space-y-3", className)}>
      {/* Trigger line */}
      <div className="flex items-start gap-2.5 px-3 py-2.5">
        <span className="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-md bg-amber-500/15">
          <Clock className="h-3 w-3 text-amber-400" aria-hidden />
        </span>
        <div className="min-w-0">
          <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
            Trigger
          </div>
          <div className="text-[13px] text-foreground/90">{r.trigger}</div>
        </div>
      </div>

      {/* Steps */}
      {r.steps.length === 0 ? (
        <div className="px-3 py-4 text-center text-xs text-muted-foreground">
          No steps declared.
        </div>
      ) : (
        <ol className="divide-y divide-border/40">
          {r.steps.map((step) => (
            <li key={step.position} className="flex items-start gap-2.5 px-3 py-2.5">
              <span
                className={cn(
                  "mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-md font-mono text-[10px] font-semibold",
                  STEP_TONE[step.kind],
                )}
                aria-hidden
              >
                {step.position}
              </span>
              <div className="min-w-0 flex-1">
                <div className="text-[13px] text-foreground/90">{step.title}</div>
                {step.detail && (
                  <div className="mt-0.5 line-clamp-2 text-[12px] leading-relaxed text-muted-foreground">
                    {step.detail}
                  </div>
                )}
                {step.technical && (
                  <div className="mt-0.5 truncate font-mono text-[10px] text-muted-foreground-soft">
                    {step.technical}
                  </div>
                )}
              </div>
            </li>
          ))}
        </ol>
      )}

      {/* Integrations */}
      {r.integrations.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 border-t border-border/40 px-3 py-2.5">
          <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
            Needs
          </span>
          {r.integrations.map((slug) => (
            <span
              key={slug}
              className="inline-flex items-center gap-1.5 rounded-full border border-border/60 bg-white/[0.04] px-2 py-0.5 text-[11px] font-medium text-foreground/90"
              title={slug}
            >
              <Puzzle className="h-3 w-3 text-cyan-400" aria-hidden />
              {integrationLabel(slug)}
            </span>
          ))}
        </div>
      )}
    </div>
  )
}

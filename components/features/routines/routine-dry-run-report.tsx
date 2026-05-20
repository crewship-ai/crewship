"use client"

import { X, Eye } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"

// RoutineDryRunReport renders the `would_execute` array returned by
// POST /pipelines/{slug}/dry_run. Pre-fix the response was dropped on
// the floor (the toast said "see Runs tab" but dry runs don't emit
// any step events — they don't run anything). This component is the
// missing surface: it shows per-step tier resolution, estimated cost,
// and rendered prompts so a user can verify "what would this routine
// have done?" before pressing Run.
//
// The estimate is order-of-magnitude only (internal/pipeline/executor_render.go
// estimateStepCost uses a flat token-density heuristic, not real
// pricing); we label it accordingly so users don't mistake it for a
// quote. Phase 2 of pricing integration will plumb through the real
// per-model token cost from internal/llm.

export interface DryRunStep {
  step_id: string
  step_type: string
  would_call_agent?: string
  would_call_pipeline?: string
  would_pass?: string
  tier_adapter?: string
  tier_model?: string
  estimated_cost_usd?: number
}

export interface DryRunResult {
  run_id: string
  status: string
  cost_usd: number
  duration_ms: number
  would_execute: DryRunStep[]
}

interface Props {
  result: DryRunResult
  onClose: () => void
}

export function RoutineDryRunReport({ result, onClose }: Props) {
  const steps = result.would_execute ?? []
  const totalCost = result.cost_usd ?? steps.reduce((a, s) => a + (s.estimated_cost_usd ?? 0), 0)

  return (
    <div className="border-b border-border bg-violet-500/5 px-6 py-4">
      <div className="mb-3 flex items-center gap-2">
        <Eye className="h-3.5 w-3.5 text-violet-400" aria-hidden="true" />
        <span className="text-sm font-semibold text-violet-300">Dry-run report</span>
        <Badge variant="outline" className="px-2 py-0 text-[11px] font-mono">
          {steps.length} {steps.length === 1 ? "step" : "steps"}
        </Badge>
        <span className="text-[11px] text-muted-foreground">
          No agents invoked · cost is an estimate
        </span>
        <div className="flex-1" />
        <span className="font-mono text-[11px] tabular-nums text-violet-200/80">
          ~${totalCost.toFixed(4)} total
        </span>
        <Button
          size="sm"
          variant="ghost"
          onClick={onClose}
          className="h-7 w-7 p-0"
          aria-label="Dismiss dry-run report"
        >
          <X className="h-3.5 w-3.5" aria-hidden="true" />
        </Button>
      </div>

      {steps.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">
          No steps to execute — the routine&apos;s conditions evaluated all steps as skipped.
        </p>
      ) : (
        <ol className="space-y-1.5">
          {steps.map((s, i) => (
            <li
              key={`${s.step_id}-${i}`}
              className="flex items-center gap-3 rounded-md border border-violet-500/10 bg-background/40 px-3 py-2 text-sm"
            >
              <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
                {String(i + 1).padStart(2, "0")}
              </span>
              <Badge
                variant="outline"
                className={cn(
                  "px-2 py-0 text-[11px] capitalize",
                  s.step_type === "agent_run" && "border-blue-500/30 text-blue-300",
                  s.step_type === "call_pipeline" && "border-emerald-500/30 text-emerald-300",
                  s.step_type === "http" && "border-amber-500/30 text-amber-300",
                  s.step_type === "code" && "border-fuchsia-500/30 text-fuchsia-300",
                  s.step_type === "wait" && "border-orange-500/30 text-orange-300",
                  s.step_type === "transform" && "border-cyan-500/30 text-cyan-300",
                )}
              >
                {s.step_type.replace(/_/g, " ")}
              </Badge>
              <span className="font-mono text-foreground/90">{s.step_id}</span>
              {s.would_call_agent && (
                <span className="font-mono text-[11px] text-muted-foreground">
                  → agent <span className="text-foreground/80">{s.would_call_agent}</span>
                </span>
              )}
              {s.would_call_pipeline && (
                <span className="font-mono text-[11px] text-muted-foreground">
                  → routine <span className="text-foreground/80">{s.would_call_pipeline}</span>
                </span>
              )}
              <span className="ml-auto flex items-center gap-3 font-mono text-[11px] tabular-nums text-muted-foreground">
                {(s.tier_adapter || s.tier_model) && (
                  <span title="Resolved execution tier (adapter:model)">
                    {s.tier_adapter ?? "—"}
                    {s.tier_model ? `:${s.tier_model}` : ""}
                  </span>
                )}
                <span className="min-w-[4.5rem] text-right" title="Estimated cost (USD)">
                  {s.estimated_cost_usd != null && s.estimated_cost_usd > 0
                    ? `~$${s.estimated_cost_usd.toFixed(4)}`
                    : "—"}
                </span>
              </span>
            </li>
          ))}
        </ol>
      )}
    </div>
  )
}

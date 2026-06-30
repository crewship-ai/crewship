"use client"

import { X, Eye, Info, ArrowRight } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import type { RoutineManifest } from "@/lib/routine-flow"
import { isManifestEmpty } from "@/lib/routine-manifest"
import { RoutineTouches } from "./routine-touches"

// RoutineDryRunReport renders the response of POST /pipelines/{slug}/dry_run.
//
// A dry run is an HONEST static plan — it does NOT invoke agents and is NOT a
// proof of success. It walks the DSL, renders templates, and reports:
//   • would_execute — the per-step plan (which agent/routine each step would
//     call, the resolved execution tier, an order-of-magnitude cost estimate);
//   • manifest — the routine's DECLARED blast radius (the integrations,
//     datastores, tools, egress hosts, agents and sub-routines it can reach).
//
// We surface both, clearly framed as "what this routine *intends* to do".
// The ACTUAL tool calls only exist after a real Run, in that run's trace.
//
// The cost estimate is order-of-magnitude only (internal/pipeline/
// executor_render.go estimateStepCost uses a flat token-density heuristic,
// not real pricing); it's labelled accordingly so users don't mistake it for
// a quote.

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
  // cost_usd / duration_ms are optional because a server that doesn't
  // populate them (older builds, partial dry-run reports) should NOT
  // be papered over as "$0.0000" — that string is indistinguishable
  // from a valid zero-cost run. Treating "unknown" as 0 hides a real
  // shape issue with the response; leave it undefined and let the
  // renderer fall back to the per-step sum.
  cost_usd?: number
  duration_ms?: number
  would_execute: DryRunStep[]
  // manifest is the routine's declared blast radius — the union of declared
  // resources and what's inferable from the step graph (integrations, egress,
  // credentials, agents, sub-routines, datastores, tools, plus has_http /
  // has_code). The dry run reports it so a reviewer sees "what this routine is
  // *allowed* to touch" without running it. Absent on older server builds.
  manifest?: RoutineManifest
}

interface Props {
  result: DryRunResult
  onClose: () => void
}

export function RoutineDryRunReport({ result, onClose }: Props) {
  const steps = result.would_execute ?? []
  const totalCost = result.cost_usd ?? steps.reduce((a, s) => a + (s.estimated_cost_usd ?? 0), 0)
  const hasManifest = !isManifestEmpty(result.manifest)

  return (
    <div className="border-b border-border bg-violet-500/5 px-6 py-4">
      <div className="mb-3 flex items-center gap-2">
        <Eye className="h-3.5 w-3.5 text-violet-400" aria-hidden="true" />
        <span className="text-sm font-semibold text-violet-300">Plan preview</span>
        <Badge variant="outline" className="px-2 py-0 text-[11px] font-mono">
          {steps.length} {steps.length === 1 ? "step" : "steps"}
        </Badge>
        <div className="flex-1" />
        <span className="font-mono text-[11px] tabular-nums text-violet-200/80">
          ~${totalCost.toFixed(4)} est.
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

      {/* Honesty note — a dry run is a static plan, not an execution and not a
          pass/fail proof. Lead with this so nobody reads the green-looking
          report as "it worked". */}
      <div className="mb-3 flex items-start gap-2 rounded-md border border-violet-500/20 bg-violet-500/[0.06] px-3 py-2">
        <Info className="mt-[1px] h-3.5 w-3.5 shrink-0 text-violet-300" aria-hidden="true" />
        <p className="text-[12px] leading-relaxed text-violet-100/80">
          <span className="font-medium text-violet-200">
            Plan preview — does not run agents or prove success.
          </span>{" "}
          This shows what the routine <em>would</em> do and what it can touch, computed by walking
          the DSL. No agents are invoked, nothing is written, and a clean preview is not a
          guarantee the real run succeeds. Cost is an order-of-magnitude estimate.
        </p>
      </div>

      {/* Step plan — the would_execute walk. */}
      <div className="mb-1 text-[10.5px] font-medium uppercase tracking-wide text-muted-foreground-soft">
        Step plan
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

      {/* Would use — the DECLARED manifest (blast radius). Reuses the same
          brand-logo chip treatment as the detail page's "What it touches" so
          the two surfaces read as one. This is declared intent, not observed
          calls. */}
      <div className="mt-4 border-t border-violet-500/10 pt-3">
        <div className="mb-1 text-[10.5px] font-medium uppercase tracking-wide text-muted-foreground-soft">
          Would use
        </div>
        {hasManifest ? (
          <RoutineTouches manifest={result.manifest} />
        ) : (
          <p className="px-1 py-1 text-[12px] text-muted-foreground">
            This routine declares no external resources.
          </p>
        )}
        <p className="mt-2 flex items-center gap-1.5 px-1 text-[11px] text-muted-foreground-soft">
          <ArrowRight className="h-3 w-3 shrink-0" aria-hidden="true" />
          This is the routine&apos;s declared intent. The actual tool calls appear in the run trace
          after a real Run.
        </p>
      </div>
    </div>
  )
}

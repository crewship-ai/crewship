"use client"

import { useEffect, useState } from "react"
import {
  AlertCircle,
  Check,
  ChevronDown,
  ChevronRight,
  Clock,
  Loader2,
  PauseCircle,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { Skeleton } from "@/components/ui/skeleton"

// WaitpointRunDetail fetches the underlying pipeline run + its
// definition and renders a step-by-step progress view: green checks
// for completed steps, the paused-here marker on the current step,
// dim circles for steps that haven't run yet, plus expandable JSON
// outputs for any step that has produced one.
//
// Two fetches because the data lives in two surfaces — pipeline_runs
// has the runtime state (current_step_id, step_outputs) while the
// pipeline definition has the step list. We don't denormalize the
// definition into pipeline_runs because the DSL evolves; the run
// row stores pipeline_version so on rollback we can still resolve
// the historical structure (deferred to v2).

interface RunResponse {
  id: string
  workspace_id: string
  pipeline_id: string
  pipeline_slug: string
  status: string
  current_step_id: string
  step_outputs: Record<string, unknown> | null
  output: string
  started_at: string
  ended_at: string
  error_message: string
  failed_at_step: string
  triggered_via: string
  triggered_by_id: string
  inputs: Record<string, unknown> | null
}

interface DSLStep {
  id: string
  type: string
  agent_slug?: string
  prompt?: string
  wait?: { kind: string; approval_prompt?: string }
}

interface PipelineDetail {
  id: string
  slug: string
  name: string
  definition?: { name?: string; steps?: DSLStep[] }
}

export function WaitpointRunDetail({
  workspaceId,
  pipelineRunId,
}: {
  workspaceId: string
  pipelineRunId: string
}) {
  const [run, setRun] = useState<RunResponse | null>(null)
  const [pipeline, setPipeline] = useState<PipelineDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    async function load() {
      setLoading(true)
      setError(null)
      try {
        const runRes = await fetch(
          `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-runs/${encodeURIComponent(pipelineRunId)}`,
        )
        if (!runRes.ok) {
          if (cancelled) return
          setError(`run lookup: ${runRes.status}`)
          setLoading(false)
          return
        }
        const runData: RunResponse = await runRes.json()
        if (cancelled) return
        setRun(runData)

        if (runData.pipeline_slug) {
          const pipeRes = await fetch(
            `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(runData.pipeline_slug)}`,
          )
          if (pipeRes.ok && !cancelled) {
            const pipeData: PipelineDetail = await pipeRes.json()
            setPipeline(pipeData)
          }
        }
      } catch (e) {
        if (cancelled) return
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    load()
    return () => { cancelled = true }
  }, [workspaceId, pipelineRunId])

  if (loading) {
    return (
      <div className="space-y-2 rounded-md border border-white/[0.06] bg-card/30 p-3">
        <Skeleton className="h-4 w-32" />
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-12 w-full" />
      </div>
    )
  }
  if (error) {
    return (
      <div className="rounded-md border border-rose-500/30 bg-rose-500/10 p-3 text-xs text-rose-300">
        Could not load run: {error}
      </div>
    )
  }
  if (!run) {
    return (
      <div className="rounded-md border border-white/[0.06] bg-card/30 p-3 text-xs text-muted-foreground">
        Run not found — it may have been pruned.
      </div>
    )
  }

  const steps = pipeline?.definition?.steps ?? []
  const currentStepID = run.current_step_id
  const currentIdx = steps.findIndex((s) => s.id === currentStepID)
  const completedSteps = Object.keys(run.step_outputs ?? {})

  // Step status:
  //   - in step_outputs → completed
  //   - id === current_step_id → paused (the wait fired here)
  //   - not yet run → pending
  function stepStatus(step: DSLStep, idx: number): "done" | "paused" | "pending" {
    if (completedSteps.includes(step.id)) return "done"
    if (step.id === currentStepID || (currentIdx === -1 && idx === completedSteps.length)) {
      return "paused"
    }
    return "pending"
  }

  return (
    <div className="space-y-3">
      {/* Run summary header */}
      <div className="flex items-center justify-between rounded-md border border-white/[0.06] bg-card/30 px-3 py-2">
        <div className="flex items-center gap-2">
          <PauseCircle className="h-4 w-4 text-amber-400" />
          <div>
            <div className="text-xs font-medium">{pipeline?.name || run.pipeline_slug}</div>
            <div className="font-mono text-[10px] text-muted-foreground">
              {run.id} · {run.status}
            </div>
          </div>
        </div>
        <a
          href={`/activity?run=${encodeURIComponent(run.id)}`}
          className="text-[10px] text-blue-400 hover:underline"
        >
          Open in Activity →
        </a>
      </div>

      {/* Step progression */}
      {steps.length > 0 ? (
        <div className="rounded-md border border-white/[0.06] bg-card/30">
          <div className="border-b border-white/[0.06] px-3 py-2 text-[10px] font-medium uppercase tracking-wider text-muted-foreground/60">
            Progress · paused at step {Math.max(currentIdx, completedSteps.length) + 1} of {steps.length}
          </div>
          <ol className="divide-y divide-white/[0.04]">
            {steps.map((step, idx) => {
              const status = stepStatus(step, idx)
              const stepOutput = run.step_outputs?.[step.id]
              return (
                <StepRow
                  key={step.id}
                  step={step}
                  index={idx + 1}
                  status={status}
                  output={stepOutput}
                />
              )
            })}
          </ol>
        </div>
      ) : (
        <div className="rounded-md border border-white/[0.06] bg-card/30 p-3 text-xs text-muted-foreground">
          Pipeline definition unavailable; showing accumulated outputs only.
        </div>
      )}

      {/* Inputs panel */}
      {run.inputs && Object.keys(run.inputs).length > 0 && (
        <CollapsiblePanel title="Inputs">
          <pre className="overflow-auto p-2 font-mono text-[11px]">{JSON.stringify(run.inputs, null, 2)}</pre>
        </CollapsiblePanel>
      )}
    </div>
  )
}

function StepRow({
  step,
  index,
  status,
  output,
}: {
  step: DSLStep
  index: number
  status: "done" | "paused" | "pending"
  output: unknown
}) {
  const [expanded, setExpanded] = useState(status === "paused")

  return (
    <li>
      <button
        onClick={() => output != null && setExpanded((v) => !v)}
        disabled={output == null}
        className={cn(
          "flex w-full items-center gap-2 px-3 py-2 text-left transition-colors",
          output != null && "hover:bg-white/[0.02]",
          status === "paused" && "bg-amber-500/5",
        )}
      >
        <StatusBadge status={status} />
        <span className="font-mono text-[10px] text-muted-foreground/60">{index}.</span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="text-xs font-medium">{step.id}</span>
            <span className="rounded bg-white/[0.06] px-1.5 py-0.5 text-[9px] font-mono text-muted-foreground">
              {step.type}
            </span>
            {step.type === "wait" && step.wait?.approval_prompt && (
              <span className="truncate text-[10px] text-muted-foreground">
                — {step.wait.approval_prompt}
              </span>
            )}
            {step.type === "agent_run" && step.agent_slug && (
              <span className="truncate text-[10px] text-muted-foreground">
                — {step.agent_slug}
              </span>
            )}
          </div>
        </div>
        {output != null && (
          <span className="text-muted-foreground/40">
            {expanded ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
          </span>
        )}
      </button>
      {expanded && output != null && (
        <div className="border-t border-white/[0.04] bg-card/20">
          <pre className="overflow-auto px-3 py-2 font-mono text-[11px] text-foreground/80">
            {typeof output === "string" ? output : JSON.stringify(output, null, 2)}
          </pre>
        </div>
      )}
    </li>
  )
}

function StatusBadge({ status }: { status: "done" | "paused" | "pending" }) {
  if (status === "done") {
    return (
      <span className="flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-emerald-500/20 text-emerald-400">
        <Check className="h-2.5 w-2.5" />
      </span>
    )
  }
  if (status === "paused") {
    return (
      <span className="flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-amber-500/30 text-amber-400">
        <Clock className="h-2.5 w-2.5 animate-pulse" />
      </span>
    )
  }
  return <span className="h-4 w-4 shrink-0 rounded-full border border-muted-foreground/30" />
}

function CollapsiblePanel({ title, children }: { title: string; children: React.ReactNode }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="overflow-hidden rounded-md border border-white/[0.06] bg-card/30">
      <button
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 px-3 py-2 text-[10px] font-medium uppercase tracking-wider text-muted-foreground hover:bg-white/[0.02]"
      >
        {open ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
        {title}
      </button>
      {open && <div className="border-t border-white/[0.06]">{children}</div>}
    </div>
  )
}

// Suppress unused warning when AlertCircle / Loader2 are referenced only in branches.
void AlertCircle
void Loader2

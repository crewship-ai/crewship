"use client"

import { ArrowRight } from "lucide-react"
import { HoverCard, HoverCardContent, HoverCardTrigger } from "@/components/ui/hover-card"
import { cn } from "@/lib/utils"
import { formatDuration } from "@/lib/activity/format-time"
import { summarizeValue } from "@/lib/format/summarize-value"
import type { StepStatus, TraceStep } from "@/lib/trace/types"

// StepHoverCard — quick-peek shown while the user hovers a step
// node on the canvas. Click still opens the side panel (full detail);
// the hover is for "what's this without committing" inspection.

export interface StepHoverPayload {
  step: TraceStep
  status: StepStatus
  durationMs?: number | null
  costUsd?: number | null
  outputSnippet?: string | null
  errorMessage?: string | null
}

interface StepHoverCardProps {
  payload: StepHoverPayload
  children: React.ReactNode
}

export function StepHoverCard({ payload, children }: StepHoverCardProps) {
  return (
    <HoverCard openDelay={200} closeDelay={50}>
      <HoverCardTrigger asChild>{children}</HoverCardTrigger>
      <HoverCardContent side="top" align="center" className="w-[280px] p-0 text-xs">
        <Header status={payload.status} step={payload.step} />
        <Stats payload={payload} />
        <InputOutput payload={payload} />
        {payload.errorMessage && payload.status === "failed" && (
          <div className="border-t border-rose-500/30 bg-rose-500/10 px-3 py-1.5 font-mono text-[10px] text-rose-300">
            {payload.errorMessage.length > 200
              ? payload.errorMessage.slice(0, 199) + "…"
              : payload.errorMessage}
          </div>
        )}
        <div className="flex border-t border-border p-2">
          <div className="flex-1 text-center text-[10px] text-muted-foreground/60">
            <span className="inline-flex items-center gap-1">
              Click for full detail
              <ArrowRight className="h-3 w-3" />
            </span>
          </div>
        </div>
      </HoverCardContent>
    </HoverCard>
  )
}

function Header({ status, step }: { status: StepStatus; step: TraceStep }) {
  return (
    <div className="flex items-center gap-1.5 border-b border-border px-3 py-2">
      <span className="font-mono text-[11px]">{step.id}</span>
      <span className="rounded bg-white/[0.06] px-1 py-0 text-[9px] uppercase tracking-wider text-muted-foreground">
        {step.type}
      </span>
      <span className={cn("ml-auto text-[10px] capitalize", STATUS_COLOR[status])}>
        {status}
      </span>
    </div>
  )
}

const STATUS_COLOR: Record<StepStatus, string> = {
  pending: "text-muted-foreground/60",
  running: "text-blue-300",
  waiting: "text-amber-300",
  success: "text-emerald-300",
  failed: "text-rose-300",
  skipped: "text-muted-foreground/40",
}

function Stats({ payload }: { payload: StepHoverPayload }) {
  const showAny =
    payload.durationMs !== undefined ||
    payload.costUsd !== undefined ||
    payload.status !== "pending"
  if (!showAny) return null
  return (
    <dl className="grid grid-cols-2 gap-x-2 gap-y-1 px-3 py-2 text-[11px]">
      {payload.durationMs !== undefined && payload.durationMs !== null && payload.durationMs > 0 && (
        <Row label="Duration">
          <span className="font-mono">{formatDuration(payload.durationMs)}</span>
        </Row>
      )}
      {payload.costUsd !== undefined && payload.costUsd !== null && payload.costUsd > 0 && (
        <Row label="Cost">
          <span className="font-mono">${payload.costUsd.toFixed(4)}</span>
        </Row>
      )}
    </dl>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground/60">{label}</dt>
      <dd className="text-right text-foreground/80">{children}</dd>
    </>
  )
}

function InputOutput({ payload }: { payload: StepHoverPayload }) {
  const inputDescr = describeStepInput(payload.step)
  const out = payload.outputSnippet
  if (!inputDescr && !out) return null
  return (
    <div className="space-y-1.5 border-t border-border px-3 py-2 text-[11px]">
      {inputDescr && (
        <div>
          <span className="text-[10px] uppercase tracking-wider text-muted-foreground/50">Input</span>
          <div className="mt-0.5 truncate font-mono text-foreground/80">{inputDescr}</div>
        </div>
      )}
      {out && (
        <div>
          <span className="text-[10px] uppercase tracking-wider text-muted-foreground/50">Output</span>
          <div
            className="mt-0.5 max-h-[60px] overflow-hidden font-mono text-[10px] text-foreground/70"
            style={{
              display: "-webkit-box",
              WebkitLineClamp: 3,
              WebkitBoxOrient: "vertical",
            }}
          >
            {out}
          </div>
        </div>
      )}
    </div>
  )
}

// describeStepInput — one-line summary of what the step is invoked
// with, picking the most important field per kind. Skips the empty
// case (some steps just inherit from the previous one).
function describeStepInput(step: TraceStep): string | null {
  switch (step.type) {
    case "http": {
      const method = (step.http?.method ?? "GET").toUpperCase()
      const url = step.http?.url
      return url ? `${method} ${url}` : method
    }
    case "agent_run":
      if (step.prompt) return summarizeValue(step.prompt, { maxChars: 120 })
      if (step.agent_slug) return `agent: ${step.agent_slug}`
      return null
    case "transform":
      if (step.transform?.expression) return step.transform.expression
      if (step.transform?.input) return `from ${step.transform.input}`
      return null
    case "code":
      return step.code?.runtime ? `${step.code.runtime} script` : "code"
    case "wait": {
      const kind = step.wait?.kind ?? "approval"
      if (step.wait?.approval_prompt) return `${kind} — ${step.wait.approval_prompt}`
      return kind
    }
    case "call_pipeline":
      return step.pipeline_slug ? `→ ${step.pipeline_slug}` : "sub-routine"
    default:
      return null
  }
}

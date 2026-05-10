"use client"

import { memo, useEffect, useRef, useState, type ReactNode } from "react"
import { Handle, Position, type NodeProps } from "@xyflow/react"
import {
  ArrowLeftRight,
  Check,
  CircleDot,
  Globe,
  Loader2,
  PauseCircle,
  ScrollText,
  Sparkles,
  Terminal,
  ThumbsDown,
  ThumbsUp,
  XCircle,
  Zap,
  type LucideIcon,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import type {
  StepKind,
  StepStatus,
  TraceStep,
  TraceStepNodeData,
  TraceTriggerNodeData,
} from "@/lib/trace/types"
import { waitpointDecide } from "@/lib/api/waitpoints"
import { HEATMAP_BORDER_CLASS } from "@/lib/trace/percentile-heatmap"
import { StepHoverCard } from "./step-hover-card"

export type { TraceStepNodeData, TraceTriggerNodeData }

// TraceStepNode — single React Flow node component used for every
// step kind in the trace canvas. The kind drives icon + subtitle; the
// status drives the colored ring + status pip; clicking the node
// selects it (handled by the canvas-level onNodeClick).
//
// Why one component for 6 step kinds: the visual shell is identical
// (icon, label, type chip, subtitle, status pip). Splitting into 6
// components meant 6× the boilerplate without any per-kind layout
// difference. The variation is data, not structure.

// Icon + label for each step kind. The "label" surfaces as a small
// type chip on the node so a colorblind user can still distinguish
// kinds when icons aren't enough.
const KIND_VISUAL: Record<StepKind, { Icon: LucideIcon; label: string; tint: string }> = {
  agent_run: { Icon: Sparkles, label: "agent", tint: "text-purple-300" },
  http: { Icon: Globe, label: "http", tint: "text-cyan-300" },
  transform: { Icon: ArrowLeftRight, label: "transform", tint: "text-emerald-300" },
  code: { Icon: Terminal, label: "code", tint: "text-amber-300" },
  wait: { Icon: PauseCircle, label: "wait", tint: "text-blue-300" },
  call_pipeline: { Icon: ScrollText, label: "sub-routine", tint: "text-violet-300" },
}

// Trigger isn't a real step kind — it's a synthetic node for the
// run's entry point (issue / schedule / webhook / manual). Local;
// nobody outside this file renders the trigger visual.
const TRIGGER_VISUAL = { Icon: Zap, label: "trigger", tint: "text-orange-300" }

const STATUS_RING: Record<StepStatus, { ring: string; bg: string }> = {
  pending: { ring: "ring-1 ring-white/[0.08]", bg: "bg-card" },
  running: {
    ring: "ring-2 ring-blue-400/60 shadow-[0_0_20px_rgba(59,130,246,0.25)]",
    bg: "bg-card",
  },
  waiting: {
    ring: "ring-2 ring-amber-400/60 shadow-[0_0_18px_rgba(251,191,36,0.2)]",
    bg: "bg-card",
  },
  success: { ring: "ring-1 ring-emerald-500/40", bg: "bg-card" },
  failed: {
    ring: "ring-2 ring-rose-500/60 shadow-[0_0_15px_rgba(244,63,94,0.2)]",
    bg: "bg-card",
  },
  skipped: { ring: "ring-1 ring-white/[0.06] opacity-60", bg: "bg-card" },
}

function StatusPip({ status }: { status: StepStatus }) {
  switch (status) {
    case "running":
      return (
        <span className="absolute -right-1 -top-1 flex h-3.5 w-3.5 items-center justify-center rounded-full bg-blue-500 ring-2 ring-background">
          <Loader2 className="h-2 w-2 animate-spin text-white" />
        </span>
      )
    case "waiting":
      return (
        <span className="absolute -right-1 -top-1 flex h-3.5 w-3.5 items-center justify-center rounded-full bg-amber-500 ring-2 ring-background">
          <PauseCircle className="h-2 w-2 animate-pulse text-white" />
        </span>
      )
    case "success":
      return (
        <span className="absolute -right-1 -top-1 flex h-3.5 w-3.5 items-center justify-center rounded-full bg-emerald-500 ring-2 ring-background">
          <Check className="h-2 w-2 text-white" />
        </span>
      )
    case "failed":
      return (
        <span className="absolute -right-1 -top-1 flex h-3.5 w-3.5 items-center justify-center rounded-full bg-rose-500 ring-2 ring-background">
          <XCircle className="h-2 w-2 text-white" />
        </span>
      )
    case "skipped":
      return (
        <span className="absolute -right-1 -top-1 h-3.5 w-3.5 rounded-full bg-white/10 ring-2 ring-background" />
      )
    case "pending":
    default:
      return null
  }
}

function subtitleFor(step: TraceStep): ReactNode {
  switch (step.type) {
    case "http": {
      const method = (step.http?.method ?? "GET").toUpperCase()
      const url = step.http?.url ?? ""
      const host = url ? hostnameFromTemplate(url) : ""
      return (
        <>
          <span className="font-mono text-foreground/80">{method}</span>
          {host && <span className="ml-1 truncate text-muted-foreground/70">{host}</span>}
        </>
      )
    }
    case "agent_run":
      return step.agent_slug ? (
        <span className="truncate font-mono text-foreground/80">{step.agent_slug}</span>
      ) : (
        <span className="text-muted-foreground/60">prompt</span>
      )
    case "transform":
      return (
        <span className="truncate font-mono text-foreground/80">
          {step.transform?.expression ?? "."}
        </span>
      )
    case "code":
      return (
        <span className="truncate font-mono text-foreground/80">
          {step.code?.runtime ?? "code"}
        </span>
      )
    case "wait": {
      const kind = step.wait?.kind ?? "approval"
      return (
        <>
          <span className="font-mono text-foreground/80">{kind}</span>
          {step.wait?.approval_prompt && (
            <span className="ml-1 truncate text-muted-foreground/70">
              · {step.wait.approval_prompt}
            </span>
          )}
        </>
      )
    }
    case "call_pipeline":
      return (
        <span className="truncate font-mono text-foreground/80">
          {step.pipeline_slug ?? "(unknown)"}
        </span>
      )
    default:
      return null
  }
}

// hostnameFromTemplate extracts the host portion of a URL even when
// the URL still contains template tokens. Returns the leading segment
// up to the first slash after the scheme, or the raw URL on parse
// failure. We don't need full URL parsing — the user just wants a
// readable subtitle ("api.github.com").
function hostnameFromTemplate(raw: string): string {
  const trimmed = raw.trim()
  // strip scheme
  const noScheme = trimmed.replace(/^https?:\/\//, "")
  const slash = noScheme.indexOf("/")
  const host = slash >= 0 ? noScheme.slice(0, slash) : noScheme
  return host.length > 32 ? host.slice(0, 31) + "…" : host
}

function TraceStepNodeBase({ data }: NodeProps) {
  const d = data as unknown as TraceStepNodeData
  const { step, status, selected, waitpoint, heatmapBucket, durationMs, costUsd, outputSnippet, errorMessage } = d
  const visual = KIND_VISUAL[step.type] ?? KIND_VISUAL.agent_run
  const Icon = visual.Icon
  const ring = STATUS_RING[status]
  const heatmapClass = heatmapBucket ? HEATMAP_BORDER_CLASS[heatmapBucket] : ""

  // tabIndex + onKeyDown make the node keyboard-activatable. Enter
  // and Space dispatch a click on the same element, which bubbles up
  // through React Flow's node wrapper and fires the canvas-level
  // onNodeClick handler — same code path as a mouse click.
  const nodeBody = (
    <div
      role="button"
      tabIndex={0}
      aria-label={`${visual.label} step ${step.id}, status ${status}`}
      aria-pressed={selected}
      onKeyDown={(e) => {
        // Only fire when the wrapper itself is focused — keydown
        // bubbles from descendants (e.g. the inline Approve/Deny
        // buttons), and pressing Enter on Approve would otherwise
        // also dispatch a wrapper click that re-selects the step.
        if (e.target !== e.currentTarget) return
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault()
          ;(e.currentTarget as HTMLElement).click()
        }
      }}
      className={cn(
        "relative w-[200px] rounded-lg border border-white/[0.06] px-2.5 py-2 transition-all focus:outline-none focus-visible:ring-2 focus-visible:ring-blue-400/80",
        ring.bg,
        ring.ring,
        selected && "ring-2 ring-blue-400",
        "hover:bg-card/80",
        heatmapClass,
      )}
    >
      <Handle
        type="target"
        position={Position.Left}
        className="!h-2 !w-2 !border-0 !bg-white/30"
        isConnectable={false}
      />
      <Handle
        type="source"
        position={Position.Right}
        className="!h-2 !w-2 !border-0 !bg-white/30"
        isConnectable={false}
      />

      <StatusPip status={status} />

      <div className="flex items-center gap-1.5">
        <span className={cn("flex h-5 w-5 items-center justify-center rounded", visual.tint)}>
          <Icon className="h-3.5 w-3.5" />
        </span>
        <span className="truncate font-mono text-xs text-foreground">{step.id}</span>
        <span className="ml-auto rounded bg-white/[0.06] px-1 py-0 text-[9px] uppercase tracking-wider text-muted-foreground">
          {visual.label}
        </span>
      </div>

      <div className="mt-1 flex items-center gap-1 text-[10px]">
        {subtitleFor(step)}
      </div>

      {waitpoint && <WaitpointActions waitpoint={waitpoint} />}
    </div>
  )
  return (
    <StepHoverCard
      payload={{
        step,
        status,
        durationMs,
        costUsd,
        outputSnippet,
        errorMessage,
      }}
    >
      {nodeBody}
    </StepHoverCard>
  )
}

// WaitpointActions — inline Approve/Deny on a paused wait step.
// Trigger.dev's ReviewNode pattern: the canvas IS the resolution
// surface, no need to bounce to /inbox for the common case.
//
// We stop event propagation on the buttons because React Flow's
// onNodeClick fires on any click within the node — without stopping,
// approve/deny would also re-select the step.
function WaitpointActions({
  waitpoint,
}: {
  waitpoint: { token: string; workspaceId: string }
}) {
  const [busy, setBusy] = useState<"approve" | "deny" | null>(null)
  // mountedRef guards setBusy after a successful decide — the
  // realtime pipeline.run.* event that fires once the run resumes
  // unmounts this node, and React warns on stale state updates if we
  // setBusy(null) on a dead component. Toast is fine to fire either
  // way (sonner is global) — only the local state needs the guard.
  const mountedRef = useRef(true)
  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])
  const decide = async (e: React.MouseEvent, approved: boolean) => {
    e.stopPropagation()
    e.preventDefault()
    setBusy(approved ? "approve" : "deny")
    // try/finally so a transport-level throw (network drop, CORS,
    // …) still clears the busy state. Without it, a thrown fetch
    // would leave both buttons disabled until the node remounts —
    // looks like a hung approval but is really an unhandled error.
    try {
      const res = await waitpointDecide(waitpoint.workspaceId, waitpoint.token, approved)
      if (res.ok) {
        toast.success(approved ? "Approved" : "Denied")
      } else {
        toast.error(res.error)
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Decide failed")
    } finally {
      if (mountedRef.current) setBusy(null)
    }
  }
  return (
    <div className="mt-1.5 flex items-center gap-1">
      <button
        type="button"
        onClick={(e) => decide(e, true)}
        disabled={busy !== null}
        aria-label="Approve waitpoint"
        className={cn(
          "flex items-center gap-1 rounded bg-emerald-500/15 px-1.5 py-0.5 text-[10px] font-medium text-emerald-300 transition-colors hover:bg-emerald-500/25 disabled:opacity-50",
        )}
      >
        {busy === "approve" ? (
          <Loader2 className="h-2.5 w-2.5 animate-spin" />
        ) : (
          <ThumbsUp className="h-2.5 w-2.5" />
        )}
        Approve
      </button>
      <button
        type="button"
        onClick={(e) => decide(e, false)}
        disabled={busy !== null}
        aria-label="Deny waitpoint"
        className={cn(
          "flex items-center gap-1 rounded bg-rose-500/15 px-1.5 py-0.5 text-[10px] font-medium text-rose-300 transition-colors hover:bg-rose-500/25 disabled:opacity-50",
        )}
      >
        {busy === "deny" ? (
          <Loader2 className="h-2.5 w-2.5 animate-spin" />
        ) : (
          <ThumbsDown className="h-2.5 w-2.5" />
        )}
        Deny
      </button>
    </div>
  )
}

export const TraceStepNode = memo(TraceStepNodeBase)

// TriggerNode — synthetic entry-point node. Same visual chrome as a
// step node but with a Zap icon, no input handle, and a smaller
// footprint. Renders the source of the run (issue id / schedule cron
// / webhook name / "manual").

function TriggerNodeBase({ data }: NodeProps) {
  const d = data as unknown as TraceTriggerNodeData
  const Icon = TRIGGER_VISUAL.Icon
  // Pin the label to the trigger SOURCE first; only fall through to
  // "manual" when triggered_via really is empty/manual. Previous
  // version dropped issue-triggered runs to "manual" when the
  // identifier was empty (deleted mission), confusing the user about
  // why the run kicked off.
  const label =
    d.triggeredVia === "issue"
      ? d.issueIdentifier || "issue"
      : d.triggeredVia === "schedule"
        ? "schedule"
        : d.triggeredVia === "webhook"
          ? "webhook"
          : d.triggeredVia === "call_pipeline"
            ? "sub-run"
            : "manual"
  return (
    <div
      role="img"
      aria-label={`Trigger ${label}, ${d.pipelineName ?? "routine"}`}
      className="relative w-[180px] rounded-lg border border-white/[0.06] bg-card px-2.5 py-2 ring-1 ring-orange-500/30"
    >
      <Handle
        type="source"
        position={Position.Right}
        className="!h-2 !w-2 !border-0 !bg-white/30"
        isConnectable={false}
      />
      <div className="flex items-center gap-1.5">
        <span className={cn("flex h-5 w-5 items-center justify-center rounded", TRIGGER_VISUAL.tint)}>
          <Icon className="h-3.5 w-3.5" />
        </span>
        <span className="truncate text-xs font-medium text-foreground">{label}</span>
        <span className="ml-auto rounded bg-orange-500/10 px-1 py-0 text-[9px] uppercase tracking-wider text-orange-300">
          {TRIGGER_VISUAL.label}
        </span>
      </div>
      <div className="mt-1 flex items-center gap-1 text-[10px] text-muted-foreground/70">
        <CircleDot className="h-2.5 w-2.5" />
        <span className="truncate">{d.pipelineName ?? "(routine)"}</span>
      </div>
    </div>
  )
}

export const TraceTriggerNode = memo(TriggerNodeBase)

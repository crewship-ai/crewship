"use client"

import { memo } from "react"
import Link from "next/link"
import { Handle, Position, type NodeProps } from "@xyflow/react"
import {
  CircleDot,
  PauseCircle,
  ScrollText,
  Workflow,
  Zap,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { statusIcon, statusTint } from "@/lib/activity/run-status"
import { relTime } from "@/lib/activity/format-time"
import type {
  OverviewIssueNodeData,
  OverviewRoutineNodeData,
  OverviewRunNodeData,
} from "@/lib/trace/build-overview-graph"

// Three React Flow custom nodes for the /activity overview canvas:
//   - OverviewIssueNode    : a Mission/Issue card (col 1)
//   - OverviewRoutineNode  : a saved pipeline card (col 2)
//   - OverviewRunNode      : the latest run for that pipeline (col 3)
//
// Click handlers are NOT on the nodes — the canvas-level onNodeClick
// dispatches based on node id prefix (iss: / rt: / run:). This keeps
// the components dumb and avoids passing callbacks via the data
// payload.

function IssueNodeBase({ data }: NodeProps) {
  const d = data as unknown as OverviewIssueNodeData
  return (
    <div
      role="button"
      tabIndex={0}
      aria-label={`Issue ${d.identifier}: ${d.title}`}
      className="relative w-[200px] rounded-lg border border-blue-500/25 bg-card px-2.5 py-2 transition-colors hover:bg-card/80 focus:outline-none focus-visible:ring-2 focus-visible:ring-blue-400/80"
    >
      <Handle type="source" position={Position.Right} className="!h-2 !w-2 !border-0 !bg-blue-400/40" isConnectable={false} />
      <div className="flex items-center gap-1.5">
        <CircleDot className="h-3.5 w-3.5 shrink-0 text-blue-300" />
        <span className="truncate font-mono text-[10px] text-blue-300">{d.identifier}</span>
        <StatusChip status={d.status} />
      </div>
      <div className="mt-1 truncate text-xs text-foreground/90" title={d.title}>
        {d.title}
      </div>
    </div>
  )
}
export const OverviewIssueNode = memo(IssueNodeBase)

function RoutineNodeBase({ data }: NodeProps) {
  const d = data as unknown as OverviewRoutineNodeData
  return (
    <Link
      href={`/routines?slug=${encodeURIComponent(d.slug)}`}
      role="button"
      aria-label={`Routine ${d.name}`}
      className="relative block w-[200px] rounded-lg border border-violet-500/25 bg-card px-2.5 py-2 transition-colors hover:bg-card/80 focus:outline-none focus-visible:ring-2 focus-visible:ring-violet-400/80"
      onClick={(e) => e.stopPropagation()}
    >
      <Handle type="target" position={Position.Left} className="!h-2 !w-2 !border-0 !bg-violet-400/40" isConnectable={false} />
      <Handle type="source" position={Position.Right} className="!h-2 !w-2 !border-0 !bg-violet-400/40" isConnectable={false} />
      <div className="flex items-center gap-1.5">
        <Workflow className="h-3.5 w-3.5 shrink-0 text-violet-300" />
        <span className="truncate text-xs font-medium">{d.name}</span>
      </div>
      <div className="mt-1 flex items-center gap-1 text-[10px] text-muted-foreground/70">
        <ScrollText className="h-2.5 w-2.5" />
        <span className="truncate font-mono">{d.slug}</span>
        {d.invocationCount !== undefined && d.invocationCount > 0 && (
          <span className="ml-auto rounded bg-white/[0.06] px-1 py-0 text-[9px]">
            {d.invocationCount} runs
          </span>
        )}
      </div>
    </Link>
  )
}
export const OverviewRoutineNode = memo(RoutineNodeBase)

function RunNodeBase({ data }: NodeProps) {
  const d = data as unknown as OverviewRunNodeData
  const tint = statusTint(d.status)
  const SI = statusIcon(d.status)
  const isWait = d.isWaitpoint || d.status === "paused"
  return (
    <div
      role="button"
      tabIndex={0}
      aria-label={`Run ${d.runId} status ${d.status}`}
      className={cn(
        "relative w-[180px] rounded-lg border border-white/[0.06] bg-card px-2.5 py-2 transition-colors hover:bg-card/80 focus:outline-none focus-visible:ring-2 focus-visible:ring-blue-400/80",
        isWait && "ring-1 ring-amber-400/40",
      )}
    >
      <Handle type="target" position={Position.Left} className="!h-2 !w-2 !border-0 !bg-white/30" isConnectable={false} />
      <div className="flex items-center gap-1.5">
        <span className={cn("flex h-4 w-4 shrink-0 items-center justify-center rounded-full", tint.bg)}>
          <SI className={cn("h-2.5 w-2.5", tint.icon)} aria-hidden="true" />
        </span>
        <span className="truncate font-mono text-[10px]">{shortId(d.runId)}</span>
        <span className={cn("ml-auto text-[10px] capitalize", tint.text)}>{d.status}</span>
      </div>
      <div className="mt-1 flex items-center gap-1 text-[10px] text-muted-foreground/60">
        <SourceIcon source={d.triggeredVia} />
        <span>{relTime(d.startedAt)}</span>
        {isWait && (
          <span className="ml-auto inline-flex items-center gap-0.5 rounded bg-amber-500/15 px-1 py-0 text-[9px] font-medium text-amber-300">
            <PauseCircle className="h-2 w-2" /> awaiting
          </span>
        )}
      </div>
    </div>
  )
}
export const OverviewRunNode = memo(RunNodeBase)

// ── helpers ────────────────────────────────────────────────────────

function StatusChip({ status }: { status: string }) {
  const s = status.toLowerCase()
  const cls =
    s === "in_progress" || s === "running"
      ? "bg-blue-500/15 text-blue-300"
      : s === "review"
        ? "bg-purple-500/15 text-purple-300"
        : s === "completed" || s === "done"
          ? "bg-emerald-500/15 text-emerald-300"
          : s === "failed"
            ? "bg-rose-500/15 text-rose-300"
            : "bg-white/[0.06] text-muted-foreground"
  return (
    <span className={cn("ml-auto rounded px-1 py-0 text-[9px] font-medium uppercase tracking-wide", cls)}>
      {status}
    </span>
  )
}

function SourceIcon({ source }: { source?: string }) {
  if (source === "schedule") return <span className="text-violet-300">⌘</span>
  if (source === "issue") return <CircleDot className="h-2.5 w-2.5 text-blue-300" />
  if (source === "webhook") return <span className="text-amber-300">⚡</span>
  if (source === "call_pipeline") return <span className="text-purple-300">↳</span>
  return <Zap className="h-2.5 w-2.5 text-muted-foreground/60" />
}

function shortId(id: string): string {
  if (id.length > 12 && id.startsWith("prn_")) return id.slice(0, 8)
  return id
}

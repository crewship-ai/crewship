"use client"

import { memo } from "react"
import { type NodeProps } from "@xyflow/react"
import { GitBranch, CheckCircle2, XCircle, Loader2, Eye } from "lucide-react"
import { cn } from "@/lib/utils"

// PipelineRunNode renders a pipeline run as a single React Flow node
// in the Orchestration → Graph view. The node sits next to the
// invoking agent's card and shows: pipeline slug, run status, step
// progress, the execution tier that was used, and a click target
// that opens the run detail side-sheet (wired by the parent layout).
//
// Status colours mirror the existing agent-card-node convention so
// the graph stays visually coherent. Pulsing dot for running runs
// gives the "alive" feel Pavel called out — the graph should look
// like a control room, not a static doc.
//
// We deliberately do NOT render full step-by-step details inside the
// node — the graph is the index, the side-sheet is the detail. A
// busy graph with 50 nodes each showing 4 lines of step trace would
// be unreadable.

export interface PipelineRunData {
  /** Pipeline slug (kebab-case). Renders as the node title. */
  pipelineSlug: string
  /** Pipeline display name; falls back to slug if absent. */
  pipelineName?: string
  /** Run id for click-through to the detail side-sheet. */
  runId: string
  /** Lifecycle status; drives icon + colour. */
  status: "running" | "completed" | "failed" | "dry_run" | "queued"
  /** Step progress shown as "2/4 steps". -1 means "unknown". */
  stepCount?: number
  stepIndex?: number
  /** Tier used (e.g. "claude/haiku"). Empty hides the chip. */
  tierLabel?: string
  /** Total cost in USD, formatted to 4 decimals on display. */
  costUsd?: number
  /** Authoring crew label — "Authored by Crew A". Optional. */
  authorCrewLabel?: string
  /** Click handler — usually opens the run detail side-sheet. */
  onClick?: (runId: string) => void
  [key: string]: unknown
}

const statusConfig = {
  running: {
    icon: Loader2,
    iconClass: "animate-spin text-blue-400",
    dot: "bg-blue-500",
    pulse: true,
    border: "border-blue-500/30",
    bg: "bg-[#0a1320]/70",
    label: "Running",
  },
  completed: {
    icon: CheckCircle2,
    iconClass: "text-emerald-400",
    dot: "bg-emerald-500",
    pulse: false,
    border: "border-emerald-500/30",
    bg: "bg-[#0a1f12]/70",
    label: "Completed",
  },
  failed: {
    icon: XCircle,
    iconClass: "text-red-400",
    dot: "bg-red-500",
    pulse: false,
    border: "border-red-500/30",
    bg: "bg-[#1f0a0a]/70",
    label: "Failed",
  },
  dry_run: {
    icon: Eye,
    iconClass: "text-purple-400",
    dot: "bg-purple-500",
    pulse: false,
    border: "border-purple-500/30",
    bg: "bg-[#180a1f]/70",
    label: "Dry run",
  },
  queued: {
    icon: Loader2,
    iconClass: "text-slate-400",
    dot: "bg-slate-400",
    pulse: false,
    border: "border-border",
    bg: "bg-[#0f1115]/70",
    label: "Queued",
  },
} as const

function PipelineRunNodeImpl({ data }: NodeProps) {
  const d = data as PipelineRunData
  const cfg = statusConfig[d.status] ?? statusConfig.queued
  const Icon = cfg.icon
  const title = d.pipelineName || d.pipelineSlug

  return (
    <div
      onClick={() => d.onClick?.(d.runId)}
      className={cn(
        "group relative w-[220px] cursor-pointer rounded-md border px-3 py-2 transition-all",
        cfg.border,
        cfg.bg,
        "hover:border-foreground/40 hover:shadow-md",
      )}
      role="button"
      aria-label={`Pipeline run ${title} (${cfg.label})`}
    >
      {/* Header: pipeline icon + status icon + slug */}
      <div className="flex items-center gap-2">
        <GitBranch className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <span className="flex-1 truncate text-[13px] font-medium text-foreground">{title}</span>
        <div className="flex items-center gap-1.5">
          {cfg.pulse && <span className={cn("h-1.5 w-1.5 rounded-full", cfg.dot, "animate-pulse")} />}
          <Icon className={cn("h-3.5 w-3.5", cfg.iconClass)} />
        </div>
      </div>

      {/* Step progress + tier + cost row. Each chip omitted if data is absent. */}
      <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-[10px] text-muted-foreground">
        {typeof d.stepIndex === "number" && typeof d.stepCount === "number" && (
          <span className="rounded-sm bg-muted/40 px-1.5 py-0.5 font-mono">
            {d.stepIndex}/{d.stepCount} steps
          </span>
        )}
        {d.tierLabel && (
          <span className="rounded-sm bg-muted/40 px-1.5 py-0.5">
            {d.tierLabel}
          </span>
        )}
        {typeof d.costUsd === "number" && d.costUsd > 0 && (
          <span className="rounded-sm bg-muted/40 px-1.5 py-0.5 font-mono">
            ${d.costUsd.toFixed(4)}
          </span>
        )}
      </div>

      {/* Authoring footer — small, low-contrast so it doesn't fight the title */}
      {d.authorCrewLabel && (
        <div className="mt-1 truncate text-[9px] uppercase tracking-wider text-muted-foreground/70">
          authored by {d.authorCrewLabel}
        </div>
      )}
    </div>
  )
}

export const PipelineRunNode = memo(PipelineRunNodeImpl)

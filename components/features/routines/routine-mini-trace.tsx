"use client"

import {
  Clock,
  Globe,
  Bot,
  Shuffle,
  Terminal,
  Hourglass,
  Workflow,
  Database,
  Server,
  Send,
  CheckCircle2,
  XCircle,
  Loader2,
  type LucideIcon,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { formatDurationDecimal } from "@/lib/time"
import type { FlowIconKey } from "@/lib/routine-flow"
import type { MiniCall, MiniStepStatus, MiniTraceNode } from "@/lib/routine-mini-trace"
import { brandIconByKey, BrandGlyph } from "./brand-icons"
import { SubSpanIcon, SUB_SPAN_STATUS_COLOR } from "@/components/features/activity/sub-span-visual"

// RoutineMiniTrace — the compact, read-only "how did this run flow + what
// did it call" projection rendered inside the Last Run card. A stacked
// strip of trigger → step rows; under each agent step its mini-calls (the
// sub-spans: file writes, bash, MCP tools) so the run is scannable without
// opening the full /activity trace canvas.

// Same lucide mapping the flow-diagram uses (kept local — small + lets the
// two surfaces diverge if needed without coupling).
const STEP_ICONS: Record<FlowIconKey, LucideIcon> = {
  trigger: Clock,
  http: Globe,
  agent: Bot,
  transform: Shuffle,
  code: Terminal,
  wait: Hourglass,
  call: Workflow,
  "store-redis": Database,
  "store-postgres": Server,
  "store-mysql": Database,
  "store-mongodb": Database,
  store: Database,
  tool: Terminal,
  out: Send,
}

// Per-kind icon tint, matching the flow diagram's node chrome.
const KIND_TINT: Record<string, string> = {
  trigger: "text-amber-400",
  agent: "text-indigo-400",
  step: "text-muted-foreground",
  store: "text-cyan-400",
  tool: "text-violet-400",
  out: "text-emerald-400",
}

function StatusGlyph({ status }: { status: MiniStepStatus }) {
  if (status === "success")
    return <CheckCircle2 className="h-3.5 w-3.5 shrink-0 text-emerald-400" aria-label="succeeded" />
  if (status === "failed")
    return <XCircle className="h-3.5 w-3.5 shrink-0 text-rose-400" aria-label="failed" />
  if (status === "running")
    return <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin text-amber-400" aria-label="running" />
  return null
}

function MiniCallRow({ call }: { call: MiniCall }) {
  const statusColor = SUB_SPAN_STATUS_COLOR[call.status] ?? "text-muted-foreground"
  return (
    <li className="flex items-center gap-1.5 py-0.5 text-[11px]">
      <SubSpanIcon kind={call.kind} tool={call.tool} className="h-3 w-3 shrink-0" />
      <span className="truncate text-foreground/80" title={call.artifactPath || call.host || call.name}>
        {call.name}
      </span>
      {call.tool && (
        <span className="shrink-0 rounded border border-violet-500/40 px-1 py-0 text-[9px] text-violet-300">
          {call.tool}
        </span>
      )}
      <span className="ml-auto flex shrink-0 items-center gap-1 tabular-nums text-muted-foreground-soft">
        {call.durationMs != null && call.durationMs > 0 && (
          <span>{formatDurationDecimal(call.durationMs)}</span>
        )}
        {call.status === "ok" ? (
          <CheckCircle2 className={cn("h-3 w-3", statusColor)} aria-hidden />
        ) : call.status === "error" ? (
          <XCircle className={cn("h-3 w-3", statusColor)} aria-hidden />
        ) : (
          <Loader2 className={cn("h-3 w-3 animate-spin", statusColor)} aria-hidden />
        )}
      </span>
    </li>
  )
}

function MiniTraceRow({ node, isLast }: { node: MiniTraceNode; isLast: boolean }) {
  const brand = brandIconByKey(node.brandIconKey)
  const fallback = STEP_ICONS[node.iconKey] ?? Shuffle
  const tint = KIND_TINT[node.kind] ?? KIND_TINT.step
  const isTrigger = node.kind === "trigger"

  return (
    <li className="relative pl-6">
      {/* connector rail */}
      <span
        aria-hidden
        className={cn(
          "absolute left-[9px] top-5 w-px bg-border/60",
          isLast ? "h-0" : "bottom-0",
        )}
      />
      {/* node icon dot */}
      <span
        aria-hidden
        className="absolute left-0 top-1 flex h-[18px] w-[18px] items-center justify-center rounded-md border border-border/60 bg-card"
      >
        <BrandGlyph brand={brand} fallback={fallback} className={cn("h-3 w-3", !brand && tint)} />
      </span>

      <div className="flex items-center gap-2 py-0.5">
        <span className={cn("text-[12px] font-medium", isTrigger ? "text-foreground/70" : "text-foreground/90")}>
          {node.label}
        </span>
        {node.detail && !isTrigger && (
          <span className="truncate font-mono text-[10px] text-muted-foreground-soft" title={node.detail}>
            {node.detail}
          </span>
        )}
        {node.model && (
          <span className="shrink-0 rounded border border-indigo-500/40 px-1 py-0 text-[9px] font-medium text-indigo-300">
            {node.model}
          </span>
        )}
        <span className="ml-auto shrink-0">
          <StatusGlyph status={node.status} />
        </span>
      </div>

      {node.calls.length > 0 && (
        <ul className="mb-1 mt-0.5 space-y-0 border-l border-border/40 pl-2.5">
          {node.calls.map((call, i) => (
            <MiniCallRow key={i} call={call} />
          ))}
        </ul>
      )}
    </li>
  )
}

export function RoutineMiniTrace({ nodes }: { nodes: MiniTraceNode[] }) {
  const totalCalls = nodes.reduce((a, n) => a + n.calls.length, 0)
  return (
    <div>
      <ol className="space-y-0.5">
        {nodes.map((node, i) => (
          <MiniTraceRow key={node.id + i} node={node} isLast={i === nodes.length - 1} />
        ))}
      </ol>
      {totalCalls === 0 && (
        <div className="mt-1 pl-6 text-[10.5px] text-muted-foreground-soft">
          No captured actions for this run.
        </div>
      )}
    </div>
  )
}

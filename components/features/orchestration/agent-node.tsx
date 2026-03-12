"use client"

import { memo } from "react"
import { Handle, Position, type NodeProps } from "@xyflow/react"
import { cn } from "@/lib/utils"

interface AgentNodeData {
  label: string
  status: string
  agentName: string
  agentSlug: string | null
  iteration: number | null
  maxIterations: number | null
  tokenCount: number | null
  estimatedCost: number | null
  durationMs: number | null
  [key: string]: unknown
}

const statusConfig: Record<string, {
  accent: string
  bg: string
  headerBg: string
  dot: string
  glow: string
  label: string
}> = {
  COMPLETED: {
    accent: "#22c55e",
    bg: "bg-[#0a1f0f]",
    headerBg: "bg-gradient-to-r from-green-600/30 to-green-500/10",
    dot: "bg-green-500",
    glow: "",
    label: "Completed",
  },
  IN_PROGRESS: {
    accent: "#3b82f6",
    bg: "bg-[#0a1220]",
    headerBg: "bg-gradient-to-r from-blue-600/30 to-blue-500/10",
    dot: "bg-blue-500 animate-pulse",
    glow: "shadow-[0_0_25px_rgba(59,130,246,0.3)]",
    label: "Running",
  },
  FAILED: {
    accent: "#ef4444",
    bg: "bg-[#1f0a0a]",
    headerBg: "bg-gradient-to-r from-red-600/30 to-red-500/10",
    dot: "bg-red-500",
    glow: "shadow-[0_0_15px_rgba(239,68,68,0.2)]",
    label: "Failed",
  },
  BLOCKED: {
    accent: "#f59e0b",
    bg: "bg-[#1f1a0a]",
    headerBg: "bg-gradient-to-r from-amber-600/30 to-amber-500/10",
    dot: "bg-amber-500",
    glow: "",
    label: "Blocked",
  },
  PENDING: {
    accent: "#64748b",
    bg: "bg-[#0f1115]",
    headerBg: "bg-gradient-to-r from-slate-600/20 to-slate-500/5",
    dot: "bg-slate-400",
    glow: "",
    label: "Pending",
  },
  REVIEW: {
    accent: "#a855f7",
    bg: "bg-[#150a1f]",
    headerBg: "bg-gradient-to-r from-purple-600/30 to-purple-500/10",
    dot: "bg-purple-500",
    glow: "shadow-[0_0_15px_rgba(168,85,247,0.2)]",
    label: "Review",
  },
  SKIPPED: {
    accent: "#6b7280",
    bg: "bg-[#0f1115]",
    headerBg: "bg-gradient-to-r from-gray-600/20 to-gray-500/5",
    dot: "bg-gray-400",
    glow: "",
    label: "Skipped",
  },
}

function getInitials(name: string): string {
  return name
    .split(/[\s_-]+/)
    .slice(0, 2)
    .map((w) => w[0]?.toUpperCase() || "")
    .join("")
}

function hashColor(slug: string | null): string {
  if (!slug) return "#64748b"
  let h = 0
  for (let i = 0; i < slug.length; i++) h = ((h << 5) - h + slug.charCodeAt(i)) | 0
  const hue = Math.abs(h) % 360
  return `hsl(${hue}, 70%, 55%)`
}

function formatTokens(count: number | null): string | null {
  if (count == null || count === 0) return null
  if (count >= 1000000) return `${(count / 1000000).toFixed(1)}M`
  if (count >= 1000) return `${(count / 1000).toFixed(1)}k`
  return `${count}`
}

function formatDuration(ms: number | null): string | null {
  if (ms == null || ms === 0) return null
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.floor(ms / 60000)}m ${Math.floor((ms % 60000) / 1000)}s`
}

function AgentNodeComponent({ data }: NodeProps) {
  const d = data as unknown as AgentNodeData
  const cfg = statusConfig[d.status] || statusConfig.PENDING
  const initials = getInitials(d.agentName)
  const color = hashColor(d.agentSlug)
  const tokens = formatTokens(d.tokenCount)
  const duration = formatDuration(d.durationMs)

  return (
    <div
      className={cn(
        "rounded-xl overflow-hidden min-w-[230px] max-w-[260px] cursor-pointer transition-all duration-300",
        "border border-white/[0.08] backdrop-blur-sm",
        cfg.bg, cfg.glow,
        d.status === "IN_PROGRESS" && "scale-[1.03]"
      )}
      style={{ borderColor: `${cfg.accent}40` }}
    >
      {/* Colored header bar */}
      <div className={cn("px-3 py-1.5 flex items-center justify-between", cfg.headerBg)}>
        <div className="flex items-center gap-1.5">
          <div className={cn("w-2 h-2 rounded-full", cfg.dot)} />
          <span className="text-[10px] font-semibold uppercase tracking-wider" style={{ color: cfg.accent }}>
            {cfg.label}
          </span>
        </div>
        {d.maxIterations && d.maxIterations > 1 && (
          <span className="text-[10px] text-white/40 font-mono">
            {d.iteration || 1}/{d.maxIterations}
          </span>
        )}
      </div>

      {/* Body */}
      <div className="px-3 py-2.5">
        <div className="flex items-start gap-2.5">
          {/* Avatar */}
          <div
            className="w-8 h-8 rounded-lg flex items-center justify-center text-[11px] font-bold text-white shrink-0"
            style={{ backgroundColor: color }}
          >
            {initials}
          </div>
          <div className="min-w-0 flex-1">
            <div className="text-[13px] font-medium text-white/90 leading-tight truncate">
              {d.label}
            </div>
            <div className="text-[11px] text-white/40 mt-0.5 truncate font-mono">
              @{d.agentSlug || "unassigned"}
            </div>
          </div>
        </div>

        {/* Metrics row */}
        {(tokens || duration || d.estimatedCost) && (
          <div className="flex items-center gap-2 mt-2 pt-2 border-t border-white/[0.06]">
            {tokens && (
              <span className="text-[10px] text-white/30 font-mono">{tokens} tok</span>
            )}
            {d.estimatedCost != null && d.estimatedCost > 0 && (
              <span className="text-[10px] text-white/30 font-mono">${d.estimatedCost.toFixed(4)}</span>
            )}
            {duration && (
              <span className="text-[10px] text-white/30 font-mono">{duration}</span>
            )}
          </div>
        )}
      </div>

      {/* Handles styled as connection dots */}
      <Handle
        type="target"
        position={Position.Left}
        className="!w-3 !h-3 !rounded-full !border-2 !-left-1.5"
        style={{ background: "#1a1d23", borderColor: cfg.accent }}
      />
      <Handle
        type="source"
        position={Position.Right}
        className="!w-3 !h-3 !rounded-full !border-2 !-right-1.5"
        style={{ background: "#1a1d23", borderColor: cfg.accent }}
      />
    </div>
  )
}

export const AgentNode = memo(AgentNodeComponent)

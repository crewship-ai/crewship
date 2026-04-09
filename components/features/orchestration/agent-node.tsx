"use client"

import { memo } from "react"
import { Handle, Position, type NodeProps } from "@xyflow/react"
import { AlertTriangle } from "lucide-react"
import { cn } from "@/lib/utils"
import { STATUS_COLORS, STATUS_BG } from "@/lib/colors"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"

interface AgentNodeData {
  label: string
  status: string
  agentName: string
  agentSlug: string | null
  avatarSeed: string | null
  avatarStyle: string | null
  iteration: number | null
  maxIterations: number | null
  tokenCount: number | null
  estimatedCost: number | null
  durationMs: number | null
  activitySnippet?: string | null
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
    accent: STATUS_COLORS.COMPLETED,
    bg: STATUS_BG.COMPLETED,
    headerBg: "bg-gradient-to-r from-green-600/30 to-green-500/10",
    dot: "bg-green-500",
    glow: "",
    label: "Completed",
  },
  IN_PROGRESS: {
    accent: STATUS_COLORS.IN_PROGRESS,
    bg: STATUS_BG.IN_PROGRESS,
    headerBg: "bg-gradient-to-r from-blue-600/30 to-blue-500/10",
    dot: "bg-blue-500 animate-pulse",
    glow: "shadow-[0_0_25px_rgba(59,130,246,0.3)]",
    label: "Running",
  },
  FAILED: {
    accent: STATUS_COLORS.FAILED,
    bg: STATUS_BG.FAILED,
    headerBg: "bg-gradient-to-r from-red-600/30 to-red-500/10",
    dot: "bg-red-500",
    glow: "shadow-[0_0_15px_rgba(239,68,68,0.2)]",
    label: "Failed",
  },
  BLOCKED: {
    accent: STATUS_COLORS.BLOCKED,
    bg: STATUS_BG.BLOCKED,
    headerBg: "bg-gradient-to-r from-amber-600/30 to-amber-500/10",
    dot: "bg-amber-500",
    glow: "",
    label: "Blocked",
  },
  PENDING: {
    accent: STATUS_COLORS.PENDING,
    bg: STATUS_BG.PENDING,
    headerBg: "bg-gradient-to-r from-slate-600/20 to-slate-500/5",
    dot: "bg-slate-400",
    glow: "",
    label: "Pending",
  },
  REVIEW: {
    accent: STATUS_COLORS.REVIEW,
    bg: STATUS_BG.REVIEW,
    headerBg: "bg-gradient-to-r from-purple-600/30 to-purple-500/10",
    dot: "bg-purple-500",
    glow: "shadow-[0_0_15px_rgba(168,85,247,0.2)]",
    label: "Review",
  },
  SKIPPED: {
    accent: STATUS_COLORS.SKIPPED,
    bg: STATUS_BG.SKIPPED,
    headerBg: "bg-gradient-to-r from-gray-600/20 to-gray-500/5",
    dot: "bg-gray-400",
    glow: "",
    label: "Skipped",
  },
  AWAITING_APPROVAL: {
    accent: STATUS_COLORS.AWAITING_APPROVAL,
    bg: STATUS_BG.AWAITING_APPROVAL,
    headerBg: "bg-gradient-to-r from-violet-600/30 to-violet-500/10",
    dot: "bg-violet-500 animate-pulse",
    glow: "shadow-[0_0_15px_rgba(139,92,246,0.2)]",
    label: "Awaiting Approval",
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
  if (!slug) return STATUS_COLORS.PENDING
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
        "border border-border backdrop-blur-sm",
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
          <span className="text-[10px] text-muted-foreground font-mono">
            {d.iteration || 1}/{d.maxIterations}
          </span>
        )}
      </div>

      {/* Body */}
      <div className="px-3 py-2.5">
        <div className="flex items-start gap-2.5">
          {/* Avatar */}
          {(d.avatarSeed || d.agentSlug || d.agentName) ? (
            <img
              src={getAgentAvatarUrl(d.avatarSeed || d.agentSlug || d.agentName, d.avatarStyle)}
              alt={d.agentName}
              className="w-8 h-8 rounded-lg shrink-0"
            />
          ) : (
            <div
              className="w-8 h-8 rounded-lg flex items-center justify-center text-[11px] font-bold text-white shrink-0"
              style={{ backgroundColor: color }}
            >
              {initials}
            </div>
          )}
          <div className="min-w-0 flex-1">
            <div className="text-[13px] font-medium text-foreground leading-tight truncate">
              {d.label}
            </div>
            <div className="flex items-center gap-1 mt-0.5">
              {!d.agentSlug && (
                <AlertTriangle className="h-3 w-3 text-amber-400 shrink-0" />
              )}
              <span className={cn(
                "text-[11px] truncate font-mono",
                d.agentSlug ? "text-muted-foreground" : "text-amber-400/70"
              )}>
                {d.agentSlug ? `@${d.agentSlug}` : "unassigned"}
              </span>
            </div>
          </div>
        </div>

        {/* Metrics row */}
        {(tokens || duration || d.estimatedCost) && (
          <div className="flex items-center gap-2 mt-2 pt-2 border-t border-border">
            {tokens && (
              <span className="text-[10px] text-muted-foreground/70 font-mono">{tokens} tok</span>
            )}
            {d.estimatedCost != null && d.estimatedCost > 0 && (
              <span className="text-[10px] text-muted-foreground/70 font-mono">${d.estimatedCost.toFixed(4)}</span>
            )}
            {duration && (
              <span className="text-[10px] text-muted-foreground/70 font-mono">{duration}</span>
            )}
          </div>
        )}

        {/* Live activity snippet */}
        {d.status === "IN_PROGRESS" && d.activitySnippet && (
          <div className="mt-2 pt-1.5 border-t border-white/[0.04]">
            <p className="text-[10px] text-blue-300/50 italic truncate leading-relaxed">
              {d.activitySnippet}
            </p>
          </div>
        )}
      </div>

      {/* Handles styled as connection dots */}
      <Handle
        type="target"
        position={Position.Left}
        className="!w-3 !h-3 !rounded-full !border-2 !-left-1.5"
        style={{ background: "hsl(var(--muted))", borderColor: cfg.accent }}
      />
      <Handle
        type="source"
        position={Position.Right}
        className="!w-3 !h-3 !rounded-full !border-2 !-right-1.5"
        style={{ background: "hsl(var(--muted))", borderColor: cfg.accent }}
      />
    </div>
  )
}

export const AgentNode = memo(AgentNodeComponent)

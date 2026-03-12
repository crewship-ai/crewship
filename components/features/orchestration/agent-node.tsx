"use client"

import { memo } from "react"
import { Handle, Position, type NodeProps } from "@xyflow/react"
import { Badge } from "@/components/ui/badge"
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
  [key: string]: unknown
}

const statusStyles: Record<string, { border: string; bg: string; text: string; dot: string; glow: string }> = {
  COMPLETED: {
    border: "border-green-500/50",
    bg: "bg-green-500/5",
    text: "text-green-600 dark:text-green-400",
    dot: "bg-green-500",
    glow: "",
  },
  IN_PROGRESS: {
    border: "border-blue-500",
    bg: "bg-blue-500/5",
    text: "text-blue-600 dark:text-blue-400",
    dot: "bg-blue-500 animate-pulse",
    glow: "shadow-[0_0_15px_rgba(59,130,246,0.5)] dark:shadow-[0_0_20px_rgba(59,130,246,0.4)]",
  },
  FAILED: {
    border: "border-red-500/50",
    bg: "bg-red-500/5",
    text: "text-red-600 dark:text-red-400",
    dot: "bg-red-500",
    glow: "",
  },
  BLOCKED: {
    border: "border-amber-500/50",
    bg: "bg-amber-500/5",
    text: "text-amber-600 dark:text-amber-400",
    dot: "bg-amber-500",
    glow: "",
  },
  PENDING: {
    border: "border-slate-300 dark:border-slate-600",
    bg: "bg-slate-500/5",
    text: "text-slate-500",
    dot: "bg-slate-400",
    glow: "",
  },
  SKIPPED: {
    border: "border-gray-400/50",
    bg: "bg-gray-500/5",
    text: "text-gray-500",
    dot: "bg-gray-400",
    glow: "",
  },
}

function getInitials(name: string): string {
  return name
    .split(/[\s_-]+/)
    .slice(0, 2)
    .map((w) => w[0]?.toUpperCase() || "")
    .join("")
}

const avatarColors = [
  "bg-blue-500", "bg-green-500", "bg-purple-500", "bg-amber-500",
  "bg-rose-500", "bg-cyan-500", "bg-indigo-500", "bg-emerald-500",
]

function getAvatarColor(slug: string | null): string {
  if (!slug) return "bg-slate-400"
  let hash = 0
  for (let i = 0; i < slug.length; i++) hash = ((hash << 5) - hash + slug.charCodeAt(i)) | 0
  return avatarColors[Math.abs(hash) % avatarColors.length]
}

function formatTokens(count: number | null): string | null {
  if (count == null || count === 0) return null
  if (count >= 1000) return `${(count / 1000).toFixed(1)}k`
  return `${count}`
}

function AgentNodeComponent({ data }: NodeProps) {
  const nodeData = data as unknown as AgentNodeData
  const style = statusStyles[nodeData.status] || statusStyles.PENDING
  const initials = getInitials(nodeData.agentName)
  const avatarColor = getAvatarColor(nodeData.agentSlug)
  const tokens = formatTokens(nodeData.tokenCount)

  return (
    <div
      className={cn(
        "rounded-xl border-2 px-3 py-2.5 min-w-[210px] max-w-[250px] cursor-pointer transition-all duration-300",
        style.border, style.bg, style.glow,
        nodeData.status === "IN_PROGRESS" && "scale-[1.02]"
      )}
    >
      <Handle type="target" position={Position.Left} className="!bg-border !w-2 !h-2" />
      <Handle type="source" position={Position.Right} className="!bg-border !w-2 !h-2" />

      <div className="flex items-start gap-2.5">
        {/* Agent avatar */}
        <div className={cn(
          "w-8 h-8 rounded-full flex items-center justify-center text-[11px] font-bold text-white shrink-0 mt-0.5",
          avatarColor
        )}>
          {initials}
        </div>
        <div className="min-w-0 flex-1">
          <div className="text-sm font-medium leading-tight truncate">{nodeData.label}</div>
          <div className="text-xs text-muted-foreground mt-0.5 truncate">
            @{nodeData.agentSlug || "unassigned"}
          </div>
          <div className="flex items-center gap-1.5 mt-1.5 flex-wrap">
            <div className="flex items-center gap-1">
              <div className={cn("w-2 h-2 rounded-full shrink-0", style.dot)} />
              <Badge variant="outline" className={cn("text-[10px] px-1.5 py-0 leading-4", style.text)}>
                {nodeData.status}
              </Badge>
            </div>
            {nodeData.maxIterations && nodeData.maxIterations > 1 && (
              <Badge variant="outline" className="text-[10px] px-1.5 py-0 leading-4 text-muted-foreground">
                {nodeData.iteration || 1}/{nodeData.maxIterations}
              </Badge>
            )}
            {tokens && (
              <Badge variant="secondary" className="text-[10px] px-1.5 py-0 leading-4">
                {tokens} tok
              </Badge>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

export const AgentNode = memo(AgentNodeComponent)

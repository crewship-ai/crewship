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
  [key: string]: unknown
}

const statusStyles: Record<string, { border: string; bg: string; text: string; dot: string }> = {
  COMPLETED: {
    border: "border-green-500/50",
    bg: "bg-green-500/5",
    text: "text-green-600 dark:text-green-400",
    dot: "bg-green-500",
  },
  IN_PROGRESS: {
    border: "border-blue-500/50",
    bg: "bg-blue-500/5",
    text: "text-blue-600 dark:text-blue-400",
    dot: "bg-blue-500 animate-pulse",
  },
  FAILED: {
    border: "border-red-500/50",
    bg: "bg-red-500/5",
    text: "text-red-600 dark:text-red-400",
    dot: "bg-red-500",
  },
  BLOCKED: {
    border: "border-amber-500/50",
    bg: "bg-amber-500/5",
    text: "text-amber-600 dark:text-amber-400",
    dot: "bg-amber-500",
  },
  PENDING: {
    border: "border-slate-300 dark:border-slate-600",
    bg: "bg-slate-500/5",
    text: "text-slate-500",
    dot: "bg-slate-400",
  },
  SKIPPED: {
    border: "border-gray-400/50",
    bg: "bg-gray-500/5",
    text: "text-gray-500",
    dot: "bg-gray-400",
  },
}

function AgentNodeComponent({ data }: NodeProps) {
  const nodeData = data as unknown as AgentNodeData
  const style = statusStyles[nodeData.status] || statusStyles.PENDING

  return (
    <div
      className={cn(
        "rounded-lg border-2 px-4 py-3 min-w-[200px] max-w-[240px] shadow-sm cursor-pointer transition-shadow hover:shadow-md",
        style.border,
        style.bg
      )}
    >
      <Handle type="target" position={Position.Left} className="!bg-border !w-2 !h-2" />
      <Handle type="source" position={Position.Right} className="!bg-border !w-2 !h-2" />

      <div className="flex items-start gap-2">
        <div className={cn("w-2.5 h-2.5 rounded-full mt-1 shrink-0", style.dot)} />
        <div className="min-w-0 flex-1">
          <div className="text-sm font-medium leading-tight truncate">{nodeData.label}</div>
          <div className="text-xs text-muted-foreground mt-1 truncate">
            @{nodeData.agentSlug || "unassigned"}
          </div>
          <div className="flex items-center gap-1.5 mt-2">
            <Badge variant="outline" className={cn("text-[10px] px-1.5 py-0", style.text)}>
              {nodeData.status}
            </Badge>
            {nodeData.maxIterations && nodeData.maxIterations > 1 && (
              <Badge variant="outline" className="text-[10px] px-1.5 py-0 text-muted-foreground">
                {nodeData.iteration || 1}/{nodeData.maxIterations}
              </Badge>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

export const AgentNode = memo(AgentNodeComponent)

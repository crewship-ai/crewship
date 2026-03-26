"use client"

import { memo, type CSSProperties } from "react"
import { Handle, Position, type NodeProps } from "@xyflow/react"
import { ChevronDown, ChevronRight, Users } from "lucide-react"

// Palette matching crew colors from the system
const colorMap: Record<string, string> = {
  blue: "#3b82f6",
  emerald: "#10b981",
  violet: "#8b5cf6",
  amber: "#f59e0b",
  rose: "#f43f5e",
  cyan: "#06b6d4",
  lime: "#84cc16",
  fuchsia: "#d946ef",
}

function resolveColor(color: string | null): string {
  if (!color) return "#64748b"
  return colorMap[color] || color
}

export interface CrewGroupData {
  label: string
  slug: string
  color: string | null
  icon: string | null
  agentCount: number
  collapsed: boolean
  taskCount: number
  activeCount: number
  completedCount: number
  failedCount: number
  onToggleCollapse?: (crewId: string) => void
  crewId: string
}

function CrewGroupNodeInner({ data, id }: NodeProps) {
  const d = data as unknown as CrewGroupData
  const accent = resolveColor(d.color)
  const collapsed = d.collapsed

  const headerStyle: CSSProperties = {
    background: `linear-gradient(135deg, ${accent}18, ${accent}08)`,
    borderBottom: collapsed ? "none" : `1px solid ${accent}20`,
  }

  return (
    <div
      className="rounded-xl border overflow-hidden"
      style={{
        borderColor: `${accent}30`,
        background: "rgba(13, 15, 20, 0.60)",
        boxShadow: `0 0 30px ${accent}08`,
        minWidth: collapsed ? 260 : undefined,
        minHeight: collapsed ? undefined : undefined,
      }}
    >
      {/* Header — interactive, receives pointer events */}
      <div
        className="flex items-center gap-2.5 px-3 py-2 cursor-pointer select-none"
        style={headerStyle}
        onClick={(e) => {
          e.stopPropagation()
          d.onToggleCollapse?.(d.crewId)
        }}
      >
        <button className="shrink-0 text-white/40 hover:text-white/70 transition-colors">
          {collapsed ? (
            <ChevronRight className="h-3.5 w-3.5" />
          ) : (
            <ChevronDown className="h-3.5 w-3.5" />
          )}
        </button>

        <div
          className="w-2.5 h-2.5 rounded-full shrink-0"
          style={{ backgroundColor: accent }}
        />

        <span className="text-xs font-semibold text-white/80 truncate flex-1">
          {d.label}
        </span>

        <div className="flex items-center gap-1 text-[10px] text-white/30">
          <Users className="h-3 w-3" />
          <span>{d.agentCount}</span>
        </div>

        {collapsed && d.taskCount > 0 && (
          <div className="flex items-center gap-1.5 text-[10px]">
            {d.activeCount > 0 && (
              <span className="text-blue-400">{d.activeCount} running</span>
            )}
            {d.completedCount > 0 && (
              <span className="text-green-400">{d.completedCount} done</span>
            )}
            {d.failedCount > 0 && (
              <span className="text-red-400">{d.failedCount} failed</span>
            )}
            {d.activeCount === 0 && d.completedCount === 0 && d.failedCount === 0 && (
              <span className="text-white/30">{d.taskCount} tasks</span>
            )}
          </div>
        )}
      </div>

      {/* Body area — pointer-events none so children get clicks */}
      {!collapsed && (
        <div style={{ pointerEvents: "none", minHeight: 40 }} />
      )}

      {/* Handles for permission edges */}
      <Handle
        type="target"
        position={Position.Left}
        id={`${id}-perm-target`}
        className="!w-2 !h-2 !bg-white/20 !border-white/10"
        style={{ top: 20 }}
      />
      <Handle
        type="source"
        position={Position.Right}
        id={`${id}-perm-source`}
        className="!w-2 !h-2 !bg-white/20 !border-white/10"
        style={{ top: 20 }}
      />
    </div>
  )
}

export const CrewGroupNode = memo(CrewGroupNodeInner)

"use client"

import { memo } from "react"
import { Handle, Position, type NodeProps } from "@xyflow/react"
import { ShieldCheck, ShieldX, ShieldAlert } from "lucide-react"
import { cn } from "@/lib/utils"
import { STATUS_COLORS, STATUS_BG } from "@/lib/colors"

export interface ApprovalGateData {
  taskTitle: string
  dependencyNames: string[]
  status: "PENDING" | "APPROVED" | "REJECTED"
  onApprove?: () => void
  onReject?: () => void
  [key: string]: unknown
}

const statusConfig = {
  PENDING: {
    accent: STATUS_COLORS.BLOCKED,
    border: "border-amber-500/40",
    bg: STATUS_BG.BLOCKED,
    glow: "shadow-[0_0_20px_rgba(245,158,11,0.15)]",
    icon: ShieldAlert,
    label: "Awaiting Review",
    labelColor: "text-amber-400",
  },
  APPROVED: {
    accent: STATUS_COLORS.COMPLETED,
    border: "border-emerald-500/40",
    bg: STATUS_BG.COMPLETED,
    glow: "shadow-[0_0_20px_rgba(34,197,94,0.15)]",
    icon: ShieldCheck,
    label: "Approved",
    labelColor: "text-emerald-400",
  },
  REJECTED: {
    accent: STATUS_COLORS.FAILED,
    border: "border-rose-500/40",
    bg: STATUS_BG.FAILED,
    glow: "shadow-[0_0_20px_rgba(239,68,68,0.15)]",
    icon: ShieldX,
    label: "Rejected",
    labelColor: "text-rose-400",
  },
} as const

function ApprovalGateNodeInner({ data, id }: NodeProps) {
  const d = data as unknown as ApprovalGateData
  const cfg = statusConfig[d.status] ?? statusConfig.PENDING
  const Icon = cfg.icon
  const isPending = d.status === "PENDING"

  return (
    <div className="relative" style={{ width: 120, height: 120 }}>
      {/* Diamond container — rotated 45deg */}
      <div
        className={cn(
          "absolute inset-0 rounded-xl border-2 overflow-hidden transition-all duration-300",
          cfg.border,
          cfg.bg,
          cfg.glow,
        )}
        style={{
          transform: "rotate(45deg)",
          transformOrigin: "center center",
          top: 12,
          left: 12,
          width: 96,
          height: 96,
        }}
      />

      {/* Inner content — rotated back to upright */}
      <div
        className="absolute inset-0 flex flex-col items-center justify-center gap-1"
        style={{ zIndex: 1 }}
      >
        <Icon
          className={cn("h-5 w-5", cfg.labelColor)}
          strokeWidth={2}
        />

        <span
          className={cn(
            "text-[9px] font-semibold uppercase tracking-wider text-center leading-tight",
            cfg.labelColor,
          )}
        >
          {cfg.label}
        </span>

        {/* Task title — tiny, truncated */}
        <span className="text-[8px] text-muted-foreground max-w-[80px] truncate text-center leading-tight">
          {d.taskTitle}
        </span>

        {/* Dependency count */}
        {d.dependencyNames.length > 0 && (
          <span className="text-[8px] text-muted-foreground font-mono">
            {d.dependencyNames.length} dep{d.dependencyNames.length !== 1 ? "s" : ""}
          </span>
        )}

        {/* Action buttons — only when PENDING */}
        {isPending && (
          <div className="flex items-center gap-1 mt-0.5">
            <button
              type="button"
              className={cn(
                "px-1.5 py-0.5 rounded text-[8px] font-semibold transition-colors",
                "bg-emerald-500/20 text-emerald-400 hover:bg-emerald-500/30",
                "border border-emerald-500/30",
              )}
              onClick={(e) => {
                e.stopPropagation()
                d.onApprove?.()
              }}
            >
              Approve
            </button>
            <button
              type="button"
              className={cn(
                "px-1.5 py-0.5 rounded text-[8px] font-semibold transition-colors",
                "bg-rose-500/20 text-rose-400 hover:bg-rose-500/30",
                "border border-rose-500/30",
              )}
              onClick={(e) => {
                e.stopPropagation()
                d.onReject?.()
              }}
            >
              Reject
            </button>
          </div>
        )}
      </div>

      {/* Handles on all 4 sides */}
      <Handle
        type="target"
        position={Position.Top}
        id={`${id}-top`}
        className="!w-2.5 !h-2.5 !rounded-full !border-2"
        style={{
          background: "hsl(var(--muted))",
          borderColor: cfg.accent,
          top: 0,
          left: "50%",
          transform: "translate(-50%, -50%)",
        }}
      />
      <Handle
        type="source"
        position={Position.Right}
        id={`${id}-right`}
        className="!w-2.5 !h-2.5 !rounded-full !border-2"
        style={{
          background: "hsl(var(--muted))",
          borderColor: cfg.accent,
          right: 0,
          top: "50%",
          transform: "translate(50%, -50%)",
        }}
      />
      <Handle
        type="source"
        position={Position.Bottom}
        id={`${id}-bottom`}
        className="!w-2.5 !h-2.5 !rounded-full !border-2"
        style={{
          background: "hsl(var(--muted))",
          borderColor: cfg.accent,
          bottom: 0,
          left: "50%",
          transform: "translate(-50%, 50%)",
        }}
      />
      <Handle
        type="target"
        position={Position.Left}
        id={`${id}-left`}
        className="!w-2.5 !h-2.5 !rounded-full !border-2"
        style={{
          background: "hsl(var(--muted))",
          borderColor: cfg.accent,
          left: 0,
          top: "50%",
          transform: "translate(-50%, -50%)",
        }}
      />
    </div>
  )
}

export const ApprovalGateNode = memo(ApprovalGateNodeInner)

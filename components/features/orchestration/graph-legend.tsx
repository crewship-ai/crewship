"use client"

import { useState } from "react"
import { Info, ChevronDown, ChevronUp } from "lucide-react"
import { cn } from "@/lib/utils"

const nodeStatuses = [
  { color: "#3b82f6", label: "Running", dot: "bg-blue-500 animate-pulse" },
  { color: "#22c55e", label: "Completed", dot: "bg-green-500" },
  { color: "#ef4444", label: "Failed", dot: "bg-red-500" },
  { color: "#f59e0b", label: "Blocked", dot: "bg-amber-500" },
  { color: "#a855f7", label: "Review", dot: "bg-purple-500" },
  { color: "#64748b", label: "Pending", dot: "bg-slate-400" },
  { color: "#6b7280", label: "Skipped", dot: "bg-gray-400" },
]

const edgeTypes = [
  { label: "Task dependency", style: "border-b-2 border-dashed border-white/40", note: "colored by pair" },
  { label: "Cross-crew dependency", style: "border-b-2 border-dashed border-purple-400", note: null },
  { label: "Bidirectional permission", style: "border-b-2 border-dashed border-cyan-400", note: null },
  { label: "Unidirectional permission", style: "border-b-2 border-dashed border-amber-400", note: null },
]

export function GraphLegend() {
  const [expanded, setExpanded] = useState(false)

  return (
    <div className="absolute bottom-3 left-3 z-10">
      <button
        onClick={() => setExpanded((prev) => !prev)}
        className={cn(
          "flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg text-xs font-medium",
          "bg-[#0d0f14]/90 backdrop-blur-sm border border-white/[0.06]",
          "text-white/40 hover:text-white/70 transition-colors cursor-pointer",
        )}
      >
        <Info className="h-3 w-3" />
        <span>Legend</span>
        {expanded ? <ChevronDown className="h-3 w-3" /> : <ChevronUp className="h-3 w-3" />}
      </button>

      {expanded && (
        <div className="mt-1.5 p-3 rounded-lg bg-[#0d0f14]/95 backdrop-blur-sm border border-white/[0.06] min-w-[200px]">
          <div className="text-[10px] uppercase tracking-wider text-white/30 font-medium mb-2">Task Status</div>
          <div className="space-y-1.5">
            {nodeStatuses.map(({ color, label, dot }) => (
              <div key={label} className="flex items-center gap-2">
                <div
                  className={cn("w-2 h-2 rounded-full shrink-0", dot)}
                  style={{ backgroundColor: color }}
                />
                <span className="text-xs text-white/60">{label}</span>
              </div>
            ))}
          </div>

          <div className="mt-3 pt-2 border-t border-white/[0.06]">
            <div className="text-[10px] uppercase tracking-wider text-white/30 font-medium mb-2">Edges</div>
            <div className="space-y-1.5">
              {edgeTypes.map(({ label, style, note }) => (
                <div key={label} className="flex items-center gap-2">
                  <div className={cn("w-5 shrink-0", style)} />
                  <span className="text-xs text-white/60">{label}</span>
                  {note && <span className="text-[10px] text-white/25">({note})</span>}
                </div>
              ))}
            </div>
          </div>

          <div className="mt-3 pt-2 border-t border-white/[0.06]">
            <div className="text-[10px] text-white/25">
              <kbd className="px-1 py-0.5 rounded bg-white/[0.06] text-white/40">Shift</kbd>+Click node to highlight connections
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

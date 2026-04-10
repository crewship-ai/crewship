"use client"

import * as React from "react"
import { cn } from "@/lib/utils"

export interface HeatmapAgent {
  id: string
  slug: string
  name: string
}

export interface HeatmapBucket {
  ts: string
  series: Record<string, number> // agent_id -> count
}

interface AgentHeatmapProps {
  agents: HeatmapAgent[]
  buckets: HeatmapBucket[]
}

/**
 * Grid of agents × hourly buckets. Cell intensity encodes task count.
 * Pure Tailwind, no chart library. Cells are 16px high, ~same logic as
 * GitHub contribution heatmaps.
 */
export function AgentHeatmap({ agents, buckets }: AgentHeatmapProps) {
  // Compute the per-agent max so the color scale is relative per row
  // (otherwise a single very-busy agent would wash out everyone else).
  const maxPerAgent = React.useMemo(() => {
    const map = new Map<string, number>()
    for (const a of agents) {
      let m = 0
      for (const b of buckets) {
        const v = b.series[a.id] ?? 0
        if (v > m) m = v
      }
      map.set(a.id, m || 1)
    }
    return map
  }, [agents, buckets])

  if (agents.length === 0 || buckets.length === 0) {
    return (
      <div className="flex items-center justify-center h-[180px] text-[11px] text-muted-foreground/50">
        No activity data yet
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-1 overflow-x-auto">
      <div className="min-w-[520px]">
      {/* Header row — hour ticks */}
      <div className="grid" style={{ gridTemplateColumns: "72px 1fr" }}>
        <div />
        <div className="grid text-[9px] font-mono text-muted-foreground/50 pb-1" style={{ gridTemplateColumns: `repeat(${buckets.length}, 1fr)` }}>
          {buckets.map((b, i) => {
            const d = new Date(b.ts)
            const hh = String(d.getHours()).padStart(2, "0")
            // Show every 3rd tick to avoid clutter
            return (
              <span key={b.ts} className="text-center">
                {i % 3 === 0 ? `${hh}h` : ""}
              </span>
            )
          })}
        </div>
      </div>
      {agents.map((a) => {
        const max = maxPerAgent.get(a.id) ?? 1
        return (
          <div key={a.id} className="grid items-center gap-2" style={{ gridTemplateColumns: "72px 1fr" }}>
            <div className="text-[10px] text-foreground/60 truncate">@{a.slug}</div>
            <div className="grid gap-0.5" style={{ gridTemplateColumns: `repeat(${buckets.length}, 1fr)` }}>
              {buckets.map((b) => {
                const v = b.series[a.id] ?? 0
                const intensity = v === 0 ? 0 : Math.ceil((v / max) * 4) // 0..4
                return (
                  <div
                    key={b.ts}
                    title={`@${a.slug} · ${new Date(b.ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })} · ${v} task${v === 1 ? "" : "s"}`}
                    className={cn(
                      "h-[14px] rounded-[2px] transition-colors",
                      intensity === 0 && "bg-white/[0.03]",
                      intensity === 1 && "bg-blue-500/20",
                      intensity === 2 && "bg-blue-500/40",
                      intensity === 3 && "bg-blue-500/65",
                      intensity === 4 && "bg-blue-500",
                    )}
                  />
                )
              })}
            </div>
          </div>
        )
      })}
      {/* Scale legend */}
      <div className="flex items-center gap-1.5 text-[9px] text-muted-foreground/50 pt-2 pl-[72px]">
        <span>less</span>
        <div className="flex gap-0.5">
          <div className="h-2.5 w-2.5 rounded-[2px] bg-white/[0.03]" />
          <div className="h-2.5 w-2.5 rounded-[2px] bg-blue-500/20" />
          <div className="h-2.5 w-2.5 rounded-[2px] bg-blue-500/40" />
          <div className="h-2.5 w-2.5 rounded-[2px] bg-blue-500/65" />
          <div className="h-2.5 w-2.5 rounded-[2px] bg-blue-500" />
        </div>
        <span>more</span>
      </div>
      </div>
    </div>
  )
}

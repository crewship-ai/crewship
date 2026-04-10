"use client"

import * as React from "react"
import { cn } from "@/lib/utils"

export interface ContainerStatsEntry {
  container_id: string
  crew_id: string
  crew_slug?: string | null
  crew_name?: string | null
  crew_color?: string | null
  cpu_percent: number
  memory_used: number
  memory_limit: number
  memory_percent: number
  pids: number
  // Most recent cpu samples, newest last. Length capped client-side (~30).
  cpu_history?: number[]
}

interface ContainerResourcesTileProps {
  entries: ContainerStatsEntry[]
}

/** Dark dashboard tile showing live container resource utilisation per crew. */
export function ContainerResourcesTile({ entries }: ContainerResourcesTileProps) {
  if (entries.length === 0) {
    return (
      <div className="flex items-center justify-center h-[96px] text-[11px] text-muted-foreground/50">
        No containers running
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-3 md:gap-0">
      {entries.map((e) => {
        const memMB = Math.round(e.memory_used / 1024 / 1024)
        const memLimitMB = Math.round(e.memory_limit / 1024 / 1024)
        const color = crewColorCSS(e.crew_color)
        const status =
          e.cpu_percent > 80 ? "hot"
            : e.cpu_percent > 30 ? "running"
            : "idle"
        const nameLabel = e.crew_slug?.toUpperCase() || e.crew_id.slice(0, 3).toUpperCase()
        return (
          <div
            key={e.container_id}
            className="md:grid md:items-center md:gap-3 md:py-2.5 md:border-b md:border-border/60 md:last:border-b-0 p-3 rounded-lg border border-border/60 bg-card/60 md:bg-transparent md:p-0 md:rounded-none md:border-x-0 md:border-t-0"
            style={{
              gridTemplateColumns: "60px 1fr 86px 156px 74px",
            }}
          >
            {/* Desktop: single row, Mobile: header + body */}
            <div className="flex items-center gap-2 text-[11px] font-medium">
              <span className="w-2 h-2 rounded-sm shrink-0" style={{ background: color }} />
              {nameLabel}
              <StatusPill status={status} className="ml-auto md:hidden" />
            </div>

            <div className="mt-2 md:mt-0">
              <CpuSparkline history={e.cpu_history ?? []} color={color} />
            </div>

            <div className={cn("hidden md:block text-[10px] font-mono tabular-nums", e.cpu_percent > 80 && "text-red-400")}>
              {e.cpu_percent.toFixed(0)}% CPU
            </div>

            {/* Mobile: inline CPU + mem + pids below sparkline */}
            <div className="md:hidden flex items-center justify-between gap-3 mt-2 text-[10px] font-mono text-muted-foreground tabular-nums">
              <span className={cn(e.cpu_percent > 80 && "text-red-400")}>{e.cpu_percent.toFixed(0)}% CPU</span>
              <span>{memMB} / {memLimitMB} MB</span>
              <span>{e.pids} PIDs</span>
            </div>

            {/* Desktop: memory bar column */}
            <div className="hidden md:block min-w-0">
              <div className="relative h-[3px] rounded-full bg-white/[0.05] overflow-hidden">
                <div
                  className={cn(
                    "absolute inset-y-0 left-0 rounded-full transition-all",
                    e.memory_percent > 85 && "bg-red-400",
                  )}
                  style={{
                    width: `${Math.min(100, e.memory_percent)}%`,
                    background: e.memory_percent > 85 ? undefined : color,
                  }}
                />
              </div>
              <div className="text-[10px] font-mono text-muted-foreground mt-1 tabular-nums">
                {memMB} / {memLimitMB} MB · {e.pids} PIDs
              </div>
            </div>

            <StatusPill status={status} className="hidden md:inline-flex" />
          </div>
        )
      })}
    </div>
  )
}

function CpuSparkline({ history, color }: { history: number[]; color: string }) {
  // Render a small inline sparkline from the recent CPU samples.
  // If we have no history yet, render a flat baseline placeholder.
  const W = 200
  const H = 22
  if (history.length < 2) {
    return (
      <svg width="100%" height={H} viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none">
        <line x1="0" y1={H - 2} x2={W} y2={H - 2} stroke="rgba(255,255,255,0.08)" strokeWidth="1" strokeDasharray="2 3" />
      </svg>
    )
  }
  const max = Math.max(...history, 1)
  const points = history.map((v, i) => {
    const x = (i / (history.length - 1)) * W
    const y = H - 2 - (v / max) * (H - 4)
    return `${x.toFixed(1)},${y.toFixed(1)}`
  })
  const linePath = points.join(" ")
  const areaPath = `0,${H} ${linePath} ${W},${H}`
  return (
    <svg width="100%" height={H} viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none">
      <polyline points={areaPath} fill={color} fillOpacity="0.15" stroke="none" />
      <polyline points={linePath} fill="none" stroke={color} strokeWidth="1.5" strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  )
}

function StatusPill({ status, className }: { status: "hot" | "running" | "idle"; className?: string }) {
  const cfg = {
    hot: { label: "Hot", cls: "text-red-400 border-red-500/30 bg-red-500/10" },
    running: { label: "Running", cls: "text-blue-400 border-blue-500/30 bg-blue-500/10" },
    idle: { label: "Idle", cls: "text-muted-foreground border-border bg-muted/20" },
  }[status]
  return (
    <span className={cn("inline-flex items-center justify-center px-2 py-0.5 rounded-md text-[9px] font-semibold uppercase tracking-wide border", cfg.cls, className)}>
      {cfg.label}
    </span>
  )
}

// Map crew color palette ID → a CSS color string. Matches the palette IDs
// used in lib/colors.ts (blue, emerald, violet, amber, rose, cyan, lime, fuchsia).
function crewColorCSS(color: string | null | undefined): string {
  switch (color) {
    case "blue":    return "rgb(96, 165, 250)"
    case "emerald": return "rgb(52, 211, 153)"
    case "violet":  return "rgb(167, 139, 250)"
    case "amber":   return "rgb(251, 191, 36)"
    case "rose":    return "rgb(251, 113, 133)"
    case "cyan":    return "rgb(34, 211, 238)"
    case "lime":    return "rgb(163, 230, 53)"
    case "fuchsia": return "rgb(232, 121, 249)"
    default:        return "rgb(148, 163, 184)"
  }
}

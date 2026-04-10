"use client"

import Link from "next/link"
import { cn } from "@/lib/utils"

export interface TopMissionEntry {
  id: string
  identifier: string | null
  title: string
  crew_id: string
  crew_color: string
  cost: number
  href?: string
}

interface TopMissionsChartProps {
  missions: TopMissionEntry[]
  format?: (n: number) => string
  emptyLabel?: string
}

function formatUsd(n: number): string {
  if (n === 0) return "$0.00"
  if (n < 0.01) return "<$0.01"
  return `$${n.toFixed(2)}`
}

/**
 * Horizontal bar list of the highest-cost missions in the window.
 * Bars are normalised to the max value so the widest bar hits 100%.
 */
export function TopMissionsChart({ missions, format = formatUsd, emptyLabel = "No missions with cost data yet" }: TopMissionsChartProps) {
  if (missions.length === 0) {
    return (
      <div className="flex items-center justify-center h-[140px] text-[11px] text-muted-foreground/50">
        {emptyLabel}
      </div>
    )
  }

  const max = Math.max(...missions.map((m) => m.cost), 0.0001)

  return (
    <div className="flex flex-col gap-2">
      {missions.map((m) => {
        const pct = Math.max(4, (m.cost / max) * 100)
        const content = (
          <>
            <div className="flex items-center gap-2 text-[11px]">
              <span className="font-mono text-[10px] text-muted-foreground shrink-0 w-[44px] truncate">
                {m.identifier || "—"}
              </span>
              <span className="text-foreground/80 flex-1 truncate">{m.title}</span>
              <span className="font-mono tabular-nums text-foreground/80 w-[52px] text-right shrink-0">
                {format(m.cost)}
              </span>
            </div>
            <div className="h-1 rounded-full bg-white/[0.06] overflow-hidden mt-1">
              <div
                className="h-full rounded-full transition-all duration-500"
                style={{ width: `${pct}%`, background: m.crew_color }}
              />
            </div>
          </>
        )
        return m.href ? (
          <Link
            key={m.id}
            href={m.href}
            className={cn("block rounded-md px-1.5 py-1 -mx-1.5 hover:bg-white/[0.03] transition-colors")}
          >
            {content}
          </Link>
        ) : (
          <div key={m.id} className="px-1.5 py-1 -mx-1.5">
            {content}
          </div>
        )
      })}
    </div>
  )
}

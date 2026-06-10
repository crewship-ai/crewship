"use client"

import Link from "next/link"
import { cn } from "@/lib/utils"
import { Progress } from "@/components/ui/progress"
import { getCrewBgClass } from "@/lib/colors"

export interface TopMissionEntry {
  id: string
  identifier: string | null
  title: string
  crew_id: string
  /** Crew palette ID (e.g. "blue", "emerald"). Resolved to a Tailwind bg class
   *  via `getCrewBgClass()`. Pass null/undefined to fall back to slate. */
  crew_color: string | null
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
 * Zero-cost entries render an empty bar (no 4 % minimum) — a visible bar
 * for $0.00 was misleading, it implied spend where none existed.
 */
export function TopMissionsChart({ missions, format = formatUsd, emptyLabel = "No missions with cost data yet" }: TopMissionsChartProps) {
  if (missions.length === 0) {
    return (
      <div className="flex items-center justify-center h-[140px] text-[11px] text-muted-foreground-soft">
        {emptyLabel}
      </div>
    )
  }

  const max = Math.max(...missions.map((m) => m.cost), 0.0001)

  return (
    <div className="flex flex-col gap-2">
      {missions.map((m) => {
        // Zero-cost entries stay at 0 % — the old `Math.max(4, ...)` floor
        // was a visual lie. Small but non-zero values get a 4 % floor so the
        // bar stays visible.
        const ratio = m.cost > 0 ? (m.cost / max) * 100 : 0
        const pct = ratio > 0
          ? Math.max(4, Math.min(100, ratio))
          : 0
        const indicatorClass = getCrewBgClass(m.crew_color)
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
            <Progress
              value={pct}
              className="h-1 bg-white/[0.06] mt-1"
              indicatorClassName={cn(indicatorClass, "transition-all duration-500")}
            />
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

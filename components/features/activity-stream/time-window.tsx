"use client"

// The 24 h / 7 d window and the lane axis under it — one copy for Activity's
// home and both ledgers (#3017), so the three pages read time the same way.

import * as React from "react"

import { cn } from "@/lib/utils"

export type TimeWindow = "24h" | "7d"

export const TIME_WINDOW_MS: Record<TimeWindow, number> = { "24h": 24 * 3_600_000, "7d": 7 * 24 * 3_600_000 }

export function WindowToggle({ value, onChange }: { value: TimeWindow; onChange: (w: TimeWindow) => void }) {
  return (
    <div role="group" aria-label="Window" className="flex overflow-hidden rounded-md border border-border font-mono text-[11px]">
      {(["24h", "7d"] as const).map((w) => (
        <button
          key={w}
          type="button"
          aria-pressed={value === w}
          onClick={() => onChange(w)}
          className={cn(
            "px-2.5 py-1 transition-colors",
            value === w ? "bg-foreground/[0.08] text-foreground" : "text-muted-foreground hover:text-foreground",
          )}
        >
          {w === "24h" ? "24 h" : "7 d"}
        </button>
      ))}
    </div>
  )
}

/**
 * Tick labels above the lanes. A week is marked once a day: six ticks over
 * seven days put two labels inside one day and skipped another.
 */
export function LaneAxis({
  from,
  to,
  win,
  className = "grid-cols-[minmax(0,200px)_1fr_76px]",
}: {
  from: number
  to: number
  win: TimeWindow
  /** The lane grid's columns, so the ticks sit over the bars. */
  className?: string
}) {
  const ticks = win === "24h" ? 6 : 7
  const labels = Array.from({ length: ticks + 1 }, (_, i) => {
    const t = new Date(from + ((to - from) * i) / ticks)
    if (i === ticks) return "now"
    return win === "24h"
      ? t.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false })
      : t.toLocaleDateString(undefined, { weekday: "short" })
  })
  return (
    <div className={cn("grid gap-3 font-mono text-[10px] text-muted-foreground-soft", className)}>
      <span />
      <span className="flex justify-between">
        {labels.map((l, i) => (
          <span key={i} data-testid="axis-label">
            {l}
          </span>
        ))}
      </span>
      <span />
    </div>
  )
}

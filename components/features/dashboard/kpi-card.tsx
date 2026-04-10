"use client"

import { cn } from "@/lib/utils"

export interface KpiCardProps {
  label: string
  value: string | number
  valueColor?: string
  subtitle?: string
  deltaLabel?: string
  deltaDirection?: "up" | "down" | "flat"
}

/**
 * KPI stat card — label + value + subtitle, nothing else.
 *
 * The first iteration had decorative sparklines, but they were rendered
 * from a deterministic mock function (no real trend data available via
 * any endpoint). That was misleading — the animation suggested a trend
 * that didn't exist. Component is now honest: just the number.
 *
 * When a per-KPI trend endpoint lands, we can reintroduce sparklines
 * fed from real data. Until then: no decoration that doesn't inform.
 */
export function KpiCard({
  label,
  value,
  valueColor,
  subtitle,
  deltaLabel,
  deltaDirection = "flat",
}: KpiCardProps) {
  const deltaArrow = deltaDirection === "up" ? "▲" : deltaDirection === "down" ? "▼" : ""
  const deltaClass =
    deltaDirection === "up" ? "text-emerald-400"
      : deltaDirection === "down" ? "text-red-400"
      : "text-muted-foreground"

  return (
    <div className="flex flex-col gap-1 px-4 py-4 rounded-xl border border-border/60 bg-card">
      <div className="text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
        {label}
      </div>
      <div
        className="text-[28px] sm:text-[32px] font-semibold leading-none tabular-nums mt-1"
        style={valueColor ? { color: valueColor } : undefined}
      >
        {value}
      </div>
      {deltaLabel ? (
        <div className={cn("text-[11px] mt-1", deltaClass)}>
          {deltaArrow && <span className="mr-0.5">{deltaArrow}</span>}
          {deltaLabel}
        </div>
      ) : subtitle ? (
        <div className="text-[11px] text-muted-foreground mt-1">{subtitle}</div>
      ) : null}
    </div>
  )
}

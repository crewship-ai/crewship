"use client"

import { cn } from "@/lib/utils"

export interface MetricCardProps {
  label: string
  value: string | number
  /** Signed delta string — caller formats (e.g. "+0.12", "-3%"). */
  deltaLabel?: string
  deltaDirection?: "up" | "down" | "flat"
  /** When true, up = green (good). Flip for regression metrics. */
  upIsGood?: boolean
  subtitle?: string
}

/**
 * Small metric tile used on the Eval overview row. Mirrors the Paymaster
 * KPI card but with a neutral colour vocabulary — metrics can be either
 * direction of "good" depending on the underlying signal.
 */
export function MetricCard({
  label,
  value,
  deltaLabel,
  deltaDirection = "flat",
  upIsGood = true,
  subtitle,
}: MetricCardProps) {
  const arrow = deltaDirection === "up" ? "▲" : deltaDirection === "down" ? "▼" : ""
  const deltaClass =
    deltaDirection === "flat"
      ? "text-muted-foreground"
      : deltaDirection === "up"
        ? upIsGood
          ? "text-emerald-400"
          : "text-red-400"
        : upIsGood
          ? "text-red-400"
          : "text-emerald-400"

  return (
    <div className="flex flex-col gap-1 px-4 py-4 rounded-xl border border-border/60 bg-card">
      <div className="text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
        {label}
      </div>
      <div className="text-[26px] sm:text-[30px] font-semibold leading-none tabular-nums mt-1">
        {value}
      </div>
      {deltaLabel ? (
        <div className={cn("text-[11px] mt-1", deltaClass)}>
          {arrow && <span className="mr-0.5">{arrow}</span>}
          {deltaLabel}
        </div>
      ) : subtitle ? (
        <div className="text-[11px] text-muted-foreground mt-1">{subtitle}</div>
      ) : null}
    </div>
  )
}

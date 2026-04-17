"use client"

import { cn } from "@/lib/utils"

export interface PaymasterKpiCardProps {
  label: string
  value: string | number
  /** Secondary line — e.g. "vs prior 7d" or "across 12 crews". */
  subtitle?: string
  /** Signed delta (e.g. "+12%", "-3.4%") — formatting is caller's job. */
  deltaLabel?: string
  deltaDirection?: "up" | "down" | "flat"
  /** When true, up = green (good); when false, up = red (more spend = bad). */
  upIsGood?: boolean
}

/**
 * Paymaster KPI tile. Mirrors `dashboard/kpi-card` but inverts the colour
 * mapping because rising cost is *bad* by default.
 */
export function PaymasterKpiCard({
  label,
  value,
  subtitle,
  deltaLabel,
  deltaDirection = "flat",
  upIsGood = false,
}: PaymasterKpiCardProps) {
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

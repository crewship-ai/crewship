"use client"

import { cn } from "@/lib/utils"

export interface KpiCardProps {
  label: string
  value: string | number
  valueColor?: string
  subtitle?: string
  deltaLabel?: string
  deltaDirection?: "up" | "down" | "flat"
  sparklineData?: number[]
  sparklineColor?: string
}

/**
 * KPI stat card with inline SVG sparkline.
 * Sparkline is a thin area chart in the bottom-right of the card.
 * Pass `sparklineData` as an array of raw values (min 2, max 40).
 */
export function KpiCard({
  label,
  value,
  valueColor,
  subtitle,
  deltaLabel,
  deltaDirection = "flat",
  sparklineData,
  sparklineColor = "rgb(96, 165, 250)",
}: KpiCardProps) {
  const deltaArrow = deltaDirection === "up" ? "▲" : deltaDirection === "down" ? "▼" : ""
  const deltaClass =
    deltaDirection === "up" ? "text-emerald-400"
      : deltaDirection === "down" ? "text-red-400"
      : "text-muted-foreground"

  return (
    <div className="relative flex flex-col gap-1 min-h-[112px] px-4 py-3.5 rounded-xl border border-border/60 bg-card overflow-hidden">
      <div className="text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
        {label}
      </div>
      <div
        className="text-[28px] font-semibold leading-none tabular-nums mt-0.5"
        style={valueColor ? { color: valueColor } : undefined}
      >
        {value}
      </div>
      {deltaLabel && (
        <div className={cn("text-[11px] mt-0.5", deltaClass)}>
          {deltaArrow && <span className="mr-0.5">{deltaArrow}</span>}
          {deltaLabel}
        </div>
      )}
      {subtitle && !deltaLabel && (
        <div className="text-[11px] text-muted-foreground mt-0.5">{subtitle}</div>
      )}

      {sparklineData && sparklineData.length >= 2 && (
        <Sparkline data={sparklineData} color={sparklineColor} />
      )}
    </div>
  )
}

function Sparkline({ data, color }: { data: number[]; color: string }) {
  const width = 100
  const height = 36
  const max = Math.max(...data)
  const min = Math.min(...data)
  const range = max - min || 1

  const points = data.map((v, i) => {
    const x = (i / (data.length - 1)) * width
    const y = height - 4 - ((v - min) / range) * (height - 8)
    return `${x.toFixed(1)},${y.toFixed(1)}`
  })
  const linePath = points.join(" ")
  const areaPath = `${linePath} ${width},${height} 0,${height}`
  const fillId = `spark-fill-${color.replace(/[^a-z0-9]/gi, "")}`

  return (
    <svg
      className="absolute right-3 bottom-3 pointer-events-none"
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      preserveAspectRatio="none"
    >
      <defs>
        <linearGradient id={fillId} x1="0" x2="0" y1="0" y2="1">
          <stop offset="0%" stopColor={color} stopOpacity={0.25} />
          <stop offset="100%" stopColor={color} stopOpacity={0} />
        </linearGradient>
      </defs>
      <polyline points={areaPath} fill={`url(#${fillId})`} stroke="none" />
      <polyline points={linePath} fill="none" stroke={color} strokeWidth="1.5" strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  )
}

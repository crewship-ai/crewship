"use client"

import { useMemo } from "react"
import { Activity, Cpu, HardDrive, Network } from "lucide-react"
import type { JournalEntry } from "@/lib/types/journal"

interface ResourceSparklinesProps {
  entries: JournalEntry[]
}

interface Metric {
  ts: number
  cpu: number | null
  ram: number | null
  disk: number | null
  net: number | null
}

/**
 * Four stacked sparklines — CPU / RAM / Disk / NetIO — showing the last 30 min
 * of `container.metrics` entries. Kept dependency-light: raw inline SVG.
 *
 * Payload assumptions (best-effort; falls back to `null` when missing):
 *   - `cpu_pct`      number 0..100
 *   - `mem_pct`      number 0..100
 *   - `disk_pct`     number 0..100
 *   - `net_bytes_s`  number (bytes per second)
 */
export function ResourceSparklines({ entries }: ResourceSparklinesProps) {
  const series = useMemo<Metric[]>(() => {
    const cutoff = Date.now() - 30 * 60 * 1000
    const points = entries
      .filter((e) => e.entry_type === "container.metrics")
      .map((e) => ({
        ts: new Date(e.ts).getTime(),
        cpu: typeof e.payload?.cpu_pct === "number" ? (e.payload.cpu_pct as number) : null,
        ram: typeof e.payload?.mem_pct === "number" ? (e.payload.mem_pct as number) : null,
        disk: typeof e.payload?.disk_pct === "number" ? (e.payload.disk_pct as number) : null,
        net: typeof e.payload?.net_bytes_s === "number" ? (e.payload.net_bytes_s as number) : null,
      }))
      .filter((p) => p.ts >= cutoff)
      .sort((a, b) => a.ts - b.ts)
    // Cap at 60 points as per spec — pick evenly spaced if over.
    if (points.length <= 60) return points
    const step = points.length / 60
    const sampled: Metric[] = []
    for (let i = 0; i < 60; i++) {
      sampled.push(points[Math.floor(i * step)])
    }
    return sampled
  }, [entries])

  return (
    <div className="flex flex-col h-full bg-card border border-border/50 rounded-lg overflow-hidden">
      <div className="flex items-center justify-between px-3 py-1.5 bg-muted/40 border-b border-border/50 shrink-0">
        <div className="flex items-center gap-2">
          <Activity className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="text-[11px] text-muted-foreground font-medium">Resources (30m)</span>
        </div>
      </div>
      <div className="flex-1 min-h-0 overflow-auto divide-y divide-border/40">
        <SparkRow
          label="CPU"
          unit="%"
          Icon={Cpu}
          values={series.map((p) => p.cpu)}
          max={100}
          color="#3b82f6"
        />
        <SparkRow
          label="RAM"
          unit="%"
          Icon={Activity}
          values={series.map((p) => p.ram)}
          max={100}
          color="#22c55e"
        />
        <SparkRow
          label="Disk"
          unit="%"
          Icon={HardDrive}
          values={series.map((p) => p.disk)}
          max={100}
          color="#f59e0b"
        />
        <SparkRow
          label="Net I/O"
          unit="B/s"
          Icon={Network}
          values={series.map((p) => p.net)}
          color="#a855f7"
          formatValue={(v) => formatBytesRate(v)}
        />
      </div>
    </div>
  )
}

function SparkRow({
  label,
  unit,
  Icon,
  values,
  max,
  color,
  formatValue,
}: {
  label: string
  unit: string
  Icon: React.ComponentType<{ className?: string }>
  values: (number | null)[]
  max?: number
  color: string
  formatValue?: (v: number) => string
}) {
  const filtered = values.filter((v): v is number => typeof v === "number")
  const latest = filtered.length > 0 ? filtered[filtered.length - 1] : null
  const peak = filtered.length > 0 ? Math.max(...filtered) : null
  const effMax = max ?? (peak ?? 1)

  return (
    <div className="px-3 py-2">
      <div className="flex items-center gap-2 mb-1">
        <Icon className="h-3 w-3 text-muted-foreground" />
        <span className="text-[10px] uppercase tracking-wider text-muted-foreground font-semibold">{label}</span>
        <span className="ml-auto text-[11px] text-foreground/90 font-mono tabular-nums">
          {latest === null ? "—" : formatValue ? formatValue(latest) : `${latest.toFixed(0)}${unit}`}
        </span>
      </div>
      <Sparkline values={values} max={effMax} color={color} />
    </div>
  )
}

/** Minimal raw-SVG sparkline; 60 points max per spec. Nulls break the line. */
function Sparkline({ values, max, color }: { values: (number | null)[]; max: number; color: string }) {
  const width = 240
  const height = 30
  if (values.length === 0 || max <= 0) {
    return (
      <svg width="100%" height={height} viewBox={`0 0 ${width} ${height}`} className="text-muted-foreground/30">
        <line x1={0} y1={height / 2} x2={width} y2={height / 2} stroke="currentColor" strokeDasharray="3 3" />
      </svg>
    )
  }
  const stepX = values.length > 1 ? width / (values.length - 1) : width
  // Build polyline segments, splitting on nulls so gaps render as breaks.
  const segments: string[] = []
  let current: string[] = []
  values.forEach((v, i) => {
    if (v === null || v === undefined) {
      if (current.length > 0) {
        segments.push(current.join(" "))
        current = []
      }
      return
    }
    const x = (i * stepX).toFixed(1)
    const y = (height - Math.max(0, Math.min(1, v / max)) * (height - 4) - 2).toFixed(1)
    current.push(`${x},${y}`)
  })
  if (current.length > 0) segments.push(current.join(" "))

  return (
    <svg width="100%" height={height} viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none">
      {segments.map((seg, i) => (
        <polyline key={i} points={seg} fill="none" stroke={color} strokeWidth={1.25} vectorEffect="non-scaling-stroke" />
      ))}
    </svg>
  )
}

function formatBytesRate(v: number): string {
  if (v < 1024) return `${v.toFixed(0)} B/s`
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(1)} KB/s`
  if (v < 1024 * 1024 * 1024) return `${(v / 1024 / 1024).toFixed(1)} MB/s`
  return `${(v / 1024 / 1024 / 1024).toFixed(2)} GB/s`
}

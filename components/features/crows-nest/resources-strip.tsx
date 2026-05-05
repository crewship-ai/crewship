"use client"

import { useMemo } from "react"
import { Cpu, MemoryStick, Network, HardDrive } from "lucide-react"
import type { JournalEntry } from "@/lib/types/journal"

interface ResourcesStripProps {
  entries: JournalEntry[]
}

interface Series {
  cpu: (number | null)[]
  mem: (number | null)[]
  net: (number | null)[]
  disk: (number | null)[]
  latest: { cpu: number | null; mem: number | null; net: number | null; disk: number | null }
}

const POINTS = 60

/**
 * Always-visible resources strip — 4 horizontal cells (CPU / MEM / NET / DISK)
 * with mini sparklines + latest value. Sourced from `container.metrics`
 * journal entries, last 30 minutes.
 */
export function ResourcesStrip({ entries }: ResourcesStripProps) {
  const s = useMemo<Series>(() => extract(entries), [entries])

  return (
    <div className="grid grid-cols-2 sm:grid-cols-4 border-b border-border/50 bg-card/40">
      <Cell
        label="CPU"
        Icon={Cpu}
        values={s.cpu}
        max={100}
        color="#22d3ee"
        latest={s.latest.cpu}
        format={(v) => `${v.toFixed(0)}%`}
      />
      <Cell
        label="MEM"
        Icon={MemoryStick}
        values={s.mem}
        max={100}
        color="#a78bfa"
        latest={s.latest.mem}
        format={(v) => `${v.toFixed(0)}%`}
      />
      <Cell
        label="NET"
        Icon={Network}
        values={s.net}
        color="#34d399"
        latest={s.latest.net}
        format={fmtBytesRate}
      />
      <Cell
        label="DISK"
        Icon={HardDrive}
        values={s.disk}
        max={100}
        color="#fb923c"
        latest={s.latest.disk}
        format={(v) => `${v.toFixed(0)}%`}
      />
    </div>
  )
}

function Cell({
  label,
  Icon,
  values,
  max,
  color,
  latest,
  format,
}: {
  label: string
  Icon: React.ComponentType<{ className?: string }>
  values: (number | null)[]
  max?: number
  color: string
  latest: number | null
  format: (v: number) => string
}) {
  return (
    <div className="px-3 py-2 flex items-center gap-3 border-r border-border/50 last:border-r-0 min-w-0">
      <div className="flex items-center gap-1.5 shrink-0 w-12">
        <Icon className="h-3 w-3 text-muted-foreground" />
        <span className="text-[10px] uppercase tracking-wider text-muted-foreground">
          {label}
        </span>
      </div>
      <div className="flex-1 min-w-0 h-7">
        <Spark values={values} max={max} color={color} />
      </div>
      <div className="font-mono tabular-nums text-[12px] text-foreground shrink-0 w-16 text-right">
        {latest === null ? <span className="text-muted-foreground/60">—</span> : format(latest)}
      </div>
    </div>
  )
}

function Spark({
  values,
  max,
  color,
}: {
  values: (number | null)[]
  max?: number
  color: string
}) {
  const numeric = values.filter((v): v is number => typeof v === "number")
  if (numeric.length === 0) {
    return (
      <svg width="100%" height="100%" preserveAspectRatio="none" className="text-muted-foreground/20">
        <line x1="0%" y1="50%" x2="100%" y2="50%" stroke="currentColor" strokeDasharray="3 3" />
      </svg>
    )
  }
  const peak = Math.max(...numeric, 1)
  const m = max ?? peak
  const w = 200
  const h = 28
  const stepX = values.length > 1 ? w / (values.length - 1) : w
  let line = ""
  let area = ""
  let started = false
  values.forEach((v, i) => {
    if (v === null || v === undefined) return
    const x = (i * stepX).toFixed(1)
    const y = (h - Math.max(0, Math.min(1, v / m)) * (h - 4) - 2).toFixed(1)
    if (!started) {
      line += `M ${x} ${y}`
      area += `M ${x} ${h} L ${x} ${y}`
      started = true
    } else {
      line += ` L ${x} ${y}`
      area += ` L ${x} ${y}`
    }
  })
  if (started) area += ` L ${w} ${h} Z`

  return (
    <svg viewBox={`0 0 ${w} ${h}`} width="100%" height="100%" preserveAspectRatio="none">
      <path d={area} fill={color} fillOpacity="0.18" />
      <path d={line} fill="none" stroke={color} strokeWidth="1.25" vectorEffect="non-scaling-stroke" />
    </svg>
  )
}

function extract(entries: JournalEntry[]): Series {
  const cutoff = Date.now() - 30 * 60 * 1000
  const points = entries
    .filter((e) => e.entry_type === "container.metrics")
    .map((e) => {
      const p = e.payload ?? {}
      return {
        ts: new Date(e.ts).getTime(),
        cpu: typeof p.cpu_pct === "number" ? (p.cpu_pct as number) : null,
        mem: typeof p.mem_pct === "number" ? (p.mem_pct as number) : null,
        net: typeof p.net_bytes_s === "number" ? (p.net_bytes_s as number) : null,
        disk: typeof p.disk_pct === "number" ? (p.disk_pct as number) : null,
      }
    })
    .filter((p) => p.ts >= cutoff)
    .sort((a, b) => a.ts - b.ts)

  // Down-sample evenly to POINTS.
  const sample = (k: "cpu" | "mem" | "net" | "disk") => {
    if (points.length === 0) return Array<number | null>(POINTS).fill(null)
    if (points.length <= POINTS) return points.map((p) => p[k])
    const out: (number | null)[] = []
    const step = points.length / POINTS
    for (let i = 0; i < POINTS; i++) out.push(points[Math.floor(i * step)][k])
    return out
  }

  const last = points[points.length - 1]
  return {
    cpu: sample("cpu"),
    mem: sample("mem"),
    net: sample("net"),
    disk: sample("disk"),
    latest: {
      cpu: last?.cpu ?? null,
      mem: last?.mem ?? null,
      net: last?.net ?? null,
      disk: last?.disk ?? null,
    },
  }
}

function fmtBytesRate(v: number): string {
  if (v < 1024) return `${v.toFixed(0)} B/s`
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(0)} KB/s`
  if (v < 1024 * 1024 * 1024) return `${(v / 1024 / 1024).toFixed(1)} MB/s`
  return `${(v / 1024 / 1024 / 1024).toFixed(2)} GB/s`
}

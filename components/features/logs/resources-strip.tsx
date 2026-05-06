"use client"

import { useMemo } from "react"
import { Cpu, MemoryStick, Network, HardDrive } from "lucide-react"
import type { JournalEntry } from "@/lib/types/journal"

interface ResourcesStripProps {
  entries: JournalEntry[]
  /**
   * "single" → entries already belong to one crew (sum across is the
   *   instantaneous reading per timestamp).
   * "aggregate" → entries cover multiple crews; the strip shows the
   *   sum across all currently-active crews at each sample point.
   */
  mode?: "single" | "aggregate"
}

interface Series {
  cpu: (number | null)[]
  mem: (number | null)[]
  net: (number | null)[]
  disk: (number | null)[]
  latest: { cpu: number | null; mem: number | null; net: number | null; disk: number | null }
  /** Number of crews observed in the window (only meaningful when mode=aggregate). */
  crewCount: number
}

const POINTS = 60

/**
 * Always-visible resources strip — 4 horizontal cells (CPU / MEM / NET / DISK)
 * with mini sparklines + latest value. Sourced from `container.metrics`
 * journal entries, last 30 minutes.
 */
export function ResourcesStrip({ entries, mode = "single" }: ResourcesStripProps) {
  const s = useMemo<Series>(() => extract(entries, mode), [entries, mode])

  return (
    <div className="grid grid-cols-2 sm:grid-cols-4 border-b border-border/50 bg-card/40 relative">
      {mode === "aggregate" && s.crewCount > 0 && (
        <div className="absolute top-1 right-2 text-[10px] font-mono uppercase tracking-wider text-muted-foreground/70 pointer-events-none">
          ∑ {s.crewCount} crews
        </div>
      )}
      <Cell
        label="CPU"
        Icon={Cpu}
        values={s.cpu}
        max={mode === "aggregate" ? undefined : 100}
        color="#22d3ee"
        latest={s.latest.cpu}
        format={(v) => `${v.toFixed(0)}%`}
      />
      <Cell
        label="MEM"
        Icon={MemoryStick}
        values={s.mem}
        max={mode === "aggregate" ? undefined : 100}
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
        max={mode === "aggregate" ? undefined : 100}
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

function extract(entries: JournalEntry[], mode: "single" | "aggregate"): Series {
  const cutoff = Date.now() - 30 * 60 * 1000

  // Parse + filter once.
  type Point = {
    ts: number
    crew: string
    cpu: number | null
    mem: number | null
    net: number | null
    disk: number | null
  }
  const points: Point[] = []
  for (const e of entries) {
    if (e.entry_type !== "container.metrics") continue
    const ts = new Date(e.ts).getTime()
    if (Number.isNaN(ts) || ts < cutoff) continue
    const p = e.payload ?? {}
    points.push({
      ts,
      crew: e.crew_id ?? "",
      cpu: typeof p.cpu_pct === "number" ? (p.cpu_pct as number) : null,
      mem: typeof p.mem_pct === "number" ? (p.mem_pct as number) : null,
      net: typeof p.net_bytes_s === "number" ? (p.net_bytes_s as number) : null,
      disk: typeof p.disk_pct === "number" ? (p.disk_pct as number) : null,
    })
  }
  points.sort((a, b) => a.ts - b.ts)

  if (mode === "single") {
    return downsamplePerPoint(points)
  }

  // Aggregate: walk a virtual timeline of POINTS evenly-spaced samples
  // across the 30-min window. At each sample T, sum the latest known
  // value per crew_id observed up to T. This matches "platform-wide
  // load right now" without conflating different crews.
  const now = Date.now()
  const start = cutoff
  const step = (now - start) / POINTS
  const latestByCrew = new Map<string, Point>()
  let pi = 0
  const cpu: (number | null)[] = []
  const mem: (number | null)[] = []
  const net: (number | null)[] = []
  const disk: (number | null)[] = []
  for (let i = 0; i < POINTS; i++) {
    const t = start + i * step
    while (pi < points.length && points[pi].ts <= t) {
      latestByCrew.set(points[pi].crew, points[pi])
      pi++
    }
    if (latestByCrew.size === 0) {
      cpu.push(null); mem.push(null); net.push(null); disk.push(null)
      continue
    }
    let sCpu = 0, sMem = 0, sNet = 0, sDisk = 0
    let nCpu = 0, nMem = 0, nNet = 0, nDisk = 0
    for (const p of latestByCrew.values()) {
      if (p.cpu !== null) { sCpu += p.cpu; nCpu++ }
      if (p.mem !== null) { sMem += p.mem; nMem++ }
      if (p.net !== null) { sNet += p.net; nNet++ }
      if (p.disk !== null) { sDisk += p.disk; nDisk++ }
    }
    cpu.push(nCpu > 0 ? sCpu : null)
    mem.push(nMem > 0 ? sMem : null)
    net.push(nNet > 0 ? sNet : null)
    disk.push(nDisk > 0 ? sDisk : null)
  }

  // Latest aggregate across all crews seen at the very end.
  let lCpu = 0, lMem = 0, lNet = 0, lDisk = 0
  let nL = 0
  for (const p of latestByCrew.values()) {
    lCpu += p.cpu ?? 0
    lMem += p.mem ?? 0
    lNet += p.net ?? 0
    lDisk += p.disk ?? 0
    nL++
  }

  return {
    cpu, mem, net, disk,
    latest: {
      cpu: nL > 0 ? lCpu : null,
      mem: nL > 0 ? lMem : null,
      net: nL > 0 ? lNet : null,
      disk: nL > 0 ? lDisk : null,
    },
    crewCount: latestByCrew.size,
  }
}

function downsamplePerPoint(points: Array<{ ts: number; cpu: number | null; mem: number | null; net: number | null; disk: number | null }>): Series {
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
    crewCount: 0,
  }
}

function fmtBytesRate(v: number): string {
  if (v < 1024) return `${v.toFixed(0)} B/s`
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(0)} KB/s`
  if (v < 1024 * 1024 * 1024) return `${(v / 1024 / 1024).toFixed(1)} MB/s`
  return `${(v / 1024 / 1024 / 1024).toFixed(2)} GB/s`
}

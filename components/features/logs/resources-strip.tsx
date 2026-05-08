"use client"

import { useMemo, useState } from "react"
import { Cpu, MemoryStick, Network, HardDrive } from "lucide-react"
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { useJournalList } from "@/hooks/use-journal-list"
import { useJournalStream } from "@/hooks/use-journal-stream"
import type { JournalEntry } from "@/lib/types/journal"

interface ResourcesStripProps {
  /**
   * Self-fetches `container.metrics` for the last 30 min so the parent
   * timeline can server-side exclude metrics from its event log without
   * starving the strip. workspaceId is required; crewId narrows the
   * fetch when non-empty.
   */
  workspaceId: string | null
  crewId?: string
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
 * journal entries, last 30 minutes. The strip self-fetches its data so
 * the parent /journal timeline can hide metrics from its event log
 * (default user preference) without starving this surface.
 */
const STRIP_WINDOW_MS = 30 * 60 * 1000

export function ResourcesStrip({ workspaceId, crewId, mode = "single" }: ResourcesStripProps) {
  // since is computed once per mount; the 30-min sliding window aging
  // is fine without a re-fetch — old samples drop off naturally as
  // newer ones come in via SSE and the extract() cutoff filter trims
  // anything older than 30 min.
  const since = useMemo(() => new Date(Date.now() - STRIP_WINDOW_MS).toISOString(), [])
  const params = useMemo<Record<string, string | undefined>>(
    () => ({
      entry_type: "container.metrics",
      crew_id: crewId || undefined,
      since,
    }),
    [crewId, since],
  )

  const { entries, prependLive } = useJournalList({
    workspaceId,
    params,
    enabled: !!workspaceId,
    limit: 500,
    maxEntries: 1500,
  })
  useJournalStream({
    workspaceId,
    params,
    enabled: !!workspaceId,
    onEntry: prependLive,
  })

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
        format={mode === "aggregate" ? fmtMB : (v) => `${v.toFixed(0)}%`}
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
  const [open, setOpen] = useState(false)
  const hasData = values.some((v) => typeof v === "number")
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label={`Open ${label} history chart`}
          className="px-3 py-2 flex items-center gap-3 border-r border-border/50 last:border-r-0 min-w-0 text-left hover:bg-white/[0.025] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-emerald-500/40 transition-colors disabled:cursor-not-allowed disabled:opacity-60"
          disabled={!hasData}
        >
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
        </button>
      </PopoverTrigger>
      <PopoverContent
        side="bottom"
        align="center"
        sideOffset={6}
        className="w-[480px] p-0 border-border/60 bg-card/95 backdrop-blur"
      >
        <ChartPanel label={label} Icon={Icon} values={values} color={color} latest={latest} format={format} max={max} />
      </PopoverContent>
    </Popover>
  )
}

function ChartPanel({
  label,
  Icon,
  values,
  color,
  latest,
  format,
  max,
}: {
  label: string
  Icon: React.ComponentType<{ className?: string }>
  values: (number | null)[]
  color: string
  latest: number | null
  format: (v: number) => string
  max?: number
}) {
  // Build a synthetic time axis: each sample maps to a relative offset
  // (0 = oldest, 30 = now in minutes). The strip's source already
  // downsamples to 60 evenly-spaced points across the 30-min window
  // (POINTS=60, step = 30/60 = 0.5 min/sample).
  const data = useMemo(() => {
    const span = STRIP_WINDOW_MS
    const stepMs = values.length > 1 ? span / (values.length - 1) : span
    const start = Date.now() - span
    return values.map((v, i) => ({
      ts: start + i * stepMs,
      value: typeof v === "number" ? v : null,
    }))
  }, [values])

  const numeric = values.filter((v): v is number => typeof v === "number")
  const peak = numeric.length > 0 ? Math.max(...numeric) : null
  const avg =
    numeric.length > 0 ? numeric.reduce((a, b) => a + b, 0) / numeric.length : null

  return (
    <div className="flex flex-col gap-2 p-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 text-xs uppercase tracking-wider text-muted-foreground">
          <Icon className="h-3.5 w-3.5" />
          <span>{label}</span>
          <span className="text-muted-foreground/60 normal-case tracking-normal">· last 30 min</span>
        </div>
        <div className="font-mono tabular-nums text-sm text-foreground">
          {latest === null ? "—" : format(latest)}
        </div>
      </div>
      <div className="h-40 -mx-1">
        <ResponsiveContainer width="100%" height="100%">
          <AreaChart data={data} margin={{ top: 6, right: 8, left: 0, bottom: 0 }}>
            <defs>
              <linearGradient id={`grad-${label}`} x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor={color} stopOpacity={0.35} />
                <stop offset="100%" stopColor={color} stopOpacity={0.02} />
              </linearGradient>
            </defs>
            <CartesianGrid stroke="rgba(255,255,255,0.06)" vertical={false} />
            <XAxis
              dataKey="ts"
              type="number"
              domain={["dataMin", "dataMax"]}
              tickFormatter={fmtClock}
              tick={{ fontSize: 10, fill: "rgba(255,255,255,0.45)" }}
              axisLine={{ stroke: "rgba(255,255,255,0.12)" }}
              tickLine={false}
              minTickGap={32}
            />
            <YAxis
              tickFormatter={(v) => format(v as number)}
              tick={{ fontSize: 10, fill: "rgba(255,255,255,0.45)" }}
              axisLine={{ stroke: "rgba(255,255,255,0.12)" }}
              tickLine={false}
              width={50}
              domain={max !== undefined ? [0, max] : ["auto", "auto"]}
            />
            <Tooltip
              contentStyle={{
                background: "rgba(20,22,28,0.95)",
                border: "1px solid rgba(255,255,255,0.08)",
                borderRadius: 6,
                fontSize: 11,
              }}
              labelFormatter={(v) => fmtClock(v as number)}
              formatter={(v) =>
                typeof v === "number" ? [format(v), label] : ["—", label]
              }
            />
            <Area
              type="monotone"
              dataKey="value"
              stroke={color}
              strokeWidth={1.5}
              fill={`url(#grad-${label})`}
              isAnimationActive={false}
              connectNulls={false}
            />
          </AreaChart>
        </ResponsiveContainer>
      </div>
      <div className="flex items-center gap-4 text-[10px] font-mono text-muted-foreground border-t border-border/40 pt-2">
        <span>peak <span className="text-foreground/80">{peak === null ? "—" : format(peak)}</span></span>
        <span>avg <span className="text-foreground/80">{avg === null ? "—" : format(avg)}</span></span>
        <span>now <span className="text-foreground/80">{latest === null ? "—" : format(latest)}</span></span>
      </div>
    </div>
  )
}

function fmtClock(ms: number): string {
  const d = new Date(ms)
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`
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

  // Parse + filter once. Backend payload (per internal/server/stats.go):
  //   cpu_pct  — float, % of container CPU quota
  //   ram_pct  — float, % of container memory limit
  //   ram_mb   — float, absolute MB used
  //   net_rx   — int, **cumulative** bytes received since container start
  //   net_tx   — int, **cumulative** bytes transmitted since container start
  //   (no disk field — backend hasn't wired one yet)
  //
  // Net rate is derived per-crew from consecutive cumulative samples.
  // The per-point "net" value is left as null until we compute deltas.
  type Point = {
    ts: number
    crew: string
    cpu: number | null
    /** ram_pct in single mode, ram_mb in aggregate (sum across crews makes
     *  sense in MB; sum across containers' relative percentages doesn't). */
    mem: number | null
    netCum: number | null
    net: number | null
    disk: number | null
  }
  const points: Point[] = []
  for (const e of entries) {
    if (e.entry_type !== "container.metrics") continue
    const ts = new Date(e.ts).getTime()
    if (Number.isNaN(ts) || ts < cutoff) continue
    const p = e.payload ?? {}
    const rx = typeof p.net_rx === "number" ? (p.net_rx as number) : null
    const tx = typeof p.net_tx === "number" ? (p.net_tx as number) : null
    const netCum = rx !== null && tx !== null ? rx + tx : null
    const memValue = mode === "aggregate"
      ? (typeof p.ram_mb === "number" ? (p.ram_mb as number) : null)
      : (typeof p.ram_pct === "number" ? (p.ram_pct as number) : null)
    points.push({
      ts,
      crew: e.crew_id ?? "",
      cpu: typeof p.cpu_pct === "number" ? (p.cpu_pct as number) : null,
      mem: memValue,
      netCum,
      net: null,
      disk: null,
    })
  }
  points.sort((a, b) => a.ts - b.ts)

  // Compute per-crew net rate (bytes/sec) from cumulative deltas. Two
  // consecutive samples for the same crew → bytes/sec over that window.
  // Container restart resets the counter; treat negative deltas as null.
  const lastByCrew = new Map<string, Point>()
  for (const p of points) {
    if (p.netCum === null) continue
    const prev = lastByCrew.get(p.crew)
    if (prev && prev.netCum !== null) {
      const dtMs = p.ts - prev.ts
      const dBytes = p.netCum - prev.netCum
      if (dtMs > 0 && dBytes >= 0) {
        p.net = dBytes / (dtMs / 1000)
      }
    }
    lastByCrew.set(p.crew, p)
  }

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

function fmtMB(v: number): string {
  if (v < 1024) return `${v.toFixed(0)} MB`
  return `${(v / 1024).toFixed(2)} GiB`
}

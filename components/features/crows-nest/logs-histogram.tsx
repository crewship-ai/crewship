"use client"

import { useMemo } from "react"
import { Bar, BarChart, ResponsiveContainer, XAxis, Tooltip } from "recharts"
import type { JournalEntry } from "@/lib/types/journal"
import { SEVERITY_COLOR, severityOf } from "@/lib/journal-style"

interface LogsHistogramProps {
  entries: JournalEntry[]
  /** Bucket width in seconds. Default 10s × 90 = 15min window. */
  bucketSec?: number
  buckets?: number
  height?: number
}

interface Bucket {
  ts: number
  label: string
  info: number
  notice: number
  warn: number
  error: number
}

/**
 * Stacked-bar histogram showing journal-event volume over the last
 * `buckets * bucketSec` seconds, coloured by severity. Mirrors the
 * Grafana Explore "logs volume" panel — gives the user a full-window
 * pulse without scrolling the list.
 */
export function LogsHistogram({
  entries,
  bucketSec = 10,
  buckets = 90,
  height = 56,
}: LogsHistogramProps) {
  const data = useMemo<Bucket[]>(() => {
    const now = Date.now()
    const windowMs = bucketSec * buckets * 1000
    const startMs = now - windowMs
    const out: Bucket[] = []
    for (let i = 0; i < buckets; i++) {
      const ts = startMs + i * bucketSec * 1000
      out.push({ ts, label: fmtClock(ts), info: 0, notice: 0, warn: 0, error: 0 })
    }
    for (const e of entries) {
      const t = new Date(e.ts).getTime()
      if (Number.isNaN(t)) continue
      if (t < startMs || t > now) continue
      const idx = Math.min(buckets - 1, Math.floor((t - startMs) / (bucketSec * 1000)))
      const sev = severityOf(e.severity)
      out[idx][sev] += 1
    }
    return out
  }, [entries, bucketSec, buckets])

  return (
    <div className="px-3 py-2 border-b border-border/50 bg-card/40">
      <div className="flex items-end justify-between mb-1">
        <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
          Events / {bucketSec}s · stacked by severity
        </div>
        <div className="text-[10px] font-mono tabular-nums text-muted-foreground/70">
          {data[0]?.label} ──── {data[data.length - 1]?.label}
        </div>
      </div>
      <div style={{ height }}>
        <ResponsiveContainer width="100%" height="100%">
          <BarChart data={data} margin={{ top: 0, right: 0, left: 0, bottom: 0 }} barCategoryGap={1}>
            <XAxis dataKey="ts" hide />
            <Tooltip
              cursor={{ fill: "rgba(255,255,255,0.04)" }}
              content={<HistTooltip />}
            />
            <Bar dataKey="info"   stackId="s" fill={SEVERITY_COLOR.info}   isAnimationActive={false} />
            <Bar dataKey="notice" stackId="s" fill={SEVERITY_COLOR.notice} isAnimationActive={false} />
            <Bar dataKey="warn"   stackId="s" fill={SEVERITY_COLOR.warn}   isAnimationActive={false} />
            <Bar dataKey="error"  stackId="s" fill={SEVERITY_COLOR.error}  isAnimationActive={false} />
          </BarChart>
        </ResponsiveContainer>
      </div>
    </div>
  )
}

function HistTooltip({ active, payload }: { active?: boolean; payload?: Array<{ payload?: Bucket }> }) {
  if (!active || !payload?.length) return null
  const p = payload[0]?.payload
  if (!p) return null
  const total = p.info + p.notice + p.warn + p.error
  if (total === 0) return null
  return (
    <div className="rounded border border-border bg-popover px-2 py-1.5 text-[11px] font-mono shadow-lg">
      <div className="text-muted-foreground tabular-nums">{p.label}</div>
      <div className="flex items-center gap-1.5 mt-0.5">
        <span className="inline-block h-2 w-2 rounded-sm" style={{ background: SEVERITY_COLOR.info }} />
        <span className="text-foreground/80">info</span>
        <span className="ml-auto tabular-nums">{p.info}</span>
      </div>
      {p.notice > 0 && (
        <div className="flex items-center gap-1.5 mt-0.5">
          <span className="inline-block h-2 w-2 rounded-sm" style={{ background: SEVERITY_COLOR.notice }} />
          <span className="text-foreground/80">notice</span>
          <span className="ml-auto tabular-nums">{p.notice}</span>
        </div>
      )}
      {p.warn > 0 && (
        <div className="flex items-center gap-1.5 mt-0.5">
          <span className="inline-block h-2 w-2 rounded-sm" style={{ background: SEVERITY_COLOR.warn }} />
          <span className="text-foreground/80">warn</span>
          <span className="ml-auto tabular-nums">{p.warn}</span>
        </div>
      )}
      {p.error > 0 && (
        <div className="flex items-center gap-1.5 mt-0.5">
          <span className="inline-block h-2 w-2 rounded-sm" style={{ background: SEVERITY_COLOR.error }} />
          <span className="text-foreground/80">error</span>
          <span className="ml-auto tabular-nums">{p.error}</span>
        </div>
      )}
      <div className="border-t border-border/60 mt-1 pt-0.5 flex justify-between">
        <span className="text-muted-foreground">total</span>
        <span className="tabular-nums">{total}</span>
      </div>
    </div>
  )
}

function fmtClock(ts: number): string {
  const d = new Date(ts)
  const hh = String(d.getHours()).padStart(2, "0")
  const mm = String(d.getMinutes()).padStart(2, "0")
  const ss = String(d.getSeconds()).padStart(2, "0")
  return `${hh}:${mm}:${ss}`
}

"use client"

import { useMemo } from "react"
import { Bar, BarChart, Cell, ResponsiveContainer, Tooltip, XAxis } from "recharts"
import { X } from "lucide-react"
import type { JournalEntry } from "@/lib/types/journal"
import { SEVERITY_COLOR, severityOf } from "@/lib/journal-style"
import { sinceFromTimeRange, type TimeRange } from "./time-range-picker"

export interface BucketRange {
  fromMs: number
  toMs: number
}

interface LogsHistogramProps {
  entries: JournalEntry[]
  /** Active time range — defines the histogram window. */
  timeRange?: TimeRange
  /** Currently-selected bucket — highlighted + drives drill-down filtering in the parent. */
  selected?: BucketRange | null
  /** Click-to-drill: emit bucket on click, null when user clears. */
  onSelect?: (range: BucketRange | null) => void
  height?: number
}

interface Bucket {
  fromMs: number
  toMs: number
  label: string
  info: number
  notice: number
  warn: number
  error: number
  total: number
  selected: boolean
}

const BUCKET_COUNT = 60

/**
 * Stacked-bar histogram of journal-event volume across the active time
 * window — Elastic Discover-style. Click a bar to drill into that
 * bucket; the parent narrows the rendered list to that slice. Click
 * the highlighted bar (or the inline pill) again to clear.
 *
 * Window selection:
 *   1. `timeRange` prop wins → window = [now − range, now]
 *   2. else, infer from the entries' min/max ts
 *   3. else, fall back to last 15 minutes
 */
export function LogsHistogram({
  entries,
  timeRange,
  selected,
  onSelect,
  height = 64,
}: LogsHistogramProps) {
  const { data, fromMs: windowFrom, toMs: windowTo } = useMemo(() => {
    return computeBuckets(entries, timeRange, selected)
  }, [entries, timeRange, selected])

  const totalEvents = data.reduce((s, b) => s + b.total, 0)

  const handleBarClick = (state: unknown) => {
    if (!onSelect) return
    const s = state as { activePayload?: Array<{ payload?: Bucket }> } | null | undefined
    const bucket = s?.activePayload?.[0]?.payload
    if (!bucket) return
    if (selected && selected.fromMs === bucket.fromMs) {
      onSelect(null)
      return
    }
    onSelect({ fromMs: bucket.fromMs, toMs: bucket.toMs })
  }

  return (
    <div className="px-3 py-2 border-b border-border/50 bg-card/40">
      <div className="flex items-end justify-between mb-1 gap-3 flex-wrap">
        <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
          Events · stacked by severity
          {totalEvents > 0 && (
            <span className="ml-2 normal-case opacity-70">{totalEvents.toLocaleString()} in window</span>
          )}
        </div>
        <div className="flex items-center gap-2">
          {selected && onSelect && (
            <button
              type="button"
              onClick={() => onSelect(null)}
              className="inline-flex items-center gap-1 h-5 px-1.5 rounded border border-sky-500/40 bg-sky-500/10 text-[10px] font-mono text-sky-300 hover:bg-sky-500/20"
              title="Clear bucket selection"
            >
              {fmtRange(selected.fromMs, selected.toMs)}
              <X className="h-2.5 w-2.5" />
            </button>
          )}
          <div className="text-[10px] font-mono tabular-nums text-muted-foreground/70">
            {fmtClock(windowFrom)} ─ {fmtClock(windowTo)}
          </div>
        </div>
      </div>
      <div style={{ height, cursor: onSelect ? "pointer" : "default" }}>
        <ResponsiveContainer width="100%" height="100%">
          <BarChart
            data={data}
            margin={{ top: 0, right: 0, left: 0, bottom: 0 }}
            barCategoryGap={1}
            onClick={handleBarClick}
          >
            <XAxis dataKey="fromMs" hide />
            <Tooltip cursor={{ fill: "rgba(255,255,255,0.06)" }} content={<HistTooltip />} />
            <Bar dataKey="info" stackId="s" isAnimationActive={false}>
              {data.map((b, i) => (
                <Cell
                  key={`info-${i}`}
                  fill={SEVERITY_COLOR.info}
                  fillOpacity={selected ? (b.selected ? 1 : 0.25) : 1}
                />
              ))}
            </Bar>
            <Bar dataKey="notice" stackId="s" isAnimationActive={false}>
              {data.map((b, i) => (
                <Cell
                  key={`notice-${i}`}
                  fill={SEVERITY_COLOR.notice}
                  fillOpacity={selected ? (b.selected ? 1 : 0.25) : 1}
                />
              ))}
            </Bar>
            <Bar dataKey="warn" stackId="s" isAnimationActive={false}>
              {data.map((b, i) => (
                <Cell
                  key={`warn-${i}`}
                  fill={SEVERITY_COLOR.warn}
                  fillOpacity={selected ? (b.selected ? 1 : 0.25) : 1}
                />
              ))}
            </Bar>
            <Bar dataKey="error" stackId="s" isAnimationActive={false}>
              {data.map((b, i) => (
                <Cell
                  key={`error-${i}`}
                  fill={SEVERITY_COLOR.error}
                  fillOpacity={selected ? (b.selected ? 1 : 0.25) : 1}
                />
              ))}
            </Bar>
          </BarChart>
        </ResponsiveContainer>
      </div>
    </div>
  )
}

function computeBuckets(
  entries: JournalEntry[],
  timeRange: TimeRange | undefined,
  selected: BucketRange | null | undefined,
): { data: Bucket[]; fromMs: number; toMs: number } {
  const now = Date.now()
  let fromMs: number
  let toMs: number = now

  if (timeRange) {
    const since = sinceFromTimeRange(timeRange)
    fromMs = since ? new Date(since).getTime() : computeFromEntries(entries, now).fromMs
  } else {
    const inferred = computeFromEntries(entries, now)
    fromMs = inferred.fromMs
    toMs = inferred.toMs
  }

  // Guard: ensure non-zero range so we always have meaningful buckets.
  if (toMs - fromMs < 1000) toMs = fromMs + 1000

  const bucketMs = (toMs - fromMs) / BUCKET_COUNT
  const buckets: Bucket[] = []
  for (let i = 0; i < BUCKET_COUNT; i++) {
    const f = fromMs + i * bucketMs
    const t = f + bucketMs
    buckets.push({
      fromMs: f,
      toMs: t,
      label: fmtClock(f),
      info: 0,
      notice: 0,
      warn: 0,
      error: 0,
      total: 0,
      selected: Boolean(selected && Math.abs(f - selected.fromMs) < bucketMs / 2),
    })
  }

  for (const e of entries) {
    const ts = new Date(e.ts).getTime()
    if (Number.isNaN(ts) || ts < fromMs || ts > toMs) continue
    const idx = Math.min(BUCKET_COUNT - 1, Math.floor((ts - fromMs) / bucketMs))
    const sev = severityOf(e.severity)
    buckets[idx][sev] += 1
    buckets[idx].total += 1
  }

  return { data: buckets, fromMs, toMs }
}

function computeFromEntries(entries: JournalEntry[], now: number): { fromMs: number; toMs: number } {
  if (entries.length === 0) {
    return { fromMs: now - 15 * 60 * 1000, toMs: now }
  }
  let min = Infinity
  let max = -Infinity
  for (const e of entries) {
    const t = new Date(e.ts).getTime()
    if (Number.isNaN(t)) continue
    if (t < min) min = t
    if (t > max) max = t
  }
  if (!isFinite(min) || !isFinite(max)) return { fromMs: now - 15 * 60 * 1000, toMs: now }
  // Pad both sides slightly so the first/last bucket isn't a sliver.
  const span = Math.max(max - min, 60_000)
  return { fromMs: min - span * 0.02, toMs: max + span * 0.02 }
}

function HistTooltip({
  active,
  payload,
}: {
  active?: boolean
  payload?: Array<{ payload?: Bucket }>
}) {
  if (!active || !payload?.length) return null
  const p = payload[0]?.payload
  if (!p) return null
  if (p.total === 0) return null
  return (
    <div className="rounded border border-border bg-popover px-2 py-1.5 text-[11px] font-mono shadow-lg">
      <div className="text-muted-foreground tabular-nums">{fmtRange(p.fromMs, p.toMs)}</div>
      {p.info > 0 && <SevRow label="info" color={SEVERITY_COLOR.info} value={p.info} />}
      {p.notice > 0 && <SevRow label="notice" color={SEVERITY_COLOR.notice} value={p.notice} />}
      {p.warn > 0 && <SevRow label="warn" color={SEVERITY_COLOR.warn} value={p.warn} />}
      {p.error > 0 && <SevRow label="error" color={SEVERITY_COLOR.error} value={p.error} />}
      <div className="border-t border-border/60 mt-1 pt-0.5 flex justify-between">
        <span className="text-muted-foreground">total</span>
        <span className="tabular-nums">{p.total}</span>
      </div>
    </div>
  )
}

function SevRow({ label, color, value }: { label: string; color: string; value: number }) {
  return (
    <div className="flex items-center gap-1.5 mt-0.5">
      <span className="inline-block h-2 w-2 rounded-sm" style={{ background: color }} />
      <span className="text-foreground/80">{label}</span>
      <span className="ml-auto tabular-nums">{value}</span>
    </div>
  )
}

function fmtClock(ts: number): string {
  if (!Number.isFinite(ts)) return "—"
  const d = new Date(ts)
  const hh = String(d.getHours()).padStart(2, "0")
  const mm = String(d.getMinutes()).padStart(2, "0")
  const ss = String(d.getSeconds()).padStart(2, "0")
  return `${hh}:${mm}:${ss}`
}

function fmtRange(fromMs: number, toMs: number): string {
  const span = toMs - fromMs
  // For multi-day spans, include the date.
  if (span > 24 * 60 * 60 * 1000) {
    return `${fmtDateTime(fromMs)} → ${fmtDateTime(toMs)}`
  }
  return `${fmtClock(fromMs)} → ${fmtClock(toMs)}`
}

function fmtDateTime(ts: number): string {
  if (!Number.isFinite(ts)) return "—"
  const d = new Date(ts)
  const m = String(d.getMonth() + 1).padStart(2, "0")
  const day = String(d.getDate()).padStart(2, "0")
  return `${m}-${day} ${fmtClock(ts)}`
}

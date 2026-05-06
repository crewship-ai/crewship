"use client"

import { useCallback, useMemo, useRef } from "react"
import { Bar, BarChart, Cell, ResponsiveContainer, Tooltip, XAxis } from "recharts"
import type { JournalEntry } from "@/lib/types/journal"
import { SEVERITY_COLOR, severityOf } from "@/lib/journal-style"
import { sinceFromTimeRange, untilFromTimeRange, type TimeRange, type CustomRange } from "./time-range-picker"

export interface BucketRange {
  fromMs: number
  toMs: number
}

interface LogsHistogramProps {
  entries: JournalEntry[]
  timeRange?: TimeRange
  customRange?: CustomRange | null
  /** Currently-active bucket selection — shaded on the chart. */
  selected?: BucketRange | null
  /**
   * Click commits a single-bucket selection. Click the already-selected
   * bucket to clear (emit `null`). Plain click only — no drag, no
   * hover tracking — to avoid accidental selections during cursor
   * movement.
   */
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
}

const BUCKET_COUNT = 60

/**
 * Stacked-bar histogram of journal-event volume across the active time
 * window. **Click a bar** to filter the list to that time bucket.
 * **Click the same bar again** (or the toolbar pill) to clear.
 *
 * No hover-to-select, no drag — every selection is a deliberate click.
 * The narrowing is client-side so the list updates within one render
 * frame; the time-range picker stays untouched (it controls what the
 * backend fetches, not what's filtered after).
 */
export function LogsHistogram({
  entries,
  timeRange,
  customRange,
  selected,
  onSelect,
  height = 64,
}: LogsHistogramProps) {
  const { data, fromMs: windowFrom, toMs: windowTo } = useMemo(
    () => computeBuckets(entries, timeRange, customRange),
    [entries, timeRange, customRange],
  )

  const totalEvents = data.reduce((s, b) => s + b.total, 0)

  const containerRef = useRef<HTMLDivElement>(null)

  const handleClick = useCallback(
    (e: React.MouseEvent<HTMLDivElement>) => {
      if (!onSelect) return
      const rect = containerRef.current?.getBoundingClientRect()
      if (!rect || rect.width <= 0) return
      const ratio = (e.clientX - rect.left) / rect.width
      if (ratio < 0 || ratio > 1) return
      const idx = Math.max(0, Math.min(BUCKET_COUNT - 1, Math.floor(ratio * BUCKET_COUNT)))
      const b = data[idx]
      if (!b) return
      // Toggle off if clicking the same single-bucket selection.
      if (selected && selected.fromMs === b.fromMs && selected.toMs === b.toMs) {
        onSelect(null)
        return
      }
      onSelect({ fromMs: b.fromMs, toMs: b.toMs })
    },
    [onSelect, data, selected],
  )

  return (
    <div className="px-3 py-2 border-b border-border/50 bg-card/40">
      <div className="flex items-end justify-between mb-1 gap-3 flex-wrap">
        <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
          Events · stacked by severity
          {totalEvents > 0 && (
            <span className="ml-2 normal-case opacity-70">{totalEvents.toLocaleString()} in window</span>
          )}
          {onSelect && totalEvents > 0 && !selected && (
            <span className="ml-2 normal-case opacity-50 italic">click a bar to filter</span>
          )}
        </div>
        <div className="text-[10px] font-mono tabular-nums text-muted-foreground/70">
          {windowFmt(windowFrom, windowTo)}
        </div>
      </div>
      <div
        ref={containerRef}
        onClick={handleClick}
        className="relative [&_*]:!outline-none"
        style={{
          height,
          cursor: onSelect ? "pointer" : "default",
        }}
      >
        {selected && <SelectionOverlay data={data} selected={selected} />}

        <ResponsiveContainer width="100%" height="100%">
          <BarChart
            data={data}
            margin={{ top: 0, right: 0, left: 0, bottom: 0 }}
            barCategoryGap={1}
            accessibilityLayer={false}
          >
            <XAxis dataKey="fromMs" hide />
            <Tooltip cursor={{ fill: "rgba(255,255,255,0.06)" }} content={<HistTooltip />} />
            <Bar dataKey="info" stackId="s" isAnimationActive={false}>
              {data.map((b, i) => (
                <Cell
                  key={`info-${i}`}
                  fill={SEVERITY_COLOR.info}
                  fillOpacity={selected ? (isInRange(b, selected) ? 1 : 0.25) : 1}
                />
              ))}
            </Bar>
            <Bar dataKey="notice" stackId="s" isAnimationActive={false}>
              {data.map((b, i) => (
                <Cell
                  key={`notice-${i}`}
                  fill={SEVERITY_COLOR.notice}
                  fillOpacity={selected ? (isInRange(b, selected) ? 1 : 0.25) : 1}
                />
              ))}
            </Bar>
            <Bar dataKey="warn" stackId="s" isAnimationActive={false}>
              {data.map((b, i) => (
                <Cell
                  key={`warn-${i}`}
                  fill={SEVERITY_COLOR.warn}
                  fillOpacity={selected ? (isInRange(b, selected) ? 1 : 0.25) : 1}
                />
              ))}
            </Bar>
            <Bar dataKey="error" stackId="s" isAnimationActive={false}>
              {data.map((b, i) => (
                <Cell
                  key={`error-${i}`}
                  fill={SEVERITY_COLOR.error}
                  fillOpacity={selected ? (isInRange(b, selected) ? 1 : 0.25) : 1}
                />
              ))}
            </Bar>
          </BarChart>
        </ResponsiveContainer>
      </div>
    </div>
  )
}

/** Whether the bucket falls within the selected range (with half-bucket tolerance). */
function isInRange(b: Bucket, sel: BucketRange): boolean {
  const halfBucket = (b.toMs - b.fromMs) / 2
  return b.fromMs >= sel.fromMs - halfBucket && b.toMs <= sel.toMs + halfBucket
}

/** Persistent selection overlay — sits behind the bars. */
function SelectionOverlay({ data, selected }: { data: Bucket[]; selected: BucketRange }) {
  if (data.length === 0) return null
  const halfBucket = (data[0].toMs - data[0].fromMs) / 2
  let firstIdx = -1
  let lastIdx = -1
  for (let i = 0; i < data.length; i++) {
    if (data[i].fromMs >= selected.fromMs - halfBucket && data[i].toMs <= selected.toMs + halfBucket) {
      if (firstIdx === -1) firstIdx = i
      lastIdx = i
    }
  }
  if (firstIdx < 0 || lastIdx < 0) return null
  const left = (firstIdx / BUCKET_COUNT) * 100
  const width = ((lastIdx - firstIdx + 1) / BUCKET_COUNT) * 100
  return (
    <div
      className="absolute top-0 bottom-0 pointer-events-none border-l border-r border-sky-500/60 bg-sky-500/10"
      style={{ left: `${left}%`, width: `${width}%` }}
    />
  )
}

function computeBuckets(
  entries: JournalEntry[],
  timeRange: TimeRange | undefined,
  customRange: CustomRange | null | undefined,
): { data: Bucket[]; fromMs: number; toMs: number } {
  const now = Date.now()
  let fromMs: number
  let toMs: number = now

  if (timeRange) {
    const since = sinceFromTimeRange(timeRange, customRange)
    fromMs = since ? new Date(since).getTime() : computeFromEntries(entries, now).fromMs
    toMs = untilFromTimeRange(timeRange, customRange)
  } else {
    const inferred = computeFromEntries(entries, now)
    fromMs = inferred.fromMs
    toMs = inferred.toMs
  }

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
    })
  }

  for (const e of entries) {
    const ts = tsOf(e)
    if (Number.isNaN(ts) || ts < fromMs || ts > toMs) continue
    const idx = Math.min(BUCKET_COUNT - 1, Math.floor((ts - fromMs) / bucketMs))
    const sev = severityOf(e.severity)
    buckets[idx][sev] += 1
    buckets[idx].total += 1
  }

  return { data: buckets, fromMs, toMs }
}

function tsOf(e: JournalEntry): number {
  return (e as JournalEntry & { _tsMs?: number })._tsMs ?? new Date(e.ts).getTime()
}

function computeFromEntries(entries: JournalEntry[], now: number): { fromMs: number; toMs: number } {
  if (entries.length === 0) {
    return { fromMs: now - 15 * 60 * 1000, toMs: now }
  }
  let min = Infinity
  let max = -Infinity
  for (const e of entries) {
    const t = tsOf(e)
    if (Number.isNaN(t)) continue
    if (t < min) min = t
    if (t > max) max = t
  }
  if (!isFinite(min) || !isFinite(max)) return { fromMs: now - 15 * 60 * 1000, toMs: now }
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
  if (span > 24 * 60 * 60 * 1000) {
    return `${fmtDateTime(fromMs)} → ${fmtDateTime(toMs)}`
  }
  return `${fmtClock(fromMs)} → ${fmtClock(toMs)}`
}

function windowFmt(fromMs: number, toMs: number): string {
  if (!Number.isFinite(fromMs) || !Number.isFinite(toMs)) return "—"
  const span = toMs - fromMs
  if (span > 24 * 60 * 60 * 1000) {
    return `${fmtDateTime(fromMs)} ─ ${fmtDateTime(toMs)}`
  }
  return `${fmtClock(fromMs)} ─ ${fmtClock(toMs)}`
}

function fmtDateTime(ts: number): string {
  if (!Number.isFinite(ts)) return "—"
  const d = new Date(ts)
  const m = String(d.getMonth() + 1).padStart(2, "0")
  const day = String(d.getDate()).padStart(2, "0")
  return `${m}-${day} ${fmtClock(ts)}`
}

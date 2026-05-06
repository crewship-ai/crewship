"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
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
  /** Active time range — defines the histogram window. */
  timeRange?: TimeRange
  /** Custom [from, to] window when timeRange === "custom". */
  customRange?: CustomRange | null
  /**
   * Called when the user clicks a single bar OR drags across several
   * to commit a range selection. The parent should treat the emitted
   * range as the new active time window — which means narrowing the
   * fetched data, NOT just doing a client-side filter. To "go back",
   * the user picks a wider preset from the time-range picker.
   */
  onRangeSelect?: (range: BucketRange) => void
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
/** Pixel distance the pointer must travel between down and up to count as a drag. */
const DRAG_THRESHOLD_PX = 8

/**
 * Stacked-bar histogram of journal-event volume across the active time
 * window. Click a bar to drill into that bucket's time range; **press
 * and drag** across multiple bars to commit a multi-bucket range.
 *
 * The histogram window IS the active time range — selection narrows
 * the time range itself rather than running a parallel client-side
 * filter. This matches Elastic Discover / Grafana Logs: the chart
 * always shows the full extent of what's loaded, and clicking a slice
 * zooms in.
 */
export function LogsHistogram({
  entries,
  timeRange,
  customRange,
  onRangeSelect,
  height = 64,
}: LogsHistogramProps) {
  const { data, fromMs: windowFrom, toMs: windowTo } = useMemo(
    () => computeBuckets(entries, timeRange, customRange),
    [entries, timeRange, customRange],
  )

  const totalEvents = data.reduce((s, b) => s + b.total, 0)

  // ─── Drag-or-click selection ────────────────────────────────────────
  const containerRef = useRef<HTMLDivElement>(null)
  const dragStartIdxRef = useRef<number | null>(null)
  const dragStartXRef = useRef<number | null>(null)
  const dataRef = useRef(data)
  const onRangeSelectRef = useRef(onRangeSelect)
  useEffect(() => { dataRef.current = data }, [data])
  useEffect(() => { onRangeSelectRef.current = onRangeSelect }, [onRangeSelect])

  // Visual drag-end index (only set once the pointer has moved past
  // DRAG_THRESHOLD_PX, so a quick click never paints a drag overlay).
  const [dragVisualEnd, setDragVisualEnd] = useState<number | null>(null)
  const [isDragging, setIsDragging] = useState(false)

  const pixelToIndex = useCallback((clientX: number): number | null => {
    const rect = containerRef.current?.getBoundingClientRect()
    if (!rect || rect.width <= 0) return null
    const ratio = (clientX - rect.left) / rect.width
    if (ratio < 0 || ratio > 1) return null
    const idx = Math.floor(ratio * BUCKET_COUNT)
    return Math.max(0, Math.min(BUCKET_COUNT - 1, idx))
  }, [])

  const handlePointerDown = useCallback(
    (e: React.PointerEvent<HTMLDivElement>) => {
      if (!onRangeSelect) return
      if (e.button !== 0) return
      const idx = pixelToIndex(e.clientX)
      if (idx === null) return
      e.preventDefault()
      dragStartIdxRef.current = idx
      dragStartXRef.current = e.clientX
      setDragVisualEnd(null)
      setIsDragging(false)
    },
    [onRangeSelect, pixelToIndex],
  )

  const handlePointerMove = useCallback(
    (e: React.PointerEvent<HTMLDivElement>) => {
      if (dragStartIdxRef.current === null || dragStartXRef.current === null) return
      const dx = Math.abs(e.clientX - dragStartXRef.current)
      if (dx < DRAG_THRESHOLD_PX) return
      const idx = pixelToIndex(e.clientX)
      if (idx === null) return
      setIsDragging(true)
      setDragVisualEnd(idx)
    },
    [pixelToIndex],
  )

  // Document-level mouseup so a release outside the chart still finalizes.
  useEffect(() => {
    const finalize = (e: MouseEvent) => {
      const startIdx = dragStartIdxRef.current
      const startX = dragStartXRef.current
      dragStartIdxRef.current = null
      dragStartXRef.current = null
      setDragVisualEnd(null)
      setIsDragging(false)
      if (startIdx === null || startX === null) return
      const onSel = onRangeSelectRef.current
      if (!onSel) return
      const buckets = dataRef.current
      const moved = Math.abs(e.clientX - startX) >= DRAG_THRESHOLD_PX
      if (!moved) {
        // Click — single bucket.
        const b = buckets[startIdx]
        if (b) onSel({ fromMs: b.fromMs, toMs: b.toMs })
        return
      }
      // Drag — find end bucket from final pointer position.
      const rect = containerRef.current?.getBoundingClientRect()
      if (!rect || rect.width <= 0) return
      const ratio = (e.clientX - rect.left) / rect.width
      const endIdx = Math.max(0, Math.min(BUCKET_COUNT - 1, Math.floor(ratio * BUCKET_COUNT)))
      const lo = Math.min(startIdx, endIdx)
      const hi = Math.max(startIdx, endIdx)
      const startBucket = buckets[lo]
      const endBucket = buckets[hi]
      if (!startBucket || !endBucket) return
      onSel({ fromMs: startBucket.fromMs, toMs: endBucket.toMs })
    }
    document.addEventListener("mouseup", finalize)
    return () => document.removeEventListener("mouseup", finalize)
  }, [])

  // Drag overlay rectangle as % of chart width.
  const dragRect = useMemo(() => {
    if (!isDragging || dragStartIdxRef.current === null || dragVisualEnd === null) return null
    const lo = Math.min(dragStartIdxRef.current, dragVisualEnd)
    const hi = Math.max(dragStartIdxRef.current, dragVisualEnd)
    return {
      left: (lo / BUCKET_COUNT) * 100,
      width: ((hi - lo + 1) / BUCKET_COUNT) * 100,
    }
  }, [isDragging, dragVisualEnd])

  return (
    <div className="px-3 py-2 border-b border-border/50 bg-card/40">
      <div className="flex items-end justify-between mb-1 gap-3 flex-wrap">
        <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
          Events · stacked by severity
          {totalEvents > 0 && (
            <span className="ml-2 normal-case opacity-70">{totalEvents.toLocaleString()} in window</span>
          )}
          {onRangeSelect && totalEvents > 0 && (
            <span className="ml-2 normal-case opacity-50 italic">click a bar to zoom · drag for range</span>
          )}
        </div>
        <div className="text-[10px] font-mono tabular-nums text-muted-foreground/70">
          {windowFmt(windowFrom, windowTo)}
        </div>
      </div>
      <div
        ref={containerRef}
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        className="relative [&_*]:!outline-none"
        style={{
          height,
          cursor: onRangeSelect ? (isDragging ? "ew-resize" : "crosshair") : "default",
          userSelect: dragStartIdxRef.current !== null ? "none" : undefined,
        }}
      >
        {dragRect && (
          <div
            className="absolute top-0 bottom-0 pointer-events-none border border-dashed border-sky-400/60 bg-sky-500/15 z-10"
            style={{ left: `${dragRect.left}%`, width: `${dragRect.width}%` }}
          />
        )}

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
              {data.map((_, i) => <Cell key={`info-${i}`} fill={SEVERITY_COLOR.info} />)}
            </Bar>
            <Bar dataKey="notice" stackId="s" isAnimationActive={false}>
              {data.map((_, i) => <Cell key={`notice-${i}`} fill={SEVERITY_COLOR.notice} />)}
            </Bar>
            <Bar dataKey="warn" stackId="s" isAnimationActive={false}>
              {data.map((_, i) => <Cell key={`warn-${i}`} fill={SEVERITY_COLOR.warn} />)}
            </Bar>
            <Bar dataKey="error" stackId="s" isAnimationActive={false}>
              {data.map((_, i) => <Cell key={`error-${i}`} fill={SEVERITY_COLOR.error} />)}
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

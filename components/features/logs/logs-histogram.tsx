"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Bar, BarChart, Cell, ResponsiveContainer, Tooltip, XAxis } from "recharts"
import { X } from "lucide-react"
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
  /** Currently-selected bucket range — drives drill-down filtering in the parent. */
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
}

const BUCKET_COUNT = 60

/**
 * Stacked-bar histogram of journal-event volume across the active time
 * window. Click a bar to drill into that bucket; **drag across bars to
 * select a multi-bucket range** (Elastic Discover style). Click the
 * highlighted region or the inline pill to clear.
 *
 * Drag detection is done at the wrapper-div level using pointer pixel
 * coordinates → bucket index. We deliberately don't go through
 * recharts' onClick/onMouseDown state machine because activePayload
 * isn't reliably populated on the very first mousedown without a prior
 * hover, which led to "click does nothing" reports.
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

  // ─── Drag-to-select ──────────────────────────────────────────────────
  const containerRef = useRef<HTMLDivElement>(null)
  const [dragStartIdx, setDragStartIdx] = useState<number | null>(null)
  const [dragEndIdx, setDragEndIdx] = useState<number | null>(null)

  // Refs mirror state for the document-level mouseup handler — using
  // state directly there hits stale-closure issues across re-renders.
  const dragStartIdxRef = useRef<number | null>(null)
  const dragEndIdxRef = useRef<number | null>(null)
  const dataRef = useRef(data)
  const selectedRef = useRef(selected)
  const onSelectRef = useRef(onSelect)
  useEffect(() => { dragStartIdxRef.current = dragStartIdx }, [dragStartIdx])
  useEffect(() => { dragEndIdxRef.current = dragEndIdx }, [dragEndIdx])
  useEffect(() => { dataRef.current = data }, [data])
  useEffect(() => { selectedRef.current = selected }, [selected])
  useEffect(() => { onSelectRef.current = onSelect }, [onSelect])

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
      if (!onSelect) return
      // Ignore right-click / middle-click — only respond to primary button.
      if (e.button !== 0) return
      const idx = pixelToIndex(e.clientX)
      if (idx === null) return
      e.preventDefault()
      setDragStartIdx(idx)
      setDragEndIdx(idx)
    },
    [onSelect, pixelToIndex],
  )

  const handlePointerMove = useCallback(
    (e: React.PointerEvent<HTMLDivElement>) => {
      if (dragStartIdxRef.current === null) return
      const idx = pixelToIndex(e.clientX)
      if (idx === null) return
      if (idx !== dragEndIdxRef.current) setDragEndIdx(idx)
    },
    [pixelToIndex],
  )

  // Document mouseup so a drag that ends outside the chart still
  // finalizes — and so we never depend on recharts' synthetic event
  // bubbling to fire mouseup at the right time.
  useEffect(() => {
    const finalize = () => {
      const s = dragStartIdxRef.current
      const e = dragEndIdxRef.current
      if (s === null || e === null) return
      setDragStartIdx(null)
      setDragEndIdx(null)
      const onSel = onSelectRef.current
      if (!onSel) return
      const buckets = dataRef.current
      const lo = Math.min(s, e)
      const hi = Math.max(s, e)
      const startBucket = buckets[lo]
      const endBucket = buckets[hi]
      if (!startBucket || !endBucket) return
      // Pure click: lo === hi → toggle the single bucket.
      if (lo === hi) {
        const sel = selectedRef.current
        if (sel && sel.fromMs === startBucket.fromMs && sel.toMs === startBucket.toMs) {
          onSel(null)
        } else {
          onSel({ fromMs: startBucket.fromMs, toMs: startBucket.toMs })
        }
      } else {
        onSel({ fromMs: startBucket.fromMs, toMs: endBucket.toMs })
      }
    }
    document.addEventListener("mouseup", finalize)
    return () => document.removeEventListener("mouseup", finalize)
  }, [])

  // Bucket-level "is this in the selected range" — driven by props, not
  // the in-flight drag overlay.
  const isBucketSelected = useCallback(
    (b: Bucket) => {
      if (!selected) return false
      // Tolerance of half a bucket in case of float drift.
      const halfBucket = (b.toMs - b.fromMs) / 2
      return b.fromMs >= selected.fromMs - halfBucket && b.toMs <= selected.toMs + halfBucket
    },
    [selected],
  )

  // Live drag overlay — left/width as % of chart width, computed from
  // bucket indices so it always snaps to bar edges.
  const dragRect =
    dragStartIdx !== null && dragEndIdx !== null && dragStartIdx !== dragEndIdx
      ? {
          left: (Math.min(dragStartIdx, dragEndIdx) / BUCKET_COUNT) * 100,
          width: ((Math.abs(dragEndIdx - dragStartIdx) + 1) / BUCKET_COUNT) * 100,
        }
      : null

  const isDragging = dragStartIdx !== null

  return (
    <div className="px-3 py-2 border-b border-border/50 bg-card/40">
      <div className="flex items-end justify-between mb-1 gap-3 flex-wrap">
        <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
          Events · stacked by severity
          {totalEvents > 0 && (
            <span className="ml-2 normal-case opacity-70">{totalEvents.toLocaleString()} in window</span>
          )}
          {onSelect && totalEvents > 0 && !selected && (
            <span className="ml-2 normal-case opacity-50 italic">click or drag to filter</span>
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
            {windowFmt(windowFrom, windowTo)}
          </div>
        </div>
      </div>
      <div
        ref={containerRef}
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        className="relative [&_*]:!outline-none"
        style={{
          height,
          cursor: onSelect ? (isDragging ? "ew-resize" : "crosshair") : "default",
          userSelect: isDragging ? "none" : undefined,
        }}
      >
        {/* Selection overlay (committed selection) — rendered as a
            positioned div so we sidestep recharts' ReferenceArea
            quirks with categorical axes. */}
        {selected && !isDragging && (
          <SelectionOverlay
            data={data}
            selected={selected}
          />
        )}

        {/* Drag overlay (in-flight) — purely a visual hint while the
            user sweeps, doesn't touch parent state. */}
        {dragRect && (
          <div
            className="absolute top-0 bottom-0 pointer-events-none border border-dashed border-sky-400/60 bg-sky-500/15"
            style={{ left: `${dragRect.left}%`, width: `${dragRect.width}%` }}
          />
        )}

        <ResponsiveContainer width="100%" height="100%">
          <BarChart
            data={data}
            margin={{ top: 0, right: 0, left: 0, bottom: 0 }}
            barCategoryGap={1}
            // Disable recharts' built-in keyboard accessibility layer —
            // the focus outline it adds rendered as a giant blue
            // rectangle around the whole chart in dark mode and looked
            // like a phantom selection. Drag/click via wrapper div is
            // what drives selection now.
            accessibilityLayer={false}
          >
            <XAxis dataKey="fromMs" hide />
            <Tooltip cursor={{ fill: "rgba(255,255,255,0.06)" }} content={<HistTooltip />} />
            <Bar dataKey="info" stackId="s" isAnimationActive={false}>
              {data.map((b, i) => (
                <Cell
                  key={`info-${i}`}
                  fill={SEVERITY_COLOR.info}
                  fillOpacity={selected ? (isBucketSelected(b) ? 1 : 0.25) : 1}
                />
              ))}
            </Bar>
            <Bar dataKey="notice" stackId="s" isAnimationActive={false}>
              {data.map((b, i) => (
                <Cell
                  key={`notice-${i}`}
                  fill={SEVERITY_COLOR.notice}
                  fillOpacity={selected ? (isBucketSelected(b) ? 1 : 0.25) : 1}
                />
              ))}
            </Bar>
            <Bar dataKey="warn" stackId="s" isAnimationActive={false}>
              {data.map((b, i) => (
                <Cell
                  key={`warn-${i}`}
                  fill={SEVERITY_COLOR.warn}
                  fillOpacity={selected ? (isBucketSelected(b) ? 1 : 0.25) : 1}
                />
              ))}
            </Bar>
            <Bar dataKey="error" stackId="s" isAnimationActive={false}>
              {data.map((b, i) => (
                <Cell
                  key={`error-${i}`}
                  fill={SEVERITY_COLOR.error}
                  fillOpacity={selected ? (isBucketSelected(b) ? 1 : 0.25) : 1}
                />
              ))}
            </Bar>
          </BarChart>
        </ResponsiveContainer>
      </div>
    </div>
  )
}

/** Persistent selection overlay — sits behind the bars to tint the chosen range. */
function SelectionOverlay({ data, selected }: { data: Bucket[]; selected: BucketRange }) {
  const halfBucket = (data[0]?.toMs ?? 0) - (data[0]?.fromMs ?? 0)
  const firstIdx = data.findIndex(
    (b) => b.fromMs >= selected.fromMs - halfBucket / 2,
  )
  const lastIdx = data.length - 1 - [...data].reverse().findIndex(
    (b) => b.toMs <= selected.toMs + halfBucket / 2,
  )
  if (firstIdx < 0 || lastIdx < 0 || lastIdx < firstIdx) return null
  const left = (firstIdx / BUCKET_COUNT) * 100
  const width = ((lastIdx - firstIdx + 1) / BUCKET_COUNT) * 100
  return (
    <div
      className="absolute top-0 bottom-0 pointer-events-none border-l border-r border-sky-500/50 bg-sky-500/10"
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

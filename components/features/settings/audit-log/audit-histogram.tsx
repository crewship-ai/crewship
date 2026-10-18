"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import { cn } from "@/lib/utils"
import type { BucketRange } from "@/components/features/logs/logs-histogram"

/** v67 `.au-hist`: 24 bars. */
const BUCKETS = 24
/** Travel in px between mouse down and up that makes a drag, not a click. */
const DRAG_PX = 10

interface Bucket { fromMs: number; toMs: number; n: number }

/** The window is anchored by the page (a slice picked on the chart must keep
 *  matching its bars while events arrive); with none ("All") it is the span
 *  of the events up to now. */
function computeBuckets(times: number[], window: { fromMs: number; toMs: number } | null) {
  const now = Date.now()
  const fromMs = window ? window.fromMs : times.length ? Math.min(...times) : now - 7 * 86_400_000
  let toMs = window ? window.toMs : now
  if (toMs - fromMs < 1000) toMs = fromMs + 1000
  const step = (toMs - fromMs) / BUCKETS
  const data: Bucket[] = Array.from({ length: BUCKETS }, (_, i) => ({ fromMs: fromMs + i * step, toMs: fromMs + (i + 1) * step, n: 0 }))
  for (const t of times) {
    if (Number.isNaN(t) || t < fromMs || t > toMs) continue
    data[Math.min(BUCKETS - 1, Math.floor((t - fromMs) / step))].n += 1
  }
  return { data, fromMs, toMs }
}

const clock = (ms: number) => new Date(ms).toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit", hour12: false })
const day = (ms: number) => `${new Date(ms).toLocaleDateString("en-US", { weekday: "short" })} ${new Date(ms).getDate()} ${new Date(ms).toLocaleDateString("en-US", { month: "short" })}`

const inSlice = (b: Bucket, s: BucketRange) => b.fromMs >= s.fromMs - 1 && b.toMs <= s.toMs + 1

/**
 * The Audit log's chart (v67 `.au-hist`): the events across the range as
 * solid bars in the accent, one colour — severity is the table's job — with
 * the start of the range, a hint and "Today" under the axis. A click picks
 * one bar, a drag a slice of time, the same bar again clears it; the arrow
 * keys, Home, End, Enter and Escape do the same from the keyboard.
 */
export function AuditHistogram({ times, window, endsToday, selected, onSelect, height = 64 }: {
  /** The events' times, in ms. */
  times: number[]
  /** The range the bars span; null for "All". */
  window: { fromMs: number; toMs: number } | null
  /** The range runs up to now, so the axis ends in "Today". */
  endsToday: boolean
  selected: BucketRange | null
  onSelect: (range: BucketRange | null) => void
  height?: number
}) {
  const { data, fromMs, toMs } = useMemo(() => computeBuckets(times, window), [times, window])
  const ref = useRef<HTMLDivElement>(null)
  const latest = useRef({ data, onSelect })
  useEffect(() => { latest.current = { data, onSelect } })
  const indexAt = useCallback((x: number) => {
    const r = ref.current?.getBoundingClientRect()
    if (!r || r.width <= 0) return null
    const ratio = (x - r.left) / r.width
    return ratio < 0 || ratio > 1 ? null : Math.max(0, Math.min(BUCKETS - 1, Math.floor(ratio * BUCKETS)))
  }, [])

  const [drag, setDrag] = useState<{ x: number; i: number } | null>(null)
  const [dragTo, setDragTo] = useState<number | null>(null)
  const swallowClick = useRef(false)
  useEffect(() => {
    if (!drag) return
    const move = (e: MouseEvent) => {
      if (Math.abs(e.clientX - drag.x) < DRAG_PX) return
      const i = indexAt(e.clientX)
      if (i !== null) setDragTo(i)
    }
    const up = (e: MouseEvent) => {
      setDrag(null); setDragTo(null)
      if (Math.abs(e.clientX - drag.x) < DRAG_PX) return
      const end = indexAt(e.clientX)
      if (end === null) return
      const lo = latest.current.data[Math.min(drag.i, end)], hi = latest.current.data[Math.max(drag.i, end)]
      if (!lo || !hi) return
      swallowClick.current = true
      latest.current.onSelect({ fromMs: lo.fromMs, toMs: hi.toMs })
    }
    document.addEventListener("mousemove", move)
    document.addEventListener("mouseup", up)
    return () => { document.removeEventListener("mousemove", move); document.removeEventListener("mouseup", up) }
  }, [drag, indexAt])

  const toggle = (b: Bucket) => onSelect(selected && selected.fromMs === b.fromMs && selected.toMs === b.toMs ? null : { fromMs: b.fromMs, toMs: b.toMs })
  const [focusAt, setFocusAt] = useState<number | null>(null)
  const onKey = (e: React.KeyboardEvent) => {
    const go = (next: number) => { e.preventDefault(); setFocusAt(next) }
    if (e.key === "ArrowLeft") go(Math.max(0, (focusAt ?? BUCKETS - 1) - 1))
    else if (e.key === "ArrowRight") go(Math.min(BUCKETS - 1, (focusAt ?? -1) + 1))
    else if (e.key === "Home") go(0)
    else if (e.key === "End") go(BUCKETS - 1)
    else if ((e.key === "Enter" || e.key === " ") && focusAt !== null) { e.preventDefault(); toggle(data[focusAt]) }
    else if (e.key === "Escape" && selected) { e.preventDefault(); onSelect(null) }
  }

  const peak = Math.max(1, ...data.map((b) => b.n))
  const dragRect = drag && dragTo !== null ? { lo: Math.min(drag.i, dragTo), hi: Math.max(drag.i, dragTo) } : null
  const spansDays = toMs - fromMs > 86_400_000
  return (
    <div data-slot="audit-histogram" className="rounded-card border border-border bg-card px-4 pb-2 pt-3">
      <div
        ref={ref}
        role="application"
        aria-label="Event volume histogram. Use arrow keys to navigate buckets, Enter to filter, Escape to clear."
        tabIndex={0}
        onMouseDown={(e) => { if (e.button === 0) { const i = indexAt(e.clientX); if (i !== null) setDrag({ x: e.clientX, i }) } }}
        onClick={(e) => {
          if (swallowClick.current) { swallowClick.current = false; return }
          const i = indexAt(e.clientX)
          if (i !== null) toggle(data[i])
        }}
        onKeyDown={onKey}
        onBlur={() => setFocusAt(null)}
        style={{ height }}
        className="relative flex cursor-pointer select-none items-end gap-[3px] rounded outline-none focus-visible:ring-1 focus-visible:ring-primary/60"
      >
        {data.map((b, i) => {
          const picked = selected ? inSlice(b, selected) : i === BUCKETS - 1
          const dim = selected && !picked
          return (
            <i
              key={b.fromMs}
              data-bar
              title={`${b.n} ${b.n === 1 ? "event" : "events"}`}
              className={cn("block min-h-[3px] flex-1 rounded-t-sm", picked ? "bg-primary" : "bg-primary/35", dim && "opacity-60", focusAt === i && "outline outline-1 outline-primary")}
              style={{ height: `${Math.max(4, (b.n / peak) * 100)}%` }}
            />
          )
        })}
        {dragRect && (
          <span aria-hidden className="pointer-events-none absolute inset-y-0 border border-dashed border-primary/60 bg-primary/15"
            style={{ left: `${(dragRect.lo / BUCKETS) * 100}%`, width: `${((dragRect.hi - dragRect.lo + 1) / BUCKETS) * 100}%` }} />
        )}
      </div>
      <div className="mt-1 flex justify-between text-micro text-muted-foreground-soft">
        <span>{spansDays ? day(fromMs) : clock(fromMs)}</span>
        <span>Drag to pick a slice of time</span>
        <span>{endsToday ? "Today" : spansDays ? day(toMs) : clock(toMs)}</span>
      </div>
    </div>
  )
}

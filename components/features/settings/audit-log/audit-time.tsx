"use client"

import { useMemo, useState } from "react"
import { motion, useReducedMotion } from "motion/react"
import { ChevronLeft, ChevronRight } from "lucide-react"

import { cn } from "@/lib/utils"

import { AUDIT_RANGES, formatDay, type AuditFilters } from "./audit-filters"
import { dayInBounds, lastDays, monthGrid, utcDay, type DayTally } from "./audit-activity"

const SHORT: Record<string, string> = { "1h": "1h", "24h": "24h", "7d": "7 days", "30d": "30 days", "90d": "90 days", all: "All" }
const MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"]

/** The preset ranges as a compact grid, for the side panel's Time section. */
export function AuditRangePresets({ filters, onChange }: { filters: AuditFilters; onChange: (next: Partial<AuditFilters>) => void }) {
  return (
    <div className="grid grid-cols-3 gap-1 px-2 pb-2 pt-1" role="group" aria-label="Time range">
      {AUDIT_RANGES.map((r) => {
        const on = filters.range === r.value
        return (
          <button
            key={r.value}
            type="button"
            data-drill-close
            aria-pressed={on}
            title={r.label}
            onClick={() => onChange({ range: r.value, from: "", to: "" })}
            className={cn(
              "h-7 rounded-md border text-[11.5px] transition-colors",
              on ? "border-primary/40 bg-primary/10 font-medium text-primary-hover" : "border-border bg-card text-muted-foreground hover:text-foreground",
            )}
          >
            {SHORT[r.value]}
          </button>
        )
      })}
    </div>
  )
}

/**
 * A month of days with how much happened on each (the bar under the number).
 * A click picks one day, Shift+click stretches the range to it. Days are UTC,
 * like the range the table sends.
 */
export function AuditCalendar({
  filters,
  bounds,
  tally,
  onChange,
  now,
}: {
  filters: AuditFilters
  bounds: { from?: string; to?: string }
  tally: Map<string, DayTally>
  onChange: (next: Partial<AuditFilters>) => void
  now: Date
}) {
  const today = utcDay(now)
  const anchor = filters.range === "custom" && filters.to ? filters.to : today
  const [view, setView] = useState(() => ({ y: Number(anchor.slice(0, 4)), m: Number(anchor.slice(5, 7)) - 1 }))
  const cells = useMemo(() => monthGrid(view.y, view.m), [view])
  const max = Math.max(1, ...cells.map((d) => (d ? tally.get(d)?.total ?? 0 : 0)))
  const atThisMonth = view.y === now.getUTCFullYear() && view.m === now.getUTCMonth()
  const hasRange = filters.range !== "all"
  const move = (by: number) => setView(({ y, m }) => ({ y: m + by < 0 ? y - 1 : m + by > 11 ? y + 1 : y, m: (m + by + 12) % 12 }))

  const pick = (day: string, extend: boolean) => {
    if (extend && filters.range === "custom" && filters.from) {
      const lo = day < filters.from ? day : filters.from
      const hi = day > (filters.to || filters.from) ? day : filters.to || filters.from
      onChange({ range: "custom", from: lo, to: hi })
    } else {
      onChange({ range: "custom", from: day, to: day })
    }
  }

  return (
    <div className="px-2 pb-1" data-slot="audit-calendar">
      <div className="flex items-center justify-between px-0.5 pb-1.5 text-xs font-medium">
        <button type="button" onClick={() => move(-1)} aria-label="Previous month" className="grid h-6 w-6 place-items-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground">
          <ChevronLeft className="h-3.5 w-3.5" />
        </button>
        <span aria-live="polite">{MONTHS[view.m]} {view.y}</span>
        <button type="button" onClick={() => move(1)} disabled={atThisMonth} aria-label="Next month" className="grid h-6 w-6 place-items-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-30">
          <ChevronRight className="h-3.5 w-3.5" />
        </button>
      </div>
      <div className="grid grid-cols-7 gap-0.5">
        {["M", "T", "W", "T", "F", "S", "S"].map((d, i) => (
          <span key={i} className="pb-0.5 text-center font-mono text-[9.5px] text-muted-foreground-soft" aria-hidden>{d}</span>
        ))}
        {cells.map((day, i) => {
          if (!day) return <span key={`b${i}`} />
          const count = tally.get(day)?.total ?? 0
          const future = day > today
          const inRange = hasRange && dayInBounds(day, bounds)
          const prevIn = hasRange && dayInBounds(cells[i - 1] ?? "", bounds) && i % 7 !== 0
          const nextIn = hasRange && dayInBounds(cells[i + 1] ?? "", bounds) && i % 7 !== 6
          return (
            <button
              key={day}
              type="button"
              data-day={day}
              data-drill-close
              disabled={future}
              onClick={(e) => pick(day, e.shiftKey)}
              aria-pressed={inRange}
              aria-label={`${formatDay(day)}: ${count} event${count === 1 ? "" : "s"}`}
              title={`${formatDay(day)} · ${count} event${count === 1 ? "" : "s"}`}
              className={cn(
                "relative grid h-7 place-items-center font-mono text-[11px] tabular-nums text-muted-foreground transition-colors hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-30",
                inRange ? "bg-primary/[0.13] text-foreground" : "rounded-md",
                inRange && !prevIn && "rounded-l-md",
                inRange && !nextIn && "rounded-r-md",
                day === today && "font-semibold text-foreground ring-1 ring-inset ring-border",
                day === today && !inRange && "rounded-md",
              )}
            >
              {Number(day.slice(8))}
              {count > 0 && (
                <span
                  aria-hidden
                  className="absolute bottom-[3px] left-1/2 h-[3px] w-3.5 -translate-x-1/2 rounded-full bg-primary"
                  style={{ opacity: 0.25 + 0.75 * (count / max) }}
                />
              )}
            </button>
          )
        })}
      </div>
      <p className="px-0.5 pt-1.5 text-[10.5px] leading-snug text-muted-foreground-soft">
        Bar under a day = how much happened. Shift-click extends the range.
      </p>
    </div>
  )
}

/**
 * Events per day for the last two weeks, runs split into finished and failed.
 * A bar is a shortcut to that day; bars outside the current range dim.
 */
export function AuditHistogram({
  tally,
  bounds,
  onPickDay,
  now,
  truncated,
  days: n = 14,
}: {
  tally: Map<string, DayTally>
  bounds: { from?: string; to?: string }
  onPickDay: (day: string) => void
  now: Date
  truncated?: boolean
  days?: number
}) {
  const reduce = useReducedMotion()
  const days = lastDays(n, now)
  const max = Math.max(1, ...days.map((d) => tally.get(d)?.total ?? 0))
  return (
    <div className="rounded-card border border-border bg-card px-4 pb-3 pt-3" data-slot="audit-histogram">
      <div className="mb-2.5 flex flex-wrap items-center justify-between gap-2">
        <span className="eyebrow text-muted-foreground">Events per day · last {n} days{truncated ? " · latest 500" : ""}</span>
        <span className="flex gap-3 text-[11px] text-muted-foreground">
          <Legend className="bg-success" label="completed" />
          <Legend className="bg-destructive" label="failed" />
          <Legend className="bg-primary" label="changes" />
        </span>
      </div>
      <div className="grid h-20 items-end gap-1 sm:gap-1.5" style={{ gridTemplateColumns: `repeat(${n}, minmax(0, 1fr))` }}>
        {days.map((day, i) => {
          const t = tally.get(day) ?? { total: 0, completed: 0, failed: 0 }
          const other = t.total - t.completed - t.failed
          const h = t.total ? Math.max(6, (t.total / max) * 80) : 2
          return (
            <motion.button
              key={day}
              type="button"
              onClick={() => onPickDay(day)}
              aria-label={`${formatDay(day)}: ${t.total} events, ${t.failed} failed`}
              title={`${formatDay(day)} · ${t.total} events · ${t.failed} failed`}
              initial={reduce ? false : { scaleY: 0 }}
              animate={{ scaleY: 1 }}
              transition={{ duration: 0.45, delay: i * 0.02, ease: [0.2, 0.7, 0.2, 1] }}
              style={{ height: h, transformOrigin: "bottom" }}
              className={cn(
                "flex flex-col-reverse overflow-hidden rounded-[4px] bg-border/60 transition-opacity hover:opacity-100",
                !dayInBounds(day, bounds) && "opacity-30",
              )}
            >
              {t.total > 0 && (
                <>
                  <span className="bg-success/70" style={{ flex: t.completed }} />
                  <span className="bg-destructive/75" style={{ flex: t.failed }} />
                  <span className="bg-primary/65" style={{ flex: other }} />
                </>
              )}
            </motion.button>
          )
        })}
      </div>
      <div className="mt-1 grid gap-1 font-mono text-[10px] text-muted-foreground-soft sm:gap-1.5" style={{ gridTemplateColumns: `repeat(${n}, minmax(0, 1fr))` }} aria-hidden>
        {days.map((d, i) => (
          <span key={d} className={cn("text-center", i % 2 === 1 && "invisible sm:visible")}>{Number(d.slice(8))}</span>
        ))}
      </div>
    </div>
  )
}

function Legend({ className, label }: { className: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <i className={cn("inline-block h-2 w-2 rounded-[2px]", className)} aria-hidden />
      {label}
    </span>
  )
}

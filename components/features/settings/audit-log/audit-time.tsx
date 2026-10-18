"use client"

import { useMemo, useState } from "react"
import { CalendarDays, ChevronLeft, ChevronRight } from "lucide-react"

import { cn } from "@/lib/utils"

import { Button } from "@/components/ui/button"
import { Segmented } from "@/components/ui/segmented"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { TimeRangePicker, type TimeRange } from "@/components/features/logs/time-range-picker"

import { AUDIT_RANGES, DEFAULT_AUDIT_FILTERS, formatDay, rangeLabel, type AuditFilters } from "./audit-filters"
import { dayInBounds, monthGrid, utcDay, type DayTally } from "./audit-activity"

const MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"]
const PICKER_PRESETS: TimeRange[] = [...AUDIT_RANGES.map((r) => r.value), "custom"]
/** The segment labels of v67 S-AUDIT ("1 h · 24 h · 7 d · 30 d · 90 d · All");
 *  each segment's accessible name is the long label. */
const SHORT: Record<string, string> = { "1h": "1 h", "24h": "24 h", "7d": "7 d", "30d": "30 d", "90d": "90 d", all: "All" }

/** "5 Oct – 11 Oct", the year only when it is not this one. */
function shortDay(day: string): string {
  const full = formatDay(day)
  return day.slice(0, 4) === String(new Date().getUTCFullYear()) ? full.replace(/ \d{4}$/, "") : full
}

function customText(filters: AuditFilters): string | undefined {
  if (filters.range !== "custom") return undefined
  if (filters.from && filters.to) return filters.from === filters.to ? shortDay(filters.from) : `${shortDay(filters.from)} – ${shortDay(filters.to)}`
  return filters.from ? `Since ${shortDay(filters.from)}` : "Custom"
}

/**
 * The Audit log's time range as ONE control on the top bar (v67 S-AUDIT,
 * PRD POP-07): the presets as a segmented radio group, and beside it a
 * calendar button for a custom range — the month calendar where each day
 * shows how much happened. A custom range unchecks the presets and names
 * itself next to the calendar. Arrow keys move along the presets.
 *
 * A phone has no room for six segments beside the title: it keeps the
 * compact menu (AuditTimePicker below), which is the same one control.
 */
export function AuditRangeBar({ filters, onChange, bounds, tally, now }: {
  filters: AuditFilters
  onChange: (next: Partial<AuditFilters>) => void
  bounds: { from?: string; to?: string }
  tally: Map<string, DayTally>
  now: Date
}) {
  const [open, setOpen] = useState(false)
  const custom = filters.range === "custom"
  const pick = (value: AuditFilters["range"]) => onChange({ range: value, from: "", to: "", sliceFrom: "", sliceTo: "" })
  const label = customText(filters)

  return (
    <div className="flex items-center gap-1.5" data-slot="audit-range">
      <Segmented
        ariaLabel="Time range"
        value={filters.range}
        onChange={pick}
        options={AUDIT_RANGES.map((r) => ({
          value: r.value,
          ariaLabel: r.label,
          label: <span className="text-micro tabular-nums">{SHORT[r.value] ?? r.label}</span>,
        }))}
      />
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <Button variant={custom ? "outline" : "ghost"} size={label ? "sm" : "icon-sm"} aria-label="Custom range" aria-pressed={custom}
            title={custom ? rangeLabel(filters) : "Custom range"} className={cn(label && "gap-1.5 px-2 text-micro")}>
            <CalendarDays className="h-3.5 w-3.5" aria-hidden />
            {label}
          </Button>
        </PopoverTrigger>
        <PopoverContent align="end" className="w-80 p-3">
          <div className="space-y-2" data-slot="audit-custom-range">
            <div className="text-micro font-semibold text-muted-foreground">Custom range</div>
            <AuditCalendar filters={filters} bounds={bounds} tally={tally} onChange={(next) => onChange({ ...next, sliceFrom: "", sliceTo: "" })} now={now} />
            <div className="flex items-center justify-between gap-2 border-t border-border pt-2">
              <span className="truncate text-micro tabular-nums text-foreground/85">{custom ? rangeLabel(filters) : "Pick a day"}</span>
              <span className="flex gap-1.5">
                {custom && (
                  <Button variant="ghost" size="sm" className="px-2.5 text-xs" onClick={() => { pick(DEFAULT_AUDIT_FILTERS.range); setOpen(false) }}>Clear</Button>
                )}
                <Button size="sm" className="px-3 text-xs" onClick={() => setOpen(false)}>Done</Button>
              </span>
            </div>
          </div>
        </PopoverContent>
      </Popover>
    </div>
  )
}

/**
 * The range picker on the sub-bar: the Crew Journal's control (clock, "7d ▾",
 * the presets as a menu) with the Audit log's own set — up to 90 days — and
 * "Custom…" opening the month calendar, where each day shows how much
 * happened.
 */
export function AuditTimePicker({ filters, onChange, bounds, tally, now }: {
  filters: AuditFilters
  onChange: (next: Partial<AuditFilters>) => void
  bounds: { from?: string; to?: string }
  tally: Map<string, DayTally>
  now: Date
}) {
  const custom = filters.range === "custom"
  const customLabel = custom
    ? filters.from && filters.to
      ? filters.from === filters.to ? shortDay(filters.from) : `${shortDay(filters.from)} – ${shortDay(filters.to)}`
      : filters.from ? `Since ${shortDay(filters.from)}` : "Custom"
    : undefined
  return (
    <TimeRangePicker
      value={filters.range as TimeRange}
      presets={PICKER_PRESETS}
      customLabel={customLabel}
      triggerClassName="h-8 gap-1.5 px-2.5 text-xs tabular-nums [&>svg]:opacity-60 coarse:h-[44px]"
      onChange={(v) => { if (v !== "custom") onChange({ range: v as AuditFilters["range"], from: "", to: "", sliceFrom: "", sliceTo: "" }) }}
      renderCustom={(close) => (
        <div className="space-y-2" data-slot="audit-custom-range">
          <div className="text-micro font-semibold text-muted-foreground">Custom range</div>
          <AuditCalendar filters={filters} bounds={bounds} tally={tally} onChange={(next) => onChange({ ...next, sliceFrom: "", sliceTo: "" })} now={now} />
          <div className="flex items-center justify-between gap-2 border-t border-border pt-2">
            <span className="truncate text-micro tabular-nums text-foreground/85">{custom ? rangeLabel(filters) : "Pick a day"}</span>
            <span className="flex gap-1.5">
              {custom && (
                <Button variant="ghost" size="sm" className="px-2.5 text-xs" onClick={() => { onChange({ range: DEFAULT_AUDIT_FILTERS.range, from: "", to: "", sliceFrom: "", sliceTo: "" }); close() }}>Clear</Button>
              )}
              <Button size="sm" className="px-3 text-xs" onClick={close}>Done</Button>
            </span>
          </div>
        </div>
      )}
    />
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
          <span key={i} className="pb-0.5 text-center font-mono text-micro text-muted-foreground-soft" aria-hidden>{d}</span>
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
                "relative grid h-7 place-items-center font-mono text-micro tabular-nums text-muted-foreground transition-colors hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-30",
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
      <p className="px-0.5 pt-1.5 text-micro leading-snug text-muted-foreground-soft">
        Bar under a day = how much happened. Shift-click extends the range.
      </p>
    </div>
  )
}

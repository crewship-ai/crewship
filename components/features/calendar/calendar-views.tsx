"use client"

import type { LucideIcon } from "lucide-react"
import { Check, Clock, Hand, Info, Play, Plus, X } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { addDays, dateKey } from "@/lib/routine-calendar"
import {
  CELL_CAP,
  agendaLayout,
  dayLabel,
  monthLayout,
  nowLineAfter,
  weekLayout,
  type CalendarItem,
  type CalendarItemStatus,
  type CalendarScope,
} from "@/lib/calendar/model"
import { cn } from "@/lib/utils"

// Week, Agenda and Month (CAL-WEEK / CAL-AGENDA / CAL-MONTH). The colour of an
// item is its layer, the icon its state — never colour alone. Routine starts
// and runs are the violet layer.

export const STATUS_ICON: Record<CalendarItemStatus, { icon: LucideIcon; className: string; label: string }> = {
  ok: { icon: Check, className: "text-success", label: "Done" },
  wait: { icon: Hand, className: "text-warn", label: "Waiting for someone" },
  bad: { icon: X, className: "text-destructive", label: "Could not finish" },
  plan: { icon: Clock, className: "text-primary", label: "Planned" },
  run: { icon: Play, className: "text-primary", label: "Running" },
  info: { icon: Info, className: "text-muted-foreground", label: "Stopped" },
}

/** The layer's colour bar, as a CSS variable the item classes read. */
const LAYER_BAR = "[--lc:var(--purple)]"

function StatusIcon({ status, className }: { status: CalendarItemStatus; className?: string }) {
  const s = STATUS_ICON[status]
  const Icon = s.icon
  return <Icon aria-label={s.label} role="img" className={cn("size-3 shrink-0", s.className, className)} />
}

const weekdays = Array.from({ length: 7 }, (_, i) => new Date(2026, 0, 5 + i).toLocaleDateString("en-GB", { weekday: "short" }))

/** One item in a week cell or a month day: time, state icon, name. */
function ItemChip({ item, selected, onPick, compact = false }: { item: CalendarItem; selected: boolean; onPick: (id: string) => void; compact?: boolean }) {
  return (
    <button
      type="button"
      data-calendar-item={item.id}
      aria-pressed={selected}
      onClick={(e) => {
        e.stopPropagation()
        onPick(item.id)
      }}
      title={`${item.title} · ${item.subtitle}`}
      className={cn(
        "kit-tap flex w-full min-w-0 items-center gap-1 rounded-md text-left text-label transition-colors",
        LAYER_BAR,
        compact ? "px-1.5 py-px" : "px-1.5 py-1",
        "shadow-[inset_3px_0_0_var(--lc)]",
        item.status === "plan" ? "bg-card ring-1 ring-inset ring-border" : "bg-purple/10",
        item.status === "bad" && "ring-1 ring-inset ring-destructive/50",
        "hover:bg-purple/20",
        selected && "outline outline-2 outline-offset-1 outline-primary",
      )}
    >
      {item.time && <span className="shrink-0 font-mono text-micro text-muted-foreground">{item.time}</span>}
      <StatusIcon status={item.status} />
      <span className="min-w-0 truncate font-medium">{item.title}</span>
    </button>
  )
}

const dayHead = (d: Date, todayKey: string) => {
  const key = dateKey(d)
  return (
    <span className={cn("flex items-baseline justify-center gap-1.5 px-1 py-2 text-label", key < todayKey && "opacity-70")}>
      <small className="text-muted-foreground">{d.toLocaleDateString("en-GB", { weekday: "short" })}</small>
      <b className={cn("font-semibold", key === todayKey && "rounded-full bg-primary px-1.5 text-primary-foreground")}>{d.getDate()}</b>
    </span>
  )
}

/** Week: an All day row (date-only items), then only the hours that have something; a "now" line this week. */
export function CalendarWeek({
  items,
  from,
  selectedId,
  onPick,
  onOpenDay,
  onPlan,
  now = new Date(),
}: {
  items: CalendarItem[]
  from: Date
  selectedId: string | null
  onPick: (id: string) => void
  /** "+N" in a cell: the day's agenda. */
  onOpenDay: (key: string) => void
  /** A click on an empty part of a cell plans a start there (absent without the right). */
  onPlan?: (at: Date) => void
  now?: Date
}) {
  const { days, allDay, timed, hours } = weekLayout(items, from)
  const todayKey = dateKey(now)
  const thisWeek = days.some((d) => dateKey(d) === todayKey)
  const nowAfter = thisWeek ? nowLineAfter(hours, now) : null
  const todayCol = days.findIndex((d) => dateKey(d) === todayKey)
  const GRID = "grid grid-cols-[56px_repeat(7,minmax(0,1fr))]"
  const cell = (list: CalendarItem[], day: Date, hour: number | null) => {
    const key = dateKey(day)
    const shown = list.slice(0, CELL_CAP)
    const more = list.length - shown.length
    return (
      <div
        key={key}
        data-testid={hour == null ? `week-allday-${key}` : `week-cell-${key}-${hour}`}
        className={cn("group relative flex min-h-11 min-w-0 flex-col gap-1 border-l border-border p-1", key === todayKey && "bg-primary/[0.04]")}
      >
        {shown.map((i) => (
          <ItemChip key={i.id} item={i} selected={selectedId === i.id} onPick={onPick} />
        ))}
        {more > 0 && (
          <button type="button" onClick={() => onOpenDay(key)} className="kit-tap self-start rounded px-1 text-micro text-muted-foreground hover:text-foreground">
            +{more}
          </button>
        )}
        {onPlan && hour != null && (
          <button
            type="button"
            aria-label={`Schedule a start on ${day.toLocaleDateString("en-GB", { weekday: "short", day: "numeric", month: "short" })} at ${String(hour).padStart(2, "0")}:00`}
            onClick={() => onPlan(new Date(day.getFullYear(), day.getMonth(), day.getDate(), hour))}
            className="kit-tap mt-auto grid min-h-6 place-items-center rounded-md border border-dashed border-border text-muted-foreground opacity-0 transition-opacity focus-visible:opacity-100 group-hover:opacity-100 coarse:hidden"
          >
            <Plus className="size-3" aria-hidden />
          </button>
        )}
      </div>
    )
  }
  const nowLine = (
    <div key="now" className={cn(GRID, "relative h-0")} aria-hidden data-testid="week-now">
      <span className="absolute -top-2 left-1 bg-card px-0.5 font-mono text-micro text-destructive">
        {now.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })}
      </span>
      <i className="relative -top-px h-0.5 bg-destructive" style={{ gridColumn: `${todayCol + 2}` }} />
    </div>
  )
  return (
    <div className="space-y-2">
      <div className="overflow-x-auto rounded-xl border border-border bg-card" role="grid" aria-label="Week">
        <div className="min-w-[760px]">
          <div className={cn(GRID, "border-b border-border bg-muted/40")} role="row">
            <span />
            {days.map((d) => (
              <span key={dateKey(d)} role="columnheader">
                {dayHead(d, todayKey)}
              </span>
            ))}
          </div>
          {allDay.length > 0 && (
            <div className={cn(GRID, "border-b border-border bg-muted/40")} role="row">
              <span className="px-1.5 py-2 text-right font-mono text-micro text-muted-foreground">All day</span>
              {days.map((d) => cell(allDay.filter((i) => i.day === dateKey(d)), d, null))}
            </div>
          )}
          {nowAfter === -1 && nowLine}
          {hours.map((h, idx) => (
            <div key={h} role="row">
              <div className={cn(GRID, "border-b border-border")}>
                <span data-hour-label className="px-1.5 py-2 text-right font-mono text-micro text-muted-foreground">{String(h).padStart(2, "0")}:00</span>
                {days.map((d) => cell(timed.filter((i) => i.day === dateKey(d) && i.at.getHours() === h), d, h))}
              </div>
              {nowAfter === idx && nowLine}
            </div>
          ))}
          {!hours.length && !allDay.length && (
            <p className="px-4 py-6 text-center text-body text-muted-foreground">Nothing this week.</p>
          )}
        </div>
      </div>
      <p className="text-label text-muted-foreground">Only hours with something in them are shown.</p>
    </div>
  )
}

/** Agenda: Today, Tomorrow, then each day that has something; a busy routine folds into one row. */
export function CalendarAgenda({
  items,
  scope,
  selectedId,
  onPick,
  now = new Date(),
}: {
  items: CalendarItem[]
  scope: CalendarScope
  selectedId: string | null
  onPick: (id: string) => void
  now?: Date
}) {
  const days = agendaLayout(items)
  const todayKey = dateKey(now)
  const row = (item: CalendarItem, extra?: { count: number; first: string; range: string }) => (
    <button
      key={item.id}
      type="button"
      data-calendar-item={item.id}
      aria-pressed={selectedId === item.id}
      onClick={() => onPick(item.id)}
      className={cn(
        "kit-tap flex w-full min-w-0 items-center gap-2.5 rounded-lg border border-border bg-card px-3 py-2 text-left transition-colors hover:border-foreground/20",
        LAYER_BAR,
        selectedId === item.id && "border-primary ring-1 ring-primary",
      )}
    >
      <span className="w-11 shrink-0 font-mono text-label text-muted-foreground">{extra ? extra.first : (item.time ?? "Due")}</span>
      <i aria-hidden className="w-[3px] self-stretch rounded-sm bg-[var(--lc)]" />
      <span className="min-w-0 flex-1">
        <b className="block truncate text-body font-medium">
          {item.title}
          {extra && <span className="ml-1.5 font-mono text-label font-semibold text-primary">×{extra.count}</span>}
        </b>
        <small className="flex min-w-0 items-center gap-1 text-label text-muted-foreground">
          <StatusIcon status={item.status} />
          <span className="truncate">{extra ? `Planned · ${extra.range}` : item.subtitle}</span>
        </small>
      </span>
      {scope === "workspace" && item.mine && <Badge variant="secondary">Mine</Badge>}
    </button>
  )
  return (
    <div className="grid gap-3.5" aria-label="Agenda">
      {days.map((d) => (
        <section key={d.key} aria-label={dayLabel(d.key, now)} className="grid gap-1">
          <header className="flex items-baseline gap-2 px-1">
            <b className="text-body font-semibold">{dayLabel(d.key, now)}</b>
            {(d.key === todayKey || d.key === dateKey(addDays(now, 1))) && (
              <small className="text-label text-muted-foreground">{new Date(`${d.key}T12:00:00`).toLocaleDateString("en-GB", { weekday: "short", day: "numeric", month: "short" })}</small>
            )}
          </header>
          {d.rows.map((r) =>
            r.kind === "item"
              ? row(r.item)
              : row(r.items[0], { count: r.items.length, first: r.items[0].time ?? "", range: `${r.items[0].time}–${r.items.at(-1)!.time}` }),
          )}
        </section>
      ))}
    </div>
  )
}

/** Month: whole weeks; three items a day, "+N more", a red dot when something failed. */
export function CalendarMonth({
  items,
  anchor,
  selectedId,
  onPick,
  onOpenDay,
  now = new Date(),
}: {
  items: CalendarItem[]
  anchor: Date
  selectedId: string | null
  onPick: (id: string) => void
  onOpenDay: (key: string) => void
  now?: Date
}) {
  const cells = monthLayout(items, anchor)
  const todayKey = dateKey(now)
  return (
    <div className="overflow-hidden rounded-xl border border-border bg-card" role="grid" aria-label="Month">
      <div className="grid grid-cols-7 border-b border-border bg-muted/40" role="row">
        {weekdays.map((d) => (
          <span key={d} role="columnheader" className="px-2 py-1.5 text-label text-muted-foreground">
            {d}
          </span>
        ))}
      </div>
      <div className="grid grid-cols-7">
        {cells.map((c) => (
          <div
            key={c.key}
            data-testid={`month-day-${c.key}`}
            role="gridcell"
            className={cn(
              "flex min-h-14 min-w-0 flex-col gap-0.5 overflow-hidden border-b border-r border-border p-1 md:min-h-24 md:p-1.5 [&:nth-child(7n)]:border-r-0",
              !c.inMonth && "bg-foreground/[0.02] text-muted-foreground",
            )}
          >
            <header className="flex items-center gap-1">
              <button
                type="button"
                onClick={() => onOpenDay(c.key)}
                aria-label={`Open ${dayLabel(c.key, now)}, ${c.shown.length + c.more} ${c.shown.length + c.more === 1 ? "date" : "dates"}`}
                className={cn("kit-tap rounded-full px-1.5 text-label font-semibold hover:bg-muted", c.key === todayKey && "bg-primary text-primary-foreground hover:bg-primary")}
              >
                {c.date.getDate()}
              </button>
              {c.risk && <i role="img" aria-label="Something could not finish" className="size-2 rounded-full bg-destructive" />}
            </header>
            <div className="hidden min-w-0 flex-col gap-0.5 md:flex">
              {c.shown.map((i) => (
                <ItemChip key={i.id} item={i} selected={selectedId === i.id} onPick={onPick} compact />
              ))}
              {c.more > 0 && (
                <button type="button" onClick={() => onOpenDay(c.key)} className="kit-tap self-start px-1 text-micro text-muted-foreground hover:text-foreground">
                  +{c.more} more
                </button>
              )}
            </div>
            {c.shown.length > 0 && (
              <span className="px-1 font-mono text-micro text-muted-foreground md:hidden">{c.shown.length + c.more}</span>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}

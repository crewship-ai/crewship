"use client"

import { formatRoutineTime, routineTimeZone } from "@/lib/routine-time"

import { routinePresetSummary } from "@/lib/routine-preset-summary"

import { useEffect, useMemo, useRef, useState } from "react"
import Link from "next/link"
import { AlertCircle, ChevronRight, Plus } from "lucide-react"
import { useUrlSelection } from "@/hooks/use-issue-detail"
import { useAbilities } from "@/hooks/use-abilities"
import type { Pipeline } from "@/hooks/use-pipelines"
import { roleAtLeast } from "@/lib/routine-governance"
import { apiFetch } from "@/lib/api-fetch"
import {
  CALENDAR_VIEWS,
  CALENDAR_LABELS,
  addDays,
  calendarRange,
  calendarWindows,
  dateKey,
  moveCalendar,
  parseCalendarDate,
} from "@/lib/routine-calendar"
import {
  CALENDAR_FILTERS,
  CALENDAR_FILTER_LABELS,
  agendaItems,
  calendarClock,
  calendarFilterCounts,
  calendarOutcome,
  calendarVersionLabel,
  daySummary,
  groupByRoutine,
  matchesCalendarFilter,
  sortByTime,
  timeRange,
  type CalendarEntry,
  type CalendarFilter,
  type CalendarOutcome,
  type RoutineGroup,
} from "@/lib/routine-calendar-groups"
import { CrewIcon } from "@/components/ui/crew-icon"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { routineRunPresentation } from "@/lib/routine-run-presentation"
import { cn } from "@/lib/utils"
import { RoutineCalendarSchedule } from "./routine-calendar-schedule"

// The calendar's density rules live in lib/routine-calendar-groups.ts and are
// unit-tested there; this file only draws them (proposal §3, screen 1b): a
// month cell has a fixed height and never scrolls, a busy day folds to one
// row per routine, and a click on a day opens an agenda grouped by routine —
// not the hour grid, which stays for Day / 3 days / Week.

const weekdays = Array.from({ length: 7 }, (_, i) =>
  new Date(2026, 0, 5 + i).toLocaleDateString("en-GB", { weekday: "short" }),
)

const DOT: Record<CalendarOutcome, string> = {
  planned: "bg-primary",
  completed: "bg-success",
  failed: "bg-destructive",
  waiting: "bg-warn",
  running: "bg-primary",
  stopped: "bg-muted-foreground",
}
const FILTER_DOT: Record<CalendarFilter, string> = {
  all: "bg-muted-foreground",
  planned: "bg-primary",
  ran: "bg-success",
  waiting: "bg-warn",
  failed: "bg-destructive",
}

const runHref = (event: CalendarEntry) =>
  `/routines?${new URLSearchParams({ slug: event.slug, run: event.id })}`
const planHref = (slug: string) => `/routines?${new URLSearchParams({ slug, view: "plan" })}`
const entryHref = (event: CalendarEntry) =>
  event.kind === "run" ? runHref(event) : planHref(event.slug)

export function RoutineCalendar({
  workspaceId,
  routines,
}: {
  workspaceId: string
  routines: Pipeline[]
}) {
  const [viewParam, setView] = useUrlSelection("calendar")
  const view = CALENDAR_VIEWS.find((v) => v === viewParam) ?? "month"
  const [dateParam, setDate] = useUrlSelection("date")
  const anchor = parseCalendarDate(dateParam) ?? addDays(new Date(), 0)
  const anchorKey = dateKey(anchor)
  // The agenda is a day opened from the month or year grid; it keeps the
  // range loaded so ‹ Month returns without a second fetch.
  const [agendaDay, setAgendaDay] = useState<string | null>(null)
  const [openGroup, setOpenGroup] = useState<string | null>(null)
  const { from, to } = calendarRange(anchor, view)
  const fromKey = from.toISOString(),
    toKey = to.toISOString()
  const [events, setEvents] = useState<CalendarEntry[]>([])
  const [error, setError] = useState(false)
  const [truncated, setTruncated] = useState(false)
  const [loading, setLoading] = useState(true)
  const [revision, setRevision] = useState(0)
  const [filter, setFilter] = useState<CalendarFilter>("all")
  const [scheduleDate, setScheduleDate] = useState<Date | null>(null)
  const { role } = useAbilities()
  const canSchedule = roleAtLeast(role, "MEMBER")
  const timeGrid = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const grid = timeGrid.current
    const morning = grid?.querySelector<HTMLElement>('[data-calendar-hour="8"]')
    if (grid && morning && !loading)
      grid.scrollTop +=
        morning.getBoundingClientRect().top - grid.getBoundingClientRect().top - 42
  }, [view, anchorKey, loading])
  useEffect(() => {
    setAgendaDay(null)
  }, [view, anchorKey])
  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setEvents([])
    setError(false)
    setTruncated(false)
    // Year is twelve bounded requests, at most three at once. A failed month
    // never presents a partially loaded year as an empty/complete calendar.
    void (async () => {
      try {
        const windows = calendarWindows(new Date(fromKey), new Date(toKey))
        const collected: CalendarEntry[] = []
        let incomplete = false
        let index = 0
        await Promise.all(
          Array.from({ length: Math.min(3, windows.length) }, async () => {
            while (index < windows.length && !controller.signal.aborted) {
              const window = windows[index++]
              const qs = new URLSearchParams({
                from: window.from.toISOString(),
                to: window.to.toISOString(),
              })
              const res = await apiFetch(
                `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/calendar?${qs}`,
                { signal: controller.signal },
              )
              if (!res.ok) throw new Error("calendar")
              const data = await res.json()
              collected.push(...data.events)
              incomplete ||= data.truncated
            }
          }),
        )
        if (!controller.signal.aborted) {
          setEvents(collected)
          setTruncated(incomplete)
        }
      } catch {
        if (!controller.signal.aborted) setError(true)
      } finally {
        if (!controller.signal.aborted) setLoading(false)
      }
    })()
    return () => controller.abort()
  }, [workspaceId, fromKey, toKey, revision])
  const bySlug = useMemo(() => new Map(routines.map((r) => [r.slug, r])), [routines])
  const known = useMemo(() => events.filter((e) => bySlug.has(e.slug)), [events, bySlug])
  // Chip counts come from the whole loaded range, never the filtered view —
  // otherwise choosing Failed would zero every other chip.
  const counts = useMemo(() => calendarFilterCounts(known), [known])
  const byDay = useMemo(() => {
    const map = new Map<string, CalendarEntry[]>()
    for (const event of known) {
      if (!matchesCalendarFilter(event, filter)) continue
      const key = dateKey(new Date(event.at))
      const list = map.get(key) ?? []
      list.push(event)
      map.set(key, list)
    }
    for (const list of map.values())
      list.sort((a, b) => Date.parse(a.at) - Date.parse(b.at))
    return map
  }, [known, filter])
  const plan = (day: Date, hour?: number) => {
    if (!canSchedule) return
    const proposed = new Date(day.getFullYear(), day.getMonth(), day.getDate(), hour ?? 9)
    if (
      hour == null &&
      dateKey(day) === dateKey(new Date()) &&
      proposed.getTime() <= Date.now()
    ) {
      const now = new Date()
      proposed.setHours(now.getHours() + 1, 0, 0, 0)
    }
    setScheduleDate(proposed)
  }
  const icon = (slug: string, className?: string) => {
    const routine = bySlug.get(slug) ?? { slug }
    return (
      <CrewIcon
        icon={resolveRoutineIcon(routine)}
        color={resolveRoutineColor(routine)}
        size="sm"
        className={className}
      />
    )
  }
  const nameOf = (slug: string, fallback?: string) => bySlug.get(slug)?.name || fallback || slug
  const eventTitle = (event: CalendarEntry) => {
    const label =
      event.kind === "run"
        ? routineRunPresentation(event).label
        : `Planned · ${calendarVersionLabel(event)}`
    return `${nameOf(event.slug, event.name)} · ${formatRoutineTime(event.at)} · ${label}`
  }

  /** One entry in the hour grid: time, icon, name, state. */
  const eventLink = (event: CalendarEntry) => {
    const routine = bySlug.get(event.slug)!
    const label =
      event.kind === "run"
        ? routineRunPresentation(event).label
        : `${event.kind === "pending" ? "Scheduled once" : "Planned"} · ${event.pinned_version ? `v${event.pinned_version}` : event.kind === "pending" ? "legacy live version" : "published at start"}`
    return (
      <Link
        key={`${event.kind}:${event.id}`}
        href={entryHref(event)}
        title={eventTitle(event)}
        className={cn(
          "my-1 flex min-w-0 items-start gap-1.5 rounded-lg border-l-2 p-1.5 text-[11px] hover:bg-muted",
          event.kind === "run"
            ? "border-muted-foreground/50 bg-muted/30"
            : "border-primary bg-primary/10",
        )}
      >
        {icon(event.slug)}
        <span className="min-w-0">
          <span className="block font-medium">{calendarClock(event.at)}</span>
          <span className="block truncate">{routine.name || event.name}</span>
          <span className="block text-muted-foreground">{label}</span>
          {event.kind !== "run" && (
            <span className="block truncate text-muted-foreground">
              {routinePresetSummary(event.inputs)}
            </span>
          )}
        </span>
      </Link>
    )
  }
  /** An hour with several starts of one routine is one chip, not four rows. */
  const hourCell = (list: CalendarEntry[], day: Date) =>
    groupByRoutine(list).map((group) => {
      if (group.entries.length === 1) return eventLink(group.entries[0])
      return (
        <button
          key={`group:${group.slug}`}
          type="button"
          onClick={() => {
            setAgendaDay(dateKey(day))
            setOpenGroup(group.slug)
          }}
          title={`${nameOf(group.slug, group.name)} · ${group.entries.length} starts · ${timeRange(group.entries)}`}
          className={cn(
            "my-1 flex w-full min-w-0 items-center gap-1.5 rounded-lg border-l-2 p-1.5 text-left text-[11px] hover:bg-muted",
            group.allPlanned
              ? "border-primary bg-primary/10"
              : "border-muted-foreground/50 bg-muted/30",
          )}
        >
          {icon(group.slug)}
          <span className="min-w-0 flex-1 truncate">{nameOf(group.slug, group.name)}</span>
          <span className="shrink-0 rounded bg-primary/15 px-1 font-mono text-[10px] font-semibold text-primary">
            ×{group.entries.length}
          </span>
        </button>
      )
    })

  const openDay = (day: Date) => {
    setOpenGroup(null)
    setAgendaDay(dateKey(day))
  }
  /** The Day view for that date — the hour grid, not the agenda. */
  const openDayView = (day: Date) => {
    setOpenGroup(null)
    setAgendaDay(null)
    setDate(dateKey(day))
    setView("day")
  }

  /** A month cell: the two earliest starts of the day in order, then how
   * many follow. The cell itself opens the Day view; one glance says what
   * the day begins with, the Day view says the rest. */
  const monthCellBody = (list: CalendarEntry[]) => {
    const sorted = sortByTime(list)
    const shown = sorted.slice(0, 2)
    const later = sorted.length - shown.length
    return (
      <>
        {shown.map((event) => {
          const outcome = calendarOutcome(event)
          return (
            <div
              key={`${event.kind}:${event.id}`}
              title={eventTitle(event)}
              className="flex min-w-0 items-center gap-1.5 text-[11px]"
            >
              <span
                aria-hidden
                className={cn("hidden h-2 w-2 shrink-0 rounded-full md:block", DOT[outcome])}
              />
              <span
                className={cn(
                  "hidden shrink-0 font-mono text-[10px] md:inline",
                  outcome === "planned" ? "text-primary" : "text-muted-foreground",
                )}
              >
                {calendarClock(event.at)}
              </span>
              {icon(event.slug, "!h-4 !w-4 [&>svg]:h-3 [&>svg]:w-3")}
              <span className="hidden min-w-0 truncate md:inline">
                {nameOf(event.slug, event.name)}
              </span>
            </div>
          )
        })}
        {later > 0 && (
          <span
            data-testid="calendar-cell-later"
            className="mt-auto truncate text-[10px] text-muted-foreground"
          >
            +{later} later
          </span>
        )}
      </>
    )
  }

  const monthGrid = (month: Date, compact = false) => {
    const first = new Date(month.getFullYear(), month.getMonth(), 1)
    const count = new Date(month.getFullYear(), month.getMonth() + 1, 0).getDate()
    return (
      <div className={cn("grid grid-cols-7", compact ? "gap-1" : "gap-1 md:gap-1.5")}>
        {weekdays.map((day) => (
          <div key={day} className="py-1 text-center text-[11px] text-muted-foreground">
            {day}
          </div>
        ))}
        {Array.from({ length: (first.getDay() + 6) % 7 }, (_, i) => (
          <div key={`blank-${i}`} />
        ))}
        {Array.from({ length: count }, (_, i) => {
          const day = addDays(first, i),
            key = dateKey(day),
            list = byDay.get(key) ?? [],
            today = key === dateKey(new Date())
          const entries = `${list.length} ${list.length === 1 ? "entry" : "entries"}`
          if (compact) {
            // Year: a density mark per day says how many starts, not which.
            const density = Math.min(4, Math.ceil(list.length / 3))
            return (
              <button
                type="button"
                key={key}
                aria-label={`Open ${key}, ${entries}`}
                onClick={() => openDay(day)}
                className={cn(
                  "flex min-h-8 flex-col items-center rounded-md p-0.5 text-xs hover:bg-muted",
                  today && "bg-primary/15 text-primary",
                )}
              >
                <span>{i + 1}</span>
                <span
                  aria-hidden
                  className={cn("mt-0.5 h-1 rounded-full", list.length && "bg-primary")}
                  style={{
                    width: `${Math.max(4, density * 5)}px`,
                    opacity: list.length ? 0.4 + density * 0.15 : 0,
                  }}
                />
              </button>
            )
          }
          return (
            <div
              key={key}
              role="button"
              tabIndex={0}
              aria-label={`Open ${key}, ${entries}`}
              data-testid={`calendar-day-${key}`}
              onClick={() => openDayView(day)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault()
                  openDayView(day)
                }
              }}
              className={cn(
                "flex h-[72px] min-w-0 cursor-pointer flex-col gap-0.5 overflow-hidden rounded-xl border border-border/60 bg-muted/20 p-1 transition-colors hover:border-primary/50 hover:bg-muted/40 focus-visible:outline focus-visible:outline-primary md:h-28 md:p-1.5",
                today && "border-primary/60",
              )}
            >
              <div className="flex items-center justify-between">
                <span className={cn("px-1 text-xs", today && "font-semibold text-primary")}>
                  {i + 1}
                </span>
                {canSchedule && (
                  <button
                    type="button"
                    aria-label={`Schedule on ${key}`}
                    onClick={(e) => {
                      e.stopPropagation()
                      plan(day)
                    }}
                    className="rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"
                  >
                    <Plus className="h-3 w-3" />
                  </button>
                )}
              </div>
              {monthCellBody(list)}
            </div>
          )
        })}
      </div>
    )
  }

  const agenda = (key: string) => {
    const day = parseCalendarDate(key) ?? anchor
    const list = byDay.get(key) ?? []
    const items = agendaItems(list)
    const waiting = list.filter((e) => calendarOutcome(e) === "waiting")
    const summary = daySummary(list)
    const groupRow = (group: RoutineGroup) => {
      const open = openGroup === group.slug
      const first = group.entries[0]
      const kinds = group.allPlanned
        ? [
            ...new Set(
              group.entries.map((e) => (e.kind === "pending" ? "one-time starts" : "schedule")),
            ),
          ].join(" · ")
        : ""
      const meta = [
        group.allPlanned ? "planned" : "ran",
        timeRange(group.entries),
        group.cadence,
        kinds,
        group.allPlanned && first.pinned_version ? `pinned v${first.pinned_version}` : "",
        group.allPlanned ? `with: ${routinePresetSummary(first.inputs)}` : daySummary(group.entries),
      ]
        .filter(Boolean)
        .join(" · ")
      return (
        <div key={group.slug} className="border-t border-border/60 first:border-t-0">
          <div className="flex flex-wrap items-center gap-3 px-4 py-2.5">
            {icon(group.slug)}
            <div className="min-w-0 flex-1">
              <span className="text-sm font-medium">{nameOf(group.slug, group.name)}</span>{" "}
              <span className="font-mono text-xs font-semibold text-primary">
                ×{group.entries.length}
              </span>
              <span className="block truncate text-xs text-muted-foreground">{meta}</span>
            </div>
            <div className="flex gap-2">
              <Button asChild size="sm" variant="outline">
                <Link href={planHref(group.slug)}>Open routine</Link>
              </Button>
              <Button
                size="sm"
                variant="outline"
                aria-expanded={open}
                onClick={() => setOpenGroup(open ? null : group.slug)}
              >
                {open ? "Hide" : `Show all ${group.entries.length}`}
              </Button>
            </div>
          </div>
          {open && (
            <div className="flex flex-wrap gap-1.5 px-4 pb-3 md:pl-14">
              {group.entries.map((event) => (
                <Link
                  key={`${event.kind}:${event.id}`}
                  href={entryHref(event)}
                  title={eventTitle(event)}
                  className="inline-flex items-center gap-1.5 rounded-md border border-border/60 px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground hover:bg-muted"
                >
                  <span
                    aria-hidden
                    className={cn("h-1.5 w-1.5 rounded-full", DOT[calendarOutcome(event)])}
                  />
                  {calendarClock(event.at)}
                </Link>
              ))}
            </div>
          )}
        </div>
      )
    }
    const entryRow = (event: CalendarEntry) => {
      const outcome = calendarOutcome(event)
      const presentation = routineRunPresentation(event)
      return (
        <Link
          key={`${event.kind}:${event.id}`}
          href={entryHref(event)}
          className="grid grid-cols-[48px_minmax(0,1fr)_24px] items-center gap-3 border-t border-border/60 px-4 py-2.5 first:border-t-0 hover:bg-muted/40 md:grid-cols-[56px_auto_minmax(0,1fr)_auto_24px]"
        >
          <span
            className={cn(
              "font-mono text-xs",
              outcome === "planned" ? "text-primary" : "text-muted-foreground",
            )}
          >
            {calendarClock(event.at)}
          </span>
          <span className="hidden md:block">{icon(event.slug)}</span>
          <span className="min-w-0">
            <span className="block truncate text-sm font-medium">
              {nameOf(event.slug, event.name)}
            </span>
            <span className="block truncate text-xs text-muted-foreground">
              {event.kind === "run"
                ? `Ran · ${presentation.label}`
                : `Planned · ${calendarVersionLabel(event)} · ${routinePresetSummary(event.inputs)}`}
            </span>
          </span>
          <span className="hidden md:block">
            {event.kind === "run" ? (
              <StatusPill
                tone={
                  presentation.tone === "destructive"
                    ? "danger"
                    : presentation.tone === "default"
                      ? "muted"
                      : presentation.tone
                }
                label={presentation.label}
              />
            ) : (
              <StatusPill tone="blue" label="Planned" />
            )}
          </span>
          <ChevronRight aria-hidden className="h-4 w-4 text-muted-foreground" />
        </Link>
      )
    }
    return (
      <section
        aria-label="Day agenda"
        className="overflow-hidden rounded-xl border border-border/60 bg-card"
      >
        <div className="flex flex-wrap items-center gap-2 border-b border-border/60 px-4 py-2.5">
          <span className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
            {summary || "Nothing on this day"}
          </span>
          <span className="flex-1" />
          <Button size="sm" variant="outline" onClick={() => setAgendaDay(null)}>
            ‹ {view === "year" ? "Year" : view === "month" ? "Month" : CALENDAR_LABELS[view]}
          </Button>
          {canSchedule && (
            <Button size="sm" onClick={() => plan(day)}>
              <Plus className="mr-1 h-3.5 w-3.5" />
              Schedule a start
            </Button>
          )}
        </div>
        {waiting.length > 0 && (
          <div
            role="status"
            className="m-3 flex flex-wrap items-center gap-3 rounded-lg border border-warn/30 bg-warn/10 px-3 py-2 text-sm"
          >
            <AlertCircle className="h-4 w-4 shrink-0 text-warn" aria-hidden />
            <span className="flex-1 font-medium text-warn">
              {waiting.length === 1
                ? "1 run waiting for your decision"
                : `${waiting.length} runs waiting for your decision`}
            </span>
            <Button asChild size="sm" variant="outline">
              <Link href={runHref(waiting[0])}>Decide</Link>
            </Button>
          </div>
        )}
        <div>
          {items.map((item) =>
            item.kind === "group" ? groupRow(item.group) : entryRow(item.entry),
          )}
          {!items.length && (
            <p className="px-4 py-6 text-sm text-muted-foreground">
              {filter === "all" ? "Nothing on this day." : "No entries match the filter."}
            </p>
          )}
        </div>
      </section>
    )
  }

  const title = agendaDay
    ? (parseCalendarDate(agendaDay) ?? anchor).toLocaleDateString("en-GB", {
        weekday: "long",
        day: "numeric",
        month: "long",
        year: "numeric",
      })
    : view === "year"
      ? String(anchor.getFullYear())
      : view === "month"
        ? anchor.toLocaleDateString("en-GB", { month: "long", year: "numeric" })
        : `${from.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric" })}${view !== "day" ? ` – ${addDays(to, -1).toLocaleDateString("en-GB", { day: "numeric", month: "short" })}` : ""}`
  const days = Array.from(
    { length: view === "week" ? 7 : view === "three-days" ? 3 : 1 },
    (_, i) => addDays(from, i),
  )
  return (
    <section className="space-y-3 rounded-3xl border border-white/[0.06] bg-card p-4">
      {/* One toolbar, as the prototype draws it: views · ‹ Today › · title · filters. */}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <nav
          aria-label="Calendar views"
          className="flex items-center gap-0.5 rounded-md border border-border/60 p-0.5"
        >
          {CALENDAR_VIEWS.map((v) => (
            <button
              key={v}
              aria-pressed={view === v && !agendaDay}
              onClick={() => {
                setAgendaDay(null)
                setView(v)
              }}
              className={cn(
                "rounded px-2.5 py-1 text-xs transition-colors",
                view === v && !agendaDay
                  ? "bg-primary/15 font-medium text-primary"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              {CALENDAR_LABELS[v]}
            </button>
          ))}
        </nav>
        <div className="flex items-center gap-0.5 rounded-md border border-border/60 p-0.5">
          <button
            type="button"
            aria-label="Previous"
            onClick={() => setDate(dateKey(moveCalendar(anchor, view, -1)))}
            className="rounded px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
          >
            ‹
          </button>
          <button
            type="button"
            onClick={() => setDate(dateKey(new Date()))}
            className="rounded px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
          >
            Today
          </button>
          <button
            type="button"
            aria-label="Next"
            onClick={() => setDate(dateKey(moveCalendar(anchor, view, 1)))}
            className="rounded px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
          >
            ›
          </button>
        </div>
        <label className="relative">
          <span className="sr-only">Go to date</span>
          <h2 className="cursor-pointer font-medium">{title}</h2>
          <input
            aria-label="Go to date"
            type="date"
            value={anchorKey}
            onChange={(e) => {
              if (parseCalendarDate(e.target.value)) setDate(e.target.value)
            }}
            className="absolute inset-0 cursor-pointer opacity-0"
          />
        </label>
        <span className="text-xs text-muted-foreground">{routineTimeZone()}</span>
        <div className="flex-1" />
        <div
          role="group"
          aria-label="Calendar events"
          className="flex flex-wrap items-center gap-1.5"
        >
          {CALENDAR_FILTERS.map((f) => (
            <button
              key={f}
              type="button"
              aria-pressed={filter === f}
              onClick={() => setFilter(f)}
              className={cn(
                "inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px] transition-colors",
                filter === f
                  ? "border-primary/40 bg-primary/[0.12] text-primary"
                  : "border-white/[0.08] bg-white/[0.02] text-muted-foreground hover:text-foreground/80",
              )}
            >
              <span aria-hidden className={cn("h-2 w-2 rounded-full", FILTER_DOT[f])} />
              <span>{CALENDAR_FILTER_LABELS[f]}</span>
              <span className="text-[10px] tabular-nums opacity-60">{counts[f]}</span>
            </button>
          ))}
        </div>
        {canSchedule && (
          <Button size="sm" onClick={() => plan(anchor)}>
            <Plus className="mr-1 h-3.5 w-3.5" />
            Schedule a start
          </Button>
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        <span className="text-primary">Blue</span> = planned start, colours = how a run ended.
        A day shows its first two starts; open the day for every start.
      </p>
      {loading && <p role="status">Loading calendar…</p>}
      {error && (
        <p role="alert">
          Calendar could not be loaded completely.{" "}
          <button className="underline" onClick={() => setRevision((v) => v + 1)}>
            Retry
          </button>
        </p>
      )}
      {truncated && (
        <p role="status" className="text-xs text-warn">
          Some occurrences are omitted in this range. Open a shorter view to inspect a
          busy date.
        </p>
      )}
      {agendaDay ? (
        agenda(agendaDay)
      ) : view === "year" ? (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
          {Array.from({ length: 12 }, (_, i) => new Date(anchor.getFullYear(), i, 1)).map(
            (month) => (
              <section
                key={month.getMonth()}
                className="rounded-xl border border-border/60 bg-muted/20 p-3"
              >
                <button
                  className="mb-2 text-sm font-medium hover:text-primary"
                  onClick={() => {
                    setDate(dateKey(month))
                    setView("month")
                  }}
                >
                  {month.toLocaleDateString("en-GB", { month: "long" })}
                </button>
                {monthGrid(month, true)}
              </section>
            ),
          )}
        </div>
      ) : view === "month" ? (
        monthGrid(anchor)
      ) : (
        <div
          ref={timeGrid}
          className="max-h-[65dvh] overflow-auto rounded-xl border border-border/60"
        >
          <div
            className={
              days.length === 7
                ? "min-w-[840px]"
                : days.length === 3
                  ? "min-w-[570px]"
                  : "min-w-[260px]"
            }
          >
            <div
              className={`sticky top-0 z-10 grid bg-card ${days.length === 7 ? "grid-cols-[54px_repeat(7,minmax(0,1fr))]" : days.length === 3 ? "grid-cols-[54px_repeat(3,minmax(0,1fr))]" : "grid-cols-[54px_minmax(0,1fr)]"}`}
            >
              <div />
              {days.map((day) => (
                <button
                  key={dateKey(day)}
                  onClick={() => openDay(day)}
                  className="border-l p-3 text-xs font-medium hover:bg-muted"
                >
                  {day.toLocaleDateString("en-GB", {
                    weekday: "short",
                    day: "numeric",
                    month: "short",
                  })}
                </button>
              ))}
            </div>
            {Array.from({ length: 24 }, (_, hour) => (
              <div
                key={hour}
                data-calendar-hour={hour}
                className={`grid border-t border-border/50 ${days.length === 7 ? "grid-cols-[54px_repeat(7,minmax(0,1fr))]" : days.length === 3 ? "grid-cols-[54px_repeat(3,minmax(0,1fr))]" : "grid-cols-[54px_minmax(0,1fr)]"}`}
              >
                <span className="p-2 text-[11px] text-muted-foreground">
                  {String(hour).padStart(2, "0")}:00
                </span>
                {days.map((day) => (
                  <div
                    key={dateKey(day)}
                    className="relative min-h-20 min-w-0 border-l border-border/50 p-1"
                  >
                    <button
                      disabled={!canSchedule}
                      aria-label={`Schedule on ${dateKey(day)} at ${String(hour).padStart(2, "0")}:00`}
                      onClick={() => plan(day, hour)}
                      className="absolute inset-0 hover:bg-muted/30 focus-visible:bg-muted"
                    />
                    <div className="relative pointer-events-none [&>a]:pointer-events-auto [&>button]:pointer-events-auto">
                      {hourCell(
                        (byDay.get(dateKey(day)) ?? []).filter(
                          (e) => new Date(e.at).getHours() === hour,
                        ),
                        day,
                      )}
                    </div>
                  </div>
                ))}
              </div>
            ))}
          </div>
        </div>
      )}
      {scheduleDate && (
        <RoutineCalendarSchedule
          workspaceId={workspaceId}
          routines={routines}
          date={scheduleDate}
          onClose={() => setScheduleDate(null)}
          onScheduled={() => {
            setScheduleDate(null)
            setRevision((v) => v + 1)
          }}
        />
      )}
    </section>
  )
}

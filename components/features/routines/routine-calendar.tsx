"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import Link from "next/link"
import { Plus } from "lucide-react"
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
import { CrewIcon } from "@/components/ui/crew-icon"
import { Button } from "@/components/ui/button"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { routineRunPresentation } from "@/lib/routine-run-presentation"
import { cn } from "@/lib/utils"
import { RoutineCalendarSchedule } from "./routine-calendar-schedule"

interface CalendarEvent {
  id: string
  kind: "planned" | "pending" | "run"
  at: string
  slug: string
  name: string
  status?: string
  outcome?: string
  pinned_version?: number | null
}
const weekdays = Array.from({ length: 7 }, (_, i) =>
  new Date(2026, 0, 5 + i).toLocaleDateString("en-GB", { weekday: "short" }),
)

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
  const { from, to } = calendarRange(anchor, view)
  const fromKey = from.toISOString(),
    toKey = to.toISOString()
  const [events, setEvents] = useState<CalendarEvent[]>([])
  const [error, setError] = useState(false)
  const [truncated, setTruncated] = useState(false)
  const [loading, setLoading] = useState(true)
  const [revision, setRevision] = useState(0)
  const [filter, setFilter] = useState("all")
  const [scheduleDate, setScheduleDate] = useState<Date | null>(null)
  const { role } = useAbilities()
  const canSchedule = roleAtLeast(role, "MEMBER")
  const timeGrid = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const grid = timeGrid.current
    const morning = grid?.querySelector<HTMLElement>('[data-calendar-hour="8"]')
    if (grid && morning && !loading)
      grid.scrollTop += morning.getBoundingClientRect().top - grid.getBoundingClientRect().top - 42
  }, [view, anchorKey, loading])
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
        const collected: CalendarEvent[] = []
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
  const byDay = useMemo(() => {
    const map = new Map<string, CalendarEvent[]>()
    for (const event of events) {
      if (
        !bySlug.has(event.slug) ||
        (filter === "planned" && event.kind === "run") ||
        (filter === "history" && event.kind !== "run")
      )
        continue
      const key = dateKey(new Date(event.at))
      const list = map.get(key) ?? []
      list.push(event)
      map.set(key, list)
    }
    for (const list of map.values()) list.sort((a, b) => Date.parse(a.at) - Date.parse(b.at))
    return map
  }, [events, bySlug, filter])
  const plan = (day: Date, hour?: number) => {
    if (!canSchedule) return
    const proposed = new Date(day.getFullYear(), day.getMonth(), day.getDate(), hour ?? 9)
    if (hour == null && dateKey(day) === dateKey(new Date()) && proposed.getTime() <= Date.now()) {
      const now = new Date()
      proposed.setHours(now.getHours() + 1, 0, 0, 0)
    }
    setScheduleDate(proposed)
  }
  const eventLink = (event: CalendarEvent) => {
    const routine = bySlug.get(event.slug)!
    const label =
      event.kind === "run"
        ? routineRunPresentation(event).label
        : `${event.kind === "pending" ? "Scheduled once" : "Planned"} · ${event.pinned_version ? `v${event.pinned_version}` : event.kind === "pending" ? "legacy live version" : "published at start"}`
    return (
      <Link
        key={`${event.kind}:${event.id}`}
        href={
          event.kind === "run"
            ? `/routines?${new URLSearchParams({ slug: event.slug, run: event.id })}`
            : `/routines?${new URLSearchParams({ slug: event.slug, view: "plan" })}`
        }
        title={`${routine.name || event.name} · ${new Date(event.at).toLocaleString("en-GB")} · ${label}`}
        className={cn(
          "my-1 flex min-w-0 items-start gap-1.5 rounded-lg border-l-2 p-1.5 text-[11px] hover:bg-muted",
          event.kind === "run"
            ? "border-muted-foreground/50 bg-muted/30"
            : "border-primary bg-primary/10",
        )}
      >
        <CrewIcon
          icon={resolveRoutineIcon(routine)}
          color={resolveRoutineColor(routine)}
          size="sm"
        />
        <span className="min-w-0">
          <span className="block font-medium">
            {new Date(event.at).toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })}
          </span>
          <span className="block truncate">{routine.name || event.name}</span>
          <span className="block text-muted-foreground">{label}</span>
        </span>
      </Link>
    )
  }
  const monthGrid = (month: Date, compact = false) => {
    const first = new Date(month.getFullYear(), month.getMonth(), 1)
    const count = new Date(month.getFullYear(), month.getMonth() + 1, 0).getDate()
    return (
      <div className={compact ? "" : "overflow-x-auto"}>
        <div className={cn("grid grid-cols-7 gap-1", !compact && "min-w-[700px] gap-2")}>
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
            return compact ? (
              <button
                type="button"
                key={key}
                aria-label={`Schedule on ${key}${list.length ? `, ${list.length} events` : ""}`}
                disabled={!canSchedule}
                onClick={() => plan(day)}
                className={cn(
                  "min-h-8 rounded-md p-0.5 text-xs hover:bg-muted disabled:opacity-100",
                  today && "bg-primary/15 text-primary",
                )}
              >
                <span>{i + 1}</span>
                <span className="flex min-h-4 justify-center gap-0.5">
                  {list.slice(0, 2).map((event) => {
                    const r = bySlug.get(event.slug)!
                    return (
                      <span key={`${event.kind}:${event.id}`} title={r.name}>
                        <CrewIcon
                          icon={resolveRoutineIcon(r)}
                          color={resolveRoutineColor(r)}
                          size="sm"
                          className="h-4 w-4 [&>svg]:h-3 [&>svg]:w-3"
                        />
                      </span>
                    )
                  })}
                  {list.length > 2 && <span className="text-[9px]">+{list.length - 2}</span>}
                </span>
              </button>
            ) : (
              <div
                key={key}
                className={cn(
                  "min-h-32 min-w-0 rounded-xl border border-border/60 bg-muted/20 p-2",
                  today && "border-primary/60",
                )}
              >
                <button
                  type="button"
                  disabled={!canSchedule}
                  aria-label={`Schedule on ${key}`}
                  onClick={() => plan(day)}
                  className={cn(
                    "flex w-full items-center justify-between rounded px-1 py-1 text-xs hover:bg-muted disabled:opacity-100",
                    today && "text-primary",
                  )}
                >
                  <span>{i + 1}</span>
                  {canSchedule && <Plus className="h-3 w-3" />}
                </button>
                <div className="max-h-52 overflow-y-auto">{list.map(eventLink)}</div>
              </div>
            )
          })}
        </div>
      </div>
    )
  }
  const title =
    view === "year"
      ? String(anchor.getFullYear())
      : view === "month"
        ? anchor.toLocaleDateString("en-GB", { month: "long", year: "numeric" })
        : `${from.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric" })}${view !== "day" ? ` – ${addDays(to, -1).toLocaleDateString("en-GB", { day: "numeric", month: "short" })}` : ""}`
  const days = Array.from({ length: view === "week" ? 7 : view === "three-days" ? 3 : 1 }, (_, i) =>
    addDays(from, i),
  )
  return (
    <section className="space-y-4 rounded-3xl border border-white/[0.06] bg-card p-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="font-medium">{title}</h2>
        <div className="flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="outline"
            onClick={() => setDate(dateKey(moveCalendar(anchor, view, -1)))}
          >
            Previous
          </Button>
          <Button size="sm" variant="outline" onClick={() => setDate(dateKey(new Date()))}>
            Today
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={loading}
            onClick={() => setRevision((v) => v + 1)}
          >
            Refresh
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setDate(dateKey(moveCalendar(anchor, view, 1)))}
          >
            Next
          </Button>
          {canSchedule && (
            <Button size="sm" onClick={() => plan(anchor)}>
              Add routine
            </Button>
          )}
        </div>
      </div>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <nav aria-label="Calendar views" className="flex flex-wrap gap-1">
          {CALENDAR_VIEWS.map((v) => (
            <button
              key={v}
              aria-pressed={view === v}
              onClick={() => setView(v)}
              className={cn(
                "rounded-full px-3 py-1.5 text-xs",
                view === v ? "bg-muted font-medium" : "text-muted-foreground hover:bg-muted/60",
              )}
            >
              {CALENDAR_LABELS[v]}
            </button>
          ))}
        </nav>
        <div className="flex flex-wrap gap-2">
          <input
            aria-label="Go to date"
            type="date"
            value={anchorKey}
            onChange={(e) => {
              if (parseCalendarDate(e.target.value)) setDate(e.target.value)
            }}
            className="min-w-0 rounded-md border bg-card p-1 text-xs"
          />
          <select
            aria-label="Calendar events"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            className="rounded-md border bg-card p-1 text-xs"
          >
            <option value="all">All events</option>
            <option value="planned">Planned starts</option>
            <option value="history">Run history</option>
          </select>
        </div>
      </div>
      <p className="text-xs text-muted-foreground">
        {Intl.DateTimeFormat().resolvedOptions().timeZone} · Blue entries are planned starts;
        history shows actual runs.{canSchedule && " Click a date or time to schedule a routine."}
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
          Some occurrences are omitted in this range. Open a shorter view to inspect a busy date.
        </p>
      )}
      {view === "year" ? (
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
          className="max-h-[65vh] overflow-auto rounded-xl border border-border/60"
        >
          <div style={{ minWidth: days.length === 7 ? 840 : days.length === 3 ? 570 : 260 }}>
            <div
              className="sticky top-0 z-10 grid bg-card"
              style={{ gridTemplateColumns: `54px repeat(${days.length}, minmax(0, 1fr))` }}
            >
              <div />
              {days.map((day) => (
                <button
                  key={dateKey(day)}
                  disabled={!canSchedule}
                  onClick={() => plan(day)}
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
                className="grid border-t border-border/50"
                style={{ gridTemplateColumns: `54px repeat(${days.length}, minmax(0, 1fr))` }}
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
                    <div className="relative pointer-events-none [&>a]:pointer-events-auto">
                      {(byDay.get(dateKey(day)) ?? [])
                        .filter((e) => new Date(e.at).getHours() === hour)
                        .map(eventLink)}
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

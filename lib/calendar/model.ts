import type { Pipeline } from "@/hooks/use-pipelines"
import { addDays, dateKey, parseCalendarDate } from "@/lib/routine-calendar"
import { calendarOutcome, type CalendarEntry } from "@/lib/routine-calendar-groups"
import { routineRunPresentation } from "@/lib/routine-run-presentation"

// The Calendar page's model (Harbor 2, F-01). Calendar is a view, not a store:
// every item is the date of something that already exists, and an item shows
// only when its source is readable. Today the one source with a date range is
// the routine calendar (schedules, one-time starts, runs); issue due dates,
// milestones and decision deadlines join when the backend serves them.
// Everything here is pure so the views and their tests share one truth.

export const CALENDAR_PAGE_VIEWS = ["agenda", "week", "month"] as const
export type CalendarPageView = (typeof CALENDAR_PAGE_VIEWS)[number]
export const CALENDAR_VIEW_LABELS: Record<CalendarPageView, string> = { agenda: "Agenda", week: "Week", month: "Month" }
export const isCalendarPageView = (v: string | null | undefined): v is CalendarPageView =>
  CALENDAR_PAGE_VIEWS.includes(v as CalendarPageView)

export type CalendarScope = "mine" | "workspace"
export const CALENDAR_SCOPE_LABELS: Record<CalendarScope, string> = { mine: "Mine", workspace: "Workspace" }

/** What the item's icon says: planned, done, waiting for a person, failed, running, stopped. */
export type CalendarItemStatus = "plan" | "ok" | "wait" | "bad" | "run" | "info"

/** Layers with a live source. The others (issues, milestones, decisions) need the backend. */
export type CalendarLayer = "routine"
export const CALENDAR_LAYER_LABELS: Record<CalendarLayer, string> = { routine: "Routine starts and runs" }

export interface CalendarItem {
  /** Unique across kinds: a run and a pending start may share an id space. */
  id: string
  layer: CalendarLayer
  kind: CalendarEntry["kind"]
  at: Date
  /** The local day (browser zone) the item sits on. */
  day: string
  /** "HH:MM" local; null for date-only items (none yet — issues and milestones will be). */
  time: string | null
  title: string
  subtitle: string
  status: CalendarItemStatus
  /** The viewer published the routine behind it. */
  mine: boolean
  crewId: string | null
  slug: string
  /** The schedule's own zone, when it has one. */
  timezone?: string
  entry: CalendarEntry
}

const pad = (n: number) => String(n).padStart(2, "0")
export const clockOf = (d: Date) => `${pad(d.getHours())}:${pad(d.getMinutes())}`

export function itemStatus(entry: CalendarEntry): CalendarItemStatus {
  switch (calendarOutcome(entry)) {
    case "planned":
      return "plan"
    case "completed":
      return "ok"
    case "waiting":
      return "wait"
    case "failed":
      return "bad"
    case "running":
      return "run"
    default:
      return "info"
  }
}

function subtitleOf(entry: CalendarEntry): string {
  if (entry.kind === "planned") return `Repeating schedule${entry.pinned_version ? ` · v${entry.pinned_version}` : ""}`
  if (entry.kind === "pending") return `One-time start${entry.pinned_version ? ` · v${entry.pinned_version}` : ""}`
  return `Run · ${routineRunPresentation(entry).label}`
}

/**
 * Routine calendar events → calendar items. An event whose routine is not in
 * the viewer's routine list is dropped: the calendar never shows a date whose
 * source the viewer cannot open.
 */
export function routineItems(events: CalendarEntry[], routines: Pipeline[], meId: string | null): CalendarItem[] {
  const bySlug = new Map(routines.map((r) => [r.slug, r]))
  const out: CalendarItem[] = []
  for (const entry of events) {
    const routine = bySlug.get(entry.slug)
    if (!routine) continue
    const at = new Date(entry.at)
    if (Number.isNaN(at.getTime())) continue
    out.push({
      id: `${entry.kind}:${entry.id}`,
      layer: "routine",
      kind: entry.kind,
      at,
      day: dateKey(at),
      time: clockOf(at),
      title: routine.name || entry.name || entry.slug,
      subtitle: subtitleOf(entry),
      status: itemStatus(entry),
      mine: !!meId && routine.author_user_id === meId,
      crewId: routine.author_crew_id || null,
      slug: entry.slug,
      timezone: entry.timezone,
      entry,
    })
  }
  return out.sort((a, b) => a.at.getTime() - b.at.getTime() || a.id.localeCompare(b.id))
}

/* ------------------------------------------------------------------ ranges */

const mondayOf = (d: Date) => addDays(d, -((d.getDay() + 6) % 7))
export const AGENDA_DAYS = 14

/** The local days a view shows: [from, to). */
export function pageRange(anchor: Date, view: CalendarPageView): { from: Date; to: Date } {
  const day = addDays(anchor, 0)
  if (view === "week") {
    const from = mondayOf(day)
    return { from, to: addDays(from, 7) }
  }
  if (view === "agenda") return { from: day, to: addDays(day, AGENDA_DAYS) }
  // Month: whole weeks, Monday first, covering the month — 5 or 6 rows.
  const first = new Date(day.getFullYear(), day.getMonth(), 1)
  const last = new Date(day.getFullYear(), day.getMonth() + 1, 0)
  const from = mondayOf(first)
  const to = addDays(mondayOf(last), 7)
  return { from, to }
}

/**
 * Split a range into requests the routine calendar accepts (at most 32 days).
 * Windows are whole local days; 31 days stays under the limit across a DST
 * change. A month grid of 35–42 days is two requests.
 */
export function apiWindows(from: Date, to: Date, maxDays = 31): { from: Date; to: Date }[] {
  const out: { from: Date; to: Date }[] = []
  for (let cursor = from; cursor < to; ) {
    const next = addDays(cursor, maxDays)
    const end = next < to ? next : to
    out.push({ from: cursor, to: end })
    cursor = end
  }
  return out
}

export function movePage(anchor: Date, view: CalendarPageView, direction: number): Date {
  if (view === "month") return new Date(anchor.getFullYear(), anchor.getMonth() + direction, 1)
  return addDays(anchor, direction * (view === "week" ? 7 : AGENDA_DAYS))
}

const fmt = (d: Date, o: Intl.DateTimeFormatOptions) => d.toLocaleDateString("en-GB", o)

/** "12 – 18 October 2026", "October 2026", "Next 2 weeks". */
export function rangeTitle(anchor: Date, view: CalendarPageView, now = new Date()): string {
  if (view === "month") return fmt(anchor, { month: "long", year: "numeric" })
  const { from, to } = pageRange(anchor, view)
  if (view === "agenda" && dateKey(from) === dateKey(now)) return "Next 2 weeks"
  const last = addDays(to, -1)
  const sameYear = from.getFullYear() === last.getFullYear()
  const sameMonth = sameYear && from.getMonth() === last.getMonth()
  const start = sameMonth
    ? String(from.getDate())
    : fmt(from, sameYear ? { day: "numeric", month: "long" } : { day: "numeric", month: "long", year: "numeric" })
  return `${start} – ${fmt(last, { day: "numeric", month: "long", year: "numeric" })}`
}

/** "Today", "Tomorrow", else "Fri 23 Oct". */
export function dayLabel(key: string, now = new Date()): string {
  if (key === dateKey(now)) return "Today"
  if (key === dateKey(addDays(now, 1))) return "Tomorrow"
  const d = parseCalendarDate(key)
  return d ? fmt(d, { weekday: "short", day: "numeric", month: "short" }) : key
}

/* ----------------------------------------------------------------- filters */

export const STATUS_FACETS = [
  { id: "risk", label: "At risk", test: (s: CalendarItemStatus) => s === "bad" },
  { id: "waiting", label: "Waiting for someone", test: (s: CalendarItemStatus) => s === "wait" },
  { id: "planned", label: "Planned", test: (s: CalendarItemStatus) => s === "plan" },
  { id: "done", label: "Done", test: (s: CalendarItemStatus) => s === "ok" },
] as const
export type StatusFacet = (typeof STATUS_FACETS)[number]["id"]

export interface CalendarFilters {
  scope: CalendarScope
  crewId: string | null
  statuses: StatusFacet[]
  query: string
}
export const EMPTY_CALENDAR_FILTERS: CalendarFilters = { scope: "mine", crewId: null, statuses: [], query: "" }

const inScope = (i: CalendarItem, scope: CalendarScope) => scope === "workspace" || i.mine
const matchesQuery = (i: CalendarItem, q: string) => {
  const t = q.trim().toLowerCase()
  return !t || i.title.toLowerCase().includes(t) || i.slug.toLowerCase().includes(t) || i.subtitle.toLowerCase().includes(t)
}
/** Nothing checked = everything; checking narrows (same as every other panel). */
const matchesStatus = (i: CalendarItem, statuses: StatusFacet[]) =>
  !statuses.length || STATUS_FACETS.some((f) => statuses.includes(f.id) && f.test(i.status))

export function filterItems(items: CalendarItem[], f: CalendarFilters, skip?: "status" | "scope" | "crew"): CalendarItem[] {
  return items.filter(
    (i) =>
      (skip === "scope" || inScope(i, f.scope)) &&
      (skip === "crew" || !f.crewId || i.crewId === f.crewId) &&
      (skip === "status" || matchesStatus(i, f.statuses)) &&
      matchesQuery(i, f.query),
  )
}

/** Panel counts: each count ignores its own facet, so picking one never zeroes the rest. */
export function calendarCounts(items: CalendarItem[], f: CalendarFilters) {
  const byScope = filterItems(items, f, "scope")
  const byCrew = filterItems(items, f, "crew")
  const byStatus = filterItems(items, f, "status")
  const crews = new Map<string, number>()
  for (const i of byCrew) if (i.crewId) crews.set(i.crewId, (crews.get(i.crewId) ?? 0) + 1)
  return {
    mine: byScope.filter((i) => i.mine).length,
    workspace: byScope.length,
    crews,
    statuses: Object.fromEntries(STATUS_FACETS.map((s) => [s.id, byStatus.filter((i) => s.test(i.status)).length])) as Record<StatusFacet, number>,
  }
}

/**
 * "Need you soon": what is waiting for a person, and runs that failed today.
 * Only rules the data can back — the calendar never invents a reason.
 */
export function needYouSoon(items: CalendarItem[], now = new Date(), cap = 4): CalendarItem[] {
  const today = dateKey(now)
  return items.filter((i) => i.status === "wait" || (i.status === "bad" && i.day === today)).slice(0, cap)
}

/* ------------------------------------------------------------------- views */

/** Week: the days, an All day row for date-only items, and only the hours that have something. */
export function weekLayout(items: CalendarItem[], from: Date) {
  const days = Array.from({ length: 7 }, (_, i) => addDays(from, i))
  const keys = new Set(days.map(dateKey))
  const inWeek = items.filter((i) => keys.has(i.day))
  const hours = [...new Set(inWeek.filter((i) => i.time).map((i) => i.at.getHours()))].sort((a, b) => a - b)
  return { days, allDay: inWeek.filter((i) => !i.time), timed: inWeek.filter((i) => i.time), hours }
}

/** Where the "now" line sits: after the last shown hour at or before now; -1 = above the first row. */
export function nowLineAfter(hours: number[], now: Date): number {
  let at = -1
  hours.forEach((h, idx) => {
    if (h <= now.getHours()) at = idx
  })
  return at
}

export const CELL_CAP = 3
export const MONTH_CAP = 3

/** Month: the grid's days, items per day capped at three with "+N more", a risk dot. */
export function monthLayout(items: CalendarItem[], anchor: Date) {
  const { from, to } = pageRange(anchor, "month")
  const byDay = new Map<string, CalendarItem[]>()
  for (const i of items) (byDay.get(i.day) ?? byDay.set(i.day, []).get(i.day)!).push(i)
  const cells: { date: Date; key: string; inMonth: boolean; shown: CalendarItem[]; more: number; risk: boolean }[] = []
  for (let d = from; d < to; d = addDays(d, 1)) {
    const key = dateKey(d)
    const list = byDay.get(key) ?? []
    cells.push({
      date: d,
      key,
      inMonth: d.getMonth() === anchor.getMonth(),
      shown: list.slice(0, MONTH_CAP),
      more: Math.max(0, list.length - MONTH_CAP),
      risk: list.some((i) => i.status === "bad"),
    })
  }
  return cells
}

export type AgendaRow =
  | { kind: "item"; item: CalendarItem }
  /** A routine with many planned starts in one day reads as one row, not forty. */
  | { kind: "group"; slug: string; title: string; items: CalendarItem[] }

export const AGENDA_GROUP_MIN = 4

/** Agenda: days in order, empty days left out; date-only items ("Due") first. */
export function agendaLayout(items: CalendarItem[]) {
  const byDay = new Map<string, CalendarItem[]>()
  for (const i of items) (byDay.get(i.day) ?? byDay.set(i.day, []).get(i.day)!).push(i)
  return [...byDay.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, list]) => {
      const sorted = [...list].sort((a, b) => (a.time ? 1 : 0) - (b.time ? 1 : 0) || a.at.getTime() - b.at.getTime())
      const planned = new Map<string, CalendarItem[]>()
      for (const i of sorted) if (i.status === "plan") (planned.get(i.slug) ?? planned.set(i.slug, []).get(i.slug)!).push(i)
      const rows: AgendaRow[] = []
      const grouped = new Set<string>()
      for (const i of sorted) {
        const group = i.status === "plan" ? planned.get(i.slug)! : null
        if (group && group.length >= AGENDA_GROUP_MIN) {
          if (!grouped.has(i.slug)) {
            grouped.add(i.slug)
            rows.push({ kind: "group", slug: i.slug, title: i.title, items: group })
          }
          continue
        }
        rows.push({ kind: "item", item: i })
      }
      return { key, rows }
    })
}

/* ------------------------------------------------------------------- links */

/** The Calendar page for a view and day, optionally on one layer. */
export function calendarHref(opts: { view?: CalendarPageView; date?: string | null; layers?: CalendarLayer } = {}): string {
  const q = new URLSearchParams()
  if (opts.view) q.set("view", opts.view)
  if (opts.date) q.set("date", opts.date)
  if (opts.layers) q.set("layers", opts.layers)
  const s = q.toString()
  return s ? `/calendar?${s}` : "/calendar"
}

/** Routines › Calendar views → the Calendar page (Day, 3 days and Year have no counterpart). */
export function calendarHrefFromRoutines(view: string | null, date: string | null): string {
  // The Routines tab opens on Month when no view is named.
  const mapped: CalendarPageView = view === "day" || view === "three-days" || view === "week" ? "week" : "month"
  return calendarHref({ view: mapped, date: parseCalendarDate(date) ? date : null, layers: "routine" })
}

/** Where an item's main action goes. */
export const routineHref = (slug: string) => `/routines?${new URLSearchParams({ slug, view: "plan" })}`
export const runHref = (slug: string, runId: string) => `/routines?${new URLSearchParams({ slug, run: runId })}`

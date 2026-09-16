import { routineRunPresentation } from "./routine-run-presentation"

// Pure grouping and filtering for the calendar's day agenda. Routines with
// one or two entries stay as rows; larger groups can be expanded. Month-cell
// truncation belongs to RoutineCalendar, which displays two entries plus overflow.

export interface CalendarEntry {
  id: string
  /** `planned` = a repeating schedule's next starts; `pending` = a one-time
   * start; `run` = a run that happened. */
  kind: "planned" | "pending" | "run"
  at: string
  slug: string
  name?: string
  inputs?: Record<string, unknown>
  status?: string
  outcome?: string
  pinned_version?: number | null
}

export type CalendarOutcome = "planned" | "completed" | "failed" | "waiting" | "running" | "stopped"

/** One colour per entry: blue for a planned start, otherwise how the run ended. */
export function calendarOutcome(entry: CalendarEntry): CalendarOutcome {
  if (entry.kind !== "run") return "planned"
  switch (routineRunPresentation(entry).tone) {
    case "success":
      return "completed"
    case "destructive":
      return "failed"
    case "warn":
      return "waiting"
    case "blue":
      return "running"
    default:
      return "stopped"
  }
}

export const CALENDAR_FILTERS = ["all", "planned", "ran", "waiting", "failed"] as const
export type CalendarFilter = (typeof CALENDAR_FILTERS)[number]
export const CALENDAR_FILTER_LABELS: Record<CalendarFilter, string> = {
  all: "All",
  planned: "Planned",
  ran: "Ran",
  waiting: "Waiting",
  failed: "Failed",
}

export function matchesCalendarFilter(entry: CalendarEntry, filter: CalendarFilter): boolean {
  if (filter === "all") return true
  if (filter === "ran") return entry.kind === "run"
  const outcome = calendarOutcome(entry)
  if (filter === "planned") return outcome === "planned"
  return outcome === filter
}

/** Chip counts are taken over the whole loaded range, never the filtered view. */
export function calendarFilterCounts(entries: CalendarEntry[]): Record<CalendarFilter, number> {
  const counts: Record<CalendarFilter, number> = { all: 0, planned: 0, ran: 0, waiting: 0, failed: 0 }
  for (const entry of entries) {
    counts.all++
    for (const filter of CALENDAR_FILTERS)
      if (filter !== "all" && matchesCalendarFilter(entry, filter)) counts[filter]++
  }
  return counts
}

/** "08:00" in the browser's zone — the calendar's days are local days. */
export function calendarClock(at: string): string {
  const date = new Date(at)
  if (Number.isNaN(date.getTime())) return "--:--"
  return date.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })
}

export function sortByTime(entries: CalendarEntry[]): CalendarEntry[] {
  return [...entries].sort((a, b) => Date.parse(a.at) - Date.parse(b.at))
}

/** "08:00–17:15", or a single clock when every entry shares it. */
export function timeRange(entries: CalendarEntry[]): string {
  const sorted = sortByTime(entries)
  if (!sorted.length) return ""
  const first = calendarClock(sorted[0].at)
  const last = calendarClock(sorted[sorted.length - 1].at)
  return first === last ? first : `${first}–${last}`
}

/** "every 15 min" when at least three planned starts are evenly spaced; null
 * otherwise — an irregular list gets no invented rhythm, and runs that already
 * happened promise nothing about the future. */
export function cadenceLabel(entries: CalendarEntry[]): string | null {
  const planned = sortByTime(entries.filter((e) => e.kind !== "run"))
  if (planned.length < 3) return null
  const gaps: number[] = []
  for (let i = 1; i < planned.length; i++)
    gaps.push(Math.round((Date.parse(planned[i].at) - Date.parse(planned[i - 1].at)) / 60_000))
  const gap = gaps[0]
  if (gap <= 0 || gaps.some((g) => Math.abs(g - gap) > 1)) return null
  if (gap < 60) return `every ${gap} min`
  if (gap % 60 === 0) return gap === 60 ? "every hour" : `every ${gap / 60} h`
  return null
}

export interface RoutineGroup {
  slug: string
  name: string
  /** Sorted by time. */
  entries: CalendarEntry[]
  first: string
  last: string
  allPlanned: boolean
  counts: { completed: number; failed: number; waiting: number }
  cadence: string | null
}

/** One group per routine, ordered by each routine's first entry. */
export function groupByRoutine(entries: CalendarEntry[]): RoutineGroup[] {
  const map = new Map<string, CalendarEntry[]>()
  for (const entry of sortByTime(entries)) {
    const list = map.get(entry.slug) ?? []
    list.push(entry)
    map.set(entry.slug, list)
  }
  return [...map.entries()].map(([slug, list]) => {
    const counts = { completed: 0, failed: 0, waiting: 0 }
    for (const entry of list) {
      const outcome = calendarOutcome(entry)
      if (outcome === "completed" || outcome === "failed" || outcome === "waiting") counts[outcome]++
    }
    return {
      slug,
      name: list.find((e) => e.name)?.name ?? slug,
      entries: list,
      first: calendarClock(list[0].at),
      last: calendarClock(list[list.length - 1].at),
      allPlanned: list.every((e) => e.kind !== "run"),
      counts,
      cadence: cadenceLabel(list),
    }
  })
}

/** "41 planned · 2 ran · 1 failed · 1 waiting" — zero counts are left out. */
export function daySummary(entries: CalendarEntry[]): string {
  let planned = 0,
    ran = 0,
    failed = 0,
    waiting = 0
  for (const entry of entries) {
    if (entry.kind === "run") ran++
    else planned++
    const outcome = calendarOutcome(entry)
    if (outcome === "failed") failed++
    if (outcome === "waiting") waiting++
  }
  return [
    planned ? `${planned} planned` : "",
    ran ? `${ran} ran` : "",
    failed ? `${failed} failed` : "",
    waiting ? `${waiting} waiting` : "",
  ]
    .filter(Boolean)
    .join(" · ")
}

export const AGENDA_COLLAPSE_ABOVE = 2

export type AgendaItem =
  | { kind: "entry"; entry: CalendarEntry; group: RoutineGroup }
  | { kind: "group"; group: RoutineGroup }

/** Routines with one or two entries stay as rows; more collapse into a group
 * the reader can expand. Order follows each routine's first entry. */
export function agendaItems(entries: CalendarEntry[]): AgendaItem[] {
  const items: AgendaItem[] = []
  for (const group of groupByRoutine(entries)) {
    if (group.entries.length > AGENDA_COLLAPSE_ABOVE) items.push({ kind: "group", group })
    else for (const entry of group.entries) items.push({ kind: "entry", entry, group })
  }
  return items
}

/** What version a planned entry will use, in the Plan's words. */
export function calendarVersionLabel(entry: CalendarEntry): string {
  if (entry.kind === "run") return ""
  if (entry.pinned_version) return `pinned v${entry.pinned_version}`
  if (entry.kind === "pending") return "one-time start · version live at dispatch"
  return "schedule · latest published"
}

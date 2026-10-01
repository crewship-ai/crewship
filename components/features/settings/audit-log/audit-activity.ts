/**
 * Audit activity — what the side panel's calendar, the histogram and the
 * facet counts read. Pure, framework-free.
 *
 * The server has no per-day summary, so the page reads a window of events
 * (useAuditActivity, a few pages at most) and tallies them here. Days are UTC
 * calendar days, the same days a custom range sends (audit-filters.ts).
 */

import { AUDIT_CATEGORIES, AUDIT_RESULTS, resultAction, type AuditFilters } from "./audit-filters"

export interface ActivityEvent {
  at: string
  action: string
  entityType: string
  userId: string | null
}

export interface DayTally {
  total: number
  completed: number
  failed: number
}

const DAY_MS = 86_400_000

export const utcDay = (d: Date) => d.toISOString().slice(0, 10)

export function addDays(day: string, n: number): string {
  return utcDay(new Date(Date.parse(`${day}T00:00:00Z`) + n * DAY_MS))
}

/** The last `n` days ending today, oldest first. */
export function lastDays(n: number, now: Date): string[] {
  const today = utcDay(now)
  return Array.from({ length: n }, (_, i) => addDays(today, i - n + 1))
}

/** How each day went: every event, and the runs that finished or failed. */
export function tallyByDay(events: ActivityEvent[]): Map<string, DayTally> {
  const out = new Map<string, DayTally>()
  for (const e of events) {
    const day = e.at.slice(0, 10)
    const t = out.get(day) ?? { total: 0, completed: 0, failed: 0 }
    t.total++
    if (e.action.endsWith(".failed")) t.failed++
    else if (e.action.endsWith(".completed")) t.completed++
    out.set(day, t)
  }
  return out
}

/** A month as a Monday-first grid: leading nulls, then every day. */
export function monthGrid(year: number, month: number): (string | null)[] {
  const first = new Date(Date.UTC(year, month, 1))
  const lead = (first.getUTCDay() + 6) % 7
  const days = new Date(Date.UTC(year, month + 1, 0)).getUTCDate()
  const cells: (string | null)[] = Array(lead).fill(null)
  for (let d = 1; d <= days; d++) cells.push(utcDay(new Date(Date.UTC(year, month, d))))
  return cells
}

/** Whether a day falls inside the bounds a range sends (from inclusive by day, to exclusive). */
export function dayInBounds(day: string, bounds: { from?: string; to?: string }): boolean {
  if (bounds.from && day < bounds.from.slice(0, 10)) return false
  if (bounds.to && day >= bounds.to.slice(0, 10)) return false
  return true
}

export interface FacetCounts {
  category: Record<string, number>
  person: Record<string, number>
  result: Record<string, number>
}

/**
 * Per-value counts for the side panel. Each facet is counted with every OTHER
 * filter applied and its own left open — the number next to "Failed" is how
 * many rows picking it would show.
 */
export function facetCounts(events: ActivityEvent[], filters: AuditFilters, bounds: { from?: string; to?: string }): FacetCounts {
  const inTime = events.filter((e) => (!bounds.from || e.at >= bounds.from) && (!bounds.to || e.at < bounds.to))
  const cat = (e: ActivityEvent) => filters.category === "all" || e.entityType === filters.category
  const person = (e: ActivityEvent) => !filters.userId || e.userId === filters.userId
  const result = (e: ActivityEvent) => !filters.result || e.action === resultAction(filters.result)

  const out: FacetCounts = { category: {}, person: {}, result: {} }
  const known = new Set<string>(AUDIT_CATEGORIES.map((c) => c.value))
  for (const e of inTime) {
    if (person(e) && result(e)) {
      out.category.all = (out.category.all ?? 0) + 1
      if (known.has(e.entityType)) out.category[e.entityType] = (out.category[e.entityType] ?? 0) + 1
    }
    if (cat(e) && result(e) && e.userId) out.person[e.userId] = (out.person[e.userId] ?? 0) + 1
    if (cat(e) && person(e)) {
      const r = AUDIT_RESULTS.find((x) => e.action === resultAction(x.value))
      if (r) out.result[r.value] = (out.result[r.value] ?? 0) + 1
    }
  }
  return out
}

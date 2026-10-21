import type { Pipeline } from "@/hooks/use-pipelines"
import type { CalendarEntry } from "@/lib/routine-calendar-groups"
import { routineItems } from "@/lib/calendar/model"

export const ROUTINES = [
  { slug: "scan", name: "Weekly scan", status: "active", author_user_id: "me", author_crew_id: "research", head_version: 4 },
  { slug: "triage", name: "Nightly triage", status: "active", author_user_id: "other", author_crew_id: "ops" },
] as Pipeline[]

/** A local instant as the API sends it (UTC RFC 3339). */
export const at = (y: number, m: number, d: number, h = 9, min = 0) => new Date(y, m - 1, d, h, min).toISOString()

export const entry = (e: Partial<CalendarEntry> & { id: string; at: string; slug: string }): CalendarEntry => ({ kind: "planned", ...e })

export const items = (events: CalendarEntry[]) => routineItems(events, ROUTINES, "me")

"use client"

import { useCallback, useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { apiWindows } from "@/lib/calendar/model"
import type { CalendarEntry } from "@/lib/routine-calendar-groups"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"

export interface CalendarEventsState {
  events: CalendarEntry[]
  loading: boolean
  error: boolean
  truncated: boolean
  reload: () => void
}

/** Schedules and one-time starts emit no realtime event, so the page polls. */
export const CALENDAR_POLL_MS = 60_000

/**
 * The routine calendar for a page range: one request per ≤ 31-day window
 * (a week or agenda is one, a month grid two). A failed window fails the
 * range — a half-loaded month never reads as an empty one.
 */
export function useCalendarEvents(workspaceId: string | null, from: Date, to: Date): CalendarEventsState {
  const fromKey = from.toISOString()
  const toKey = to.toISOString()
  const [events, setEvents] = useState<CalendarEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(false)
  const [truncated, setTruncated] = useState(false)
  const [revision, setRevision] = useState(0)
  const reload = useCallback(() => setRevision((r) => r + 1), [])

  // A changed range starts over; a reload of the same range keeps what is on
  // screen until the new answer arrives.
  useEffect(() => {
    setEvents([])
    setLoading(true)
  }, [workspaceId, fromKey, toKey])

  useEffect(() => {
    if (!workspaceId) return
    const controller = new AbortController()
    setError(false)
    void (async () => {
      try {
        const windows = apiWindows(new Date(fromKey), new Date(toKey))
        const pages = await Promise.all(
          windows.map(async (w) => {
            const qs = new URLSearchParams({ from: w.from.toISOString(), to: w.to.toISOString() })
            const res = await apiFetch(
              `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/calendar?${qs}`,
              { signal: controller.signal },
            )
            if (!res.ok) throw new Error(`calendar ${res.status}`)
            return (await res.json()) as { events?: CalendarEntry[]; truncated?: boolean }
          }),
        )
        if (controller.signal.aborted) return
        setEvents(pages.flatMap((p) => p.events ?? []))
        setTruncated(pages.some((p) => !!p.truncated))
      } catch {
        if (!controller.signal.aborted) setError(true)
      } finally {
        if (!controller.signal.aborted) setLoading(false)
      }
    })()
    return () => controller.abort()
  }, [workspaceId, fromKey, toKey, revision])

  useEffect(() => {
    const t = setInterval(reload, CALENDAR_POLL_MS)
    return () => clearInterval(t)
  }, [reload])
  useRealtimeEventSafe("pipeline.run.started", reload)
  useRealtimeEventSafe("pipeline.run.completed", reload)
  useRealtimeEventSafe("pipeline.run.failed", reload)
  useRealtimeEventSafe("pipeline.waitpoint.created", reload)
  useRealtimeEventSafe("pipeline.saved", reload)

  return { events, loading, error, truncated, reload }
}

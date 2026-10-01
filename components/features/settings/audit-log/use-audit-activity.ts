"use client"

import { useEffect, useState } from "react"

import { apiFetch } from "@/lib/api-fetch"

import { addDays, type ActivityEvent } from "./audit-activity"
import type { AuditSource } from "./audit-filters"

/** The server caps a page at 100 rows; five pages is plenty for a month of a
 *  normal workspace and bounded for a noisy one. */
const PAGE = 100
const MAX_PAGES = 5

export interface AuditActivity {
  events: ActivityEvent[]
  /** More events exist in the window than were read; tallies are a floor. */
  truncated: boolean
  loading: boolean
  /** Why the read stopped short, when it did. The events are then a floor. */
  error: string | null
}

/**
 * The events of one trail between two UTC days (`to` inclusive), read for
 * the calendar, the histogram and the facet counts. A read that fails part
 * way keeps what it got and is marked truncated with the reason: the panel
 * then treats its numbers as a floor, never as the whole trail (review R5).
 */
export function useAuditActivity(workspaceId: string | null, source: AuditSource, fromDay: string, toDay: string, enabled = true): AuditActivity {
  const [state, setState] = useState<AuditActivity>({ events: [], truncated: false, loading: true, error: null })

  useEffect(() => {
    if (!workspaceId || !enabled) return
    const controller = new AbortController()
    setState((s) => ({ ...s, loading: true }))
    ;(async () => {
      const events: ActivityEvent[] = []
      let truncated = false
      let error: string | null = null
      try {
        for (let page = 1; page <= MAX_PAGES; page++) {
          const params = new URLSearchParams({
            workspace_id: workspaceId, source, page: String(page), limit: String(PAGE),
            date_from: `${fromDay}T00:00:00.000Z`, date_to: `${addDays(toDay, 1)}T00:00:00.000Z`,
          })
          const res = await apiFetch(`/api/v1/audit?${params}`, { signal: controller.signal })
          if (!res.ok) {
            truncated = true
            error = `The audit trail could not be read in full (HTTP ${res.status})`
            break
          }
          const raw = await res.json()
          const rows: Record<string, unknown>[] = Array.isArray(raw?.data) ? raw.data : []
          for (const r of rows) {
            const user = r.user && typeof r.user === "object" ? (r.user as { id?: string }).id : undefined
            events.push({
              at: String(r.created_at ?? ""),
              action: String(r.action ?? ""),
              entityType: String(r.entity_type ?? ""),
              userId: (user || (r.user_id as string | null)) ?? null,
            })
          }
          const totalPages = Number(raw?.pagination?.total_pages ?? 1)
          if (page >= totalPages) break
          if (page === MAX_PAGES) truncated = true
        }
        if (!controller.signal.aborted) setState({ events, truncated, loading: false, error })
      } catch {
        if (!controller.signal.aborted) setState({ events, truncated: true, loading: false, error: "The audit trail could not be read" })
      }
    })()
    return () => controller.abort()
  }, [workspaceId, source, fromDay, toDay, enabled])

  return state
}

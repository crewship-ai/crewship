"use client"

import { useEffect, useMemo, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import type { ServerRoutineHealth } from "@/lib/routine-health"

/** GET /pipeline-health (ODCH-RT-07), by slug; null until it answers or when
 *  it cannot (the Health table then falls back to the runs the page loaded). */
export function useRoutineHealth(workspaceId: string): ReadonlyMap<string, ServerRoutineHealth> | null {
  const [rows, setRows] = useState<ServerRoutineHealth[] | null>(null)
  useEffect(() => {
    const controller = new AbortController()
    apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-health?days=30`, { signal: controller.signal })
      .then((r) => (r.ok ? r.json() : null))
      .then((body: { routines?: ServerRoutineHealth[] } | null) => {
        if (!controller.signal.aborted) setRows(Array.isArray(body?.routines) ? body!.routines! : null)
      })
      .catch(() => {})
    return () => controller.abort()
  }, [workspaceId])
  return useMemo(() => (rows ? new Map(rows.map((r) => [r.slug, r])) : null), [rows])
}

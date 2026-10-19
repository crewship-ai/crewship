"use client"

import { useEffect, useState } from "react"

import { apiFetch } from "@/lib/api-fetch"
import { QUICK_VIEWS, quickViewQuery, type QuickView } from "@/components/features/issues/issues-view-model"

export type QuickViewCounts = Record<QuickView, number | null>

const UNKNOWN = Object.fromEntries(QUICK_VIEWS.map((v) => [v.id, null])) as QuickViewCounts

/**
 * The count beside each Views row: the server's X-Total-Count for that view
 * (a one-row request each), so it is exact however many issues exist. A view
 * whose request fails stays null (shown as nothing, never as 0). `refreshKey`
 * re-reads the counts, e.g. when the list refetches after a live event.
 */
export function useQuickViewCounts(
  workspaceId: string | null | undefined,
  { crewId, missionType = "issue", refreshKey }: { crewId?: string | null; missionType?: string; refreshKey?: unknown },
): QuickViewCounts {
  const [counts, setCounts] = useState<QuickViewCounts>(UNKNOWN)
  useEffect(() => {
    if (!workspaceId) return
    let live = true
    const base =
      `/api/v1/issues?workspace_id=${encodeURIComponent(workspaceId)}&limit=1` +
      (crewId ? `&crew_id=${encodeURIComponent(crewId)}` : "") +
      (missionType === "mission" ? "&mission_type=mission" : "")
    void Promise.all(
      QUICK_VIEWS.map(async (v) => {
        try {
          const q = quickViewQuery(v.id)
          const res = await apiFetch(q ? `${base}&${q}` : base)
          if (!res.ok) return [v.id, null] as const
          const header = res.headers.get("X-Total-Count")
          return [v.id, header !== null && header !== "" && Number.isFinite(Number(header)) ? Number(header) : null] as const
        } catch {
          return [v.id, null] as const
        }
      }),
    ).then((entries) => {
      if (live) setCounts(Object.fromEntries(entries) as QuickViewCounts)
    })
    return () => {
      live = false
    }
  }, [workspaceId, crewId, missionType, refreshKey])
  return counts
}

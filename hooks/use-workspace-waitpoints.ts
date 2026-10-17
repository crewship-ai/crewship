"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import type { PendingWaitpoint } from "@/hooks/use-pending-approval"

// The pending approval waitpoints of a workspace, for the Routines overview's
// Needs you (routines.md R02). A decision is made by run id: the token is
// looked up here and only sent to POST …/waitpoints/{token}/approve (Manager
// and above on the server), never rendered.

export function useWorkspaceWaitpoints(workspaceId: string | null | undefined) {
  const [waitpoints, setWaitpoints] = useState<PendingWaitpoint[]>([])
  const req = useRef(0)
  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setWaitpoints([])
      return
    }
    const id = ++req.current
    try {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/waitpoints`)
      if (!res.ok) throw new Error(String(res.status))
      const body: unknown = await res.json()
      if (id === req.current) setWaitpoints(Array.isArray(body) ? (body as PendingWaitpoint[]) : [])
    } catch {
      if (id === req.current) setWaitpoints([])
    }
  }, [workspaceId])
  useEffect(() => {
    void refresh()
  }, [refresh])
  useRealtimeEventSafe("pipeline.waitpoint.created", refresh)
  useRealtimeEventSafe("inbox.updated", refresh)

  const decide = useCallback(
    async (runId: string, approved: boolean): Promise<boolean> => {
      const wp = waitpoints.find((w) => w.pipeline_run_id === runId && w.kind === "approval")
      if (!workspaceId || !wp) return false
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/waitpoints/${encodeURIComponent(wp.token)}/approve`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ approved, comment: "" }),
      })
      if (!res.ok) return false
      setWaitpoints((list) => list.filter((w) => w.token !== wp.token))
      void refresh()
      return true
    },
    [waitpoints, workspaceId, refresh],
  )
  return { waitpoints, decide, refresh }
}

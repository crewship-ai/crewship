"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEvent } from "@/hooks/use-realtime"

// usePendingApproval — resolves the approval waitpoint a SINGLE routine run is
// currently parked on, so the routine detail page can surface an inline
// "approve / reject" affordance right where the user clicked Run (instead of
// making them hunt through the workspace-wide Wait points tab or /inbox).
//
// The list endpoint (/pipelines/waitpoints) only returns *pending* waitpoints
// workspace-wide; we filter to this run client-side. Realtime events keep it
// fresh with no manual refresh: a new park (pipeline.waitpoint.created), a
// resume/finish (pipeline.run.completed/failed), or any inbox state change
// (inbox.updated — covers approve/reject landing from another surface or the
// timeout sweeper) all re-fetch.

export interface PendingWaitpoint {
  token: string
  pipeline_run_id: string
  step_id: string
  kind: string
  prompt: string
  invoking_crew_id?: string
  timeout_at: string
  created_at: string
}

interface UsePendingApprovalResult {
  /** The approval this run is parked on, or null when it isn't waiting. */
  waitpoint: PendingWaitpoint | null
  loading: boolean
  error: string | null
  /** True while an approve/reject request for this run is in flight. */
  deciding: boolean
  /** Approve or reject the parked run; optional comment becomes the payload. */
  decide: (approved: boolean, comment?: string) => Promise<boolean>
  refresh: () => Promise<void>
}

export function usePendingApproval(
  workspaceId: string | null | undefined,
  runId: string | null | undefined,
): UsePendingApprovalResult {
  const [waitpoint, setWaitpoint] = useState<PendingWaitpoint | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [deciding, setDeciding] = useState(false)
  // Guards against a stale response from a previous run overwriting the
  // current one when the user re-runs quickly.
  const reqIdRef = useRef(0)

  const refresh = useCallback(async () => {
    if (!workspaceId || !runId) {
      setWaitpoint(null)
      return
    }
    const reqId = ++reqIdRef.current
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/waitpoints`)
      if (reqIdRef.current !== reqId) return
      if (!res.ok) {
        // Don't collapse a 503 into "nothing pending". The list endpoint never
        // reports an empty state via 503 — it reads the waitpoints table
        // directly and returns 200 (possibly []) or 500. The only 503 that
        // reaches here is the one apiFetch synthesizes on a transient
        // session-refresh failure (X-Crewship-Refresh-Failed). Hiding a real
        // pending approval behind an auth/backend blip is worse than a
        // retryable error, so surface it like any other failure.
        throw new Error(`fetch waitpoints: ${res.status}`)
      }
      const data: PendingWaitpoint[] = await res.json()
      if (reqIdRef.current !== reqId) return
      const mine = Array.isArray(data)
        ? data.find((w) => w.pipeline_run_id === runId && w.kind === "approval") ?? null
        : null
      setWaitpoint(mine)
    } catch (e) {
      if (reqIdRef.current !== reqId) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (reqIdRef.current === reqId) setLoading(false)
    }
  }, [workspaceId, runId])

  // Invalidate the previous run's waitpoint the moment the tracked run (or
  // workspace) changes, BEFORE the new run's fetch resolves. Bumping reqId
  // discards any in-flight response from the prior run, and clearing the
  // waitpoint stops the inline Approve/Reject UI from briefly pointing at the
  // old run while run B's fetch is still outstanding.
  useEffect(() => {
    reqIdRef.current += 1
    setWaitpoint(null)
    setError(null)
    setLoading(false)
  }, [workspaceId, runId])

  useEffect(() => {
    void refresh()
  }, [refresh])

  useRealtimeEvent("pipeline.waitpoint.created", refresh)
  useRealtimeEvent("pipeline.run.completed", refresh)
  useRealtimeEvent("pipeline.run.failed", refresh)
  useRealtimeEvent("inbox.updated", refresh)

  const decide = useCallback(
    async (approved: boolean, comment?: string): Promise<boolean> => {
      if (!workspaceId || !waitpoint) return false
      setDeciding(true)
      try {
        const res = await apiFetch(
          `/api/v1/workspaces/${workspaceId}/pipelines/waitpoints/${waitpoint.token}/approve`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ approved, comment: comment ?? "" }),
          },
        )
        if (!res.ok) {
          const t = await res.text().catch(() => "")
          throw new Error(`${res.status}: ${t || res.statusText}`)
        }
        // Drop the banner immediately; the inbox.updated / run events that
        // follow re-confirm against the server.
        setWaitpoint(null)
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setDeciding(false)
      }
    },
    [workspaceId, waitpoint],
  )

  return { waitpoint, loading, error, deciding, decide, refresh }
}

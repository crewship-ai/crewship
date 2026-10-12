"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { pendingStartStatus, type PendingStart } from "@/lib/routine-pending-starts"

// usePendingStarts — accepted deferred starts of a workspace, including the
// ones that already left the queue (fired, failed, expired, cancelled), so a
// 202 never disappears from the page without saying what became of it.
//
// The dispatcher emits no event of its own, so the list polls on the
// dispatcher's 5s tick while anything still waits; a run starting or ending
// refreshes it too. The server returns at most 100 receipts, newest first.
const POLL_MS = 5_000

export function usePendingStarts(workspaceId: string | null | undefined) {
  const [starts, setStarts] = useState<PendingStart[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    abortRef.current?.abort()
    if (!workspaceId) {
      setStarts([])
      setError(null)
      return
    }
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/pending?status=all`, {
        signal: ctrl.signal,
      })
      if (ctrl.signal.aborted) return
      if (!res.ok) throw new Error("Could not load scheduled starts")
      const rows: PendingStart[] = (await res.json()) ?? []
      if (ctrl.signal.aborted) return
      setStarts(rows)
      setError(null)
    } catch (e) {
      if (ctrl.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (abortRef.current === ctrl) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    void refresh()
    return () => abortRef.current?.abort()
  }, [refresh])

  const waiting = starts.some((s) => pendingStartStatus(s) === "pending")
  useEffect(() => {
    if (!waiting) return
    const t = setInterval(() => void refresh(), POLL_MS)
    return () => clearInterval(t)
  }, [waiting, refresh])

  useRealtimeEvent("pipeline.run.started", refresh)
  useRealtimeEvent("pipeline.run.completed", refresh)
  useRealtimeEvent("pipeline.run.failed", refresh)

  return { starts, loading, error, refresh }
}

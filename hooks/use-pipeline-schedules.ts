"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

// PipelineSchedule mirrors the wire shape returned by the
// /pipeline-schedules endpoint family. We keep target_pipeline_slug
// as a separate field (not synthesised on the client) because the
// backend already resolves the slug for us — saves the UI from
// chasing pipeline IDs through a separate fetch.
export interface PipelineSchedule {
  id: string
  workspace_id: string
  name: string
  target_pipeline_id: string
  target_pipeline_slug?: string
  target_pipeline_version?: number
  cron_expr: string
  timezone: string
  inputs: Record<string, unknown>
  enabled: boolean
  last_run_at?: string
  last_status?: string
  last_run_id?: string
  next_run_at?: string
  created_at: string
  updated_at: string
}

export interface ScheduleSaveBody {
  name?: string
  target_pipeline_slug?: string
  target_pipeline_id?: string
  target_pipeline_version?: number
  cron_expr: string
  timezone?: string
  inputs?: Record<string, unknown>
  enabled?: boolean
}

// usePipelineSchedules fetches the workspace schedule list and
// exposes save/delete callbacks. Same stale-fetch + error-without-
// wipe ergonomics as usePipelines so a transient 5xx doesn't blank
// the table.
export function usePipelineSchedules(workspaceId: string | null | undefined) {
  const [schedules, setSchedules] = useState<PipelineSchedule[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setSchedules([])
      return
    }
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/pipeline-schedules`,
        { signal: controller.signal },
      )
      if (controller.signal.aborted) return
      if (!res.ok) {
        // 503 = backend not wired yet (scheduler skipped on test
        // server / build without DB). Treat as "no schedules" rather
        // than a hard error so the page still renders.
        if (res.status === 503) {
          setSchedules([])
          setLoading(false)
          return
        }
        setError(`pipeline schedules: ${res.status}`)
        setLoading(false)
        return
      }
      const data: PipelineSchedule[] = await res.json()
      if (controller.signal.aborted) return
      setSchedules(Array.isArray(data) ? data : [])
    } catch (e) {
      if (controller.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!controller.signal.aborted) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    refresh()
    return () => abortRef.current?.abort()
  }, [refresh])

  const create = useCallback(
    async (body: ScheduleSaveBody): Promise<PipelineSchedule | null> => {
      if (!workspaceId) return null
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/pipeline-schedules`,
        {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify(body),
        },
      )
      if (!res.ok) {
        const txt = await res.text()
        throw new Error(`create schedule failed: ${res.status} ${txt}`)
      }
      const out: PipelineSchedule = await res.json()
      await refresh()
      return out
    },
    [workspaceId, refresh],
  )

  const update = useCallback(
    async (id: string, body: ScheduleSaveBody): Promise<PipelineSchedule | null> => {
      if (!workspaceId) return null
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/pipeline-schedules/${id}`,
        {
          method: "PATCH",
          headers: { "content-type": "application/json" },
          body: JSON.stringify(body),
        },
      )
      if (!res.ok) {
        const txt = await res.text()
        throw new Error(`update schedule failed: ${res.status} ${txt}`)
      }
      const out: PipelineSchedule = await res.json()
      await refresh()
      return out
    },
    [workspaceId, refresh],
  )

  const remove = useCallback(
    async (id: string): Promise<void> => {
      if (!workspaceId) return
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/pipeline-schedules/${id}`,
        { method: "DELETE" },
      )
      if (!res.ok && res.status !== 404) {
        throw new Error(`delete schedule failed: ${res.status}`)
      }
      await refresh()
    },
    [workspaceId, refresh],
  )

  return { schedules, loading, error, refresh, create, update, remove }
}

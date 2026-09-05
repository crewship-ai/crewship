"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEvent } from "@/hooks/use-realtime"

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
  // Wake gate (v115) — when set, each cron tick first runs the referenced
  // agentless routine as a cheap probe; the target routine only fires when
  // the probe's output signals "wake" (see internal/pipeline/schedules.go).
  // wake_pipeline_slug is resolved server-side alongside the id so the UI
  // doesn't need a second lookup.
  wake_pipeline_id?: string
  wake_pipeline_slug?: string
  wake_fail_closed?: boolean
  wake_check_count?: number
  wake_fire_count?: number
  last_wake_at?: string
  last_wake_status?: string
  // Missed-run catch-up (#1422). catchup_policy is always populated
  // (defaults to "once" server-side); last_missed_count is telemetry from
  // the most recent tick, 0 meaning current or a fully-drained backlog.
  catchup_policy?: string
  last_missed_count?: number
  // Circuit breaker (#1405, F18/A6). consecutive_failures and
  // max_consecutive_failures are always present; disabled_reason is ""
  // for an operator-initiated disable and "circuit_breaker" once the
  // breaker has tripped it — see lib/schedule-health.ts, which turns
  // these four into the read-only status the schedules tab renders.
  consecutive_failures: number
  max_consecutive_failures: number
  disabled_reason?: string
  // "draft" for a trigger atomic routine authoring created with
  // activation="draft" (B8, #2359) and still awaiting MANAGER sign-off;
  // omitted for every ordinary schedule. The editor must not offer a plain
  // enable toggle for a draft — only the activate action clears it.
  activation?: "draft"
}

export interface ScheduleSaveBody {
  name?: string
  target_pipeline_slug?: string
  target_pipeline_id?: string
  // Pointer-shaped on the wire: undefined = omit the key (keep existing
  // pin on PATCH), null = explicit clear, a number = set/re-pin.
  target_pipeline_version?: number | null
  cron_expr?: string
  timezone?: string
  inputs?: Record<string, unknown>
  enabled?: boolean
  // Reliability surface (B9, §13.2). All optional and absent-keeps-
  // existing on PATCH, mirroring the server's merge semantics exactly.
  catchup_policy?: "skip" | "once" | "all"
  max_consecutive_failures?: number
  wake_pipeline_slug?: string
  wake_pipeline_id?: string
  wake_inputs?: Record<string, unknown>
  wake_fail_closed?: boolean
}

// SchedulePreview is the wire shape of GET
// /pipeline-schedules/preview (B9, #2362, §13.2 "When").
export interface SchedulePreview {
  cron_expr: string
  timezone: string
  occurrences: string[]
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

  // preview computes the next N fire times for a cron/timezone pair
  // WITHOUT saving anything (B9, §13.2 "When") — the editor calls this on
  // every keystroke while cron/timezone are still being drafted, so it
  // takes them as plain arguments rather than reading from state.
  const preview = useCallback(
    async (cronExpr: string, timezone: string, count = 5): Promise<SchedulePreview> => {
      if (!workspaceId) throw new Error("no workspace")
      const params = new URLSearchParams({ cron_expr: cronExpr, timezone, count: String(count) })
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/pipeline-schedules/preview?${params.toString()}`,
      )
      if (!res.ok) {
        const txt = await res.text()
        throw new Error(`preview failed: ${res.status} ${txt}`)
      }
      return res.json()
    },
    [workspaceId],
  )

  // next_run_at moves the moment a schedule fires, so a run starting is
  // exactly when "Firing next" goes stale. Without this the upcoming
  // list kept naming a time that had already passed until someone
  // reloaded the page.
  useRealtimeEvent("pipeline.run.started", refresh)
  useRealtimeEvent("pipeline.run.completed", refresh)
  useRealtimeEvent("pipeline.saved", refresh)

  return { schedules, loading, error, refresh, create, update, remove, preview }
}

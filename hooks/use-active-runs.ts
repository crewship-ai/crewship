"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRealtimeEvent } from "@/hooks/use-realtime"

// useActiveRuns — the data behind the global Activity Bar: a live list of
// runs currently in flight across the workspace, both agent runs (issue
// "Start" / assignments) and routine (pipeline) runs.
//
// Server is the source of truth: we poll the two "active runs" endpoints and
// treat every relevant workspace WS event as an invalidation that triggers an
// immediate refetch. This mirrors usePipelineRuns / use-dashboard-data and is
// far more robust than accumulating WS deltas — agent runs broadcast
// `assignment.updated` (not a workspace `run.started`), runs can finish before
// a delta is processed, and a dropped frame would otherwise leave the bar
// wrong until reload. Polling + WS-as-accelerator can't drift.

export type ActiveRunKind = "agent" | "routine"

export interface ActiveRunItem {
  id: string
  kind: ActiveRunKind
  /** Display name — agent name or routine slug. */
  label: string
  /** Optional second line (trigger, current step…). */
  sublabel?: string
  /** ISO start time for ordering + elapsed display. */
  startedAt?: string
  /** Where clicking the row should navigate. */
  href: string
}

const AGENT_HREF = "/activity"
const ROUTINE_HREF = "/routines"
// Steady safety poll. WS events make updates near-instant; this only backstops
// dropped frames / runs whose start event we never saw.
const POLL_MS = 6000

function str(o: Record<string, unknown> | undefined, ...keys: string[]): string | undefined {
  if (!o) return undefined
  for (const k of keys) {
    const v = o[k]
    if (typeof v === "string" && v) return v
  }
  return undefined
}

export function useActiveRuns(workspaceId: string | null | undefined) {
  const [runs, setRuns] = useState<ActiveRunItem[]>([])
  const [loading, setLoading] = useState(false)
  const inFlight = useRef(false)
  const debounce = useRef<ReturnType<typeof setTimeout> | null>(null)

  const fetchActive = useCallback(async () => {
    if (!workspaceId || inFlight.current) return
    inFlight.current = true
    setLoading(true)
    try {
      const [agentRes, routineRes] = await Promise.allSettled([
        fetch(`/api/v1/runs?workspace_id=${encodeURIComponent(workspaceId)}&status=RUNNING&limit=50`),
        fetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/runs/active`),
      ])

      const next: ActiveRunItem[] = []

      if (agentRes.status === "fulfilled" && agentRes.value.ok) {
        const json = await agentRes.value.json().catch(() => null)
        const rows: unknown[] = Array.isArray(json?.data) ? json.data : []
        for (const raw of rows) {
          const row = raw as Record<string, unknown>
          // Defensive: only keep genuinely-running rows even if the server
          // ignored the status filter.
          const status = (str(row, "status") ?? "").toUpperCase()
          if (status && status !== "RUNNING" && status !== "QUEUED") continue
          const id = str(row, "id")
          if (!id) continue
          next.push({
            id,
            kind: "agent",
            label: str(row, "agent_name", "agent_slug", "agent_id") ?? "Agent run",
            sublabel: str(row, "trigger_type"),
            startedAt: str(row, "started_at", "created_at"),
            href: AGENT_HREF,
          })
        }
      }

      if (routineRes.status === "fulfilled" && routineRes.value.ok) {
        const json = await routineRes.value.json().catch(() => null)
        const rows: unknown[] = Array.isArray(json) ? json : Array.isArray(json?.rows) ? json.rows : []
        for (const raw of rows) {
          const row = raw as Record<string, unknown>
          const id = str(row, "id", "run_id")
          if (!id) continue
          next.push({
            id,
            kind: "routine",
            label: str(row, "pipeline_name", "pipeline_slug") ?? "Routine run",
            sublabel: str(row, "current_step_id"),
            startedAt: str(row, "started_at"),
            href: ROUTINE_HREF,
          })
        }
      }

      next.sort((a, b) => (b.startedAt ?? "").localeCompare(a.startedAt ?? ""))
      setRuns(next)
    } finally {
      inFlight.current = false
      setLoading(false)
    }
  }, [workspaceId])

  // Coalesce bursts of WS events into one refetch shortly after.
  const invalidate = useCallback(() => {
    if (debounce.current) clearTimeout(debounce.current)
    debounce.current = setTimeout(() => {
      void fetchActive()
    }, 400)
  }, [fetchActive])

  // Initial load + steady safety poll.
  useEffect(() => {
    if (!workspaceId) {
      setRuns([])
      return
    }
    void fetchActive()
    const t = setInterval(() => void fetchActive(), POLL_MS)
    return () => {
      clearInterval(t)
      if (debounce.current) clearTimeout(debounce.current)
    }
  }, [workspaceId, fetchActive])

  // WS events that imply a run started/changed/ended — each just nudges a
  // refetch. Agent runs surface via assignment.updated (workspace channel),
  // routine runs via pipeline.run.* / pipeline.step.started.
  useRealtimeEvent("assignment.updated", invalidate)
  useRealtimeEvent("run.started", invalidate)
  useRealtimeEvent("run.completed", invalidate)
  useRealtimeEvent("run.failed", invalidate)
  useRealtimeEvent("mission.updated", invalidate)
  useRealtimeEvent("pipeline.run.started", invalidate)
  useRealtimeEvent("pipeline.run.completed", invalidate)
  useRealtimeEvent("pipeline.run.failed", invalidate)
  useRealtimeEvent("pipeline.step.started", invalidate)

  return { runs, count: runs.length, loading }
}

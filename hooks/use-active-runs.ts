"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"

// useActiveRuns — the data behind the global Activity Bar: a live list of
// runs currently in flight across the workspace, both agent runs (issue
// "Start" / assignments) and routine (pipeline) runs.
//
// Agent runs and pipeline runs have different data models (see lib/run-activity
// notes), so we seed from both their endpoints on mount and then keep the set
// fresh from realtime: *.started adds, terminal events remove. Keyed by run id.

export type ActiveRunKind = "agent" | "routine"

export interface ActiveRunItem {
  /** run_id (agent) / pipeline run_id (routine). Unique within the set. */
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

function str(o: Record<string, unknown> | undefined, ...keys: string[]): string | undefined {
  if (!o) return undefined
  for (const k of keys) {
    const v = o[k]
    if (typeof v === "string" && v) return v
  }
  return undefined
}

export function useActiveRuns(workspaceId: string | null | undefined) {
  // Keyed map so add/remove from realtime is O(1) and dedupes against seed.
  const [items, setItems] = useState<Map<string, ActiveRunItem>>(new Map())
  const [loading, setLoading] = useState(false)
  const wsRef = useRef(workspaceId)
  wsRef.current = workspaceId

  const upsert = useCallback((item: ActiveRunItem) => {
    setItems((prev) => {
      const next = new Map(prev)
      next.set(item.id, { ...next.get(item.id), ...item })
      return next
    })
  }, [])

  const remove = useCallback((id: string | undefined) => {
    if (!id) return
    setItems((prev) => {
      if (!prev.has(id)) return prev
      const next = new Map(prev)
      next.delete(id)
      return next
    })
  }, [])

  // Seed from both run sources on workspace change. Failures are tolerated —
  // realtime still populates runs that start while the page is open.
  useEffect(() => {
    if (!workspaceId) {
      setItems(new Map())
      return
    }
    let cancelled = false
    setLoading(true)
    const seed = new Map<string, ActiveRunItem>()

    const agents = fetch(
      `/api/v1/runs?workspace_id=${encodeURIComponent(workspaceId)}&status=RUNNING&limit=50`,
    )
      .then((r) => (r.ok ? r.json() : null))
      .then((json) => {
        const rows: unknown[] = Array.isArray(json?.data) ? json.data : []
        for (const raw of rows) {
          const row = raw as Record<string, unknown>
          const id = str(row, "id")
          if (!id) continue
          seed.set(id, {
            id,
            kind: "agent",
            label: str(row, "agent_name", "agent_slug", "agent_id") ?? "Agent run",
            sublabel: str(row, "trigger_type"),
            startedAt: str(row, "started_at", "created_at"),
            href: AGENT_HREF,
          })
        }
      })
      .catch(() => {})

    const routines = fetch(
      `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/runs/active`,
    )
      .then((r) => (r.ok ? r.json() : null))
      .then((json) => {
        const rows: unknown[] = Array.isArray(json) ? json : Array.isArray(json?.rows) ? json.rows : []
        for (const raw of rows) {
          const row = raw as Record<string, unknown>
          const id = str(row, "id", "run_id")
          if (!id) continue
          seed.set(id, {
            id,
            kind: "routine",
            label: str(row, "pipeline_name", "pipeline_slug") ?? "Routine run",
            sublabel: str(row, "current_step_id"),
            startedAt: str(row, "started_at"),
            href: ROUTINE_HREF,
          })
        }
      })
      .catch(() => {})

    Promise.all([agents, routines]).then(() => {
      if (cancelled) return
      setItems(seed)
      setLoading(false)
    })

    return () => {
      cancelled = true
    }
  }, [workspaceId])

  // ---- realtime: keep the set in sync ----
  useRealtimeEvent(
    "run.started",
    useCallback(
      (e: RealtimeEvent) => {
        const p = e.payload as Record<string, unknown>
        const id = str(p, "run_id")
        if (!id) return
        upsert({
          id,
          kind: "agent",
          label: str(p, "agent_name", "agent_id") ?? "Agent run",
          startedAt: new Date().toISOString(),
          href: AGENT_HREF,
        })
      },
      [upsert],
    ),
  )
  const onAgentDone = useCallback(
    (e: RealtimeEvent) => remove(str(e.payload as Record<string, unknown>, "run_id")),
    [remove],
  )
  useRealtimeEvent("run.completed", onAgentDone)
  useRealtimeEvent("run.failed", onAgentDone)

  useRealtimeEvent(
    "pipeline.run.started",
    useCallback(
      (e: RealtimeEvent) => {
        const p = e.payload as Record<string, unknown>
        const id = str(p, "run_id")
        if (!id) return
        upsert({
          id,
          kind: "routine",
          label: str(p, "pipeline_slug") ?? "Routine run",
          startedAt: new Date().toISOString(),
          href: ROUTINE_HREF,
        })
      },
      [upsert],
    ),
  )
  const onRoutineDone = useCallback(
    (e: RealtimeEvent) => remove(str(e.payload as Record<string, unknown>, "run_id")),
    [remove],
  )
  useRealtimeEvent("pipeline.run.completed", onRoutineDone)
  useRealtimeEvent("pipeline.run.failed", onRoutineDone)

  // Newest first.
  const runs = Array.from(items.values()).sort((a, b) =>
    (b.startedAt ?? "").localeCompare(a.startedAt ?? ""),
  )

  return { runs, count: runs.length, loading }
}

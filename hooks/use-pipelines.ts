"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"

// Pipeline mirrors the wire shape returned by GET
// /api/v1/workspaces/{ws}/pipelines (list endpoint, no definition).
//
// We keep this lean — the list view never needs the full DSL JSON.
// The detail side-sheet, when wired, fetches the dedicated GET
// /pipelines/{slug} endpoint and decodes the definition there.
export interface Pipeline {
  id: string
  slug: string
  name: string
  description?: string
  dsl_version: string
  definition_hash: string
  ephemeral: boolean
  workspace_visible: boolean
  invocation_count: number
  last_invoked_at?: string
  last_invocation_status?: string
  // Lifecycle status: "active" (normal/runnable), "proposed" (risky /
  // agent-authored, awaiting MANAGER+ approval), "disabled" (killed by
  // OWNER/ADMIN). Absent on older payloads → treated as "active".
  status?: "active" | "proposed" | "disabled"
  author_crew_id?: string
  author_agent_id?: string
  // author_agent_name is denormalized server-side from the agents
  // table so the routines list can render a human label without a
  // second fetch. Empty when author_agent_id is empty or the agent
  // was deleted.
  author_agent_name?: string
  author_user_id?: string
  authored_via: "agent_tool_call" | "user_api" | "imported" | "seed"
  created_at: string
  updated_at: string
  // linked_issue_count: how many issues bind this routine via
  // missions.routine_id. linked_issues holds up to 3 recent issue
  // identifiers (e.g. ["ENG-12","ENG-9","ENG-7"]) so the catalog
  // row can render a chip like "ENG-12 +2".
  linked_issue_count?: number
  linked_issues?: string[]
}

// PipelineRunSummary is the shape ListRuns returns — a journal entry
// flattened with the pipeline_id and run_id fields surfaced. Matches
// the per-run cards rendered in the Graph view.
export interface PipelineRunSummary {
  id: string
  ts: string
  entry_type: string
  severity: string
  summary: string
  pipeline_id?: string
  run_id?: string
  payload?: unknown
}

// usePipelines fetches the workspace's pipeline list and exposes a
// refresh callback. Errors are surfaced as a state field rather than
// thrown; the Graph view treats them as "no pipelines available" and
// renders the existing empty state instead of crashing.
export function usePipelines(workspaceId: string | null | undefined) {
  const [pipelines, setPipelines] = useState<Pipeline[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Track the in-flight controller so a workspace switch (or rapid
  // refresh) can cancel the previous request. Without this, an
  // older response could resolve last and overwrite state for the
  // newer workspace — visible as the wrong pipelines list flashing
  // when the user switches contexts.
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setPipelines([])
      return
    }
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines`, {
        signal: controller.signal,
      })
      if (controller.signal.aborted) return
      if (!res.ok) {
        // 4xx/5xx — keep prior list rather than wiping it; just
        // surface the error in case the caller wants to render a
        // banner. Wiping on every transient error makes the graph
        // flicker.
        setError(`pipelines list: ${res.status}`)
        setLoading(false)
        return
      }
      const data: Pipeline[] = await res.json()
      if (controller.signal.aborted) return
      setPipelines(Array.isArray(data) ? data : [])
    } catch (e) {
      if (controller.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!controller.signal.aborted) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    refresh()
    return () => {
      abortRef.current?.abort()
    }
  }, [refresh])

  // Live refresh on pipeline events. Pipeline saves and runs change
  // invocation_count + last_invocation_status, which the registry
  // row in the Graph view needs to render fresh data without a
  // page reload. Cheap because the list endpoint is small + cached.
  useRealtimeEvent("pipeline.run.completed", refresh)
  useRealtimeEvent("pipeline.run.failed", refresh)

  return { pipelines, loading, error, refresh }
}

// usePipelineRuns fetches the journal-backed run history for one
// pipeline by slug. Used by the run detail side-sheet (when wired)
// and by the Graph view when a pipeline node is clicked.
export function usePipelineRuns(workspaceId: string | null | undefined, slug: string | null) {
  const [runs, setRuns] = useState<PipelineRunSummary[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Same stale-fetch guard as usePipelines — switching slugs
  // rapidly must not let an older response overwrite state for
  // the newer slug.
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId || !slug) {
      setRuns([])
      return
    }
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller
    setLoading(true)
    setError(null)
    try {
      // include_steps=1 widens the response to include pipeline.step.*
      // events alongside pipeline.run.* — the Runs sub-tab uses the
      // step entries to render the waterfall timeline when a run is
      // expanded. Server caps the LIMIT regardless so payload stays
      // bounded (50 entries ≈ 5 runs × 11-event lifecycle).
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/pipelines/${slug}/runs?limit=50&include_steps=1`,
        { signal: controller.signal },
      )
      if (controller.signal.aborted) return
      if (!res.ok) {
        setError(`pipeline runs: ${res.status}`)
        setLoading(false)
        return
      }
      const data: PipelineRunSummary[] = await res.json()
      if (controller.signal.aborted) return
      setRuns(Array.isArray(data) ? data : [])
    } catch (e) {
      if (controller.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!controller.signal.aborted) setLoading(false)
    }
  }, [workspaceId, slug])

  useEffect(() => {
    refresh()
    return () => {
      abortRef.current?.abort()
    }
  }, [refresh])

  return { runs, loading, error, refresh }
}

"use client"

import { useCallback, useEffect, useState } from "react"

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
  author_crew_id?: string
  author_agent_id?: string
  author_user_id?: string
  authored_via: "agent_tool_call" | "user_api" | "imported" | "seed"
  created_at: string
  updated_at: string
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

  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setPipelines([])
      return
    }
    setLoading(true)
    setError(null)
    try {
      const res = await fetch(`/api/v1/workspaces/${workspaceId}/pipelines`)
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
      setPipelines(Array.isArray(data) ? data : [])
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    refresh()
  }, [refresh])

  return { pipelines, loading, error, refresh }
}

// usePipelineRuns fetches the journal-backed run history for one
// pipeline by slug. Used by the run detail side-sheet (when wired)
// and by the Graph view when a pipeline node is clicked.
export function usePipelineRuns(workspaceId: string | null | undefined, slug: string | null) {
  const [runs, setRuns] = useState<PipelineRunSummary[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId || !slug) {
      setRuns([])
      return
    }
    setLoading(true)
    setError(null)
    try {
      const res = await fetch(
        `/api/v1/workspaces/${workspaceId}/pipelines/${slug}/runs?limit=50`,
      )
      if (!res.ok) {
        setError(`pipeline runs: ${res.status}`)
        setLoading(false)
        return
      }
      const data: PipelineRunSummary[] = await res.json()
      setRuns(Array.isArray(data) ? data : [])
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [workspaceId, slug])

  useEffect(() => {
    refresh()
  }, [refresh])

  return { runs, loading, error, refresh }
}

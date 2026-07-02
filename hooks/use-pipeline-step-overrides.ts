"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

// PipelineStepOverride mirrors one row of the wire shape returned by
// GET /api/v1/workspaces/{ws}/pipelines/{slug}/overrides — the v121
// runtime override layer that lets an operator tweak a single step's
// prompt or model tier without bumping the routine version (applied at
// run start, over the versioned DSL). See
// internal/api/pipeline_step_overrides.go.
export interface PipelineStepOverride {
  step_id: string
  prompt?: string
  model_override?: string
}

// usePipelineStepOverrides fetches the routine's active per-step
// overrides. Read-only — this dashboard surface only visualizes which
// steps carry an override; setting/clearing one still goes through the
// existing PUT/DELETE endpoints via other tooling (agent/CLI).
// Best-effort: any fetch failure just resolves to an empty list so a
// missing/legacy deployment doesn't block the rest of the routine
// detail from rendering.
export function usePipelineStepOverrides(
  workspaceId: string | null | undefined,
  slug: string | null | undefined,
) {
  const [overrides, setOverrides] = useState<PipelineStepOverride[]>([])
  const [loading, setLoading] = useState(false)
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId || !slug) {
      setOverrides([])
      return
    }
    abortRef.current?.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(slug)}/overrides`,
        { signal: ctrl.signal },
      )
      if (ctrl.signal.aborted) return
      if (!res.ok) {
        setOverrides([])
        return
      }
      const data = await res.json().catch(() => null)
      if (ctrl.signal.aborted) return
      const list = data && Array.isArray(data.overrides) ? (data.overrides as PipelineStepOverride[]) : []
      setOverrides(list)
    } catch {
      if (!ctrl.signal.aborted) setOverrides([])
    } finally {
      if (!ctrl.signal.aborted) setLoading(false)
    }
  }, [workspaceId, slug])

  useEffect(() => {
    refresh()
    return () => abortRef.current?.abort()
  }, [refresh])

  return { overrides, loading, refresh }
}

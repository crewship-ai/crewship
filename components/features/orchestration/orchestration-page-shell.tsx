"use client"

import { useCallback, useEffect, useState } from "react"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { PageLoading } from "@/components/layout/page-state"
import { OrchestrationLayout } from "@/components/features/orchestration/orchestration-layout"
import { apiFetch } from "@/lib/api-fetch"
import type { CrewSummary } from "@/lib/types/orchestration"

/**
 * /issues data shell: the workspace and its crews (the New issue composer and
 * the Crew facet need them). The issues themselves, their realtime updates
 * and the panel's lookups live in OrchestrationLayout.
 *
 * It used to also fetch missions with their tasks, agents and crew
 * connections — and poll missions every 3 s — for the Graph, Timeline and
 * the bottom drawer the old orchestration container drew. Those live on
 * Activity now, so the Issues page no longer pays for them.
 */
export function OrchestrationPageShell() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [crews, setCrews] = useState<CrewSummary[]>([])
  const [loading, setLoading] = useState(true)
  // A failed fetch is not an empty workspace (S6): it is said, with a retry.
  const [loadError, setLoadError] = useState<string | null>(null)

  const fetchData = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await apiFetch(`/api/v1/crews?workspace_id=${workspaceId}`)
      if (res.ok) setCrews(await res.json())
      setLoadError(res.ok ? null : `Could not load crews (HTTP ${res.status})`)
    } catch (e) {
      setLoadError(e instanceof Error ? e.message : "Could not reach the server")
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => { void fetchData() }, [fetchData])
  // A dropped socket may have missed a crew change.
  useRealtimeEvent("realtime.reconnected", useCallback(() => void fetchData(), [fetchData]))

  if (loading || wsLoading || !workspaceId) return <PageLoading variant="list" />

  return <OrchestrationLayout crews={crews} workspaceId={workspaceId} onRefresh={fetchData} loadError={loadError} />
}

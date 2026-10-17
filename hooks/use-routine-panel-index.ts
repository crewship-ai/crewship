"use client"

import { useEffect, useMemo, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { usePipelineSchedules } from "@/hooks/use-pipeline-schedules"
import { usePipelineWebhooks } from "@/hooks/use-pipeline-webhooks"
import { useBudgetSummary } from "@/hooks/use-routine-budget"
import type { Pipeline } from "@/hooks/use-pipelines"
import { buildRoutinePanelIndex, type RoutinePanelIndex } from "@/lib/routine-panel-index"

/** Ids of the slash-command catalog (`routine.run:<slug>` for a routine's
 *  chat command). Fetched directly: the panel must not depend on a query
 *  client being mounted. A refusal or an error leaves the list empty. */
function useChatCommandIds(workspaceId: string | null | undefined): string[] {
  const [ids, setIds] = useState<string[]>([])
  useEffect(() => {
    if (!workspaceId) return
    const controller = new AbortController()
    apiFetch(`/api/v1/slash-commands?workspace_id=${encodeURIComponent(workspaceId)}`, { signal: controller.signal })
      .then((r) => (r.ok ? r.json() : []))
      .then((body: unknown) => {
        if (controller.signal.aborted) return
        const list = Array.isArray(body) ? body : Array.isArray((body as { data?: unknown })?.data) ? (body as { data: unknown[] }).data : []
        setIds(list.map((c) => (c && typeof c === "object" && typeof (c as { id?: unknown }).id === "string" ? (c as { id: string }).id : "")).filter(Boolean))
      })
      .catch(() => {})
    return () => controller.abort()
  }, [workspaceId])
  return ids
}

/** Triggers, next start and spend for the Routines panel, composed from the
 *  schedule, webhook, budget and chat command lists (lib/routine-panel-index). */
export function useRoutinePanelIndex(workspaceId: string, routines: Pipeline[]): RoutinePanelIndex {
  const { schedules } = usePipelineSchedules(workspaceId)
  const { webhooks } = usePipelineWebhooks(workspaceId)
  const { summary } = useBudgetSummary(workspaceId)
  const chatCommandIds = useChatCommandIds(workspaceId)
  return useMemo(
    () => buildRoutinePanelIndex({ routines, schedules, webhooks, chatCommandIds, spend: summary }),
    [routines, schedules, webhooks, chatCommandIds, summary],
  )
}

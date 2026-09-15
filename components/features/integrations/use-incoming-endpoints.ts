"use client"
import { useCallback } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { usePipelineWebhooks } from "@/hooks/use-pipeline-webhooks"
import { usePipelines } from "@/hooks/use-pipelines"
import { usePages } from "@/hooks/use-pages"
import { incomingRows, type IncomingTarget } from "./incoming-model"

export async function incomingJSON<T>(
  path: string,
  init?: RequestInit,
): Promise<T> {
  const res = await apiFetch(path, init)
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    throw new Error(
      body?.detail ?? body?.error ?? `Request failed (${res.status})`,
    )
  }
  if (res.status === 204) return undefined as T
  return res.json()
}
export function useIncomingEndpoints(workspaceId: string, enabled: boolean) {
  const client = useQueryClient()
  const hooks = usePipelineWebhooks(enabled ? workspaceId : null)
  const routines = usePipelines(enabled ? workspaceId : null)
  const pages = usePages(enabled ? workspaceId : null)
  const agents = useQuery({
    queryKey: ["incoming-webhook-agents", workspaceId],
    enabled,
    queryFn: ({ signal }) =>
      incomingJSON<IncomingTarget[]>(
        `/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`,
        { signal },
      ),
  })
  const targets: IncomingTarget[] = [
    ...routines.pipelines.map((t) => ({
      id: t.id,
      name: t.name,
      slug: t.slug,
      kind: "routine" as const,
    })),
    ...(agents.data ?? []).map((t) => ({ ...t, kind: "agent" as const })),
    ...pages.pages.map((t) => ({
      id: t.id,
      name: t.name,
      slug: t.slug,
      kind: "page" as const,
    })),
  ]
  const refreshHooks = hooks.refresh
  const refreshRoutines = routines.refresh
  const refreshPages = pages.refresh
  const refresh = useCallback(() => {
    void refreshHooks()
    void refreshRoutines()
    void refreshPages()
    void client.invalidateQueries({
      queryKey: ["incoming-webhook-agents", workspaceId],
    })
    void client.invalidateQueries({ queryKey: ["page-webhooks", workspaceId] })
    void client.invalidateQueries({
      queryKey: ["incoming-receipts", workspaceId],
    })
  }, [refreshHooks, refreshRoutines, refreshPages, client, workspaceId])
  return {
    targets,
    rows: incomingRows(targets, hooks.webhooks),
    hooks,
    refresh,
    loading:
      enabled &&
      (hooks.loading || routines.loading || pages.loading || agents.isPending),
    error:
      hooks.error ??
      routines.error ??
      pages.error ??
      agents.error?.message ??
      null,
  }
}
export type IncomingData = ReturnType<typeof useIncomingEndpoints>

"use client"

import { useCallback } from "react"
import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"

interface ProjectRevision { revision: number; digest: string; git_commit: string; actor?: string; created_at: string; restorable: boolean }
interface ProjectHistory { revisions: ProjectRevision[]; next_before: number }

export function usePageProjectHistory(workspace: string, slug: string) {
  const client = useQueryClient()
  const key = ["page-project-history", workspace, slug] as const
  const invalidate = useCallback(() => { void client.invalidateQueries({ queryKey: ["page-project-history", workspace, slug] }) }, [client, workspace, slug])
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("page.deleted", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)
  const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}/project`
  const query = useInfiniteQuery({
    queryKey: key, initialPageParam: 0, retry: false, gcTime: 0,
    queryFn: async ({ pageParam, signal }): Promise<ProjectHistory> => {
      const params = new URLSearchParams({ workspace_id: workspace })
      if (pageParam) params.set("before", String(pageParam))
      const response = await apiFetch(`${endpoint}/history?${params}`, { signal })
      if (!response.ok) { const body = await response.json().catch(() => null); throw new Error(body?.error ?? "Could not load source history.") }
      return response.json()
    },
    getNextPageParam: page => page.next_before || undefined,
  })
  const restore = useMutation({
    mutationFn: async ({ revision, expectedRevision }: { revision: number; expectedRevision: number }) => {
      const params = new URLSearchParams({ workspace_id: workspace })
      const response = await apiFetch(`${endpoint}/restore?${params}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ revision, expected_revision: expectedRevision }) })
      if (!response.ok) { const body = await response.json().catch(() => null); throw new Error(body?.error ?? "Could not restore source revision.") }
      return response.json()
    },
    onSettled: () => {
      invalidate()
      void client.invalidateQueries({ queryKey: ["page-preview", workspace, slug] })
    },
  })
  return { query, restore }
}

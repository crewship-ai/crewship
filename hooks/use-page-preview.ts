"use client"

import { useCallback } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"

export interface PreviewArtifact {
  format: "crewship-page-preview/v1"
  javascript: string
  css: string
  toolchain: string
}
export interface PreviewBuild {
  id: string
  source_revision: number
  state: "running" | "ready" | "failed" | "interrupted"
  error?: string
}
export interface PagePreview {
  development_same_origin?: boolean
  runtime_url: string
  revision: number
  build: PreviewBuild | null
  artifact?: PreviewArtifact
}
export const pagePreviewKeys = { detail: (workspace: string, slug: string) => ["page-preview", workspace, slug] as const }

export function usePagePreview(workspace: string, slug: string) {
  const client = useQueryClient()
  const key = pagePreviewKeys.detail(workspace, slug)
  const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}/project`
  const params = new URLSearchParams({ workspace_id: workspace })
  const invalidate = useCallback(() => { void client.invalidateQueries({ queryKey: pagePreviewKeys.detail(workspace, slug) }) }, [client, workspace, slug])
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("page.deleted", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)
  const query = useQuery({
    queryKey: key,
    queryFn: async ({ signal }): Promise<PagePreview> => {
      const response = await apiFetch(`${endpoint}/preview?${params}`, { signal })
      if (response.status === 404) throw new Error("This Page has no application draft yet. Ask an agent to create one.")
      if (response.status === 503) throw new Error("Application previews are not enabled on this installation.")
      if (response.status === 403) throw new Error("You need permission to edit this Page to open its application preview.")
      if (!response.ok) throw new Error("Could not load the application preview.")
      return response.json()
    },
    retry: false,
    gcTime: 0,
    // Completion normally arrives via WS. This is only a missed-event backstop.
    refetchInterval: query => query.state.data?.build?.state === "running" ? 60_000 : false,
    refetchIntervalInBackground: false,
  })
  const build = useMutation({
    mutationFn: async (revision: number): Promise<PreviewBuild> => {
      const response = await apiFetch(`${endpoint}/build?${params}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ expected_revision: revision }) })
      if (!response.ok) {
        const body = await response.json().catch(() => null)
        throw new Error(body?.error ?? "Could not start the preview build.")
      }
      return response.json()
    },
    onSuccess: job => {
      // Clear the previous artifact immediately, including when a rebuild fails.
      client.setQueryData<PagePreview>(key, old => ({ revision: old?.revision ?? job.source_revision, runtime_url: old?.runtime_url ?? "", build: job }))
      invalidate()
    },
  })
  return { query, build }
}

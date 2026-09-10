"use client"

import { useCallback } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import type { PreviewArtifact } from "@/hooks/use-page-preview"
import type { FencedPublishRequest } from "@/lib/pages/editor-contract"

export interface PagePublication { version: number; build_id: string; source_revision: number; artifact_digest: string; git_commit: string; created_at: string; live_version?: number; published?: boolean; is_current?: boolean; replayed?: boolean }
export interface PageApplication { publication: PagePublication | null; publication_version?: number; can_publish: boolean; runtime_url?: string; development_same_origin?: boolean; artifact?: PreviewArtifact; etag?: string }
export function usePageApplication(workspace: string, slug: string, enabled = true) {
  const client = useQueryClient()
  const key = ["page-application", workspace, slug] as const
  const invalidate = useCallback(() => { void client.invalidateQueries({ queryKey: ["page-application", workspace, slug] }) }, [client, workspace, slug])
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("page.deleted", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)
  const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}`
  const params = new URLSearchParams({ workspace_id: workspace })
  const query = useQuery({ queryKey: key, enabled, retry: false, gcTime: 0,
    queryFn: async ({ signal }): Promise<PageApplication> => {
      const previous = client.getQueryData<PageApplication>(["page-application", workspace, slug])
      const response = await apiFetch(`${endpoint}/application?${params}`, { signal, headers: previous?.etag ? { "If-None-Match": previous.etag } : undefined })
      if (response.status === 304 && previous?.publication && previous.artifact) return previous
      if (!response.ok) { const body = await response.json().catch(() => null); throw Object.assign(new Error(body?.error ?? "Could not load the Page application."), { status: response.status }) }
      return { ...await response.json(), etag: response.headers.get("ETag") ?? undefined }
    },
    refetchInterval: query => query.state.data?.publication ? 60_000 : false,
    refetchIntervalInBackground: false,
  })
  // `POST .../project/check` is not called from the browser any more. The
  // review snapshot reports the same refusals as named blockers before a
  // person consents, and publish re-runs the full candidate check anyway, so
  // a second button that says "checks passed" would be a third place for the
  // same fact to be stated and to go stale. The endpoint and its CLI command
  // (`crewship page project check`) are unchanged.
  const publish = useMutation({ mutationFn: async (request: FencedPublishRequest) => {
    const response = await apiFetch(`${endpoint}/project/publish?${params}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(request) })
    const body = await response.json(); if (!response.ok) throw new Error(body?.error ?? "Publication failed."); return body as PagePublication
  }, onSettled: () => { invalidate(); void client.invalidateQueries({ queryKey: ["page-publications", workspace, slug] }); void client.invalidateQueries({ queryKey: ["pages", workspace] }) } })
  return { query, publish }
}

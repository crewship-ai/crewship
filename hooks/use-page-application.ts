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
  const check = useMutation({ mutationFn: async ({ build, revision }: { build: string; revision: number }) => {
    const response = await apiFetch(`${endpoint}/project/check?${params}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ build_id: build, expected_revision: revision }) })
    const body = await response.json(); if (!response.ok) throw new Error(body?.error ?? "Application check failed."); return body
  } })
  // The two digests are required by the type as well as by the server. They
  // are what the person reviewed, and the server compares them inside the
  // publishing transaction — the three compare-and-swap points that were
  // already there fence the publication counter, the draft revision and a
  // definition read moments earlier in the same request, none of which can
  // notice that the baseline someone actually read moved underneath them.
  // Leaving them optional here would turn that into a 400 at the click
  // instead of an error at the keyboard.
  const publish = useMutation({ mutationFn: async (request: FencedPublishRequest) => {
    const response = await apiFetch(`${endpoint}/project/publish?${params}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(request) })
    const body = await response.json(); if (!response.ok) throw new Error(body?.error ?? "Publication failed."); return body as PagePublication
  }, onSettled: () => { invalidate(); void client.invalidateQueries({ queryKey: ["page-publications", workspace, slug] }); void client.invalidateQueries({ queryKey: ["pages", workspace] }) } })
  return { query, check, publish }
}

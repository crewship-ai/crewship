"use client"

import { useCallback } from "react"
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import type { PagePublication } from "./use-page-application"

export interface PublicationReceipt extends PagePublication { source_digest: string; actor: string; rollback_of: number; withdrawn_at: string }
interface PublicationHistory { publications: PublicationReceipt[]; publication_version: number; published: boolean; can_publish: boolean; next_before: number }
interface ArchivedProject { revision: number; git_commit: string; project: { files: { path: string; encoding: string; content: string }[] } }
export function usePagePublications(workspace: string, slug: string, revision?: number) {
  const client = useQueryClient()
  const invalidate = useCallback(() => {
    for (const name of ["page-publications", "page-application", "pages"]) void client.invalidateQueries({ queryKey: [name, workspace] })
  }, [client, workspace])
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("page.deleted", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)
  const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}/project`
  async function request(path: string, init?: RequestInit) {
    const response = await apiFetch(path, init)
    const body = await response.json()
    if (!response.ok) throw new Error(body?.error ?? "Could not load application history.")
    return body
  }
  const query = useInfiniteQuery({ queryKey: ["page-publications", workspace, slug], initialPageParam: 0, retry: false, gcTime: 0,
    queryFn: ({ pageParam, signal }): Promise<PublicationHistory> => request(`${endpoint}/publications?${new URLSearchParams({ workspace_id: workspace, ...(pageParam ? { before: String(pageParam) } : {}) })}`, { signal }),
    getNextPageParam: page => page.next_before || undefined,
  })
  const source = useQuery({ queryKey: ["page-publication-source", workspace, slug, revision], enabled: !!revision, retry: false, gcTime: 0,
    queryFn: ({ signal }): Promise<ArchivedProject> => request(`${endpoint}/history/${revision}?${new URLSearchParams({ workspace_id: workspace })}`, { signal }),
  })
  const withdraw = useMutation({ mutationFn: (version: number) => request(`${endpoint}/unpublish?${new URLSearchParams({ workspace_id: workspace })}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ expected_publication: version }) }), onSettled: invalidate })
  return { query, source, withdraw }
}

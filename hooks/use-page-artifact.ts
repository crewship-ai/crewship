"use client"

import { useCallback } from "react"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { pagesKeys } from "@/hooks/use-pages"

export interface PageArtifact {
  head_rev: number
  revision: number
  html: string
  revisions: { revision: number; created_at: string; author_user_id: string }[]
  can_write: boolean
}
export function usePageArtifact(workspace: string, slug: string, revision?: number) {
  const client = useQueryClient()
  const key = ["page-artifact", workspace, slug] as const
  const invalidate = useCallback(() => { void client.invalidateQueries({ queryKey: ["page-artifact", workspace, slug] }) }, [client, workspace, slug])
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("page.deleted", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)
  const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}/artifact`
  const params = new URLSearchParams({ workspace_id: workspace })
  const query = useQuery({
    queryKey: [...key, revision ?? "head"],
    enabled: Boolean(workspace && slug), retry: false, gcTime: 0,
    refetchInterval: 60000, refetchIntervalInBackground: false,
    queryFn: async ({ signal }): Promise<PageArtifact> => {
      const selected = new URLSearchParams(params)
      if (revision !== undefined) selected.set("revision", String(revision))
      const response = await apiFetch(`${endpoint}?${selected}`, { signal })
      if (!response.ok) {
        const body = await response.json().catch(() => null)
        throw new Error(body?.error ?? "Could not load this artifact. Your access may have changed.")
      }
      return response.json()
    },
  })
  const save = useMutation({
    mutationFn: async (input: { html: string; base_rev: number } | { revision: number; base_rev: number }) => {
      const response = await apiFetch(`${endpoint}/${"html" in input ? "revisions" : "restore"}?${params}`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ...input, mutation_id: crypto.randomUUID() }),
      })
      if (!response.ok) {
        const body = await response.json().catch(() => null)
        if (response.status === 409 && body?.error === "artifact revision changed; reload before saving") throw new Error("A newer revision was saved. Your source is still here; review the latest revision before saving again.")
        throw new Error(body?.error ?? "Could not save this artifact.")
      }
      return response.json() as Promise<{ revision: number; head_rev: number }>
    },
    onSuccess: async () => {
      await Promise.all([
        client.invalidateQueries({ queryKey: key }),
        client.invalidateQueries({ queryKey: pagesKeys.all(workspace) }),
      ])
    },
  })
  return { query, save }
}

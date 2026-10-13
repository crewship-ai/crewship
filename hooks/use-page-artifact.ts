"use client"

import { useCallback, useEffect, useRef } from "react"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { pagesKeys } from "@/hooks/use-pages"

export class PageArtifactError extends Error {
  constructor(message: string, public status: number) { super(message) }
}

export interface PageArtifact {
  head_rev: number
  revision: number
  html: string
  revisions: { revision: number; created_at: string; author_user_id: string; author_agent_id?: string; author_run_id?: string }[]
  can_write: boolean
}
export function usePageArtifact(workspace: string, slug: string, revision?: number) {
  const client = useQueryClient()
  const pending = useRef(new Map<string, string>())
  useEffect(() => { pending.current.clear() }, [workspace, slug])
  const key = ["page-artifact", workspace, slug] as const
  const invalidate = useCallback(() => { void client.invalidateQueries({ queryKey: ["page-artifact", workspace, slug] }) }, [client, workspace, slug])
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("page.deleted", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)
  const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}/artifact`
  const params = new URLSearchParams({ workspace_id: workspace })
  const query = useQuery<PageArtifact>({
    queryKey: [...key, revision ?? "head"],
    enabled: Boolean(workspace && slug), retry: false, gcTime: 0,
    refetchInterval: 60000, refetchIntervalInBackground: false,
    // Preserve same-page content while selecting history so private comment
    // drafts stay mounted. Never carry a document across workspace/page keys.
    placeholderData: (previous, previousQuery) => previousQuery?.queryKey[1] === workspace && previousQuery?.queryKey[2] === slug ? previous : undefined,
    queryFn: async ({ signal }): Promise<PageArtifact> => {
      const selected = new URLSearchParams(params)
      if (revision !== undefined) selected.set("revision", String(revision))
      const response = await apiFetch(`${endpoint}?${selected}`, { signal })
      if (!response.ok) {
        const body = await response.json().catch(() => null)
        throw new PageArtifactError(body?.error ?? "Could not load this artifact. Your access may have changed.", response.status)
      }
      return response.json()
    },
  })
  const save = useMutation({
    mutationFn: async (input: { html: string; base_rev: number } | { revision: number; base_rev: number }) => {
      // Keep only a digest, not another potentially 10 MiB source per retry.
      const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(JSON.stringify({ workspace, slug, input })))
      const fingerprint = Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, "0")).join("")
      if (!pending.current.has(fingerprint)) {
        if (pending.current.size >= 32) throw new Error("Too many unconfirmed saves. Retry an earlier save before creating another.")
        pending.current.set(fingerprint, crypto.randomUUID())
      }
      const response = await apiFetch(`${endpoint}/${"html" in input ? "revisions" : "restore"}?${params}`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ...input, mutation_id: pending.current.get(fingerprint) }),
      })
      if (!response.ok) {
        const body = await response.json().catch(() => null)
        if (response.status === 409 && body?.error === "artifact revision changed; reload before saving") throw new Error("A newer revision was saved. Your source is still here; review the latest revision before saving again.")
        throw new Error(body?.error ?? "Could not save this artifact.")
      }
      const result = await response.json() as { revision: number; head_rev: number }
      pending.current.delete(fingerprint)
      return result
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

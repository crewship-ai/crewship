"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"

export interface PageArtifact {
  page_id: string
  html: string
  revision: number
  head_rev: number
  can_write: boolean
  revisions: { revision: number; created_at: string; restored_from?: number }[]
}
export interface ArtifactMutation {
  page_id: string
  base_rev: number
  mutation_id: string
  html?: string
  revision?: number
}
export class PageArtifactError extends Error {
  constructor(readonly status: number, message: string) { super(message) }
}
async function artifactResponse(response: Response) {
  const data = await response.json()
  if (!response.ok) throw new PageArtifactError(response.status, typeof data?.error === "string" ? data.error : "Artifact request failed")
  return data
}

/** Page identity and payload belong to the caller's original attempt. No
 * precondition refresh or mutation retry occurs inside this transport. */
export function usePageArtifact(workspaceId: string, slug: string, pageId: string, revision?: number) {
  const [data, setData] = useState<PageArtifact | null>(null)
  const [error, setError] = useState<Error | null>(null)
  const [busy, setBusy] = useState(false)
  const generation = useRef(0)
  const mounted = useRef(true)
  const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}/artifact`
  const query = new URLSearchParams({ workspace_id: workspaceId, page_id: pageId })
  if (revision !== undefined) query.set("revision", String(revision))
  const url = `${endpoint}?${query}`
  const deny = useCallback((failure: unknown) => {
    if (failure instanceof PageArtifactError && [401, 403, 404, 409].includes(failure.status)) {
      generation.current++ // Older authorized GETs cannot refill revoked state.
      setData(null)
    }
    setError(failure instanceof Error ? failure : new Error("Artifact request failed"))
  }, [])
  const refresh = useCallback(async () => {
    const read = ++generation.current
    try {
      const result = await artifactResponse(await apiFetch(url)) as PageArtifact
      if (mounted.current && read === generation.current) {
        if (result.page_id !== pageId) throw new PageArtifactError(409, "Page identity changed; reopen this Page")
        setData(result); setError(null)
      }
    } catch (failure) { if (mounted.current && read === generation.current) deny(failure) }
  }, [url, pageId, deny])
  useEffect(() => {
    mounted.current = true; setData(null); setError(null); void refresh()
    return () => { mounted.current = false; generation.current++ }
  }, [refresh])
  useRealtimeEventSafe("page.updated", refresh)
  useRealtimeEventSafe("realtime.reconnected", refresh)
  const mutate = useCallback(async (attempt: Readonly<ArtifactMutation>, restore: boolean) => {
    if (attempt.page_id !== pageId) throw new PageArtifactError(409, "Page identity changed; discard this draft before reopening")
    generation.current++
    setBusy(true); setError(null)
    try {
      const response = await artifactResponse(await apiFetch(`${endpoint}/${restore ? "restore" : "revisions"}?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(attempt),
      }))
      if (response.page_id !== pageId) throw new PageArtifactError(409, "Page identity changed")
      return response as { page_id: string; revision: number; head_rev: number }
    } catch (failure) { if (mounted.current) deny(failure); throw failure }
    finally { if (mounted.current) setBusy(false) }
  }, [endpoint, workspaceId, pageId, deny])
  return { data, error, busy, refresh, mutate }
}

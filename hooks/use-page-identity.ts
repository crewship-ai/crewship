"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { resolvedPageIdentity, type PageLinkReference, type ResolvedPageIdentity } from "@/lib/pages/stable-links"

export interface PageIdentityBinding {
  readonly expectedPageId: string
  readonly viewerId: string
  readonly scope: string
}
interface Resolution {
  identity: ResolvedPageIdentity
  binding: PageIdentityBinding
}
interface RequestState {
  key: string
  resolution?: Resolution
  error?: string
}

// No cross-viewer metadata cache. A fresh authorized resolution gives detail
// reads a unique scope, including after unmount/remount with a persistent cache.
export function useResolvedPageIdentity(
  workspaceId: string,
  reference: PageLinkReference | undefined,
  viewerId: string | null,
  eligible: boolean,
) {
  const [retry, setRetry] = useState(0)
  const generation = useRef(0)
  const [state, setState] = useState<RequestState | null>(null)
  const pageId = reference?.kind === "page" ? reference.pageId : null
  const enabled = Boolean(pageId && reference?.kind === "page" && reference.workspaceId === workspaceId && viewerId && eligible)
  // Eligibility transitions get a new key even when access is regained before
  // an old request settles. Module reloads cannot alias a persistent query cache.
  const key = useMemo(() => enabled ? JSON.stringify([viewerId, workspaceId, pageId, retry, crypto.randomUUID()]) : null,
    [enabled, viewerId, workspaceId, pageId, retry])

  useEffect(() => {
    if (!key || !pageId || !viewerId) return
    const request = ++generation.current
    const controller = new AbortController()
    const current = () => !controller.signal.aborted && generation.current === request
    setState({ key })
    void (async () => {
      try {
        const params = new URLSearchParams({ workspace_id: workspaceId })
        const response = await apiFetch(`/api/v1/pages/resolve/id/${encodeURIComponent(pageId)}?${params}`, { signal: controller.signal, cache: "no-store" })
        if (!response.ok) throw new Error("Page link is unavailable")
        const identity = resolvedPageIdentity(await response.json(), pageId)
        if (current()) setState({ key, resolution: { identity, binding: {
          expectedPageId: pageId,
          viewerId,
          scope: key,
        } } })
      } catch {
        if (current()) setState({ key, error: "Page link is unavailable. Check your workspace and Page access." })
      }
    })()
    return () => controller.abort()
  }, [key, pageId, viewerId, workspaceId])

  const current = key !== null && state?.key === key ? state : null
  return {
    resolution: current?.resolution ?? null,
    loading: enabled && !current?.resolution && !current?.error,
    error: current?.error ?? null,
    reload: () => setRetry(value => value + 1),
  }
}

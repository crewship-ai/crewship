"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { z } from "zod"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"

const identity = z.string().regex(/^[A-Za-z0-9_-]{1,128}$/)
const match = z.object({
  page_id: identity,
  slug: z.string().regex(/^[a-z0-9][a-z0-9_-]{0,63}$/),
  name: z.string().max(4096),
  content_type: z.enum(["document", "slides"]),
  schema_version: z.number().int().min(1).max(2),
  source_revision: z.number().int().nonnegative(),
  source_generation: z.number().int().nonnegative(),
  snippet: z.string().refine(value => new TextEncoder().encode(value).length <= 512),
}).strict()
const responseSchema = z.object({
  scope: z.literal("native-docs-slides"),
  matches: z.array(match).max(50),
  has_more: z.boolean(),
  cursor: z.string().regex(/^[A-Za-z0-9_-]{43}$/).optional(),
}).strict().refine(value => value.has_more === Boolean(value.cursor) && (!value.has_more || value.matches.length > 0))
export type NativePageSearchMatch = z.infer<typeof match>
interface SearchState {
  key: string
  query: string
  matches: NativePageSearchMatch[]
  cursor?: string
  busy: boolean
  complete: boolean
  error?: string
}

export function useNativePageSearch(workspaceId: string, viewerId: string | null, enabled: boolean) {
  const key = useMemo(() => enabled && viewerId && workspaceId ? crypto.randomUUID() : null, [workspaceId, viewerId, enabled])
  const [state, setState] = useState<SearchState | null>(null)
  const generation = useRef(0)
  const pending = useRef<AbortController | null>(null)
  useEffect(() => () => { pending.current?.abort(); generation.current++ }, [key])
  const current = key && state?.key === key ? state : null

  const clear = () => {
    pending.current?.abort()
    generation.current++
    setState(null)
  }
  const invalidate = useCallback(() => {
    if (!key) return
    pending.current?.abort()
    generation.current++
    setState(previous => previous?.key === key ? { key, query: previous.query, matches: [], busy: false, complete: false, error: "Results may have changed. Search again to check current access and content." } : null)
  }, [key])
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("page.deleted", invalidate)
  useRealtimeEventSafe("page.folder.updated", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)
  useEffect(() => {
    if (!key) return
    window.addEventListener("focus", invalidate)
    window.addEventListener("online", invalidate)
    const visibility = () => { if (document.visibilityState === "visible") invalidate() }
    document.addEventListener("visibilitychange", visibility)
    return () => {
      window.removeEventListener("focus", invalidate)
      window.removeEventListener("online", invalidate)
      document.removeEventListener("visibilitychange", visibility)
    }
  }, [key, invalidate])
  useEffect(() => {
    if (!current?.complete) return
    // Grant/issuer changes do not all emit Page events. Bound how long an
    // idle result batch can survive missed events, without automatic retries.
    const batchGeneration = generation.current
    const expiry = window.setTimeout(() => { if (generation.current === batchGeneration) invalidate() }, 15000)
    return () => window.clearTimeout(expiry)
  }, [current, invalidate])
  const search = async (query: string, more = false) => {
    if (!key) return
    const previous = more && current?.query === query ? current : null
    if (more && (!previous?.cursor || previous.busy)) return
    pending.current?.abort()
    const controller = new AbortController()
    pending.current = controller
    const request = ++generation.current
    const active = () => !controller.signal.aborted && request === generation.current
    setState({ key, query, matches: [], busy: true, complete: false })
    try {
      const params = new URLSearchParams({ workspace_id: workspaceId, q: query, limit: "20" })
      if (previous?.cursor) params.set("cursor", previous.cursor)
      const response = await apiFetch(`/api/v1/pages/search/content?${params}`, { signal: controller.signal, cache: "no-store" })
      if (!response.ok) {
        const message = response.status === 422 ? "Some native content is unsupported or exceeds search limits." :
          response.status === 401 || response.status === 403 ? "Search access is unavailable. Check your current workspace and permissions." :
            response.status === 400 ? "This search or continuation is unavailable. Search again explicitly." :
              "Native text search is not ready or changed. Try searching again."
        throw new Error(message)
      }
      const result = responseSchema.parse(await response.json())
      // Show the newly authorized batch only: earlier cursor pages may have
      // been revoked and the endpoint does not re-emit their metadata.
      if (active()) setState({ key, query, matches: result.matches, cursor: result.cursor, busy: false, complete: true })
    } catch (error) {
      if (active()) setState({ key, query, matches: [], busy: false, complete: false, error: error instanceof z.ZodError ? "Native search returned an unsupported response. Try again." : error instanceof Error ? error.message : "Native search is unavailable." })
    }
  }
  return { matches: current?.matches ?? [], cursor: current?.cursor, busy: current?.busy ?? false, complete: current?.complete ?? false, error: current?.error, query: current?.query, search, clear }
}

"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"

export interface ArtifactComment {
  id: string
  body: string
  author_user_id: string
  author_name?: string
  author_agent_id?: string
  author_run_id?: string
  created_at: string
  version?: number
  edited_at?: string
  deleted_at?: string
  can_edit?: boolean
  can_delete?: boolean
  can_react?: boolean
  can_view_history?: boolean
  reactions?: { emoji: string; count: number; reacted_by_me: boolean }[]
}
export interface ArtifactCommentHistory { version: number; body: string; action: string; actor_user_id: string; created_at: string }
export type ArtifactCommentChange = { action: "edit"; commentId: string; body: string } | { action: "delete"; commentId: string } | { action: "react"; commentId: string; emoji: string; active: boolean }
export type ArtifactAnchor = { schema_version: 1; type: "document" } | { schema_version: 1; type: "point"; coordinate_space: "viewport"; x: number; y: number; viewport_width: number; viewport_height: number }

export interface ArtifactThread {
  id: string
  revision: number
  anchor: ArtifactAnchor
  status: "open" | "resolved"
  version: number
  created_at: string
  updated_at: string
  created_by_user_id: string
  comments: ArtifactComment[]
}
interface ThreadsResult {
  next_cursor?: string
  threads: ArtifactThread[]
  can_comment: boolean
  can_resolve: boolean
}
export class ArtifactCommentError extends Error {
  constructor(message: string, public status: number) { super(message) }
}
export type ArtifactReplyWrite = { action: "reply"; threadId: string; base_version: number; body: string }
export type ThreadWrite =
  | { action: "create"; revision: number; body: string; anchor?: ArtifactAnchor }
  | ArtifactReplyWrite
  | (ArtifactCommentChange & { threadId: string; base_version: number })
  | { action: "resolve"; threadId: string; base_version: number; resolved: boolean }

export function useArtifactThreads(workspace: string, slug: string, status: "all" | "open" | "resolved" = "all") {
  const client = useQueryClient()
  const [accessError, setAccessError] = useState<ArtifactCommentError | null>(null)
  const [uncertainReplies, setUncertainReplies] = useState<ArtifactReplyWrite[]>([])
  const submissions = useRef(new Map<string, { id: string; input: ThreadWrite }>())
  const key = ["artifact-threads", workspace, slug, status] as const
  const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}/artifact/threads`
  const params = new URLSearchParams({ workspace_id: workspace })
  const invalidate = useCallback(() => {
    void client.invalidateQueries({ queryKey: ["artifact-threads", workspace, slug] })
  }, [client, workspace, slug])
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("page.deleted", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)
  const pagesQuery = useInfiniteQuery({
    initialPageParam: "",
    getNextPageParam: (last: ThreadsResult) => last.next_cursor || undefined,
    queryKey: key, enabled: Boolean(workspace && slug) && !accessError, retry: false, gcTime: 0,
    refetchInterval: 60000, refetchIntervalInBackground: false,
    queryFn: async ({ signal, pageParam }): Promise<ThreadsResult> => {
      const pageParams = new URLSearchParams(params)
      pageParams.set("status", status)
      if (pageParam) pageParams.set("cursor", pageParam)
      const response = await apiFetch(`${endpoint}?${pageParams}`, { signal })
      if (!response.ok) {
        const body = await response.json().catch(() => null)
        throw new ArtifactCommentError(body?.error ?? "Could not load comments.", response.status)
      }
      const result = await response.json()
      setAccessError(null)
      return result
    },
  })
  const query = { ...pagesQuery, data: pagesQuery.data ? {
    ...pagesQuery.data.pages[0],
    threads: Array.from(new Map(pagesQuery.data.pages.flatMap(page => page.threads).map(thread => [thread.id, thread])).values()),
  } : undefined }
  const fingerprintOf = (input: ThreadWrite) => JSON.stringify({ workspace, slug, input })
  const denyAccess = useCallback((error: ArtifactCommentError) => {
    setAccessError(error)
    submissions.current.clear()
    setUncertainReplies([])
    client.removeQueries({ queryKey: ["artifact-threads", workspace, slug] })
  }, [client, workspace, slug])
  useEffect(() => {
    const error = query.error
    if (!accessError && error instanceof ArtifactCommentError && [401, 403, 404].includes(error.status)) denyAccess(error)
  }, [query.error, accessError, denyAccess])
  useEffect(() => {
    submissions.current.clear()
    setUncertainReplies([])
    setAccessError(null)
  }, [workspace, slug])
  // Never evict an unconfirmed identity: interleaving another thread's write
  // must not turn a retry into a duplicate. Refuse new entries if the local
  // bounded set fills; successful exact submissions free their own entries.
  const write = useMutation({
    retry: false,
    mutationFn: async (input: ThreadWrite): Promise<ArtifactThread> => {
      if ("body" in input && (!input.body.trim() || new TextEncoder().encode(input.body).length > 4096)) throw new ArtifactCommentError("Comments must contain text and fit within 4 KiB.", 400)
      const fingerprint = fingerprintOf(input)
      if (!submissions.current.has(fingerprint)) {
        if (submissions.current.size >= 256) throw new ArtifactCommentError("Too many unconfirmed submissions. Retry existing submissions before sending more.", 400)
        submissions.current.set(fingerprint, { id: crypto.randomUUID(), input })
      }
      const mutation_id = submissions.current.get(fingerprint)!.id
      let url: string
      let method = "POST"
      let body: Record<string, unknown>
      if (input.action === "create") {
        url = endpoint
        body = { revision: input.revision, anchor: input.anchor ?? { schema_version: 1, type: "document" }, body: input.body, mutation_id }
      } else if (input.action === "reply") {
        url = `${endpoint}/${encodeURIComponent(input.threadId)}/replies`
        body = { body: input.body, base_version: input.base_version, mutation_id }
      } else if (input.action === "edit" || input.action === "delete" || input.action === "react") {
        url = `${endpoint}/${encodeURIComponent(input.threadId)}/comments/${encodeURIComponent(input.commentId)}`
        method = input.action === "edit" ? "PATCH" : input.action === "delete" ? "DELETE" : "PUT"
        body = { base_version: input.base_version, mutation_id }
        if (input.action === "edit") body.body = input.body
        if (input.action === "react") { url += `/reactions/${encodeURIComponent(input.emoji)}`; body.active = input.active }
      } else {
        url = `${endpoint}/${encodeURIComponent(input.threadId)}`
        method = "PATCH"
        body = { resolved: input.resolved, base_version: input.base_version, mutation_id }
      }
      const response = await apiFetch(`${url}?${params}`, {
        method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal: AbortSignal.timeout(20000),
      })
      if (!response.ok) {
        const result = await response.json().catch(() => null)
        throw new ArtifactCommentError(response.status === 409 && result?.code === "stale_thread"
          ? "This thread changed. Refresh comments, review the latest replies, then try again. Your draft is still here."
          : result?.error ?? "Could not save the comment.", response.status)
      }
      const result = await response.json()
      if (!result || typeof result.id !== "string" || !Number.isSafeInteger(result.version)) throw new Error("The comment result was not confirmed. Retry the unchanged request.")
      return result
    },
    onError: (error, input) => {
      if (error instanceof ArtifactCommentError && error.status >= 400 && error.status < 500 && error.status !== 408) submissions.current.delete(fingerprintOf(input))
      if (error instanceof ArtifactCommentError && [401, 403, 404].includes(error.status)) { denyAccess(error); return }
      if (input.action === "reply" && (!(error instanceof ArtifactCommentError) || error.status >= 500 || error.status === 408)) {
        setUncertainReplies(old => [...old.filter(item => fingerprintOf(item) !== fingerprintOf(input)), input])
      }
    },
    onSuccess: async (_thread, input) => {
      const fingerprint = fingerprintOf(input)
      submissions.current.delete(fingerprint)
      setUncertainReplies(old => old.filter(item => fingerprintOf(item) !== fingerprint))
      await client.invalidateQueries({ queryKey: key })
    },
  })
  const history = async (threadId: string, commentId: string): Promise<ArtifactCommentHistory[]> => {
    const response = await apiFetch(`${endpoint}/${encodeURIComponent(threadId)}/comments/${encodeURIComponent(commentId)}/history?${params}`, { signal: AbortSignal.timeout(20000) })
    if (!response.ok) {
      const error = new ArtifactCommentError("Comment history is unavailable.", response.status)
      if ([401,403,404].includes(response.status)) denyAccess(error)
      throw error
    }
    const result = await response.json()
    if (!Array.isArray(result?.history)) throw new Error("Invalid comment history.")
    return result.history
  }
  return { history, query: { ...query, data: accessError ? undefined : query.data, error: accessError ?? query.error }, write, uncertainReplies }
}

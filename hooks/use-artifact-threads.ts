"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"

export interface ArtifactComment {
  id: string
  body: string
  author_user_id: string
  author_name?: string
  created_at: string
}
export interface ArtifactThread {
  id: string
  revision: number
  anchor: { schema_version: 1; type: "document" }
  status: "open" | "resolved"
  version: number
  created_at: string
  updated_at: string
  created_by_user_id: string
  comments: ArtifactComment[]
}
interface ThreadsResult {
  threads: ArtifactThread[]
  can_comment: boolean
  can_resolve: boolean
}
export class ArtifactCommentError extends Error {
  constructor(message: string, public status: number) { super(message) }
}
export type ArtifactReplyWrite = { action: "reply"; threadId: string; base_version: number; body: string }
type ThreadWrite =
  | { action: "create"; revision: number; body: string }
  | ArtifactReplyWrite
  | { action: "resolve"; threadId: string; base_version: number; resolved: boolean }

export function useArtifactThreads(workspace: string, slug: string) {
  const client = useQueryClient()
  const [accessError, setAccessError] = useState<ArtifactCommentError | null>(null)
  const [uncertainReplies, setUncertainReplies] = useState<ArtifactReplyWrite[]>([])
  const submissions = useRef(new Map<string, { id: string; input: ThreadWrite }>())
  const key = ["artifact-threads", workspace, slug] as const
  const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}/artifact/threads`
  const params = new URLSearchParams({ workspace_id: workspace })
  const invalidate = useCallback(() => {
    void client.invalidateQueries({ queryKey: ["artifact-threads", workspace, slug] })
  }, [client, workspace, slug])
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("page.deleted", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)
  const query = useQuery({
    queryKey: key, enabled: Boolean(workspace && slug) && !accessError, retry: false, gcTime: 0,
    refetchInterval: 60000, refetchIntervalInBackground: false,
    queryFn: async ({ signal }): Promise<ThreadsResult> => {
      const response = await apiFetch(`${endpoint}?${params}`, { signal })
      if (!response.ok) {
        const body = await response.json().catch(() => null)
        throw new ArtifactCommentError(body?.error ?? "Could not load comments.", response.status)
      }
      const result = await response.json()
      setAccessError(null)
      return result
    },
  })
  const fingerprintOf = (input: ThreadWrite) => JSON.stringify({ workspace, slug, input })
  const denyAccess = useCallback((error: ArtifactCommentError) => {
    setAccessError(error)
    submissions.current.clear()
    setUncertainReplies([])
    client.removeQueries({ queryKey: ["artifact-threads", workspace, slug], exact: true })
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
        body = { revision: input.revision, anchor: { schema_version: 1, type: "document" }, body: input.body, mutation_id }
      } else if (input.action === "reply") {
        url = `${endpoint}/${encodeURIComponent(input.threadId)}/replies`
        body = { body: input.body, base_version: input.base_version, mutation_id }
      } else {
        url = `${endpoint}/${encodeURIComponent(input.threadId)}`
        method = "PATCH"
        body = { resolved: input.resolved, base_version: input.base_version, mutation_id }
      }
      const response = await apiFetch(`${url}?${params}`, {
        method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
      })
      if (!response.ok) {
        const result = await response.json().catch(() => null)
        throw new ArtifactCommentError(response.status === 409 && result?.code === "stale_thread"
          ? "This thread changed. Refresh comments, review the latest replies, then try again. Your draft is still here."
          : result?.error ?? "Could not save the comment.", response.status)
      }
      return response.json()
    },
    onError: (error, input) => {
      if (error instanceof ArtifactCommentError && [401, 403, 404].includes(error.status)) { denyAccess(error); return }
      if (input.action === "reply" && (!(error instanceof ArtifactCommentError) || error.status >= 500)) {
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
  return { query: { ...query, data: accessError ? undefined : query.data, error: accessError ?? query.error }, write, uncertainReplies }
}

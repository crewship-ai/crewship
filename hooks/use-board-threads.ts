"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"

export interface BoardComment {
  id: string
  body: string
  author_user_id: string
  author_name?: string
  created_at: string
}
export type BoardAnchor = { schema_version: 1; type: "board"; board_seq: number } | { schema_version: 1; type: "board-element"; board_seq: number; element_id: string; element_rev: number; snapshot?: { x: number; y: number; width: number; height: number; angle: number; element_type: string } }

export interface BoardThread {
  id: string
  anchor: BoardAnchor
  status: "open" | "resolved"
  version: number
  created_at: string
  updated_at: string
  created_by_user_id: string
  comments: BoardComment[]
}
interface ThreadsResult {
  threads: BoardThread[]
  can_comment: boolean
  can_resolve: boolean
}
export class BoardCommentError extends Error {
  constructor(message: string, public status: number) { super(message) }
}
export type BoardReplyWrite = { action: "reply"; threadId: string; base_version: number; body: string }
type ThreadWrite =
  | { action: "create"; body: string; anchor: BoardAnchor }
  | BoardReplyWrite
  | { action: "resolve"; threadId: string; base_version: number; resolved: boolean }
type BoardCreateWrite = Extract<ThreadWrite, { action: "create" }>

export function useBoardThreads(workspace: string, slug: string) {
  const client = useQueryClient()
  const [accessError, setAccessError] = useState<BoardCommentError | null>(null)
  const [uncertainReplies, setUncertainReplies] = useState<BoardReplyWrite[]>([])
  const [uncertainCreate, setUncertainCreate] = useState<BoardCreateWrite | null>(null)
  const submissions = useRef(new Map<string, { id: string; input: ThreadWrite }>())
  const key = ["board-threads", workspace, slug] as const
  const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}/board/threads`
  const params = new URLSearchParams({ workspace_id: workspace })
  const invalidate = useCallback(() => {
    void client.invalidateQueries({ queryKey: ["board-threads", workspace, slug] })
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
        throw new BoardCommentError(body?.error ?? "Could not load comments.", response.status)
      }
      const result = await response.json()
      setAccessError(null)
      return result
    },
  })
  const fingerprintOf = (input: ThreadWrite) => JSON.stringify({ workspace, slug, input })
  const denyAccess = useCallback((error: BoardCommentError) => {
    setAccessError(error)
    submissions.current.clear()
    setUncertainReplies([])
    setUncertainCreate(null)
    client.removeQueries({ queryKey: ["board-threads", workspace, slug], exact: true })
  }, [client, workspace, slug])
  useEffect(() => {
    const error = query.error
    if (!accessError && error instanceof BoardCommentError && [401, 403, 404].includes(error.status)) denyAccess(error)
  }, [query.error, accessError, denyAccess])
  useEffect(() => {
    submissions.current.clear()
    setUncertainReplies([])
    setUncertainCreate(null)
    setAccessError(null)
  }, [workspace, slug])
  useEffect(() => {
    if (query.data?.can_comment === false) {
      submissions.current.clear()
      setUncertainReplies([])
      setUncertainCreate(null)
    }
  }, [query.data?.can_comment])
  // Never evict an unconfirmed identity: interleaving another thread's write
  // must not turn a retry into a duplicate. Refuse new entries if the local
  // bounded set fills; successful exact submissions free their own entries.
  const write = useMutation({
    retry: false,
    mutationFn: async (input: ThreadWrite): Promise<BoardThread> => {
      if ("body" in input && (!input.body.trim() || new TextEncoder().encode(input.body).length > 4096)) throw new BoardCommentError("Comments must contain text and fit within 4 KiB.", 400)
      const fingerprint = fingerprintOf(input)
      if (input.action === "create" && uncertainCreate && fingerprint !== fingerprintOf(uncertainCreate)) throw new BoardCommentError("Retry the unconfirmed comment with its original text and anchor before starting another thread.", 400)
      if (!submissions.current.has(fingerprint)) {
        if (submissions.current.size >= 256) throw new BoardCommentError("Too many unconfirmed submissions. Retry existing submissions before sending more.", 400)
        submissions.current.set(fingerprint, { id: crypto.randomUUID(), input })
      }
      const mutation_id = submissions.current.get(fingerprint)!.id
      let url: string
      const method = "POST"
      let body: Record<string, unknown>
      if (input.action === "create") {
        url = endpoint
        body = { anchor: input.anchor, body: input.body, mutation_id }
      } else if (input.action === "reply") {
        url = `${endpoint}/${encodeURIComponent(input.threadId)}/comments`
        body = { body: input.body, base_version: input.base_version, mutation_id }
      } else {
        url = `${endpoint}/${encodeURIComponent(input.threadId)}/resolve`
        body = { resolved: input.resolved, base_version: input.base_version, mutation_id }
      }
      const response = await apiFetch(`${url}?${params}`, {
        method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal: AbortSignal.timeout(20000),
      })
      if (!response.ok) {
        const result = await response.json().catch(() => null)
        throw new BoardCommentError(response.status === 409 && result?.code === "stale_thread"
          ? "This thread changed. Refresh comments, review the latest replies, then try again. Your draft is still here."
          : result?.error ?? "Could not save the comment.", response.status)
      }
      return response.json()
    },
    onError: (error, input) => {
      if (error instanceof BoardCommentError && [401, 403, 404].includes(error.status)) { denyAccess(error); return }
      if (input.action === "create") {
        if (!(error instanceof BoardCommentError) || error.status >= 500) setUncertainCreate(input)
        else setUncertainCreate(old => old && fingerprintOf(old) === fingerprintOf(input) ? null : old)
      }
      if (input.action === "reply" && (!(error instanceof BoardCommentError) || error.status >= 500)) {
        setUncertainReplies(old => [...old.filter(item => fingerprintOf(item) !== fingerprintOf(input)), input])
      }
    },
    onSuccess: async (_thread, input) => {
      const fingerprint = fingerprintOf(input)
      submissions.current.delete(fingerprint)
      if (input.action === "create") setUncertainCreate(null)
      setUncertainReplies(old => old.filter(item => fingerprintOf(item) !== fingerprint))
      await client.invalidateQueries({ queryKey: key })
    },
  })
  return { query: { ...query, data: accessError ? undefined : query.data, error: accessError ?? query.error }, write, uncertainReplies, uncertainCreate }
}

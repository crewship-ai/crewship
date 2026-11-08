"use client"

import { useEffect, useRef } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { DocumentAPIError, documentRequest } from "@/hooks/use-page-document"

/** One stored remark as the comments read returns it; a root carries its resolved anchor. */
export interface DocumentComment {
  id: string; body: string; actor: string; rev: number; version: number; resolved: boolean; created_at: string
  parent?: string
  block?: string; quote?: string; from?: number; to?: number
  status?: "attached" | "collapsed" | "detached" | "resolved"
}
export interface DocumentComments { comments: DocumentComment[]; canComment: boolean; canResolve: boolean }

const text = (value: unknown, max: number): value is string => typeof value === "string" && value.length <= max
const count = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0
const STATUSES = ["attached", "collapsed", "detached", "resolved"]

/** Accepts exactly the comments read for this page; anything else hides the discussion instead of guessing. */
export function parseDocumentComments(value: unknown, pageId: string): DocumentComments {
  const fail = () => { throw new Error("Comments could not be read.") }
  if (!value || typeof value !== "object" || Array.isArray(value)) fail()
  const record = value as Record<string, unknown>
  if (record.page_id !== pageId || !Array.isArray(record.comments) || record.comments.length > 1000) fail()
  const comments = (record.comments as unknown[]).map(item => {
    if (!item || typeof item !== "object") fail()
    const c = item as Record<string, unknown>
    if (!text(c.id, 128) || !c.id || !text(c.body, 10000) || !text(c.actor, 256) || !count(c.rev) || !count(c.version) || typeof c.resolved !== "boolean" || !text(c.created_at, 64)) fail()
    if (c.parent !== undefined && (!text(c.parent, 128) || !c.parent)) fail()
    if (c.parent === undefined && (!text(c.block, 128) || !text(c.quote, 10000) || !STATUSES.includes(c.status as string))) fail()
    if ((c.from !== undefined && !count(c.from)) || (c.to !== undefined && !count(c.to))) fail()
    const comment: DocumentComment = { id: c.id as string, body: c.body as string, actor: c.actor as string, rev: c.rev as number, version: c.version as number, resolved: c.resolved as boolean, created_at: c.created_at as string }
    if (c.parent !== undefined) comment.parent = c.parent as string
    else Object.assign(comment, { block: c.block, quote: c.quote, status: c.status, ...(c.from !== undefined && c.to !== undefined ? { from: c.from, to: c.to } : {}) })
    return comment
  })
  return { comments, canComment: record.can_comment === true, canResolve: record.can_resolve === true }
}

export type CommentWrite =
  | { kind: "thread"; block: string; from: number; to: number; text: string }
  | { kind: "reply"; parent: string; text: string }
  | { kind: "resolve"; comment: string; version: number; resolved: boolean }

/** What a refused comment write means for the person who sent it. */
export function commentFailure(error: unknown): string {
  if (error instanceof DocumentAPIError) {
    if (error.status === 409) return "The page changed while you wrote. Your text is kept; send it again."
    if ([401, 403, 404].includes(error.status)) return "You can no longer comment on this page."
    if (error.status === 413) return "This selection is too long to comment on."
    if (error.status === 429) return "Too many comments right now. Try again shortly."
    if (error.status === 400) return error.message
  }
  return "The comment could not be sent. Your text is kept; try again."
}

/**
 * The page's discussion. The read follows document read authority; writes go
 * to the same document routes with the confirmed revision and epoch, so a
 * comment is never anchored against text this tab has not seen confirmed.
 */
export function useDocumentComments({ workspaceId, slug, pageId, humanId, enabled, head }: {
  workspaceId: string; slug: string; pageId: string; humanId: string; enabled: boolean
  head?: { schema: number; epoch: string; rev: number }
}) {
  const client = useQueryClient()
  const key = ["document-comments", workspaceId, pageId, humanId] as const
  const query = useQuery({
    queryKey: key, enabled, retry: false, staleTime: 15_000,
    queryFn: async ({ signal }) => parseDocumentComments(await documentRequest(workspaceId, slug, "/comments", undefined, signal), pageId),
  })
  // A confirmed revision can move or remove an anchor: read the threads again.
  const seenRev = useRef(head?.rev)
  const ready = query.isSuccess
  useEffect(() => {
    if (!ready || head?.rev === undefined || seenRev.current === head.rev) return
    seenRev.current = head.rev
    void client.invalidateQueries({ queryKey: key })
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the key is derived from the identity props
  }, [ready, head?.rev, client, workspaceId, pageId, humanId])
  const write = useMutation({
    mutationFn: async (change: CommentWrite) => {
      if (!head) throw new Error("The document is not ready.")
      const base = { id: crypto.randomUUID(), schema: head.schema, page_id: pageId, epoch: head.epoch, ifRev: head.rev }
      const { kind, ...fields } = change
      await documentRequest(workspaceId, slug, kind === "resolve" ? "/comments/resolve" : "/comments", { ...base, ...fields })
    },
    onSettled: () => client.invalidateQueries({ queryKey: key }),
  })
  return { query, write }
}

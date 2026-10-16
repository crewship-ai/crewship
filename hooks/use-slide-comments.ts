"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { SlidesAPIError } from "./use-page-slides"
import type { SlideCommentCreate, SlideCommentEvent, SlideCommentPage, SlideCommentReply, SlideCommentResolve, SlideCommentThread, SlideCommentReceipt } from "@/lib/slides/comments"
import { validCommentEvent, validCommentPage, validCommentThread } from "@/lib/slides/comment-validation"

export interface CommentAttempt { path: string; body: SlideCommentCreate | SlideCommentReply | SlideCommentResolve; threadId?: string }
async function request<T>(workspace: string, slug: string, path: string, body?: unknown, cursor?: string): Promise<T> {
  const query = new URLSearchParams({ workspace_id: workspace, limit: "25" }); if (cursor) query.set("cursor", cursor)
  const response = await apiFetch(`/api/v1/pages/${encodeURIComponent(slug)}/slides/comments${path}?${query}`, body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
  const value = await response.json().catch(() => null)
  if (!response.ok) throw new SlidesAPIError(value?.error ?? `Comment request failed (${response.status}).`, response.status)
  return value as T
}
export function useSlideComments(workspace: string, slug: string, pageId: string) {
  const [threads, setThreads] = useState<SlideCommentThread[]>([]), [page, setPage] = useState<SlideCommentPage<SlideCommentThread>>()
  const [history, setHistory] = useState<{ threadId: string; page: SlideCommentPage<SlideCommentEvent> }>()
  const [error, setError] = useState<string>(), [status, setStatus] = useState("Loading comments…")
  const [busy, setBusy] = useState(false), [pending, setPending] = useState<CommentAttempt>(), [blocked, setBlocked] = useState(false)
  const generation = useRef(0), listRequest = useRef(0), historyRequest = useRef(0), inFlight = useRef(false), attempt = useRef<CommentAttempt | undefined>(undefined)
  const fail = useCallback((cause: unknown) => {
    if (cause instanceof SlidesAPIError && [401, 403, 404].includes(cause.status)) { listRequest.current++; historyRequest.current++; setThreads([]); setPage(undefined); setHistory(undefined); setBlocked(true); setStatus("Comment access changed. Retained drafts have not been sent.") }
    setError(cause instanceof Error ? cause.message : "Could not load comments.")
  }, [])
  const refresh = useCallback(async (cursor?: string) => {
    const lifecycle = generation.current, read = ++listRequest.current
    try {
      const value = await request<unknown>(workspace, slug, "", undefined, cursor)
      if (generation.current !== lifecycle || listRequest.current !== read) return
      if (!validCommentPage(value, (item): item is SlideCommentThread => validCommentThread(item, pageId))) throw new Error("Unsupported comment response.")
      setPage(value); setThreads(previous => cursor ? [...previous, ...value.items.filter(item => !previous.some(old => old.id === item.id))] : value.items)
      setError(undefined)
      if (!attempt.current) { setBlocked(false); setStatus("Comments reflect their reported saved revision.") }
    } catch (cause) { if (generation.current === lifecycle && listRequest.current === read) fail(cause) }
  }, [workspace, slug, pageId, fail])
  const loadHistory = useCallback(async (threadId: string, cursor?: string) => {
    const lifecycle = generation.current, read = ++historyRequest.current
    try {
      const value = await request<unknown>(workspace, slug, `/${encodeURIComponent(threadId)}/history`, undefined, cursor)
      if (generation.current !== lifecycle || historyRequest.current !== read) return
      if (!validCommentPage(value, validCommentEvent)) throw new Error("Unsupported comment history.")
      setHistory(previous => ({ threadId, page: { ...value, items: cursor && previous?.threadId === threadId ? [...previous.page.items, ...value.items] : value.items } })); setError(undefined)
    } catch (cause) { if (generation.current === lifecycle && historyRequest.current === read) fail(cause) }
  }, [workspace, slug, fail])
  const initialRefresh = useRef(refresh); initialRefresh.current = refresh
  const invalidate = useCallback(() => { generation.current++; listRequest.current++; historyRequest.current++ }, [])
  useEffect(() => {
    invalidate(); void initialRefresh.current()
    return invalidate
    // A rename updates endpoints without replacing drafts or original attempts.
  }, [workspace, pageId, invalidate])
  async function send(proposed?: CommentAttempt) {
    if (inFlight.current || blocked) return false
    const selected = attempt.current ?? proposed
    if (!selected || selected.body.page_id !== pageId) return false
    const lifecycle = generation.current
    attempt.current = structuredClone(selected); setPending(attempt.current); setBusy(true); inFlight.current = true; setError(undefined)
    try {
      const receipt = await request<SlideCommentReceipt>(workspace, slug, selected.path, selected.body)
      if (generation.current !== lifecycle) return false
      const expectedThread = selected.threadId ?? selected.body.mutation_id, expectedVersion = "if_version" in selected.body ? selected.body.if_version + 1 : 1
      if (receipt.mutation_id !== selected.body.mutation_id || receipt.thread_id !== expectedThread || receipt.version !== expectedVersion || typeof receipt.duplicate !== "boolean") throw new Error("Invalid comment acknowledgement. Retry the original request.")
      attempt.current = undefined; setPending(undefined); setStatus(`Comment version ${receipt.version} confirmed${receipt.duplicate ? " (original request)" : ""}.`)
      await refresh(); if (selected.threadId) await loadHistory(selected.threadId)
      return true
    } catch (cause) {
      if (generation.current !== lifecycle) return false
      if (cause instanceof SlidesAPIError && cause.status >= 400 && cause.status < 500 && ![408, 429].includes(cause.status)) {
        setBlocked(true); setStatus(cause.status === 409 ? "Comment conflict. Review current history before discarding the original attempt; no automatic rebase." : "Comment rejected. Your draft and original request are retained.")
      } else setStatus("Comment outcome uncertain. Retry sends the exact original request.")
      fail(cause); return false
    } finally { if (generation.current === lifecycle) { inFlight.current = false; setBusy(false) } }
  }
  async function discardAttempt() { if (inFlight.current) return; attempt.current = undefined; setPending(undefined); setBlocked(false); await refresh() }
  return { threads, page, history, error, status, busy, pending, blocked, refresh, loadHistory, send, discardAttempt }
}

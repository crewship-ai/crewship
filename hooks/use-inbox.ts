"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRealtimeEvent } from "@/hooks/use-realtime"

// InboxItem mirrors the wire shape from /api/v1/inbox. State =
// 'unread' | 'read' | 'resolved'; kind tells the UI which actions
// the item supports (approve waitpoint, retry run, resolve escalation,
// etc.). Payload is kind-specific structured data.
export interface InboxItem {
  id: string
  workspace_id: string
  kind: "waitpoint" | "escalation" | "failed_run" | "message"
  source_id: string
  target_user_id?: string
  target_role?: string
  title: string
  body_md?: string
  sender_type?: "agent" | "crew" | "system" | "pipeline"
  sender_id?: string
  sender_name?: string
  state: "unread" | "read" | "resolved"
  priority: "urgent" | "high" | "medium" | "low"
  blocking: boolean
  payload?: Record<string, unknown>
  read_at?: string
  resolved_at?: string
  resolved_by_user_id?: string
  resolved_action?: string
  created_at: string
  updated_at: string
}

interface InboxListResponse {
  rows: InboxItem[]
  count: number
  unread_count: number
}

// useInbox manages the workspace inbox feed: fetches the list, exposes
// the unread badge count, and provides patch helpers that flip an item
// between unread / read / resolved. The realtime event "inbox.updated"
// triggers a refresh so a decision made in another tab (or by a peer)
// propagates without a manual reload.
export function useInbox(workspaceId: string | null | undefined, stateFilter?: "unread" | "read" | "resolved" | "all") {
  const [items, setItems] = useState<InboxItem[]>([])
  const [unreadCount, setUnreadCount] = useState(0)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setItems([])
      setUnreadCount(0)
      return
    }
    abortRef.current?.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    setError(null)
    try {
      const url = stateFilter && stateFilter !== "all"
        ? `/api/v1/inbox?state=${stateFilter}`
        : "/api/v1/inbox"
      const res = await fetch(url, { signal: ctrl.signal })
      if (ctrl.signal.aborted) return
      if (!res.ok) {
        setError(`inbox: ${res.status}`)
        setLoading(false)
        return
      }
      const data: InboxListResponse = await res.json()
      if (ctrl.signal.aborted) return
      setItems(data.rows ?? [])
      setUnreadCount(data.unread_count ?? 0)
    } catch (e) {
      if (ctrl.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!ctrl.signal.aborted) setLoading(false)
    }
  }, [workspaceId, stateFilter])

  useEffect(() => {
    refresh()
    return () => { abortRef.current?.abort() }
  }, [refresh])

  // Live refresh: any inbox state change emits inbox.updated; the
  // bell + page mirror it without a poll loop. Cheap because it just
  // re-fires the same workspace-scoped GET.
  useRealtimeEvent("inbox.updated", refresh)
  // Source-of-truth events also touch inbox rows (escalation.created,
  // pipeline.waitpoint.created) — listen so the inbox lights up the
  // moment a new item lands, not on next poll.
  useRealtimeEvent("escalation.created", refresh)
  useRealtimeEvent("pipeline.waitpoint.created", refresh)

  const patch = useCallback(
    async (id: string, state: InboxItem["state"], resolvedAction?: string) => {
      try {
        const res = await fetch(`/api/v1/inbox/${encodeURIComponent(id)}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ state, resolved_action: resolvedAction }),
        })
        if (!res.ok) {
          const body = await res.json().catch(() => null)
          throw new Error(body?.error ?? `patch failed (${res.status})`)
        }
        // Optimistic local update — the WS event will follow and
        // re-fetch, but updating in place avoids the row jumping
        // around during the round-trip.
        setItems((prev) =>
          prev.map((it) =>
            it.id === id
              ? {
                  ...it,
                  state,
                  resolved_action: state === "resolved" ? resolvedAction ?? it.resolved_action : it.resolved_action,
                }
              : it,
          ),
        )
        // Adjust unread count locally so the bell badge updates without
        // waiting for the next refresh.
        setUnreadCount((prev) => {
          const before = items.find((it) => it.id === id)
          if (!before) return prev
          const wasUnread = before.state === "unread"
          const isUnread = state === "unread"
          if (wasUnread && !isUnread) return Math.max(0, prev - 1)
          if (!wasUnread && isUnread) return prev + 1
          return prev
        })
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      }
    },
    [items],
  )

  return { items, unreadCount, loading, error, refresh, patch }
}

// useInboxUnreadCount is the lighter cousin used by the top-bar bell
// when the full list isn't needed. Polls the dedicated /count endpoint
// every 30s plus listens to the realtime event so the badge updates
// instantly when something changes elsewhere.
export function useInboxUnreadCount(workspaceId: string | null | undefined) {
  const [count, setCount] = useState(0)

  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setCount(0)
      return
    }
    try {
      const res = await fetch("/api/v1/inbox/count")
      if (!res.ok) return
      const data: { unread_count: number } = await res.json()
      setCount(data.unread_count ?? 0)
    } catch {
      /* swallow — bell badge stays at last known good value */
    }
  }, [workspaceId])

  useEffect(() => {
    refresh()
    // 30s polling matches the scheduler tick — keeps the bell alive
    // without thrashing the API. Realtime events below cut latency
    // when something happens between polls.
    const t = setInterval(refresh, 30_000)
    return () => clearInterval(t)
  }, [refresh])

  useRealtimeEvent("inbox.updated", refresh)
  useRealtimeEvent("escalation.created", refresh)
  useRealtimeEvent("pipeline.waitpoint.created", refresh)

  return count
}

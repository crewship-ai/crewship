"use client"

import { useCallback, useEffect, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import { apiFetch } from "@/lib/api-fetch"
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
  // Present only when the sender is a real agent: the DiceBear seed/style
  // for that agent's avatar, so the inbox renders the same face the agent
  // card shows instead of a generic glyph. Blank for system/crew/pipeline.
  avatar_seed?: string
  avatar_style?: string
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

// "active" = everything not archived (unread + read), resolved excluded
// server-side — the Inbox tab's filter.
type StateFilter = "unread" | "read" | "resolved" | "all" | "active"

/**
 * Query keys follow the [resource, workspaceId, scope/params] convention
 * (see hooks/use-dashboard-data.ts for the full write-up). `all` is the
 * shared prefix: invalidating it refreshes every mounted inbox surface
 * (bell, sidebar badge, /inbox page) in one call.
 */
export const inboxKeys = {
  all: (ws: string) => ["inbox", ws] as const,
  list: (ws: string, state: StateFilter) => ["inbox", ws, "list", { state }] as const,
  count: (ws: string) => ["inbox", ws, "count"] as const,
}

/** Shared WS → cache invalidation. Any inbox state change emits
 *  inbox.updated; source-of-truth events (escalation.created,
 *  pipeline.waitpoint.created) also touch inbox rows, so the list and
 *  badge light up the moment a new item lands — no poll loop. */
function useInboxRealtimeInvalidation(workspaceId: string | null | undefined) {
  const qc = useQueryClient()
  const invalidate = useCallback(() => {
    if (!workspaceId) return
    qc.invalidateQueries({ queryKey: inboxKeys.all(workspaceId) })
  }, [qc, workspaceId])

  useRealtimeEvent("inbox.updated", invalidate)
  useRealtimeEvent("escalation.created", invalidate)
  useRealtimeEvent("pipeline.waitpoint.created", invalidate)
}

// useInbox manages the workspace inbox feed: fetches the list, exposes
// the unread badge count, and provides patch helpers that flip an item
// between unread / read / resolved. Backed by React Query — realtime
// events invalidate the cache, mutations reconcile it in place.
export function useInbox(workspaceId: string | null | undefined, stateFilter?: StateFilter) {
  const qc = useQueryClient()
  // workspace_id is required by RequireWorkspace middleware; the
  // backend route is /api/v1/inbox (no path param) so the value has to
  // land on the URL. 'all' and "no filter" hit the same URL — normalise
  // so they share a cache entry.
  const stateParam: StateFilter = stateFilter && stateFilter !== "all" ? stateFilter : "all"
  const listKey = inboxKeys.list(workspaceId ?? "", stateParam)

  const query = useQuery<InboxListResponse>({
    queryKey: listKey,
    queryFn: async ({ signal }) => {
      const params = new URLSearchParams({ workspace_id: workspaceId! })
      if (stateParam !== "all") {
        params.set("state", stateParam)
      }
      const res = await apiFetch(`/api/v1/inbox?${params.toString()}`, { signal })
      if (!res.ok) {
        throw new Error(`inbox: ${res.status}`)
      }
      return (await res.json()) as InboxListResponse
    },
    enabled: Boolean(workspaceId),
    // Single-shot like the previous hand-rolled fetch — the error
    // banner shows immediately and the WS invalidation retriggers.
    retry: false,
  })

  useInboxRealtimeInvalidation(workspaceId)

  // PATCH failures surface through the same `error` field the list
  // fetch uses (the /inbox page renders it as "Inbox unavailable: …").
  // Kept outside the query so a failed action doesn't poison the
  // cached list; cleared when fresh data lands, matching the old
  // refresh()-clears-error behaviour.
  const [patchError, setPatchError] = useState<string | null>(null)
  const { dataUpdatedAt } = query
  useEffect(() => {
    if (dataUpdatedAt) setPatchError(null)
  }, [dataUpdatedAt])

  const mutation = useMutation<
    void,
    Error,
    { id: string; state: InboxItem["state"]; resolvedAction?: string }
  >({
    mutationFn: async ({ id, state, resolvedAction }) => {
      const res = await apiFetch(
        `/api/v1/inbox/${encodeURIComponent(id)}?workspace_id=${encodeURIComponent(workspaceId!)}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ state, resolved_action: resolvedAction }),
        },
      )
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        // Propagate to the caller so a UI button can show its own
        // toast + roll back optimistic UI. Earlier silently-swallowed
        // errors meant a 409 (e.g. the source-managed kind guard)
        // never surfaced to the user.
        throw new Error(body?.error ?? `patch failed (${res.status})`)
      }
    },
    retry: false,
    onMutate: () => setPatchError(null),
    onSuccess: (_data, { id, state, resolvedAction }) => {
      // Reconcile the cached list in place (instead of refetching) so
      // a stateFilter='unread' view drops the row when it transitions
      // to read/resolved. The next inbox.updated WS event re-syncs
      // against the server anyway.
      qc.setQueryData<InboxListResponse>(listKey, (prev) => {
        if (!prev) return prev
        const before = prev.rows?.find((it) => it.id === id)
        // "active" keeps the row for any non-resolved transition (unread→
        // read stays in the Inbox view); exact filters match on the value.
        const matchesFilter =
          stateParam === "all" ||
          stateParam === state ||
          (stateParam === "active" && state !== "resolved")
        const rows = matchesFilter
          ? (prev.rows ?? []).map((it) =>
              it.id === id
                ? {
                    ...it,
                    state,
                    resolved_action:
                      state === "resolved" ? resolvedAction ?? it.resolved_action : it.resolved_action,
                  }
                : it,
            )
          : (prev.rows ?? []).filter((it) => it.id !== id)
        let unread = prev.unread_count ?? 0
        if (before) {
          const wasUnread = before.state === "unread"
          const isUnread = state === "unread"
          if (wasUnread && !isUnread) unread = Math.max(0, unread - 1)
          else if (!wasUnread && isUnread) unread = unread + 1
        }
        return { ...prev, rows, unread_count: unread }
      })
      // Mark every other inbox entry (sibling state filters, the bell
      // count) stale without refetching now: the in-place edit above
      // keeps the current view instant, the WS broadcast usually
      // re-syncs everything, and — when WS is down — a filter switch
      // re-observes a stale query and refetches instead of serving a
      // cached list that never saw this PATCH.
      qc.invalidateQueries({
        queryKey: inboxKeys.all(workspaceId!),
        refetchType: "none",
      })
    },
    onError: (err) => setPatchError(err.message),
  })
  const { mutateAsync } = mutation

  const patch = useCallback(
    async (id: string, state: InboxItem["state"], resolvedAction?: string) => {
      if (!workspaceId) return
      await mutateAsync({ id, state, resolvedAction })
    },
    [mutateAsync, workspaceId],
  )

  const refresh = useCallback(async () => {
    if (!workspaceId) return
    await qc.invalidateQueries({ queryKey: inboxKeys.all(workspaceId) })
  }, [qc, workspaceId])

  return {
    items: query.data?.rows ?? [],
    unreadCount: query.data?.unread_count ?? 0,
    // isFetching (not isLoading) mirrors the old loading flag, which
    // was set on every refresh, not just the first one.
    loading: query.isFetching,
    error: patchError ?? (query.error ? query.error.message : null),
    refresh,
    patch,
  }
}

// useInboxUnreadCount is the lighter cousin used by the top-bar bell
// when the full list isn't needed. WS events (below) are the primary
// trigger — the refetchInterval is a safety net for missed events.
// It keeps the pre-react-query 30s cadence, including background
// tabs, because WS death can be terminal (use-websocket gives up
// after MAX_RECONNECT_ATTEMPTS) and the badge is then the only
// signal that a human-in-the-loop approval is waiting.
export function useInboxUnreadCount(workspaceId: string | null | undefined) {
  const query = useQuery<number>({
    queryKey: inboxKeys.count(workspaceId ?? ""),
    queryFn: async ({ signal }) => {
      const res = await apiFetch(
        `/api/v1/inbox/count?workspace_id=${encodeURIComponent(workspaceId!)}`,
        { signal },
      )
      if (!res.ok) {
        // Throwing keeps the previous data in the cache — the bell
        // badge stays at the last known good value, as before.
        throw new Error(`inbox count: ${res.status}`)
      }
      const data: { unread_count: number } = await res.json()
      return data.unread_count ?? 0
    },
    enabled: Boolean(workspaceId),
    retry: false,
    refetchInterval: 30_000,
    refetchIntervalInBackground: true,
  })

  useInboxRealtimeInvalidation(workspaceId)

  return query.data ?? 0
}

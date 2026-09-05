"use client"

import { useCallback, useEffect, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEvent } from "@/hooks/use-realtime"

export type InboxAttentionClass = "decision" | "input" | "review" | "repair"

/** One entry of a row's `actions[]` — what the server permits, in its words. */
export interface InboxActionSpec {
  id: string
  label: string
  effect?: string
  irreversible?: boolean
}

/**
 * The closed vocabulary `POST /api/v1/inbox/{id}/act` accepts (B15, #2389).
 * It is the KIND's vocabulary, not the row's: a card written before B15 lists
 * only take_over in its actions[] and still accepts all three.
 */
export type InboxAction = "answer" | "take_over" | "dismiss"

/** The receipt an act writes — on the issue's event log and under payload.receipt. */
export interface InboxActReceipt {
  action: string
  acted_by: string
  acted_at: string
  inbox_item_id: string
  session_id?: string
  agent_version?: number
  source_run_id: string
  comment_id?: string
  delivery_id?: string
  /** answer only: the run the answer resumed. */
  run_id?: string
  dispatch_state?: string
  event_id?: string
  /** The receipt's own position on the issue's event log. */
  seq?: number
}

export interface InboxActResult {
  id: string
  state: "resolved"
  action: InboxAction
  receipt: InboxActReceipt
}

/**
 * Why an act was refused, in the terms a card can react to. The endpoint
 * answers 409 for four different situations and only some of them are
 * failures from the person's point of view:
 *
 *   already_acted  — someone else closed the card first. Refresh, don't err.
 *   concurrent     — two people acted at once; this action ran, the card
 *                    carries theirs. Refresh, don't err.
 *   undeliverable  — the answer is on the issue as a comment but nothing will
 *                    pick it up (held agent, unconnected crew). The card stays
 *                    open; `detail` says why.
 *   other          — everything else, with the server's message.
 */
export class InboxActError extends Error {
  status: number
  code: "already_acted" | "concurrent" | "undeliverable" | "other"
  detail?: string
  resolvedAction?: string
  dispatchState?: string

  constructor(
    message: string,
    opts: { status: number; code: InboxActError["code"]; detail?: string; resolvedAction?: string; dispatchState?: string },
  ) {
    super(message)
    this.name = "InboxActError"
    this.status = opts.status
    this.code = opts.code
    this.detail = opts.detail
    this.resolvedAction = opts.resolvedAction
    this.dispatchState = opts.dispatchState
  }
}

/** Classify a non-2xx act response. Exported for the tests; the hook is the caller. */
export function classifyInboxActError(status: number, body: Record<string, unknown> | null): InboxActError {
  const error = typeof body?.error === "string" ? body.error : `act failed (${status})`
  if (status === 409) {
    if (typeof body?.dispatch_state === "string" && body.dispatch_state !== "") {
      return new InboxActError(error, {
        status,
        code: "undeliverable",
        dispatchState: body.dispatch_state,
        detail: typeof body?.detail === "string" ? body.detail : undefined,
      })
    }
    if (/already acted/i.test(error)) {
      return new InboxActError(error, {
        status,
        code: "already_acted",
        resolvedAction: typeof body?.resolved_action === "string" ? body.resolved_action : undefined,
      })
    }
    if (/at the same time/i.test(error)) {
      return new InboxActError(error, { status, code: "concurrent" })
    }
  }
  return new InboxActError(error, {
    status,
    code: "other",
    detail: typeof body?.detail === "string" ? body.detail : undefined,
  })
}

// InboxItem mirrors the wire shape from /api/v1/inbox. State =
// 'unread' | 'read' | 'resolved'; kind tells the UI which actions
// the item supports (approve waitpoint, retry run, resolve escalation,
// etc.). Payload is kind-specific structured data.
export interface InboxItem {
  /** Detail read only: the source that owns this decision no longer exists. */
  source_missing?: boolean
  id: string
  workspace_id: string
  /**
   * Every value inbox.AllKinds writes. The union used to stop at four, so
   * memory_consolidation, schedule_missed and schedule_circuit_breaker_tripped
   * — all of which the backend has written since v90/v155/v168 — had no place
   * in the type and fell through the UI as generic notifications.
   */
  kind:
    | "waitpoint"
    | "escalation"
    | "failed_run"
    | "message"
    | "memory_consolidation"
    | "schedule_missed"
    | "schedule_circuit_breaker_tripped"
    | "webhook_fire_failed"
    | "automation_enqueue_failed"
    | "run_needs_human"
  source_id: string
  /**
   * The §12 attention contract (B10, #2378) — server-computed, typed columns,
   * omitted for a kind that has not adopted it. `actions` is the closed list
   * of things the server will let a person do about THIS row; a card renders
   * exactly those and nothing it invents. See docs/api-reference/inbox.mdx
   * "Item shape".
   */
  attention_class?: InboxAttentionClass
  /** The recurring condition's identity — rows sharing it collapse server-side. */
  thread_key?: string
  actions?: InboxActionSpec[]
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
  /**
   * The sender agent's stored avatar render (#1297), when it has one.
   * Absent means generate from avatar_seed, as before.
   */
  avatar_url?: string
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
  /**
   * Four-eyes, as it will be applied to THIS escalation (#1574) — the same
   * fields, computed the same way, that the crew escalations list carries
   * (#1559), so the two surfaces cannot describe one rule differently.
   *
   * Top-level and NOT in `payload` on purpose. Payload is written when the
   * escalation is raised; both inputs to this answer — the workspace
   * require_second_approver toggle and the credential's tier — change
   * afterwards, so a stored copy goes stale in the direction that matters: an
   * unguarded one-click Approve on a row whose resolve now 403s. The server
   * computes these at read time; nothing here re-derives them.
   *
   * Absent on every other kind, and on a pre-#1574 server — which degrades to
   * "say nothing", not to a claim.
   */
  second_approver_required?: boolean
  second_approver_by_workspace?: boolean
  second_approver_by_tier?: boolean
  /** The linked credential's tier, e.g. "L4 · critical". */
  security_level_label?: string
  /**
   * Facts about the consequences of granting this credential — what the person
   * deciding needs beyond the judge's argument for its own verdict.
   *
   * Server-computed at read time (detail view only), and deliberately NOT
   * recommendations. The moment this carries "you should probably deny" the
   * reader anchors on the model and stops deciding.
   *
   * The optional inner objects carry a third state the UI must not flatten:
   * absent means the query failed and nobody knows, while `exists: false` means
   * we looked and there is none. Rendering "no backup" for a failed lookup would
   * manufacture an argument against approving out of a database outage.
   */
  evidence?: {
    last_backup?: { exists: boolean; age_hours: number; scope: string }
    narrower_credential?: { exists: boolean; name?: string; security_level?: number }
  }
}

interface InboxListResponse {
  rows: InboxItem[]
  count: number
  unread_count: number
  has_more?: boolean
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
  completeList: (ws: string, state: StateFilter) => ["inbox", ws, "complete-list", { state }] as const,
  detail: (ws: string, id: string) => ["inbox", ws, "detail", id] as const,
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
export function useInbox(
  workspaceId: string | null | undefined,
  stateFilter?: StateFilter,
  options?: { loadAll?: boolean },
) {
  const qc = useQueryClient()
  // workspace_id is required by RequireWorkspace middleware; the
  // backend route is /api/v1/inbox (no path param) so the value has to
  // land on the URL. 'all' and "no filter" hit the same URL — normalise
  // so they share a cache entry.
  const stateParam: StateFilter = stateFilter && stateFilter !== "all" ? stateFilter : "all"
  const loadAll = options?.loadAll ?? false
  const listKey = loadAll
    ? inboxKeys.completeList(workspaceId ?? "", stateParam)
    : inboxKeys.list(workspaceId ?? "", stateParam)

  const query = useQuery<InboxListResponse>({
    queryKey: listKey,
    queryFn: async ({ signal }) => {
      const pageSize = loadAll ? 500 : 100
      const rows: InboxItem[] = []
      let offset = 0
      let unreadCount = 0
      do {
        const params = new URLSearchParams({
          workspace_id: workspaceId!,
        })
        if (loadAll) {
          params.set("limit", String(pageSize))
          params.set("offset", String(offset))
        }
        if (stateParam !== "all") params.set("state", stateParam)
        const res = await apiFetch(`/api/v1/inbox?${params.toString()}`, { signal })
        if (!res.ok) throw new Error(`inbox: ${res.status}`)
        const page = (await res.json()) as InboxListResponse
        rows.push(...(page.rows ?? []))
        unreadCount = page.unread_count ?? unreadCount
        if (!loadAll || !page.has_more) break
        offset += page.rows.length
      } while (true)
      return { rows, count: rows.length, unread_count: unreadCount, has_more: false }
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
      reconcile(id, state, (it) => ({
        ...it,
        state,
        resolved_action:
          state === "resolved" ? resolvedAction ?? it.resolved_action : it.resolved_action,
      }))
    },
    onError: (err) => setPatchError(err.message),
  })
  const { mutateAsync } = mutation

  /**
   * Apply a state transition to the cached list without a refetch: `update`
   * rewrites the row when the view still shows it, and the row leaves the
   * list when the view's filter no longer matches. Shared by PATCH and act so
   * the two cannot disagree about which view drops what.
   */
  function reconcile(id: string, state: InboxItem["state"], update: (it: InboxItem) => InboxItem) {
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
        ? (prev.rows ?? []).map((it) => (it.id === id ? update(it) : it))
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
    // cached list that never saw this write.
    qc.invalidateQueries({
      queryKey: inboxKeys.all(workspaceId!),
      refetchType: "none",
    })
  }

  const patch = useCallback(
    async (id: string, state: InboxItem["state"], resolvedAction?: string) => {
      if (!workspaceId) return
      await mutateAsync({ id, state, resolvedAction })
    },
    [mutateAsync, workspaceId],
  )

  // Acting on a run_needs_human card (B15, #2389; web side #2398). Unlike
  // PATCH this is not a state flip the inbox owns: the server delivers an
  // answer to the session that asked, settles the session for take_over /
  // dismiss, and writes a receipt. What comes back is the resolved card plus
  // that receipt, and the cache takes it in place — the card flips without a
  // reload and shows the run it resumed.
  const actMutation = useMutation<
    InboxActResult,
    InboxActError,
    { id: string; action: InboxAction; input?: string }
  >({
    mutationFn: async ({ id, action, input }) => {
      const res = await apiFetch(
        `/api/v1/inbox/${encodeURIComponent(id)}/act?workspace_id=${encodeURIComponent(workspaceId!)}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          // input is only meaningful for answer; the server ignores it
          // otherwise, but sending an empty string for a dismiss is noise.
          body: JSON.stringify(input != null && input !== "" ? { action, input } : { action }),
        },
      )
      if (!res.ok) {
        const body = (await res.json().catch(() => null)) as Record<string, unknown> | null
        throw classifyInboxActError(res.status, body)
      }
      return (await res.json()) as InboxActResult
    },
    retry: false,
    onSuccess: (result, { id, action }) => {
      reconcile(id, "resolved", (it) => ({
        ...it,
        state: "resolved",
        resolved_action: action,
        resolved_at: result.receipt?.acted_at ?? it.resolved_at,
        resolved_by_user_id: result.receipt?.acted_by ?? it.resolved_by_user_id,
        // Exactly where the server merges it, so a card rendered from the
        // cache and one from a fresh GET read the same field.
        payload: { ...(it.payload ?? {}), receipt: result.receipt },
      }))
    },
    onError: (err) => {
      // Somebody else finished first. That is the queue working: pull the
      // server's version of the card so the pane shows THEIR decision. The
      // page-level error banner stays quiet — the card says what happened.
      if (err.code === "already_acted" || err.code === "concurrent") {
        if (workspaceId) void qc.invalidateQueries({ queryKey: inboxKeys.all(workspaceId) })
      }
    },
  })
  const { mutateAsync: actAsync } = actMutation

  const actOnInboxItem = useCallback(
    async (id: string, action: InboxAction, input?: string): Promise<InboxActResult> => {
      if (!workspaceId) throw new InboxActError("no workspace", { status: 0, code: "other" })
      return actAsync({ id, action, input })
    },
    [actAsync, workspaceId],
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
    actOnInboxItem,
  }
}

/**
 * Fetch the server-enriched representation for the selected row.
 *
 * List responses deliberately skip expensive, decision-specific evidence.
 * Rendering a list row as the reading pane therefore hid the credential facts
 * the server already exposed from GET /inbox/{id}. Inbox v2 selects cheaply
 * from the list, then upgrades only the open row through this hook.
 */
export function useInboxItem(
  workspaceId: string | null | undefined,
  id: string | null | undefined,
) {
  const query = useQuery<InboxItem>({
    queryKey: inboxKeys.detail(workspaceId ?? "", id ?? ""),
    queryFn: async ({ signal }) => {
      const res = await apiFetch(
        `/api/v1/inbox/${encodeURIComponent(id!)}?workspace_id=${encodeURIComponent(workspaceId!)}`,
        { signal },
      )
      if (!res.ok) throw new Error(`inbox detail: ${res.status}`)
      return (await res.json()) as InboxItem
    },
    enabled: Boolean(workspaceId && id),
    retry: false,
  })

  useInboxRealtimeInvalidation(workspaceId)
  return query
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

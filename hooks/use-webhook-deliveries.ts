"use client"

/**
 * The webhook delivery ledger's data layer
 * (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §9).
 *
 * Before the ledger existed the only record of a delivery was four columns on
 * the endpoint row — last fired, last status, last run, a counter — which
 * cannot answer "did you receive this one" or "what happened to it". These
 * hooks are the read side of the answer.
 *
 * Two things this layer deliberately never has:
 *
 *   - the raw body. `GET …/webhook-deliveries/{id}` does not return it
 *     (internal/api/webhook_deliveries.go), because a signed third-party
 *     payload can carry anything the sender put in it. What comes back
 *     instead is `raw_body_available` — which is exactly the field the UI
 *     needs, since its only question is whether to offer the replay button
 *     at all.
 *   - a signing secret. §9's credential rule: secrets are shown at creation
 *     and never by a list or a log.
 */

import { useCallback } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"

import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"

// ── Wire types — mirror internal/api/webhook_deliveries.go exactly ──────────

/** "accepted" or "ignored". An ignored delivery is still recorded, so a ping
 *  or a filtered event is auditable rather than invisible. */
export type DeliveryDecision = "accepted" | "ignored"

export interface WebhookDelivery {
  id: string
  workspace_id: string
  endpoint_id: string
  /** Which surface accepted it: agent, routine or page. */
  endpoint_kind: string
  /** The signature profile that VERIFIED it — recorded, never inferred. */
  profile: string
  source_delivery_id: string
  event_type: string
  event_action: string
  signing_key_id: string
  body_sha256: string
  body_bytes: number
  filter_decision: DeliveryDecision
  filter_reason: string
  target_revision: string
  /** null for an ignored delivery: nothing was dispatched. */
  work_id: string | null
  received_at: string
  dedup_expires_at: string
  /** Whether a replay can still reproduce this delivery. */
  raw_body_available: boolean
  raw_body_expires_at: string | null
}

export interface WebhookDeliveryPage {
  items: WebhookDelivery[]
  next_cursor: string | null
}

export interface DeliveryFilters {
  endpointId?: string | null
  decision?: DeliveryDecision | null
  eventType?: string | null
  after?: string | null
}

// ── Query keys ─────────────────────────────────────────────────────────────

/** `[resource, workspaceId, params?]` — CONTRIBUTING.md, and the same shape
 *  `pagesKeys` / `inboxKeys` / `poolKeys` use. The workspace id is IN the key,
 *  not merely in the URL, so two workspaces can never share a cache entry. */
export const webhookDeliveryKeys = {
  /** Everything for one workspace; the invalidation scope. */
  all: (workspaceId: string) => ["webhook-deliveries", workspaceId] as const,
  list: (workspaceId: string, filters: DeliveryFilters = {}) =>
    ["webhook-deliveries", workspaceId, { view: "list", ...deliveryQueryParams(filters) }] as const,
  detail: (workspaceId: string, deliveryId: string) =>
    ["webhook-deliveries", workspaceId, { id: deliveryId }] as const,
}

/** The filter set as the server names it, with absent filters ABSENT rather
 *  than present-and-empty — so `{}` and `{decision: null}` hash to the same
 *  key and share one cache entry instead of fetching the same page twice. */
export function deliveryQueryParams(filters: DeliveryFilters): Record<string, string> {
  const params: Record<string, string> = {}
  if (filters.endpointId) params.endpoint_id = filters.endpointId
  if (filters.decision) params.decision = filters.decision
  if (filters.eventType) params.event_type = filters.eventType
  if (filters.after) params.after = filters.after
  return params
}

// ── Transport ──────────────────────────────────────────────────────────────

/** An HTTP failure that keeps its status, so a 404 gets its own treatment —
 *  "we never saw it" is a different answer from "we cannot read it". */
export class DeliveryRequestError extends Error {
  readonly status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = "DeliveryRequestError"
    this.status = status
  }
}

async function fetchDeliveries<T>(url: string, signal: AbortSignal | undefined, what: string): Promise<T> {
  const res = await apiFetch(url, { signal })
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { error?: string } | null
    throw new DeliveryRequestError(res.status, body?.error ?? `${what} (HTTP ${res.status})`)
  }
  return (await res.json()) as T
}

function base(workspaceId: string): string {
  return `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/webhook-deliveries`
}

// ── Realtime ───────────────────────────────────────────────────────────────

/**
 * Invalidation is the CONSUMER's job (hooks/use-realtime.tsx), and this
 * surface has no push of its own yet: nothing in internal/ broadcasts a
 * `webhook.delivered`. So the two signals it CAN honestly use are
 *
 *   - `realtime.reconnected`, the synthetic event the provider dispatches
 *     after the socket comes back. Every push during the gap is lost, so the
 *     only correct response is to re-read the authoritative snapshot. §9:
 *     "after a reconnect the client fetches an authoritative snapshot."
 *   - `run.*`, because an accepted delivery is what produced the run.
 *
 * Safe variants throughout: a delivery list must stay renderable (and unit
 * testable) outside the dashboard layout, where no RealtimeProvider is mounted.
 */
function useDeliveryRealtime(workspaceId: string | null | undefined): void {
  const qc = useQueryClient()
  const invalidate = useCallback(() => {
    if (!workspaceId) return
    qc.invalidateQueries({ queryKey: webhookDeliveryKeys.all(workspaceId) })
  }, [qc, workspaceId])

  useRealtimeEventSafe("realtime.reconnected", invalidate)
  useRealtimeEventSafe("run.started", invalidate)
  useRealtimeEventSafe("run.completed", invalidate)
  useRealtimeEventSafe("run.failed", invalidate)
}

// ── Hooks ──────────────────────────────────────────────────────────────────

export function useWebhookDeliveries(
  workspaceId: string | null | undefined,
  filters: DeliveryFilters = {},
) {
  useDeliveryRealtime(workspaceId)
  const params = deliveryQueryParams(filters)
  const query = useQuery({
    queryKey: webhookDeliveryKeys.list(workspaceId ?? "", filters),
    enabled: Boolean(workspaceId),
    queryFn: ({ signal }) => {
      const qs = new URLSearchParams(params).toString()
      return fetchDeliveries<WebhookDeliveryPage>(
        `${base(workspaceId as string)}${qs ? `?${qs}` : ""}`,
        signal,
        "Could not read the delivery ledger",
      )
    },
  })
  return {
    deliveries: query.data?.items ?? [],
    nextCursor: query.data?.next_cursor ?? null,
    loading: query.isPending && Boolean(workspaceId),
    error: query.error as DeliveryRequestError | null,
    refetch: query.refetch,
  }
}

/**
 * One delivery. `notFound` is separated from `error` on purpose: the ledger
 * outliving the payload means "we received it, we can no longer replay it",
 * which is a different sentence from "we never saw it" — and the replay
 * affordance reads both.
 */
export function useWebhookDelivery(
  workspaceId: string | null | undefined,
  deliveryId: string | null | undefined,
) {
  useDeliveryRealtime(workspaceId)
  const query = useQuery({
    queryKey: webhookDeliveryKeys.detail(workspaceId ?? "", deliveryId ?? ""),
    enabled: Boolean(workspaceId) && Boolean(deliveryId),
    retry: false,
    queryFn: ({ signal }) =>
      fetchDeliveries<WebhookDelivery>(
        `${base(workspaceId as string)}/${encodeURIComponent(deliveryId as string)}`,
        signal,
        "Could not read the delivery",
      ),
  })
  const error = query.error as DeliveryRequestError | null
  return {
    delivery: query.data ?? null,
    loading: query.isPending && Boolean(workspaceId) && Boolean(deliveryId),
    notFound: error?.status === 404,
    error: error?.status === 404 ? null : error,
    refetch: query.refetch,
  }
}

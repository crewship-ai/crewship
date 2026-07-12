"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useApiResource, type UseApiResourceState } from "@/hooks/use-api-resource"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import {
  agentSpendResponseSchema,
  crewSpendResponseSchema,
  subscriptionUsageResponseSchema,
  topSpendersResponseSchema,
  type AgentSpendResponse,
  type CrewSpendResponse,
  type PaymasterRange,
  type SubscriptionUsageResponse,
  type TopSpendersResponse,
} from "@/lib/types/paymaster"

/**
 * Shared fetch state shape. `notConfigured=true` when the endpoint 404s —
 * the page uses this to show a "Paymaster not yet configured" empty state
 * instead of a generic error.
 *
 * All four hooks below are thin wrappers over the generic
 * `useApiResource`: 404→notConfigured, non-2xx→`HTTP <status>`, rejected
 * fetch→"Network error", and a schema parse failure degrades to empty
 * rows (`{ rows: [] }`) rather than a hard error. `reloadKey` forces a
 * refetch without swapping `range` to a different value and back (which
 * would fire two fetches).
 */
type FetchState<T> = UseApiResourceState<T>

/**
 * Liveness for the spend surfaces: bump a counter (debounced 2s so a burst
 * of terminal events causes ONE refetch) whenever a run or routine run
 * finishes — both carry cost_usd, i.e. the moment the ledger changed.
 * Provider-tolerant (useRealtimeEventSafe): outside the dashboard layout
 * or in unit tests the hooks behave exactly as before (manual reloadKey).
 */
function usePaymasterLiveKey(): number {
  const [liveKey, setLiveKey] = useState(0)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const bump = useCallback(() => {
    if (timerRef.current) return // one trailing bump per 2s window
    timerRef.current = setTimeout(() => {
      timerRef.current = null
      setLiveKey((k) => k + 1)
    }, 2000)
  }, [])
  useEffect(() => () => {
    if (timerRef.current) clearTimeout(timerRef.current)
  }, [])
  useRealtimeEventSafe("run.completed", bump)
  useRealtimeEventSafe("pipeline.run.completed", bump)
  return liveKey
}

/** Fetch `/api/v1/paymaster/spend/by-crew?range=…`. 404 → notConfigured. */
export function useCrewSpend(range: PaymasterRange, enabled = true, reloadKey = 0): FetchState<CrewSpendResponse> {
  const liveKey = usePaymasterLiveKey()
  const { data, loading, error, notConfigured } = useApiResource<CrewSpendResponse>(
    `/api/v1/paymaster/spend/by-crew?range=${range}`,
    { schema: crewSpendResponseSchema, fallback: { rows: [] }, enabled, reloadKey: reloadKey + liveKey },
  )
  return { data, loading, error, notConfigured }
}

/** Spend drill-down for a specific crew. `crewId=null` → hook is disabled. */
export function useAgentSpend(
  crewId: string | null,
  range: PaymasterRange,
  reloadKey = 0,
): FetchState<AgentSpendResponse> {
  const liveKey = usePaymasterLiveKey()
  const { data, loading, error, notConfigured } = useApiResource<AgentSpendResponse>(
    crewId
      ? `/api/v1/paymaster/spend/by-agent/${encodeURIComponent(crewId)}?range=${encodeURIComponent(range)}`
      : null,
    { schema: agentSpendResponseSchema, fallback: { rows: [] }, resetOnDisable: true, reloadKey: reloadKey + liveKey },
  )
  return { data, loading, error, notConfigured }
}

/** Top spenders — shared by the KPI card and the "Top Spenders" table. */
export function useTopSpenders(range: PaymasterRange, limit = 10, reloadKey = 0): FetchState<TopSpendersResponse> {
  const liveKey = usePaymasterLiveKey()
  const { data, loading, error, notConfigured } = useApiResource<TopSpendersResponse>(
    `/api/v1/paymaster/top-spenders?range=${range}&limit=${limit}`,
    { schema: topSpendersResponseSchema, fallback: { rows: [] }, reloadKey: reloadKey + liveKey },
  )
  return { data, loading, error, notConfigured }
}

/**
 * Subscription plan usage — flat-rate credentials grouped by plan label.
 * Returned rows have NO $ figure (subscription = flat fee covered the
 * call). UI uses CallCount + LastTS to surface "Anthropic Max — 47 calls,
 * last used 14m ago" alongside the metered $ totals.
 */
export function useSubscriptionUsage(
  range: PaymasterRange,
  reloadKey = 0,
): FetchState<SubscriptionUsageResponse> {
  const liveKey = usePaymasterLiveKey()
  const { data, loading, error, notConfigured } = useApiResource<SubscriptionUsageResponse>(
    `/api/v1/paymaster/subscriptions?range=${range}`,
    { schema: subscriptionUsageResponseSchema, fallback: { rows: [] }, reloadKey: reloadKey + liveKey },
  )
  return { data, loading, error, notConfigured }
}

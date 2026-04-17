"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import {
  agentSpendResponseSchema,
  crewSpendResponseSchema,
  topSpendersResponseSchema,
  type AgentSpendResponse,
  type CrewSpendResponse,
  type PaymasterRange,
  type TopSpendersResponse,
} from "@/lib/types/paymaster"

/**
 * Shared fetch state shape. `notConfigured=true` when the endpoint 404s —
 * the page uses this to show a "Paymaster not yet configured" empty state
 * instead of a generic error.
 */
interface FetchState<T> {
  data: T | null
  loading: boolean
  error: string | null
  notConfigured: boolean
}

const INITIAL: FetchState<never> = { data: null, loading: true, error: null, notConfigured: false }

/**
 * Fetch `/api/v1/paymaster/spend/by-crew?range=…`. Refetches whenever
 * `range` or `enabled` changes. 404 → notConfigured; 5xx → error.
 */
export function useCrewSpend(range: PaymasterRange, enabled = true): FetchState<CrewSpendResponse> {
  const [state, setState] = useState<FetchState<CrewSpendResponse>>({ ...INITIAL } as FetchState<CrewSpendResponse>)
  const reqIdRef = useRef(0)

  useEffect(() => {
    if (!enabled) {
      setState((s) => ({ ...s, loading: false }))
      return
    }
    const reqId = ++reqIdRef.current
    setState((s) => ({ ...s, loading: true, error: null }))
    ;(async () => {
      try {
        const res = await fetch(`/api/v1/paymaster/spend/by-crew?range=${range}`)
        if (reqIdRef.current !== reqId) return
        if (res.status === 404) {
          setState({ data: null, loading: false, error: null, notConfigured: true })
          return
        }
        if (!res.ok) {
          setState({ data: null, loading: false, error: `HTTP ${res.status}`, notConfigured: false })
          return
        }
        const json = await res.json()
        const parsed = crewSpendResponseSchema.safeParse(json)
        if (reqIdRef.current !== reqId) return
        if (!parsed.success) {
          setState({ data: { rows: [] }, loading: false, error: null, notConfigured: false })
          return
        }
        setState({ data: parsed.data, loading: false, error: null, notConfigured: false })
      } catch {
        if (reqIdRef.current === reqId) {
          setState({ data: null, loading: false, error: "Network error", notConfigured: false })
        }
      }
    })()
  }, [range, enabled])

  return state
}

/** Spend drill-down for a specific crew. `crewId=null` → hook is disabled. */
export function useAgentSpend(
  crewId: string | null,
  range: PaymasterRange,
): FetchState<AgentSpendResponse> {
  const [state, setState] = useState<FetchState<AgentSpendResponse>>({ ...INITIAL } as FetchState<AgentSpendResponse>)
  const reqIdRef = useRef(0)

  useEffect(() => {
    if (!crewId) {
      setState({ data: null, loading: false, error: null, notConfigured: false })
      return
    }
    const reqId = ++reqIdRef.current
    setState((s) => ({ ...s, loading: true, error: null }))
    ;(async () => {
      try {
        const res = await fetch(`/api/v1/paymaster/spend/by-agent/${encodeURIComponent(crewId)}?range=${encodeURIComponent(range)}`)
        if (reqIdRef.current !== reqId) return
        if (res.status === 404) {
          setState({ data: null, loading: false, error: null, notConfigured: true })
          return
        }
        if (!res.ok) {
          setState({ data: null, loading: false, error: `HTTP ${res.status}`, notConfigured: false })
          return
        }
        const json = await res.json()
        const parsed = agentSpendResponseSchema.safeParse(json)
        if (reqIdRef.current !== reqId) return
        if (!parsed.success) {
          setState({ data: { rows: [] }, loading: false, error: null, notConfigured: false })
          return
        }
        setState({ data: parsed.data, loading: false, error: null, notConfigured: false })
      } catch {
        if (reqIdRef.current === reqId) {
          setState({ data: null, loading: false, error: "Network error", notConfigured: false })
        }
      }
    })()
  }, [crewId, range])

  return state
}

/** Top spenders — shared by the KPI card and the "Top Spenders" table. */
export function useTopSpenders(range: PaymasterRange, limit = 10): FetchState<TopSpendersResponse> {
  const [state, setState] = useState<FetchState<TopSpendersResponse>>({ ...INITIAL } as FetchState<TopSpendersResponse>)
  const reqIdRef = useRef(0)
  const fetcher = useCallback(async () => {
    const reqId = ++reqIdRef.current
    setState((s) => ({ ...s, loading: true, error: null }))
    try {
      const res = await fetch(`/api/v1/paymaster/top-spenders?range=${range}&limit=${limit}`)
      if (reqIdRef.current !== reqId) return
      if (res.status === 404) {
        setState({ data: null, loading: false, error: null, notConfigured: true })
        return
      }
      if (!res.ok) {
        setState({ data: null, loading: false, error: `HTTP ${res.status}`, notConfigured: false })
        return
      }
      const json = await res.json()
      const parsed = topSpendersResponseSchema.safeParse(json)
      if (reqIdRef.current !== reqId) return
      if (!parsed.success) {
        setState({ data: { rows: [] }, loading: false, error: null, notConfigured: false })
        return
      }
      setState({ data: parsed.data, loading: false, error: null, notConfigured: false })
    } catch {
      if (reqIdRef.current === reqId) {
        setState({ data: null, loading: false, error: "Network error", notConfigured: false })
      }
    }
  }, [range, limit])

  useEffect(() => {
    fetcher()
  }, [fetcher])

  return state
}

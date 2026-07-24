"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

// RoutineBudget mirrors internal/api.budgetResponse
// (GET/PATCH /pipelines/{slug}/budget, #1422 item 3). Distinct from a
// routine's DSL max_cost_usd (a per-run hard gate) — this is an
// out-of-band monthly spend cap + budget-vs-actual view.
export interface RoutineBudget {
  slug: string
  has_budget: boolean
  monthly_budget_usd: number
  month: string // "2026-07"
  spent_usd: number
  pct_used?: number
  over_budget?: boolean
}

// useRoutineBudget fetches one routine's monthly budget-vs-actual and
// exposes setBudget to update (or clear, with 0) the cap. Same
// stale-fetch + error-without-wipe ergonomics as usePipelineSchedules —
// a transient 5xx doesn't blank an already-rendered meter.
export function useRoutineBudget(
  workspaceId: string | null | undefined,
  slug: string | null | undefined,
) {
  const [budget, setBudgetState] = useState<RoutineBudget | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId || !slug) {
      setBudgetState(null)
      return
    }
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/pipelines/${encodeURIComponent(slug)}/budget`,
        { signal: controller.signal },
      )
      if (controller.signal.aborted) return
      if (!res.ok) {
        // 503 = run history store not wired (test server / build
        // without DB) — degrade to "no budget data" rather than a
        // hard error so the detail panel still renders.
        if (res.status === 503 || res.status === 404) {
          setBudgetState(null)
          setLoading(false)
          return
        }
        setError(`routine budget: ${res.status}`)
        setLoading(false)
        return
      }
      const data: RoutineBudget = await res.json()
      if (controller.signal.aborted) return
      setBudgetState(data)
    } catch (e) {
      if (controller.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!controller.signal.aborted) setLoading(false)
    }
  }, [workspaceId, slug])

  useEffect(() => {
    refresh()
    return () => abortRef.current?.abort()
  }, [refresh])

  const setBudget = useCallback(
    async (monthlyBudgetUsd: number): Promise<RoutineBudget | null> => {
      if (!workspaceId || !slug) return null
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/pipelines/${encodeURIComponent(slug)}/budget`,
        {
          method: "PATCH",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ monthly_budget_usd: monthlyBudgetUsd }),
        },
      )
      if (!res.ok) {
        const txt = await res.text()
        throw new Error(`set budget failed: ${res.status} ${txt}`)
      }
      const out: RoutineBudget = await res.json()
      setBudgetState(out)
      return out
    },
    [workspaceId, slug],
  )

  return { budget, loading, error, refresh, setBudget }
}

// BudgetSummaryRow / BudgetSummary mirror
// internal/api.budgetSummaryRow / budgetSummaryResponse
// (GET /pipelines/budget-summary) — the workspace roll-up.
export interface BudgetSummaryRow {
  slug: string
  monthly_budget_usd: number
  spent_usd: number
  pct_used?: number
  over_budget?: boolean
}

export interface BudgetSummary {
  month: string
  routines: BudgetSummaryRow[]
  total_budget_usd: number
  total_spent_usd: number
}

// useBudgetSummary fetches the workspace-wide budget roll-up: every
// routine with a budget set OR spend this month.
export function useBudgetSummary(workspaceId: string | null | undefined) {
  const [summary, setSummary] = useState<BudgetSummary | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setSummary(null)
      return
    }
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/pipelines/budget-summary`,
        { signal: controller.signal },
      )
      if (controller.signal.aborted) return
      if (!res.ok) {
        if (res.status === 503) {
          setSummary(null)
          setLoading(false)
          return
        }
        setError(`budget summary: ${res.status}`)
        setLoading(false)
        return
      }
      const data: BudgetSummary = await res.json()
      if (controller.signal.aborted) return
      setSummary(data)
    } catch (e) {
      if (controller.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!controller.signal.aborted) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    refresh()
    return () => abortRef.current?.abort()
  }, [refresh])

  return { summary, loading, error, refresh }
}

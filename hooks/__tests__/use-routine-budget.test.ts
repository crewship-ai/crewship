import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, waitFor, act } from "@testing-library/react"

import { useRoutineBudget, useBudgetSummary } from "@/hooks/use-routine-budget"

function okJSON(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response
}

function errStatus(status: number): Response {
  return {
    ok: false,
    status,
    json: async () => ({ error: "x" }),
    text: async () => "error",
  } as unknown as Response
}

const BUDGET = {
  slug: "my-routine",
  has_budget: true,
  monthly_budget_usd: 50,
  month: "2026-07",
  spent_usd: 12.5,
  pct_used: 25,
  over_budget: false,
}

beforeEach(() => {
  global.fetch = vi.fn()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe("useRoutineBudget", () => {
  it("fetches the budget on mount, scoped to workspace + slug", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(okJSON(BUDGET))
    const { result } = renderHook(() => useRoutineBudget("ws1", "my-routine"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.error).toBeNull()
    expect(result.current.budget?.monthly_budget_usd).toBe(50)
    const url = (global.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).toContain("/api/v1/workspaces/ws1/pipelines/my-routine/budget")
  })

  it("does not fetch without a workspace id or slug", async () => {
    const { result } = renderHook(() => useRoutineBudget(null, "my-routine"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(global.fetch).not.toHaveBeenCalled()
    expect(result.current.budget).toBeNull()
  })

  it("degrades to null (not an error) on 404/503", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(errStatus(404))
    const { result } = renderHook(() => useRoutineBudget("ws1", "ghost"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBeNull()
    expect(result.current.budget).toBeNull()
  })

  it("surfaces a real error on 500", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(errStatus(500))
    const { result } = renderHook(() => useRoutineBudget("ws1", "my-routine"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toContain("500")
  })

  it("setBudget PATCHes and updates local state", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce(okJSON({ ...BUDGET, has_budget: false, monthly_budget_usd: 0 }))
      .mockResolvedValueOnce(okJSON({ ...BUDGET, monthly_budget_usd: 100, pct_used: 12.5 }))
    const { result } = renderHook(() => useRoutineBudget("ws1", "my-routine"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    let updated: Awaited<ReturnType<typeof result.current.setBudget>>
    await act(async () => {
      updated = await result.current.setBudget(100)
    })
    expect(updated?.monthly_budget_usd).toBe(100)
    expect(result.current.budget?.monthly_budget_usd).toBe(100)

    const patchCall = (global.fetch as ReturnType<typeof vi.fn>).mock.calls[1]
    expect(patchCall[0]).toContain("/pipelines/my-routine/budget")
    expect(patchCall[1]?.method).toBe("PATCH")
    expect(JSON.parse(patchCall[1]?.body as string)).toEqual({ monthly_budget_usd: 100 })
  })
})

describe("useBudgetSummary", () => {
  it("fetches the workspace roll-up", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      okJSON({
        month: "2026-07",
        routines: [{ slug: "a", monthly_budget_usd: 100, spent_usd: 40, pct_used: 40 }],
        total_budget_usd: 100,
        total_spent_usd: 40,
      }),
    )
    const { result } = renderHook(() => useBudgetSummary("ws1"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.summary?.routines).toHaveLength(1)
    expect(result.current.summary?.total_spent_usd).toBe(40)
    const url = (global.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).toContain("/api/v1/workspaces/ws1/pipelines/budget-summary")
  })

  it("does not fetch without a workspace id", async () => {
    const { result } = renderHook(() => useBudgetSummary(null))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(global.fetch).not.toHaveBeenCalled()
    expect(result.current.summary).toBeNull()
  })
})

import { describe, it, expect, vi, afterEach } from "vitest"
import { renderHook, waitFor, cleanup } from "@testing-library/react"

import { crewSpend30d, useCrewSpend } from "@/components/features/crews/crew-spend"

afterEach(() => { cleanup(); vi.restoreAllMocks() })

describe("crewSpend30d", () => {
  it("reads the crew's own row and every agent's cost", () => {
    const spend = crewSpend30d(
      { rows: [{ crew_id: "c2", cost_usd: 9, call_count: 1 }, { crew_id: "c1", cost_usd: 1.1, call_count: 12 }] },
      { rows: [{ agent_id: "a1", cost_usd: 0.8, call_count: 9 }, { agent_id: "a2", cost_usd: 0.3, call_count: 3 }] },
      "c1",
    )
    expect(spend).toEqual({ costUsd: 1.1, calls: 12, byAgent: new Map([["a1", 0.8], ["a2", 0.3]]) })
  })

  it("a crew with no row has spent nothing, which is not the same as unknown", () => {
    expect(crewSpend30d({ rows: [] }, { rows: [] }, "c1")).toEqual({ costUsd: 0, calls: 0, byAgent: new Map() })
  })

  it("a malformed answer is unknown, never zero", () => {
    expect(crewSpend30d({ nope: true }, { rows: [] }, "c1")).toBeNull()
    expect(crewSpend30d({ rows: [] }, null, "c1")).toBeNull()
  })
})

describe("useCrewSpend", () => {
  it("asks nothing of a reader who may not see spend", () => {
    const fetchSpy = vi.spyOn(global, "fetch")
    const { result } = renderHook(() => useCrewSpend("ws1", "c1", false))
    expect(fetchSpy).not.toHaveBeenCalled()
    expect(result.current).toBeNull()
  })

  it("reads the 30-day window per crew and per agent", async () => {
    const urls: string[] = []
    vi.spyOn(global, "fetch").mockImplementation(async (url) => {
      urls.push(String(url))
      return new Response(JSON.stringify(String(url).includes("/by-agent/")
        ? { rows: [{ agent_id: "a1", cost_usd: 2, call_count: 4 }] }
        : { rows: [{ crew_id: "c1", cost_usd: 2, call_count: 4 }] }))
    })
    const { result } = renderHook(() => useCrewSpend("ws1", "c1", true))
    await waitFor(() => expect(result.current).not.toBeNull())
    expect(result.current?.costUsd).toBe(2)
    expect(urls.some((u) => u.includes("/api/v1/paymaster/spend/by-crew") && u.includes("range=30d"))).toBe(true)
    expect(urls.some((u) => u.includes("/api/v1/paymaster/spend/by-agent/c1") && u.includes("range=30d"))).toBe(true)
  })

  it("stays unknown when a call fails", async () => {
    vi.spyOn(global, "fetch").mockResolvedValue(new Response("{}", { status: 500 }))
    const { result } = renderHook(() => useCrewSpend("ws1", "c1", true))
    await new Promise((r) => setTimeout(r, 30))
    expect(result.current).toBeNull()
  })
})

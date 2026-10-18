import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"
import { apiFetch } from "@/lib/api-fetch"
import { useQuickViewCounts } from "@/hooks/use-quick-view-counts"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

const TOTALS: Record<string, string> = { "mine=1": "3", "needs=reply": "1", "working=1": "2", "needs=review": "0", "blocked=1": "4", "": "40" }

function respond(url: string) {
  const p = new URL(url, "http://localhost").searchParams
  const key = p.get("mine") ? "mine=1" : p.get("needs") ? `needs=${p.get("needs")}` : p.get("working") ? "working=1" : p.get("blocked") ? "blocked=1" : ""
  return Promise.resolve({
    ok: true,
    status: 200,
    headers: { get: (k: string) => (k === "X-Total-Count" ? TOTALS[key] : null) },
    json: async () => [],
  } as unknown as Response)
}

describe("useQuickViewCounts", () => {
  beforeEach(() => vi.mocked(apiFetch).mockReset())

  it("reads each view's exact total from the server, not from a loaded page", async () => {
    vi.mocked(apiFetch).mockImplementation((u) => respond(String(u)))
    const { result } = renderHook(() => useQuickViewCounts("ws-1", {}))
    await waitFor(() => expect(result.current.all).toBe(40))
    expect(result.current).toEqual({ mine: 3, reply: 1, working: 2, review: 0, blocked: 4, all: 40 })
    for (const [u] of vi.mocked(apiFetch).mock.calls) {
      expect(new URL(String(u), "http://localhost").searchParams.get("limit")).toBe("1")
    }
  })

  it("keeps the crew scope", async () => {
    vi.mocked(apiFetch).mockImplementation((u) => respond(String(u)))
    renderHook(() => useQuickViewCounts("ws-1", { crewId: "c9" }))
    await waitFor(() => expect(vi.mocked(apiFetch)).toHaveBeenCalledTimes(6))
    for (const [u] of vi.mocked(apiFetch).mock.calls) {
      expect(new URL(String(u), "http://localhost").searchParams.get("crew_id")).toBe("c9")
    }
  })

  it("leaves a count unknown (null) when its request fails", async () => {
    vi.mocked(apiFetch).mockImplementation((u) =>
      new URL(String(u), "http://localhost").searchParams.get("blocked") ? Promise.reject(new Error("down")) : respond(String(u)),
    )
    const { result } = renderHook(() => useQuickViewCounts("ws-1", {}))
    await waitFor(() => expect(result.current.all).toBe(40))
    expect(result.current.blocked).toBeNull()
  })
})

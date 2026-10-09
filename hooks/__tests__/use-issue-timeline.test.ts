import { afterEach, describe, expect, it, vi } from "vitest"
import { act, renderHook, waitFor } from "@testing-library/react"

const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))

import { useIssueTimeline } from "../use-issue-timeline"

const json = (body: unknown, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json", ...headers } })

function serve(url: string): Response {
  const u = new URL(url, "http://x")
  if (u.pathname === "/api/v1/issues/msn_1") return json({ id: "msn_1", identifier: "OPS-1", crew_id: "crew_1", title: "T" })
  if (u.pathname.endsWith("/events")) {
    const before = u.searchParams.get("before_seq")
    if (before === "0") return json({ events: [ev(9, "2026-10-09"), ev(10, "2026-10-10")], has_older: true, latest_seq: 10 })
    return json({ events: [ev(8, "2026-10-08")], has_older: false, latest_seq: 10 })
  }
  if (u.pathname.endsWith("/comments")) {
    if (!u.searchParams.get("before_id")) return json([cm("c2", "2026-10-07"), cm("c3", "2026-10-11")], { "X-Has-More": "true" })
    return json([cm("c1", "2026-10-06")], { "X-Has-More": "false" })
  }
  if (u.pathname.endsWith("/runs")) return json([{ id: "a1", run_id: "run_1", status: "completed", started_at: "2026-10-12T00:00:00Z" }], { "X-Total-Count": "1" })
  return new Response("{}", { status: 404 })
}
const ev = (seq: number, day: string) => ({ id: `e${seq}`, seq, actor_type: "user", action: "status_changed", created_at: `${day}T00:00:00Z` })
const cm = (id: string, day: string) => ({ id, author_type: "user", body: id, created_at: `${day}T00:00:00Z` })

afterEach(() => fetchMock.mockReset())

describe("useIssueTimeline (#2983)", () => {
  it("reads events, comments and runs by issue and pages each on its own cursor", async () => {
    fetchMock.mockImplementation(async (url: string) => serve(url))
    const { result } = renderHook(() => useIssueTimeline("ws_1", "msn_1"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    const urls = fetchMock.mock.calls.map(([u]) => String(u))
    expect(urls.some((u) => u.startsWith("/api/v1/crews/crew_1/issues/msn_1/events?") && u.includes("before_seq=0"))).toBe(true)
    expect(urls.some((u) => u.startsWith("/api/v1/crews/crew_1/issues/msn_1/comments?") && u.includes("page_size="))).toBe(true)
    expect(urls.some((u) => u.startsWith("/api/v1/crews/crew_1/issues/msn_1/runs?") && u.includes("offset=0"))).toBe(true)

    // Below the newest page's watermark (events' oldest loaded is 10-09,
    // comments' 10-07 → the watermark is 10-09) nothing is shown yet.
    expect(result.current.items.map((i) => i.key)).toEqual(["run:a1", "cm:c3", "ev:e10", "ev:e9"])
    expect(result.current.canLoadOlder).toBe(true)

    await act(async () => {
      await result.current.loadOlder()
    })
    const older = fetchMock.mock.calls.map(([u]) => String(u))
    expect(older.some((u) => u.includes("/events?") && u.includes("before_seq=9"))).toBe(true)
    expect(older.some((u) => u.includes("/comments?") && u.includes("before_id=c2"))).toBe(true)
    expect(result.current.items.map((i) => i.key)).toEqual(["run:a1", "cm:c3", "ev:e10", "ev:e9", "ev:e8", "cm:c2", "cm:c1"])
    expect(result.current.canLoadOlder).toBe(false)
  })

  it("reports an issue it may not read instead of an empty history", async () => {
    fetchMock.mockImplementation(async () => new Response("{}", { status: 404 }))
    const { result } = renderHook(() => useIssueTimeline("ws_1", "msn_x"))
    await waitFor(() => expect(result.current.error).toBe("This issue is not available."))
    expect(result.current.items).toEqual([])
  })
})

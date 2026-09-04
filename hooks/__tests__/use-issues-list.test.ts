import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"
import { apiFetch } from "@/lib/api-fetch"
import { useIssuesList } from "@/hooks/use-issues-list"
import type { Mission } from "@/lib/types/mission"

// #2286: a fetch error used to leave `issues` at [] with nothing to tell it
// apart from a genuinely empty workspace. #2285: a 403 — the shape a
// scoped agent/CLI token failure takes (internal/api/middleware.go's
// AuthKindCLIToken, "the PAT analogue an agent, CI job, or script holds")
// — used to collapse into that same silent empty board. This hook must
// always resolve to a distinguishable state: issues, loading, or a typed
// error.

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(),
}))

function issue(id: string): Mission {
  return {
    id,
    title: `issue ${id}`,
    workspace_id: "ws-1",
    crew_id: "crew-1",
    lead_agent_id: "agent-1",
    trace_id: "trace-1",
    status: "TODO",
  } as Mission
}

function jsonResponse(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  const h = new Map(Object.entries(headers))
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: { get: (k: string) => h.get(k) ?? null },
    json: async () => body,
  } as unknown as Response
}

describe("useIssuesList", () => {
  beforeEach(() => {
    vi.mocked(apiFetch).mockReset()
  })

  it("loads issues and reports the total from X-Total-Count", async () => {
    vi.mocked(apiFetch).mockResolvedValue(
      jsonResponse(200, [issue("i1"), issue("i2")], { "X-Total-Count": "2", "X-Has-More": "false" }),
    )
    const { result } = renderHook(() => useIssuesList("ws-1"))

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.issues.map((i) => i.id)).toEqual(["i1", "i2"])
    expect(result.current.error).toBeNull()
    expect(result.current.total).toBe(2)
    expect(result.current.hasMore).toBe(false)
  })

  it("#2286: a rejected fetch renders as an error, not an empty board", async () => {
    vi.mocked(apiFetch).mockRejectedValue(new TypeError("network down"))
    const { result } = renderHook(() => useIssuesList("ws-1"))

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.issues).toEqual([])
    expect(result.current.error).not.toBeNull()
    expect(result.current.error?.kind).toBe("network")
  })

  it("#2286: a 500 response renders as an error, not an empty board", async () => {
    vi.mocked(apiFetch).mockResolvedValue(jsonResponse(500, { error: "boom" }))
    const { result } = renderHook(() => useIssuesList("ws-1"))

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.issues).toEqual([])
    expect(result.current.error?.kind).toBe("server")
    expect(result.current.error?.status).toBe(500)
  })

  it("#2285: a 403 on the agent/CLI-token path is classified distinctly, not silently empty", async () => {
    vi.mocked(apiFetch).mockResolvedValue(jsonResponse(403, { error: "unrecognized agent token" }))
    const { result } = renderHook(() => useIssuesList("ws-1"))

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.issues).toEqual([])
    expect(result.current.error?.kind).toBe("forbidden")
    expect(result.current.error?.status).toBe(403)
    // Actionable: names the likely cause, not a generic "something broke".
    expect(result.current.error?.message.toLowerCase()).toContain("token")
  })

  it("#2286: 101 issues surfaces hasMore + total rather than silently capping at 100", async () => {
    const page1 = Array.from({ length: 100 }, (_, i) => issue(`i${i}`))
    vi.mocked(apiFetch).mockResolvedValue(jsonResponse(200, page1, { "X-Total-Count": "101", "X-Has-More": "true" }))
    const { result } = renderHook(() => useIssuesList("ws-1"))

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.issues).toHaveLength(100)
    expect(result.current.total).toBe(101)
    expect(result.current.hasMore).toBe(true)
  })

  it("loadMore appends the next page instead of replacing the list", async () => {
    const page1 = [issue("i1"), issue("i2")]
    const page2 = [issue("i3")]
    vi.mocked(apiFetch)
      .mockResolvedValueOnce(jsonResponse(200, page1, { "X-Total-Count": "3", "X-Has-More": "true" }))
      .mockResolvedValueOnce(jsonResponse(200, page2, { "X-Total-Count": "3", "X-Has-More": "false" }))
    const { result } = renderHook(() => useIssuesList("ws-1"))

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.issues).toHaveLength(2)

    await result.current.loadMore()
    await waitFor(() => expect(result.current.issues).toHaveLength(3))
    expect(result.current.issues.map((i) => i.id)).toEqual(["i1", "i2", "i3"])
    expect(result.current.hasMore).toBe(false)

    // Next page's offset is how many rows are already loaded, not a fixed
    // ISSUES_PAGE_LIMIT stride — robust even when a page (like this test's
    // 2-row fixture) returns fewer than a full page.
    const secondCallUrl = String(vi.mocked(apiFetch).mock.calls[1][0])
    expect(secondCallUrl).toContain("offset=2")
  })

  it("a failed loadMore keeps the already-loaded issues on screen", async () => {
    const page1 = [issue("i1")]
    vi.mocked(apiFetch)
      .mockResolvedValueOnce(jsonResponse(200, page1, { "X-Total-Count": "2", "X-Has-More": "true" }))
      .mockResolvedValueOnce(jsonResponse(503, { error: "unavailable" }))
    const { result } = renderHook(() => useIssuesList("ws-1"))

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.issues).toHaveLength(1)

    await result.current.loadMore()
    await waitFor(() => expect(result.current.error).not.toBeNull())
    // The page that already rendered stays — only the append attempt failed.
    expect(result.current.issues).toHaveLength(1)
  })

  it("calling loadMore again after a failure retries the same page, not a new one", async () => {
    const page1 = [issue("i1")]
    const page2 = [issue("i2")]
    vi.mocked(apiFetch)
      .mockResolvedValueOnce(jsonResponse(200, page1, { "X-Total-Count": "2", "X-Has-More": "true" }))
      .mockResolvedValueOnce(jsonResponse(503, { error: "unavailable" }))
      .mockResolvedValueOnce(jsonResponse(200, page2, { "X-Total-Count": "2", "X-Has-More": "false" }))
    const { result } = renderHook(() => useIssuesList("ws-1"))

    await waitFor(() => expect(result.current.loading).toBe(false))
    await result.current.loadMore()
    await waitFor(() => expect(result.current.error).not.toBeNull())

    // This is the board's banner "Retry" action (orchestration-layout.tsx)
    // — it calls loadMore() again, not refetch(). Since the failed attempt
    // never advanced past 1 loaded row, retrying must ask for offset=1
    // again, and land the row the first attempt lost.
    await result.current.loadMore()
    await waitFor(() => expect(result.current.issues).toHaveLength(2))
    expect(result.current.error).toBeNull()
    const retryUrl = String(vi.mocked(apiFetch).mock.calls[2][0])
    expect(retryUrl).toContain("offset=1")
  })

  it("refetch clears a previous error once the request succeeds", async () => {
    vi.mocked(apiFetch)
      .mockResolvedValueOnce(jsonResponse(403, { error: "unrecognized agent token" }))
      .mockResolvedValueOnce(jsonResponse(200, [issue("i1")], { "X-Total-Count": "1", "X-Has-More": "false" }))
    const { result } = renderHook(() => useIssuesList("ws-1"))

    await waitFor(() => expect(result.current.error).not.toBeNull())

    await result.current.refetch()
    await waitFor(() => expect(result.current.error).toBeNull())
    expect(result.current.issues).toHaveLength(1)
  })

  it("refetch restores pages already loaded via loadMore instead of snapping back to page 1", async () => {
    // Mirrors OrchestrationLayout's handleIssueUpdated/onCreated, which call
    // refetch() after any issue edit or creation — a workspace with 150
    // issues where the user had clicked "Load more" once (200 loaded) must
    // not silently revert to 100 after an unrelated edit.
    const page1 = Array.from({ length: 100 }, (_, i) => issue(`p1-${i}`))
    const page2 = Array.from({ length: 50 }, (_, i) => issue(`p2-${i}`))
    vi.mocked(apiFetch)
      .mockResolvedValueOnce(jsonResponse(200, page1, { "X-Total-Count": "150", "X-Has-More": "true" }))
      .mockResolvedValueOnce(jsonResponse(200, page2, { "X-Total-Count": "150", "X-Has-More": "false" }))
    const { result } = renderHook(() => useIssuesList("ws-1"))

    await waitFor(() => expect(result.current.loading).toBe(false))
    await result.current.loadMore()
    await waitFor(() => expect(result.current.issues).toHaveLength(150))

    vi.mocked(apiFetch).mockReset()
    vi.mocked(apiFetch)
      .mockResolvedValueOnce(jsonResponse(200, page1, { "X-Total-Count": "150", "X-Has-More": "true" }))
      .mockResolvedValueOnce(jsonResponse(200, page2, { "X-Total-Count": "150", "X-Has-More": "false" }))

    await result.current.refetch()
    await waitFor(() => expect(result.current.issues).toHaveLength(150))
    expect(vi.mocked(apiFetch).mock.calls).toHaveLength(2)
    expect(String(vi.mocked(apiFetch).mock.calls[0][0])).toContain("offset=0")
    expect(String(vi.mocked(apiFetch).mock.calls[1][0])).toContain("offset=100")
  })

  it("refetch does not fetch extra pages when only one page had been loaded", async () => {
    vi.mocked(apiFetch).mockResolvedValue(
      jsonResponse(200, [issue("i1")], { "X-Total-Count": "1", "X-Has-More": "false" }),
    )
    const { result } = renderHook(() => useIssuesList("ws-1"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    vi.mocked(apiFetch).mockClear()
    await result.current.refetch()
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(apiFetch).toHaveBeenCalledTimes(1)
  })

  it("a committed search reaches the server as ?q= and re-fetches page 1 when it changes", async () => {
    vi.mocked(apiFetch).mockResolvedValue(
      jsonResponse(200, [issue("i1")], { "X-Total-Count": "1", "X-Has-More": "false" }),
    )
    const { result, rerender } = renderHook(({ q }: { q: string }) => useIssuesList("ws-1", { search: q }), {
      initialProps: { q: "" },
    })
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(String(vi.mocked(apiFetch).mock.calls[0][0])).not.toContain("q=")

    rerender({ q: "ENG-4 launch" })
    await waitFor(() => expect(vi.mocked(apiFetch).mock.calls.length).toBe(2))
    expect(String(vi.mocked(apiFetch).mock.calls[1][0])).toContain("offset=0&q=ENG-4%20launch")
  })

  it("does not fetch without a workspace id", () => {
    renderHook(() => useIssuesList(null))
    expect(apiFetch).not.toHaveBeenCalled()
  })
})

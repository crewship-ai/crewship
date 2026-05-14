import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act, waitFor } from "@testing-library/react"
import { useIssueDetail } from "@/hooks/use-issue-detail"
import type { Mission } from "@/lib/types/mission"

function mission(id: string, identifier = id.toUpperCase()): Mission {
  return {
    id,
    workspace_id: "ws-1",
    crew_id: "crew-1",
    lead_agent_id: "agent-1",
    lead_agent_name: "Lead",
    lead_agent_slug: "lead",
    trace_id: "trace-1",
    title: `Mission ${id}`,
    description: null,
    status: "PENDING",
    plan: null,
    workflow_template: null,
    total_token_count: null,
    identifier,
  } as Mission
}

/** Mints a fetch mock that resolves after `delayMs` so we can test interleaving. */
function deferred<T>(value: T, delayMs: number): Promise<{ ok: true; json: () => Promise<T> }> {
  return new Promise((resolve) => {
    setTimeout(() => resolve({ ok: true, json: async () => value }), delayMs)
  })
}

describe("useIssueDetail", () => {
  let mockFetch: ReturnType<typeof vi.fn>
  const fetchIssues = vi.fn(async () => {})
  const fetchProjects = vi.fn(async () => {})

  beforeEach(() => {
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
    fetchIssues.mockClear()
    fetchProjects.mockClear()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  function setup() {
    return renderHook(() =>
      useIssueDetail({ workspaceId: "ws-1", fetchIssues, fetchProjects }),
    )
  }

  it("starts with no selected issue and empty comments", () => {
    const { result } = setup()
    expect(result.current.selectedIssue).toBeNull()
    expect(result.current.issueComments).toEqual([])
  })

  it("handleIssueSelect opens an issue and loads its comments", async () => {
    const issue = mission("m1")
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => [{ id: "c1", body: "hi" }],
    })

    const { result } = setup()
    await act(async () => {
      await result.current.handleIssueSelect(issue)
    })

    expect(result.current.selectedIssue?.id).toBe("m1")
    expect(result.current.issueComments).toHaveLength(1)
    expect(mockFetch).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/crews/crew-1/issues/M1/comments?workspace_id=ws-1"),
    )
  })

  it("clicking the same issue toggles it closed", async () => {
    const issue = mission("m1")
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => [] })

    const { result } = setup()
    await act(async () => {
      await result.current.handleIssueSelect(issue)
    })
    expect(result.current.selectedIssue).not.toBeNull()

    await act(async () => {
      await result.current.handleIssueSelect(issue)
    })
    expect(result.current.selectedIssue).toBeNull()
    expect(result.current.issueComments).toEqual([])
  })

  it("handleIssueClose clears state", async () => {
    const issue = mission("m1")
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => [{ id: "c1" }] })

    const { result } = setup()
    await act(async () => {
      await result.current.handleIssueSelect(issue)
    })

    act(() => result.current.handleIssueClose())
    expect(result.current.selectedIssue).toBeNull()
    expect(result.current.issueComments).toEqual([])
  })

  it("guards against out-of-order comment fetches", async () => {
    // Slow fetch for issue A, fast for issue B. If the guard works, A's
    // late response must NOT smear its (single-item) comments over B's
    // (two-item) comments.
    const a = mission("a")
    const b = mission("b")
    mockFetch
      .mockImplementationOnce(() => deferred([{ id: "a-comment" }], 50))
      .mockImplementationOnce(() => deferred([{ id: "b1" }, { id: "b2" }], 5))

    const { result } = setup()

    // Start A (slow), do NOT await yet.
    let aPromise!: Promise<void>
    act(() => {
      aPromise = result.current.handleIssueSelect(a)
    })

    // Immediately switch to B (fast).
    await act(async () => {
      await result.current.handleIssueSelect(b)
    })
    expect(result.current.selectedIssue?.id).toBe("b")
    expect(result.current.issueComments).toHaveLength(2)

    // Now let A finish — its stale comments must NOT land.
    await act(async () => {
      await aPromise
    })
    expect(result.current.selectedIssue?.id).toBe("b")
    expect(result.current.issueComments).toHaveLength(2)
    expect(result.current.issueComments[0]).toMatchObject({ id: "b1" })
  })

  it("handleIssueUpdated refreshes the open issue without smearing on close", async () => {
    const a = mission("a")
    // Three mock calls: initial select comments, updated issue fetch (slow), updated comments fetch.
    mockFetch
      .mockResolvedValueOnce({ ok: true, json: async () => [{ id: "a-comment" }] })
      .mockImplementationOnce(() => deferred(mission("a"), 30))

    const { result } = setup()
    await act(async () => {
      await result.current.handleIssueSelect(a)
    })

    // Kick off an update, do NOT await.
    let updatedPromise!: Promise<void>
    act(() => {
      updatedPromise = result.current.handleIssueUpdated()
    })

    // User closes the panel mid-update.
    act(() => result.current.handleIssueClose())
    expect(result.current.selectedIssue).toBeNull()

    // Let the update finish — selectedIssue must remain null (guard works).
    await act(async () => {
      await updatedPromise
    })
    expect(result.current.selectedIssue).toBeNull()
  })
})

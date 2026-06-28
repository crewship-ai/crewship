import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act } from "@testing-library/react"
import { useIssueDetail } from "@/hooks/use-issue-detail"
import type { Mission } from "@/lib/types/mission"

function mission(id: string, overrides: Partial<Mission> = {}): Mission {
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
    identifier: id.toUpperCase(),
    ...overrides,
  } as Mission
}

describe("useIssueDetail (uncovered paths)", () => {
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

  it("clears comments when the comments endpoint responds non-ok", async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, status: 500, json: async () => ({}) })
    const { result } = setup()
    await act(async () => {
      await result.current.handleIssueSelect(mission("m1"))
    })
    expect(result.current.selectedIssue?.id).toBe("m1")
    expect(result.current.issueComments).toEqual([])
  })

  it("clears comments when the comments fetch throws", async () => {
    mockFetch.mockRejectedValueOnce(new Error("network down"))
    const { result } = setup()
    await act(async () => {
      await result.current.handleIssueSelect(mission("m1"))
    })
    expect(result.current.selectedIssue?.id).toBe("m1")
    expect(result.current.issueComments).toEqual([])
  })

  it("selecting an issue without crew_id skips the comments fetch entirely", async () => {
    const onIssueSelected = vi.fn()
    const { result } = renderHook(() =>
      useIssueDetail({ workspaceId: "ws-1", onIssueSelected, fetchIssues, fetchProjects }),
    )
    await act(async () => {
      await result.current.handleIssueSelect(mission("m1", { crew_id: null as unknown as string }))
    })
    expect(mockFetch).not.toHaveBeenCalled()
    expect(result.current.selectedIssue?.id).toBe("m1")
    expect(result.current.issueComments).toEqual([])
    expect(onIssueSelected).toHaveBeenCalledTimes(1)
  })

  it("handleIssueUpdated refreshes the issue, its comments, and the projects list", async () => {
    const fresh = mission("m1", { title: "Updated title" })
    mockFetch
      // initial select → comments
      .mockResolvedValueOnce({ ok: true, json: async () => [{ id: "c-old" }] })
      // updated issue fetch
      .mockResolvedValueOnce({ ok: true, json: async () => fresh })
      // refreshed comments
      .mockResolvedValueOnce({ ok: true, json: async () => [{ id: "c-new1" }, { id: "c-new2" }] })

    const { result } = setup()
    await act(async () => {
      await result.current.handleIssueSelect(mission("m1"))
    })
    expect(result.current.issueComments).toHaveLength(1)

    await act(async () => {
      await result.current.handleIssueUpdated()
    })

    expect(fetchIssues).toHaveBeenCalledTimes(1)
    expect(fetchProjects).toHaveBeenCalledTimes(1)
    expect(mockFetch).toHaveBeenNthCalledWith(
      2,
      "/api/v1/issues/M1?workspace_id=ws-1",
      expect.objectContaining({ credentials: "include" }),
    )
    expect(mockFetch).toHaveBeenNthCalledWith(
      3,
      "/api/v1/crews/crew-1/issues/M1/comments?workspace_id=ws-1",
      expect.objectContaining({ credentials: "include" }),
    )
    expect(result.current.selectedIssue?.title).toBe("Updated title")
    expect(result.current.issueComments).toHaveLength(2)
  })

  it("handleIssueUpdated keeps the stale issue when the refresh responds non-ok", async () => {
    mockFetch
      .mockResolvedValueOnce({ ok: true, json: async () => [] }) // select comments
      .mockResolvedValueOnce({ ok: false, status: 404, json: async () => ({}) }) // refresh fails

    const { result } = setup()
    await act(async () => {
      await result.current.handleIssueSelect(mission("m1"))
    })
    await act(async () => {
      await result.current.handleIssueUpdated()
    })

    expect(result.current.selectedIssue?.id).toBe("m1")
    expect(result.current.selectedIssue?.title).toBe("Mission m1")
    // No second comments fetch, but projects still refresh.
    expect(mockFetch).toHaveBeenCalledTimes(2)
    expect(fetchProjects).toHaveBeenCalledTimes(1)
  })

  it("handleIssueUpdated swallows a thrown refresh fetch and still refreshes projects", async () => {
    mockFetch
      .mockResolvedValueOnce({ ok: true, json: async () => [] }) // select comments
      .mockRejectedValueOnce(new Error("boom")) // refresh throws

    const { result } = setup()
    await act(async () => {
      await result.current.handleIssueSelect(mission("m1"))
    })
    await act(async () => {
      await result.current.handleIssueUpdated()
    })

    expect(result.current.selectedIssue?.id).toBe("m1")
    expect(fetchProjects).toHaveBeenCalledTimes(1)
  })

  it("handleIssueUpdated skips the per-issue refresh when the fresh issue lost its crew_id", async () => {
    const fresh = mission("m1", { crew_id: null as unknown as string })
    mockFetch
      .mockResolvedValueOnce({ ok: true, json: async () => [{ id: "c1" }] }) // select comments
      .mockResolvedValueOnce({ ok: true, json: async () => fresh }) // refresh — crew-less

    const { result } = setup()
    await act(async () => {
      await result.current.handleIssueSelect(mission("m1"))
    })
    await act(async () => {
      await result.current.handleIssueUpdated()
    })

    // Fresh issue is applied, but no follow-up comments fetch happens.
    expect(result.current.selectedIssue?.crew_id).toBeNull()
    expect(mockFetch).toHaveBeenCalledTimes(2)
    expect(fetchProjects).toHaveBeenCalledTimes(1)
  })

  it("handleIssueUpdated with nothing selected only refreshes the lists", async () => {
    const { result } = setup()
    await act(async () => {
      await result.current.handleIssueUpdated()
    })
    expect(fetchIssues).toHaveBeenCalledTimes(1)
    expect(fetchProjects).toHaveBeenCalledTimes(1)
    expect(mockFetch).not.toHaveBeenCalled()
  })

  // Drain a handful of microtasks so in-flight awaits inside the hook
  // progress to their next suspension point, deterministically.
  async function flushMicrotasks() {
    await act(async () => {
      for (let i = 0; i < 10; i++) await Promise.resolve()
    })
  }

  it("a stale comments fetch that REJECTS after a newer select is discarded", async () => {
    let rejectA!: (e: Error) => void
    mockFetch
      .mockImplementationOnce(() => new Promise((_, rej) => { rejectA = rej }))
      .mockResolvedValueOnce({ ok: true, json: async () => [{ id: "b1" }] })

    const { result } = setup()
    let aPromise!: Promise<void>
    act(() => {
      aPromise = result.current.handleIssueSelect(mission("a"))
    })
    await act(async () => {
      await result.current.handleIssueSelect(mission("b"))
    })
    expect(result.current.issueComments).toEqual([{ id: "b1" }])

    // A's late network failure must NOT clear B's comments.
    await act(async () => {
      rejectA(new Error("late failure"))
      await aPromise
    })
    expect(result.current.selectedIssue?.id).toBe("b")
    expect(result.current.issueComments).toEqual([{ id: "b1" }])
  })

  it("closing while the issue-refresh fetch is in flight discards its result", async () => {
    let resolveRefresh!: (v: unknown) => void
    mockFetch
      .mockResolvedValueOnce({ ok: true, json: async () => [] }) // select comments
      .mockImplementationOnce(() => new Promise((res) => { resolveRefresh = res }))

    const { result } = setup()
    await act(async () => {
      await result.current.handleIssueSelect(mission("m1"))
    })

    let updatedPromise!: Promise<void>
    act(() => {
      updatedPromise = result.current.handleIssueUpdated()
    })
    await flushMicrotasks()
    expect(mockFetch).toHaveBeenCalledTimes(2) // refresh fetch now in flight

    act(() => result.current.handleIssueClose())
    await act(async () => {
      resolveRefresh({ ok: true, json: async () => mission("m1", { title: "stale" }) })
      await updatedPromise
    })

    expect(result.current.selectedIssue).toBeNull()
    expect(fetchProjects).not.toHaveBeenCalled()
  })

  it("closing while the refreshed issue body is being parsed discards it", async () => {
    let resolveJson!: (v: unknown) => void
    mockFetch
      .mockResolvedValueOnce({ ok: true, json: async () => [] }) // select comments
      .mockResolvedValueOnce({
        ok: true,
        json: () => new Promise((res) => { resolveJson = res }),
      })

    const { result } = setup()
    await act(async () => {
      await result.current.handleIssueSelect(mission("m1"))
    })

    let updatedPromise!: Promise<void>
    act(() => {
      updatedPromise = result.current.handleIssueUpdated()
    })
    await flushMicrotasks()
    expect(resolveJson).toBeDefined() // json() is now pending

    act(() => result.current.handleIssueClose())
    await act(async () => {
      resolveJson(mission("m1", { title: "stale" }))
      await updatedPromise
    })

    expect(result.current.selectedIssue).toBeNull()
    expect(fetchProjects).not.toHaveBeenCalled()
  })

  it("closing while the refreshed comments fetch is in flight skips the projects refresh", async () => {
    let resolveComments!: (v: unknown) => void
    mockFetch
      .mockResolvedValueOnce({ ok: true, json: async () => [] }) // select comments
      .mockResolvedValueOnce({ ok: true, json: async () => mission("m1") }) // refresh ok
      .mockImplementationOnce(() => new Promise((res) => { resolveComments = res }))

    const { result } = setup()
    await act(async () => {
      await result.current.handleIssueSelect(mission("m1"))
    })

    let updatedPromise!: Promise<void>
    act(() => {
      updatedPromise = result.current.handleIssueUpdated()
    })
    await flushMicrotasks()
    expect(mockFetch).toHaveBeenCalledTimes(3) // refreshed-comments fetch in flight

    act(() => result.current.handleIssueClose())
    await act(async () => {
      resolveComments({ ok: true, json: async () => [{ id: "stale" }] })
      await updatedPromise
    })

    expect(result.current.selectedIssue).toBeNull()
    expect(result.current.issueComments).toEqual([])
    expect(fetchProjects).not.toHaveBeenCalled()
  })
})

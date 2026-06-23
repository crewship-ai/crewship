import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { renderHook, act, waitFor } from "@testing-library/react"

// Capture the realtime invalidation handlers the provider registers so
// tests can fire them directly. The realtime plumbing itself is covered
// in use-realtime tests — mocking it here keeps these tests focused on
// lookup fetching / stale-response semantics.
const realtimeHandlers = new Map<string, () => void>()
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: (eventType: string, cb: () => void) => {
    realtimeHandlers.set(eventType, cb)
  },
}))

import {
  JournalLookupProvider,
  useJournalLookup,
} from "@/hooks/use-journal-lookup"

interface LookupBody {
  crews?: unknown[]
  agents?: unknown[]
  missions?: unknown[]
}

function okJSON(body: LookupBody): Response {
  return {
    ok: true,
    status: 200,
    json: async () => ({ crews: [], agents: [], missions: [], ...body }),
  } as unknown as Response
}

function err(status: number): Response {
  return { ok: false, status, json: async () => ({}) } as unknown as Response
}

const crewA = { id: "c1", name: "Alpha", slug: "alpha", icon: null, color: "#f00" }
const agentA = {
  id: "a1",
  name: "Eva",
  slug: "eva",
  crew_id: "c1",
  avatar_seed: null,
  avatar_style: null,
}
const missionA = { id: "m1", title: "Ship it", status: "active" }

let mockFetch: ReturnType<typeof vi.fn>

// Mutable box some wrappers read the current workspaceId from — renderHook
// wrappers don't receive the hook's rerender props, so tests mutate this
// then call rerender().
const wsBox: { current: string | null } = { current: null }

beforeEach(() => {
  realtimeHandlers.clear()
  mockFetch = vi.fn()
  vi.stubGlobal("fetch", mockFetch)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

function makeWrapper(workspaceId: string | null) {
  return ({ children }: { children: React.ReactNode }) => (
    <JournalLookupProvider workspaceId={workspaceId}>{children}</JournalLookupProvider>
  )
}

describe("useJournalLookup — without provider", () => {
  it("returns empty maps, loading=false and a callable no-op refresh", () => {
    const { result } = renderHook(() => useJournalLookup())
    expect(result.current.crews.size).toBe(0)
    expect(result.current.agents.size).toBe(0)
    expect(result.current.missions.size).toBe(0)
    expect(result.current.loading).toBe(false)
    expect(() => result.current.refresh()).not.toThrow()
  })
})

describe("JournalLookupProvider", () => {
  it("fetches the lookup once on mount and populates id-keyed maps", async () => {
    mockFetch.mockResolvedValueOnce(
      okJSON({ crews: [crewA], agents: [agentA], missions: [missionA] }),
    )
    const { result } = renderHook(() => useJournalLookup(), {
      wrapper: makeWrapper("ws_test"),
    })
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(mockFetch).toHaveBeenCalledTimes(1)
    expect(mockFetch.mock.calls[0][0]).toBe(
      "/api/v1/journal/lookup?workspace_id=ws_test",
    )
    expect(result.current.crews.get("c1")).toEqual(crewA)
    expect(result.current.agents.get("a1")).toEqual(agentA)
    expect(result.current.missions.get("m1")).toEqual(missionA)
  })

  it("URL-encodes the workspace id", async () => {
    mockFetch.mockResolvedValueOnce(okJSON({}))
    renderHook(() => useJournalLookup(), {
      wrapper: makeWrapper("ws/with space"),
    })
    await waitFor(() => expect(mockFetch).toHaveBeenCalled())
    expect(mockFetch.mock.calls[0][0]).toContain(
      `workspace_id=${encodeURIComponent("ws/with space")}`,
    )
  })

  it("does not fetch when workspaceId is null", async () => {
    const { result } = renderHook(() => useJournalLookup(), {
      wrapper: makeWrapper(null),
    })
    await act(async () => {
      await Promise.resolve()
    })
    expect(mockFetch).not.toHaveBeenCalled()
    expect(result.current.loading).toBe(false)
  })

  it("non-ok response leaves maps empty and flips loading off", async () => {
    mockFetch.mockResolvedValueOnce(err(500))
    const { result } = renderHook(() => useJournalLookup(), {
      wrapper: makeWrapper("ws_test"),
    })
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.crews.size).toBe(0)
  })

  it("network rejection is swallowed (fail-open) and loading clears", async () => {
    mockFetch.mockRejectedValueOnce(new Error("offline"))
    const { result } = renderHook(() => useJournalLookup(), {
      wrapper: makeWrapper("ws_test"),
    })
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.crews.size).toBe(0)
    expect(result.current.agents.size).toBe(0)
  })

  it("registers invalidation handlers for all crew/agent/mission events", async () => {
    mockFetch.mockResolvedValue(okJSON({}))
    renderHook(() => useJournalLookup(), { wrapper: makeWrapper("ws_test") })
    for (const ev of [
      "crew.created",
      "crew.updated",
      "crew.deleted",
      "agent.created",
      "agent.updated",
      "agent.deleted",
      "mission.updated",
    ]) {
      expect(realtimeHandlers.has(ev), `missing handler for ${ev}`).toBe(true)
    }
  })

  it("a realtime event triggers a refetch that replaces map contents", async () => {
    mockFetch
      .mockResolvedValueOnce(okJSON({ crews: [crewA] }))
      .mockResolvedValueOnce(
        okJSON({ crews: [{ ...crewA, name: "Alpha Renamed" }] }),
      )
    const { result } = renderHook(() => useJournalLookup(), {
      wrapper: makeWrapper("ws_test"),
    })
    await waitFor(() => expect(result.current.crews.get("c1")?.name).toBe("Alpha"))

    await act(async () => {
      realtimeHandlers.get("crew.updated")!()
    })
    await waitFor(() =>
      expect(result.current.crews.get("c1")?.name).toBe("Alpha Renamed"),
    )
    expect(mockFetch).toHaveBeenCalledTimes(2)
  })

  it("refresh() from the context value refetches", async () => {
    mockFetch
      .mockResolvedValueOnce(okJSON({ missions: [missionA] }))
      .mockResolvedValueOnce(
        okJSON({ missions: [{ ...missionA, status: "done" }] }),
      )
    const { result } = renderHook(() => useJournalLookup(), {
      wrapper: makeWrapper("ws_test"),
    })
    await waitFor(() => expect(result.current.missions.get("m1")?.status).toBe("active"))

    await act(async () => {
      result.current.refresh()
    })
    await waitFor(() =>
      expect(result.current.missions.get("m1")?.status).toBe("done"),
    )
  })

  it("stale-response guard: a late response from an older fetch is discarded", async () => {
    let resolveFirst!: (r: Response) => void
    const firstResponse = new Promise<Response>((res) => {
      resolveFirst = res
    })
    mockFetch
      .mockReturnValueOnce(firstResponse) // mount fetch — kept in flight
      .mockResolvedValueOnce(okJSON({ crews: [{ ...crewA, name: "Fresh" }] }))

    const { result } = renderHook(() => useJournalLookup(), {
      wrapper: makeWrapper("ws_test"),
    })
    await act(async () => {
      await Promise.resolve()
    })

    // Newer fetch kicked off and completes while #1 is still pending.
    await act(async () => {
      realtimeHandlers.get("agent.updated")!()
    })
    await waitFor(() => expect(result.current.crews.get("c1")?.name).toBe("Fresh"))

    // Old response lands last — must NOT clobber the fresh data.
    await act(async () => {
      resolveFirst(okJSON({ crews: [{ ...crewA, name: "Stale" }] }))
      await Promise.resolve()
    })
    expect(result.current.crews.get("c1")?.name).toBe("Fresh")
    // Loading was flipped off by the newest request; the stale finally
    // block must not re-touch it.
    expect(result.current.loading).toBe(false)
  })

  it("workspace switch clears maps immediately and refetches for the new id", async () => {
    mockFetch
      .mockResolvedValueOnce(okJSON({ crews: [crewA] }))
      .mockResolvedValueOnce(
        okJSON({ crews: [{ ...crewA, id: "c2", name: "Beta" }] }),
      )

    wsBox.current = "ws_one"
    const { result, rerender } = renderHook(() => useJournalLookup(), {
      wrapper: ({ children }) => (
        <JournalLookupProvider workspaceId={wsBox.current}>
          {children}
        </JournalLookupProvider>
      ),
    })
    await waitFor(() => expect(result.current.crews.get("c1")?.name).toBe("Alpha"))
    expect(mockFetch.mock.calls[0][0]).toContain("workspace_id=ws_one")

    wsBox.current = "ws_two"
    rerender()
    await waitFor(() => expect(result.current.crews.get("c2")?.name).toBe("Beta"))
    expect(result.current.crews.has("c1")).toBe(false)
    expect(mockFetch.mock.calls[1][0]).toContain("workspace_id=ws_two")
  })

  it("late response for a previous workspace cannot overwrite the new one's data", async () => {
    let resolveOld!: (r: Response) => void
    const oldResponse = new Promise<Response>((res) => {
      resolveOld = res
    })
    // ws_one fetch stays in flight; after switching to null workspace no
    // new fetch starts (no seq bump), so only the workspace-identity
    // guard can reject the stale data.
    mockFetch.mockReturnValueOnce(oldResponse)

    wsBox.current = "ws_one"
    const { result, rerender } = renderHook(() => useJournalLookup(), {
      wrapper: ({ children }) => (
        <JournalLookupProvider workspaceId={wsBox.current}>
          {children}
        </JournalLookupProvider>
      ),
    })
    await act(async () => {
      await Promise.resolve()
    })
    expect(mockFetch).toHaveBeenCalledTimes(1)

    wsBox.current = null
    rerender()

    await act(async () => {
      resolveOld(okJSON({ crews: [crewA] }))
      await Promise.resolve()
    })
    expect(result.current.crews.size).toBe(0)
    expect(mockFetch).toHaveBeenCalledTimes(1)
  })
})

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { renderToString } from "react-dom/server"
import { renderHook, waitFor } from "@testing-library/react"

const mockFetch = vi.fn()
vi.stubGlobal("fetch", mockFetch)

import { useWorkspace, _resetWorkspaceStoreForTests } from "@/hooks/use-workspace"

// Coverage companion for use-workspace.test.ts — covers the
// disabled-storage fallbacks (readPersistedId / persistId catch arms).

const WS_A = { id: "ws-a", name: "Acme", slug: "acme", currentUserRole: "OWNER" }
const WS_B = { id: "ws-b", name: "Beta", slug: "beta", currentUserRole: "MEMBER" }

describe("useWorkspace — storage failure tolerance", () => {
  beforeEach(() => {
    mockFetch.mockReset()
    _resetWorkspaceStoreForTests()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("falls back to the first workspace when localStorage.getItem throws (private browsing)", async () => {
    vi.spyOn(window.localStorage, "getItem").mockImplementation(() => {
      throw new Error("SecurityError: storage disabled")
    })
    mockFetch.mockResolvedValue({ ok: true, json: async () => [WS_A, WS_B] })

    const { result } = renderHook(() => useWorkspace())
    await waitFor(() => expect(result.current.loading).toBe(false))

    // No persisted id readable → default to the first workspace, not a crash.
    expect(result.current.workspaceId).toBe("ws-a")
    expect(result.current.workspaces).toEqual([WS_A, WS_B])
  })

  it("loads fine when localStorage.setItem throws while persisting the default", async () => {
    vi.spyOn(window.localStorage, "getItem").mockImplementation(() => null)
    vi.spyOn(window.localStorage, "setItem").mockImplementation(() => {
      throw new Error("QuotaExceededError")
    })
    mockFetch.mockResolvedValue({ ok: true, json: async () => [WS_A] })

    const { result } = renderHook(() => useWorkspace())
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.workspaceId).toBe("ws-a")
    expect(result.current.role).toBe("OWNER")
  })
})

describe("useWorkspace — request coalescing and SSR", () => {
  beforeEach(() => {
    mockFetch.mockReset()
    _resetWorkspaceStoreForTests()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("refresh() during an in-flight load reuses the same request (no duplicate fetch)", async () => {
    let resolveFetch!: (v: unknown) => void
    mockFetch.mockImplementation(() => new Promise((res) => { resolveFetch = res }))

    const { result } = renderHook(() => useWorkspace())
    expect(mockFetch).toHaveBeenCalledTimes(1)

    // A refresh while the initial load is still on the wire must piggyback
    // on the in-flight promise instead of firing a second GET.
    const refreshed = result.current.refresh()
    expect(mockFetch).toHaveBeenCalledTimes(1)

    resolveFetch({ ok: true, json: async () => [WS_A] })
    await refreshed
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.workspaceId).toBe("ws-a")
    expect(mockFetch).toHaveBeenCalledTimes(1)
  })

  it("server-side render uses the SSR snapshot: loading, no workspaces, no fetch", () => {
    function Probe() {
      const { loading, workspaceId, workspaces } = useWorkspace()
      return React.createElement(
        "div",
        null,
        loading ? "ssr-loading" : `ready:${workspaceId}:${workspaces.length}`,
      )
    }
    // renderToString runs no effects — the hook must fall back to the
    // stable server snapshot rather than touching fetch/localStorage.
    const html = renderToString(React.createElement(Probe))
    expect(html).toContain("ssr-loading")
    expect(mockFetch).not.toHaveBeenCalled()
  })
})

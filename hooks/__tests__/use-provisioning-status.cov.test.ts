import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"

// Coverage companion for use-provisioning-status.test.ts — covers the
// provision.completed refetch path, defensive guards on bad fetch
// responses, the failed/running status mapping, and the malformed
// devcontainer_config tolerance in extractFeatureIds.

const realtimeCallbacks: Record<string, (event: { payload: Record<string, unknown> }) => void> = {}

vi.mock("@/hooks/use-realtime", () => ({
  useRealtime: () => ({
    status: "connected",
    subscribe: (eventType: string, cb: (event: { payload: Record<string, unknown> }) => void) => {
      realtimeCallbacks[eventType] = cb
      return () => { delete realtimeCallbacks[eventType] }
    },
    subscribeChannel: () => () => {},
  }),
}))

import { renderHook, act } from "@testing-library/react"
import { useProvisioningStatus } from "@/hooks/use-provisioning-status"

async function flushAsync() {
  for (let i = 0; i < 5; i++) {
    await Promise.resolve()
  }
}

describe("useProvisioningStatus — coverage companion", () => {
  let mockFetch: ReturnType<typeof vi.fn>

  beforeEach(() => {
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
    for (const k of Object.keys(realtimeCallbacks)) delete realtimeCallbacks[k]
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("provision.completed triggers a full refetch from the server", async () => {
    let provisionStatus = "running"
    mockFetch.mockImplementation((url: string) => {
      if (url.includes("/api/v1/crews?workspace_id=")) {
        return Promise.resolve({
          ok: true,
          json: async () => [
            { id: "c1", slug: "eng", name: "Eng", devcontainer_config: '{"image":"x"}', cached_image: null },
          ],
        })
      }
      return Promise.resolve({
        ok: true,
        json: async () => ({
          status: provisionStatus,
          devcontainer_config: '{"image":"x"}',
          cached_image: provisionStatus === "running" ? null : "img:fresh",
        }),
      })
    })

    const { result } = renderHook(() => useProvisioningStatus("ws-1"))
    await act(async () => { await flushAsync() })
    expect(result.current.building).toBe(1)
    const callsBefore = mockFetch.mock.calls.length

    // Server flips to done; the WS completion event must refetch rather
    // than trust an optimistic patch.
    provisionStatus = "idle"
    act(() => {
      realtimeCallbacks["provision.completed"]?.({ payload: { crew_id: "c1" } })
    })
    await act(async () => { await flushAsync() })

    expect(mockFetch.mock.calls.length).toBeGreaterThan(callsBefore)
    expect(result.current.building).toBe(0)
    expect(result.current.detail[0].status).toBe("completed")
  })

  it("provision.completed without crew_id is ignored (no refetch)", async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url.includes("/api/v1/crews?workspace_id=")) {
        return Promise.resolve({ ok: true, json: async () => [] })
      }
      return Promise.resolve({ ok: true, json: async () => ({}) })
    })
    renderHook(() => useProvisioningStatus("ws-1"))
    await act(async () => { await flushAsync() })
    const callsBefore = mockFetch.mock.calls.length

    act(() => {
      realtimeCallbacks["provision.completed"]?.({ payload: {} })
    })
    await act(async () => { await flushAsync() })
    expect(mockFetch.mock.calls.length).toBe(callsBefore)
  })

  it("tolerates malformed devcontainer_config — featureIds degrade to []", async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url.includes("/api/v1/crews?workspace_id=")) {
        return Promise.resolve({
          ok: true,
          json: async () => [
            { id: "c1", slug: "a", name: "A", devcontainer_config: "{broken json", cached_image: null },
            {
              id: "c2",
              slug: "b",
              name: "B",
              devcontainer_config: '{"image":"x","features":{"ghcr.io/devcontainers/features/python:1":{}}}',
              cached_image: null,
            },
          ],
        })
      }
      return Promise.resolve({
        ok: true,
        json: async () => ({ status: "idle", devcontainer_config: '{"image":"x"}', cached_image: null }),
      })
    })

    const { result } = renderHook(() => useProvisioningStatus("ws-2"))
    await act(async () => { await flushAsync() })

    const c1 = result.current.detail.find((d) => d.id === "c1")
    const c2 = result.current.detail.find((d) => d.id === "c2")
    expect(c1?.featureIds).toEqual([])
    expect(c2?.featureIds).toEqual(["ghcr.io/devcontainers/features/python:1"])
  })

  it("maps a non-OK per-crew provision response to idle instead of crashing", async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url.includes("/api/v1/crews?workspace_id=")) {
        return Promise.resolve({
          ok: true,
          json: async () => [
            { id: "c1", slug: "a", name: "A", devcontainer_config: '{"image":"x"}', cached_image: null },
          ],
        })
      }
      return Promise.resolve({ ok: false, status: 500, json: async () => ({}) })
    })

    const { result } = renderHook(() => useProvisioningStatus("ws-3"))
    await act(async () => { await flushAsync() })

    expect(result.current.detail).toHaveLength(1)
    expect(result.current.detail[0].status).toBe("idle")
    expect(result.current.total).toBe(0)
  })

  it("keeps the previous summary when the crew list endpoint errors", async () => {
    mockFetch.mockResolvedValue({ ok: false, status: 503, json: async () => ({}) })
    const { result } = renderHook(() => useProvisioningStatus("ws-4"))
    await act(async () => { await flushAsync() })
    expect(result.current.total).toBe(0)
    expect(result.current.detail).toEqual([])
  })

  it("ignores a non-array crew list payload", async () => {
    mockFetch.mockResolvedValue({ ok: true, json: async () => ({ error: "weird shape" }) })
    const { result } = renderHook(() => useProvisioningStatus("ws-5"))
    await act(async () => { await flushAsync() })
    expect(result.current.detail).toEqual([])
    // Only the list endpoint was hit — no per-crew fan-out on bad shape.
    expect(mockFetch.mock.calls.every(([u]) => String(u).includes("/api/v1/crews?workspace_id="))).toBe(true)
  })

  it("ignores provision.started without a steps array and provision.progress/failed without crew_id", async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url.includes("/api/v1/crews?workspace_id=")) {
        return Promise.resolve({
          ok: true,
          json: async () => [
            { id: "c1", slug: "a", name: "A", devcontainer_config: '{"image":"x"}', cached_image: null },
          ],
        })
      }
      return Promise.resolve({
        ok: true,
        json: async () => ({ status: "idle", devcontainer_config: '{"image":"x"}', cached_image: null }),
      })
    })

    const { result } = renderHook(() => useProvisioningStatus("ws-guards"))
    await act(async () => { await flushAsync() })
    const before = result.current

    act(() => {
      // started without steps → malformed, dropped
      realtimeCallbacks["provision.started"]?.({ payload: { crew_id: "c1" } })
      // progress / failed without crew_id → dropped
      realtimeCallbacks["provision.progress"]?.({ payload: { step: 1, total: 2, message: "x" } })
      realtimeCallbacks["provision.failed"]?.({ payload: { error: "boom" } })
    })

    expect(result.current).toBe(before) // referential equality — nothing changed
  })

  it("caps the progress logTail ring buffer at 50 entries", async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url.includes("/api/v1/crews?workspace_id=")) {
        return Promise.resolve({
          ok: true,
          json: async () => [
            { id: "c1", slug: "a", name: "A", devcontainer_config: '{"image":"x"}', cached_image: null },
          ],
        })
      }
      return Promise.resolve({
        ok: true,
        json: async () => ({ status: "running", devcontainer_config: '{"image":"x"}', cached_image: null }),
      })
    })

    const { result } = renderHook(() => useProvisioningStatus("ws-tail"))
    await act(async () => { await flushAsync() })

    act(() => {
      for (let i = 1; i <= 55; i++) {
        realtimeCallbacks["provision.progress"]?.({
          payload: { crew_id: "c1", step: i, total: 55, message: `step ${i}` },
        })
      }
    })

    const tail = result.current.detail[0].logTail!
    expect(tail).toHaveLength(50)
    // Oldest entries rolled off; newest preserved.
    expect(tail[0]).toBe("step 6")
    expect(tail[49]).toBe("step 55")
  })

  it("polls again on the relaxed cadence when no build is in flight", async () => {
    vi.useFakeTimers()
    try {
      mockFetch.mockImplementation((url: string) => {
        if (url.includes("/api/v1/crews?workspace_id=")) {
          return Promise.resolve({ ok: true, json: async () => [] })
        }
        return Promise.resolve({ ok: true, json: async () => ({}) })
      })

      renderHook(() => useProvisioningStatus("ws-poll"))
      await act(async () => { await vi.advanceTimersByTimeAsync(0) })
      const callsAfterMount = mockFetch.mock.calls.length
      expect(callsAfterMount).toBeGreaterThan(0)

      // Just before the 30s idle tick — nothing.
      await act(async () => { await vi.advanceTimersByTimeAsync(29_999) })
      expect(mockFetch.mock.calls.length).toBe(callsAfterMount)

      await act(async () => { await vi.advanceTimersByTimeAsync(1) })
      expect(mockFetch.mock.calls.length).toBeGreaterThan(callsAfterMount)
    } finally {
      vi.useRealTimers()
    }
  })

  it("drops a refresh that resolves after unmount (cancelRef guard)", async () => {
    let resolveList!: (v: unknown) => void
    mockFetch.mockImplementation((url: string) => {
      if (url.includes("/api/v1/crews?workspace_id=")) {
        return new Promise((res) => { resolveList = res })
      }
      return Promise.resolve({
        ok: true,
        json: async () => ({ status: "idle", devcontainer_config: '{"image":"x"}', cached_image: null }),
      })
    })

    const { result, unmount } = renderHook(() => useProvisioningStatus("ws-cancel"))
    expect(result.current.total).toBe(0)
    unmount()

    // The list lands after the hook is gone — the per-crew fan-out still
    // runs but the summary write is suppressed; nothing throws.
    resolveList({
      ok: true,
      json: async () => [
        { id: "c1", slug: "a", name: "A", devcontainer_config: '{"image":"x"}', cached_image: null },
      ],
    })
    await act(async () => { await flushAsync() })
    expect(result.current.total).toBe(0)
  })

  it("maps server status=failed into the failed bucket with its error", async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url.includes("/api/v1/crews?workspace_id=")) {
        return Promise.resolve({
          ok: true,
          json: async () => [
            { id: "c1", slug: "a", name: "A", devcontainer_config: '{"image":"x"}', cached_image: null },
          ],
        })
      }
      return Promise.resolve({
        ok: true,
        json: async () => ({
          status: "failed",
          error: "feature install exit 1",
          devcontainer_config: '{"image":"x"}',
          cached_image: null,
        }),
      })
    })

    const { result } = renderHook(() => useProvisioningStatus("ws-6"))
    await act(async () => { await flushAsync() })

    expect(result.current.failed).toBe(1)
    expect(result.current.detail[0].status).toBe("failed")
    expect(result.current.detail[0].error).toBe("feature install exit 1")
  })
})

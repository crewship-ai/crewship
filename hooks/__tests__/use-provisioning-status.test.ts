import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"

// Capture the latest callback registered per event type so tests can fire it
// directly. Mirrors the pattern in use-pending-escalations.test.ts.
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

const crewListResponse = (crews: Array<{ id: string; slug: string; name: string; cfg: string | null; img: string | null }>) =>
  crews.map((c) => ({
    id: c.id,
    slug: c.slug,
    name: c.name,
    devcontainer_config: c.cfg,
    cached_image: c.img,
  }))

describe("useProvisioningStatus", () => {
  let mockFetch: ReturnType<typeof vi.fn>

  beforeEach(() => {
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
    for (const k of Object.keys(realtimeCallbacks)) delete realtimeCallbacks[k]
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("returns empty for null workspace and does not fetch", async () => {
    const { result } = renderHook(() => useProvisioningStatus(null))
    await act(async () => { await flushAsync() })
    expect(result.current.total).toBe(0)
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it("rolls up needs_provision count from initial fetch", async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url.includes("/api/v1/crews?workspace_id=")) {
        return Promise.resolve({
          ok: true,
          json: async () => crewListResponse([
            { id: "c1", slug: "research", name: "Research", cfg: '{"image":"x"}', img: null },
          ]),
        })
      }
      // /provision endpoint — no cached image, hasConfig → needs_provision
      return Promise.resolve({
        ok: true,
        json: async () => ({ status: "idle", devcontainer_config: '{"image":"x"}', cached_image: null }),
      })
    })

    const { result } = renderHook(() => useProvisioningStatus("ws-1"))
    await act(async () => { await flushAsync() })

    expect(result.current.needsProvision).toBe(1)
    expect(result.current.building).toBe(0)
    expect(result.current.failed).toBe(0)
    expect(result.current.pendingRestart).toBe(0)
    expect(result.current.total).toBe(1)
    expect(result.current.detail[0].status).toBe("needs_provision")
  })

  it("counts agents_pending_restart only for completed crews with non-zero counter", async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url.includes("/api/v1/crews?workspace_id=")) {
        return Promise.resolve({
          ok: true,
          json: async () => crewListResponse([
            { id: "c1", slug: "research", name: "Research", cfg: '{"image":"x"}', img: "img:1" },
            { id: "c2", slug: "devops", name: "DevOps", cfg: '{"image":"x"}', img: "img:2" },
          ]),
        })
      }
      // c1 is freshly built but has agents on old image; c2 is fully synced
      const isC1 = url.includes("/c1/")
      return Promise.resolve({
        ok: true,
        json: async () => ({
          status: "idle",
          devcontainer_config: '{"image":"x"}',
          cached_image: isC1 ? "img:1" : "img:2",
          agents_pending_restart: isC1 ? 3 : 0,
        }),
      })
    })

    const { result } = renderHook(() => useProvisioningStatus("ws-2"))
    await act(async () => { await flushAsync() })

    expect(result.current.pendingRestart).toBe(1)
    expect(result.current.total).toBe(1)
    const c1 = result.current.detail.find((d) => d.id === "c1")
    expect(c1?.agentsPendingRestart).toBe(3)
    expect(c1?.status).toBe("completed")
  })

  it("provision.progress event patches a single crew without refetch", async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url.includes("/api/v1/crews?workspace_id=")) {
        return Promise.resolve({
          ok: true,
          json: async () => crewListResponse([
            { id: "c1", slug: "research", name: "Research", cfg: '{"image":"x"}', img: null },
          ]),
        })
      }
      return Promise.resolve({
        ok: true,
        json: async () => ({ status: "idle", devcontainer_config: '{"image":"x"}', cached_image: null }),
      })
    })

    const { result } = renderHook(() => useProvisioningStatus("ws-3"))
    await act(async () => { await flushAsync() })

    expect(result.current.detail[0].status).toBe("needs_provision")
    const callsBeforeWS = mockFetch.mock.calls.length

    act(() => {
      realtimeCallbacks["provision.progress"]?.({
        payload: { crew_id: "c1", step: 2, total: 5, message: "Installing python" },
      })
    })
    await act(async () => { await flushAsync() })

    expect(result.current.building).toBe(1)
    expect(result.current.detail[0].status).toBe("running")
    expect(result.current.detail[0].step).toBe(2)
    expect(result.current.detail[0].total).toBe(5)
    expect(result.current.detail[0].message).toBe("Installing python")
    // No additional fetches — the patch is purely client-side.
    expect(mockFetch.mock.calls.length).toBe(callsBeforeWS)
  })

  it("provision.failed event flips a crew to failed with error", async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url.includes("/api/v1/crews?workspace_id=")) {
        return Promise.resolve({
          ok: true,
          json: async () => crewListResponse([
            { id: "c1", slug: "research", name: "Research", cfg: '{"image":"x"}', img: null },
          ]),
        })
      }
      return Promise.resolve({
        ok: true,
        json: async () => ({ status: "running", devcontainer_config: '{"image":"x"}', cached_image: null }),
      })
    })

    const { result } = renderHook(() => useProvisioningStatus("ws-4"))
    await act(async () => { await flushAsync() })

    act(() => {
      realtimeCallbacks["provision.failed"]?.({
        payload: { crew_id: "c1", error: "feature install exit 1" },
      })
    })
    await act(async () => { await flushAsync() })

    expect(result.current.failed).toBe(1)
    expect(result.current.detail[0].status).toBe("failed")
    expect(result.current.detail[0].error).toBe("feature install exit 1")
  })

  it("ignores progress events for unknown crew IDs (defensive against stale broadcasts)", async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url.includes("/api/v1/crews?workspace_id=")) {
        return Promise.resolve({
          ok: true,
          json: async () => crewListResponse([
            { id: "c1", slug: "research", name: "Research", cfg: '{"image":"x"}', img: null },
          ]),
        })
      }
      return Promise.resolve({
        ok: true,
        json: async () => ({ status: "idle", devcontainer_config: '{"image":"x"}', cached_image: null }),
      })
    })

    const { result } = renderHook(() => useProvisioningStatus("ws-5"))
    await act(async () => { await flushAsync() })

    const before = result.current

    act(() => {
      realtimeCallbacks["provision.progress"]?.({
        payload: { crew_id: "c-deleted", step: 1, total: 5, message: "Pulling base" },
      })
    })

    expect(result.current).toBe(before) // referential equality — no rerender
  })
})

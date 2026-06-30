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
import {
  useProvisioningStatus,
  RECENT_COMPLETED_TTL_MS,
  RECENT_PRUNE_INTERVAL_MS,
} from "@/hooks/use-provisioning-status"

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

  it("provision.started event seeds the checklist steps and resets progress", async () => {
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

    const { result } = renderHook(() => useProvisioningStatus("ws-started"))
    await act(async () => { await flushAsync() })

    act(() => {
      realtimeCallbacks["provision.started"]?.({
        payload: {
          crew_id: "c1",
          steps: [
            "Pulling base image x",
            "Installing common-utils",
            "Installing python",
            "Committing image",
          ],
        },
      })
    })

    expect(result.current.detail[0].status).toBe("running")
    expect(result.current.detail[0].steps).toEqual([
      "Pulling base image x",
      "Installing common-utils",
      "Installing python",
      "Committing image",
    ])
    expect(result.current.detail[0].total).toBe(4)
    expect(result.current.detail[0].step).toBe(0)
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

  // --- provision.event: feature-granular merge + dedup ---------------------

  const singleCrewBuilding = (img: string | null = null) => (url: string) => {
    if (url.includes("/api/v1/crews?workspace_id=")) {
      return Promise.resolve({
        ok: true,
        json: async () => crewListResponse([
          { id: "c1", slug: "ops", name: "Ops", cfg: '{"image":"x"}', img },
        ]),
      })
    }
    return Promise.resolve({
      ok: true,
      json: async () => ({
        status: img ? "completed" : "idle",
        devcontainer_config: '{"image":"x"}',
        cached_image: img,
        agents_pending_restart: 0,
      }),
    })
  }

  it("folds provision.event frames into a deduped, feature-granular step list", async () => {
    mockFetch.mockImplementation(singleCrewBuilding())
    const { result } = renderHook(() => useProvisioningStatus("ws-ev"))
    await act(async () => { await flushAsync() })

    act(() => {
      realtimeCallbacks["provision.event"]?.({
        payload: { crew_id: "c1", phase: "provision", step: "feature_install", feature: "ansible", status: "started" },
      })
    })
    expect(result.current.detail[0].status).toBe("running")
    expect(result.current.detail[0].activeFeature).toBe("ansible")
    expect(result.current.detail[0].eventSteps).toHaveLength(1)

    // completed for the SAME feature must update in place (dedup), not add a row
    act(() => {
      realtimeCallbacks["provision.event"]?.({
        payload: { crew_id: "c1", phase: "provision", step: "feature_install", feature: "ansible", status: "completed", duration_ms: 1200 },
      })
    })
    expect(result.current.detail[0].eventSteps).toHaveLength(1)
    expect(result.current.detail[0].eventSteps?.[0].status).toBe("completed")
    expect(result.current.detail[0].eventSteps?.[0].durationMs).toBe(1200)
    // active feature clears once it completes
    expect(result.current.detail[0].activeFeature).toBeUndefined()

    // a new feature starts → new row + new active feature
    act(() => {
      realtimeCallbacks["provision.event"]?.({
        payload: { crew_id: "c1", phase: "provision", step: "feature_install", feature: "terraform", status: "started" },
      })
    })
    expect(result.current.detail[0].eventSteps).toHaveLength(2)
    expect(result.current.detail[0].activeFeature).toBe("terraform")
  })

  it("does not downgrade a completed step on an out-of-order started frame", async () => {
    mockFetch.mockImplementation(singleCrewBuilding())
    const { result } = renderHook(() => useProvisioningStatus("ws-ooo"))
    await act(async () => { await flushAsync() })

    act(() => {
      realtimeCallbacks["provision.event"]?.({
        payload: { crew_id: "c1", step: "feature_install", feature: "go", status: "completed" },
      })
      realtimeCallbacks["provision.event"]?.({
        payload: { crew_id: "c1", step: "feature_install", feature: "go", status: "started" },
      })
    })
    expect(result.current.detail[0].eventSteps).toHaveLength(1)
    expect(result.current.detail[0].eventSteps?.[0].status).toBe("completed")
  })

  it("captures the BuildKit log tail from a failed image_build_start event", async () => {
    mockFetch.mockImplementation(singleCrewBuilding())
    const { result } = renderHook(() => useProvisioningStatus("ws-tail"))
    await act(async () => { await flushAsync() })

    act(() => {
      realtimeCallbacks["provision.event"]?.({
        payload: {
          crew_id: "c1",
          step: "image_build_start",
          status: "failed",
          tag: "feat:abc",
          detail: "#7 RUN install ansible\n#7 0.4 error: pip not found\n#7 ERROR: exit 1",
        },
      })
    })
    expect(result.current.detail[0].status).toBe("failed")
    expect(result.current.detail[0].buildLogTail).toEqual([
      "#7 RUN install ansible",
      "#7 0.4 error: pip not found",
      "#7 ERROR: exit 1",
    ])
  })

  it("records failedStep from a per-feature install failure", async () => {
    mockFetch.mockImplementation(singleCrewBuilding())
    const { result } = renderHook(() => useProvisioningStatus("ws-fstep"))
    await act(async () => { await flushAsync() })

    act(() => {
      realtimeCallbacks["provision.event"]?.({
        payload: { crew_id: "c1", step: "feature_install", feature: "ansible", status: "failed", error: "install.sh exit 1" },
      })
    })
    expect(result.current.detail[0].status).toBe("failed")
    expect(result.current.detail[0].failedStep).toBe("ansible")
    expect(result.current.detail[0].error).toBe("install.sh exit 1")
  })

  // --- lingering completed state + timeout ---------------------------------

  it("keeps a completed build visible as a lingering recent summary", async () => {
    mockFetch.mockImplementation(singleCrewBuilding("img:1"))
    const { result } = renderHook(() => useProvisioningStatus("ws-recent"))
    await act(async () => { await flushAsync() })

    // A normally-clean completed crew is not counted…
    expect(result.current.total).toBe(0)

    // …feed a build then complete it.
    act(() => {
      realtimeCallbacks["provision.event"]?.({
        payload: { crew_id: "c1", step: "feature_install", feature: "ansible", status: "completed" },
      })
      realtimeCallbacks["provision.event"]?.({
        payload: { crew_id: "c1", step: "feature_install", feature: "terraform", status: "completed" },
      })
    })
    act(() => {
      realtimeCallbacks["provision.completed"]?.({ payload: { crew_id: "c1" } })
    })
    await act(async () => { await flushAsync() })

    const c1 = result.current.detail[0]
    expect(c1.recent?.outcome).toBe("completed")
    expect(c1.recent?.stepCount).toBe(2)
    expect(c1.recent?.features).toEqual(["ansible", "terraform"])
    expect(result.current.recentlyCompleted).toBe(1)
    expect(result.current.total).toBe(1)
  })

  it("prunes a completed recent summary after the TTL elapses", async () => {
    vi.useFakeTimers()
    try {
      mockFetch.mockImplementation(singleCrewBuilding("img:1"))
      const { result } = renderHook(() => useProvisioningStatus("ws-ttl"))
      await act(async () => { await vi.advanceTimersByTimeAsync(0) })

      act(() => {
        realtimeCallbacks["provision.completed"]?.({ payload: { crew_id: "c1" } })
      })
      await act(async () => { await vi.advanceTimersByTimeAsync(0) })
      expect(result.current.recentlyCompleted).toBe(1)
      expect(result.current.total).toBe(1)

      // Advance past the TTL → the prune sweep clears it.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(RECENT_COMPLETED_TTL_MS + RECENT_PRUNE_INTERVAL_MS)
      })
      expect(result.current.detail[0].recent).toBeUndefined()
      expect(result.current.recentlyCompleted).toBe(0)
      expect(result.current.total).toBe(0)
    } finally {
      vi.useRealTimers()
    }
  })

  // --- lingering failed state until acknowledged ---------------------------

  it("lingers a failed build (with recent summary) until acknowledged", async () => {
    mockFetch.mockImplementation(singleCrewBuilding())
    const { result } = renderHook(() => useProvisioningStatus("ws-ack"))
    await act(async () => { await flushAsync() })

    act(() => {
      realtimeCallbacks["provision.event"]?.({
        payload: { crew_id: "c1", step: "feature_install", feature: "python", status: "completed" },
      })
      realtimeCallbacks["provision.failed"]?.({
        payload: { crew_id: "c1", error: "build exit 1" },
      })
    })
    await act(async () => { await flushAsync() })

    expect(result.current.failed).toBe(1)
    expect(result.current.total).toBe(1)
    const c1 = result.current.detail[0]
    expect(c1.status).toBe("failed")
    expect(c1.recent?.outcome).toBe("failed")
    expect(c1.recent?.features).toEqual(["python"])
    expect(c1.error).toBe("build exit 1")

    // Acknowledge → drops out of the badge entirely.
    act(() => { result.current.acknowledge("c1") })
    expect(result.current.failed).toBe(0)
    expect(result.current.total).toBe(0)
    expect(result.current.detail[0].acknowledged).toBe(true)
    expect(result.current.detail[0].recent).toBeUndefined()
  })

  it("provision.started clears a prior failure's lingering state", async () => {
    mockFetch.mockImplementation(singleCrewBuilding())
    const { result } = renderHook(() => useProvisioningStatus("ws-restart"))
    await act(async () => { await flushAsync() })

    act(() => {
      realtimeCallbacks["provision.failed"]?.({ payload: { crew_id: "c1", error: "boom" } })
    })
    expect(result.current.detail[0].status).toBe("failed")

    act(() => {
      realtimeCallbacks["provision.started"]?.({
        payload: { crew_id: "c1", steps: ["Pulling base image x", "Installing python"] },
      })
    })
    const c1 = result.current.detail[0]
    expect(c1.status).toBe("running")
    expect(c1.recent).toBeUndefined()
    expect(c1.acknowledged).toBe(false)
    expect(c1.eventSteps).toEqual([])
    expect(c1.error).toBeUndefined()
  })
})

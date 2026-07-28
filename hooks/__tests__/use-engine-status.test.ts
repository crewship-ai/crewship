import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act, waitFor } from "@testing-library/react"

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(),
}))

import { apiFetch } from "@/lib/api-fetch"
import { useEngineStatus } from "@/hooks/use-engine-status"

const mockApiFetch = vi.mocked(apiFetch)

function ok(uptime = "2h 15m"): Response {
  return { ok: true, status: 200, json: async () => ({ status: "ok", uptime }) } as unknown as Response
}
function fail(status: number): Response {
  return { ok: false, status } as unknown as Response
}

// Advances past one full poll tick (including the ±15% jitter window) and
// flushes the microtasks the mocked apiFetch promise needs to resolve.
async function advancePoll() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(POLL_MAX)
  })
}

const POLL_MAX = 10_000 * 1.15 + 10 // jittered ceiling + slack

describe("useEngineStatus", () => {
  beforeEach(() => {
    mockApiFetch.mockReset()
    vi.useFakeTimers({ shouldAdvanceTime: true })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it("starts in checking state", () => {
    mockApiFetch.mockReturnValue(new Promise(() => {}))
    const { result } = renderHook(() => useEngineStatus("ws-1"))
    expect(result.current.status).toBe("checking")
    expect(result.current.uptime).toBeNull()
  })

  it("does nothing when workspaceId is null", () => {
    const { result } = renderHook(() => useEngineStatus(null))
    expect(result.current.status).toBe("checking")
    expect(mockApiFetch).not.toHaveBeenCalled()
  })

  it("sets connected when API responds OK", async () => {
    mockApiFetch.mockResolvedValue(ok("2h 15m"))

    const { result } = renderHook(() => useEngineStatus("ws-1"))

    await waitFor(() => {
      expect(result.current.status).toBe("connected")
    })
    expect(result.current.uptime).toBe("2h 15m")
  })

  it("calls correct API endpoint with workspace_id", async () => {
    mockApiFetch.mockResolvedValue(ok())

    renderHook(() => useEngineStatus("ws-123"))

    await waitFor(() => {
      expect(mockApiFetch).toHaveBeenCalledWith(
        "/api/v1/crewshipd?workspace_id=ws-123",
        expect.objectContaining({ cache: "no-store" }),
      )
    })
  })

  it("handles response without uptime field", async () => {
    mockApiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ status: "ok" }) } as unknown as Response)

    const { result } = renderHook(() => useEngineStatus("ws-1"))

    await waitFor(() => {
      expect(result.current.status).toBe("connected")
    })
    expect(result.current.uptime).toBeNull()
  })

  it("polls at 10 second intervals", async () => {
    mockApiFetch.mockResolvedValue(ok("1m"))

    renderHook(() => useEngineStatus("ws-1"))

    await waitFor(() => {
      expect(mockApiFetch).toHaveBeenCalledTimes(1)
    }, { timeout: 5000 })

    await advancePoll()

    await waitFor(() => {
      expect(mockApiFetch).toHaveBeenCalledTimes(2)
    }, { timeout: 5000 })
  })

  it("cleans up interval on unmount", async () => {
    mockApiFetch.mockResolvedValue(ok())

    const { unmount } = renderHook(() => useEngineStatus("ws-1"))

    await waitFor(() => {
      expect(mockApiFetch).toHaveBeenCalledTimes(1)
    })

    unmount()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000)
    })

    expect(mockApiFetch).toHaveBeenCalledTimes(1)
  })

  // --- degraded / disconnected state machine -------------------------

  it("a single 500 does NOT show disconnected", async () => {
    mockApiFetch.mockResolvedValue(ok())
    const { result } = renderHook(() => useEngineStatus("ws-1"))
    await waitFor(() => expect(result.current.status).toBe("connected"))

    mockApiFetch.mockResolvedValue(fail(500))
    await advancePoll()
    await waitFor(() => expect(mockApiFetch).toHaveBeenCalledTimes(2))

    // One failure holds a "we're not sure yet" state, never the red
    // disconnected state a lone deploy-restart blip would otherwise cause.
    expect(result.current.status).not.toBe("disconnected")
    expect(result.current.status).toBe("degraded")
  })

  it("two consecutive 500s DO show disconnected", async () => {
    mockApiFetch.mockResolvedValue(ok())
    const { result } = renderHook(() => useEngineStatus("ws-1"))
    await waitFor(() => expect(result.current.status).toBe("connected"))

    mockApiFetch.mockResolvedValue(fail(500))
    await advancePoll()
    await waitFor(() => expect(result.current.status).toBe("degraded"))

    await advancePoll()
    await waitFor(() => expect(result.current.status).toBe("disconnected"))
    expect(result.current.uptime).toBeNull()
  })

  it("a 502 from a reverse proxy behaves like any other real failure (two in a row disconnects)", async () => {
    mockApiFetch.mockResolvedValue(ok())
    const { result } = renderHook(() => useEngineStatus("ws-1"))
    await waitFor(() => expect(result.current.status).toBe("connected"))

    mockApiFetch.mockResolvedValue(fail(502))
    await advancePoll()
    await waitFor(() => expect(result.current.status).toBe("degraded"))
    await advancePoll()
    await waitFor(() => expect(result.current.status).toBe("disconnected"))
  })

  it("a 429 never shows disconnected, even after repeated polls", async () => {
    mockApiFetch.mockResolvedValue(ok())
    const { result } = renderHook(() => useEngineStatus("ws-1"))
    await waitFor(() => expect(result.current.status).toBe("connected"))

    mockApiFetch.mockResolvedValue(fail(429))
    for (let i = 0; i < 4; i++) {
      await advancePoll()
    }

    // Still connected — we were only throttled, the engine never
    // reported unhealthy, so the last-known-good state is preserved.
    expect(result.current.status).toBe("connected")
  })

  it("a 429 on the very first poll (never connected yet) shows degraded, not disconnected", async () => {
    mockApiFetch.mockResolvedValue(fail(429))
    const { result } = renderHook(() => useEngineStatus("ws-1"))

    await waitFor(() => expect(result.current.status).toBe("degraded"))
  })

  it("a 429 does not count toward the two-failure disconnect threshold", async () => {
    mockApiFetch.mockResolvedValue(ok())
    const { result } = renderHook(() => useEngineStatus("ws-1"))
    await waitFor(() => expect(result.current.status).toBe("connected"))

    // failure, then throttle, then failure again — throttle must not have
    // silently counted as (or reset only to immediately re-count as) the
    // second strike.
    mockApiFetch.mockResolvedValue(fail(500))
    await advancePoll()
    await waitFor(() => expect(result.current.status).toBe("degraded"))

    mockApiFetch.mockResolvedValue(fail(429))
    await advancePoll()
    await waitFor(() => expect(mockApiFetch).toHaveBeenCalledTimes(3))
    expect(result.current.status).toBe("degraded")

    mockApiFetch.mockResolvedValue(fail(500))
    await advancePoll()
    await waitFor(() => expect(result.current.status).toBe("disconnected"))
  })

  it("recovery back to 200 clears the degraded/disconnected state", async () => {
    mockApiFetch.mockResolvedValue(ok())
    const { result } = renderHook(() => useEngineStatus("ws-1"))
    await waitFor(() => expect(result.current.status).toBe("connected"))

    mockApiFetch.mockResolvedValue(fail(500))
    await advancePoll()
    await waitFor(() => expect(result.current.status).toBe("degraded"))
    await advancePoll()
    await waitFor(() => expect(result.current.status).toBe("disconnected"))

    mockApiFetch.mockResolvedValue(ok("5m"))
    await advancePoll()
    await waitFor(() => expect(result.current.status).toBe("connected"))
    expect(result.current.uptime).toBe("5m")
  })

  it("a network error behaves like a real failure (two in a row disconnects)", async () => {
    mockApiFetch.mockResolvedValue(ok())
    const { result } = renderHook(() => useEngineStatus("ws-1"))
    await waitFor(() => expect(result.current.status).toBe("connected"))

    mockApiFetch.mockRejectedValue(new Error("Network error"))
    await advancePoll()
    await waitFor(() => expect(result.current.status).toBe("degraded"))
    await advancePoll()
    await waitFor(() => expect(result.current.status).toBe("disconnected"))
  })

  it("an aborted in-flight request (unmount mid-poll) does not set state", async () => {
    // Simulates unmount racing an in-flight poll: the effect cleanup
    // aborts the controller before the fetch promise settles. The
    // resulting AbortError must be swallowed, not read as a real failure
    // that flips status after the component is gone.
    mockApiFetch.mockImplementation((_url, init?: RequestInit) =>
      new Promise((_resolve, reject) => {
        const signal = init?.signal as AbortSignal | undefined
        signal?.addEventListener("abort", () => {
          const err = new Error("aborted")
          err.name = "AbortError"
          reject(err)
        })
      }),
    )

    const rendered = renderHook(() => useEngineStatus("ws-1"))
    expect(rendered.result.current.status).toBe("checking")

    rendered.unmount()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(100)
    })

    // No assertion possible on unmounted result beyond "it didn't throw" —
    // React (or the test env) would warn/error on a setState-after-unmount,
    // which is what this test actually guards against.
    expect(rendered.result.current.status).toBe("checking")
  })
})

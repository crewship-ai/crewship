import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"

// Capture the latest realtime callback per event so tests can fire it.
const realtimeCallbacks: Record<string, (event: unknown) => void> = {}

vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: vi.fn((eventType: string, cb: (event: unknown) => void) => {
    realtimeCallbacks[eventType] = cb
  }),
}))

import { renderHook, act } from "@testing-library/react"
import { usePendingApproval } from "@/hooks/use-pending-approval"

async function flushAsync() {
  for (let i = 0; i < 6; i++) await Promise.resolve()
}

const RUN = "run_abc123"
const wp = (over: Record<string, unknown> = {}) => ({
  token: "tok_1",
  pipeline_run_id: RUN,
  step_id: "approve",
  kind: "approval",
  prompt: "Restart the auth-svc pods in production",
  timeout_at: "2026-06-30T13:15:10.000Z",
  created_at: "2026-06-29T13:15:10.000Z",
  ...over,
})

describe("usePendingApproval", () => {
  let mockFetch: ReturnType<typeof vi.fn>

  beforeEach(() => {
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
    for (const k of Object.keys(realtimeCallbacks)) delete realtimeCallbacks[k]
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("does not fetch when workspace or run is missing", async () => {
    const { result } = renderHook(() => usePendingApproval(null, RUN))
    await act(async () => { await flushAsync() })
    expect(result.current.waitpoint).toBeNull()
    expect(mockFetch).not.toHaveBeenCalled()

    const { result: r2 } = renderHook(() => usePendingApproval("ws-1", null))
    await act(async () => { await flushAsync() })
    expect(r2.current.waitpoint).toBeNull()
  })

  it("resolves the approval waitpoint for THIS run only", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => [wp({ token: "other", pipeline_run_id: "run_zzz" }), wp()],
    })
    const { result } = renderHook(() => usePendingApproval("ws-1", RUN))
    await act(async () => { await flushAsync() })

    expect(mockFetch).toHaveBeenCalledWith(
      "/api/v1/workspaces/ws-1/pipelines/waitpoints",
      expect.objectContaining({ credentials: "include" }),
    )
    expect(result.current.waitpoint?.token).toBe("tok_1")
    expect(result.current.waitpoint?.pipeline_run_id).toBe(RUN)
  })

  it("ignores non-approval kinds and other runs", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => [
        wp({ kind: "event" }),
        wp({ pipeline_run_id: "run_other" }),
      ],
    })
    const { result } = renderHook(() => usePendingApproval("ws-1", RUN))
    await act(async () => { await flushAsync() })
    expect(result.current.waitpoint).toBeNull()
  })

  it("treats 503 (store not wired) as nothing pending, not an error", async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, status: 503, json: async () => ({}) })
    const { result } = renderHook(() => usePendingApproval("ws-1", RUN))
    await act(async () => { await flushAsync() })
    expect(result.current.waitpoint).toBeNull()
    expect(result.current.error).toBeNull()
  })

  it("decide() POSTs to the approve endpoint and clears the banner", async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => [wp()] })
    const { result } = renderHook(() => usePendingApproval("ws-1", RUN))
    await act(async () => { await flushAsync() })
    expect(result.current.waitpoint?.token).toBe("tok_1")

    mockFetch.mockResolvedValueOnce({ ok: true, text: async () => "" })
    let ok = false
    await act(async () => {
      ok = await result.current.decide(true, "LGTM")
      await flushAsync()
    })
    expect(ok).toBe(true)
    expect(mockFetch).toHaveBeenLastCalledWith(
      "/api/v1/workspaces/ws-1/pipelines/waitpoints/tok_1/approve",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ approved: true, comment: "LGTM" }),
      }),
    )
    expect(result.current.waitpoint).toBeNull()
  })

  it("re-fetches on the pipeline.waitpoint.created realtime event", async () => {
    mockFetch.mockResolvedValue({ ok: true, json: async () => [] })
    renderHook(() => usePendingApproval("ws-1", RUN))
    await act(async () => { await flushAsync() })
    expect(mockFetch).toHaveBeenCalledTimes(1)

    await act(async () => {
      realtimeCallbacks["pipeline.waitpoint.created"]?.({})
      await flushAsync()
    })
    expect(mockFetch).toHaveBeenCalledTimes(2)
    expect(realtimeCallbacks["inbox.updated"]).toBeTypeOf("function")
  })
})

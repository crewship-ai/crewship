import { describe, it, expect, vi, beforeEach } from "vitest"
import React from "react"
import { renderHook, act } from "@testing-library/react"

// Mock the WebSocket hook so we can drive status transitions and capture
// the options (url / getToken / onMessage) that RealtimeProvider wires up.
// use-websocket has its own dedicated test files; here we only care about
// the provider's logic on top of it.
const sendMock = vi.fn()
let wsStatus = "disconnected"
let capturedOpts: {
  url: string
  getToken: () => Promise<string | null>
  onMessage: (msg: { type: string; payload?: unknown }) => void
} | null = null

vi.mock("@/hooks/use-websocket", () => ({
  useWebSocket: (opts: never) => {
    capturedOpts = opts
    return { status: wsStatus, send: sendMock }
  },
}))

let mockWorkspaceId: string | null = "ws1"
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: mockWorkspaceId }),
}))

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(),
}))

import {
  RealtimeProvider,
  useRealtime,
  useRealtimeEvent,
  useRealtimeChannel,
} from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"

const apiFetchMock = apiFetch as ReturnType<typeof vi.fn>

const wrapper = ({ children }: { children: React.ReactNode }) => (
  <RealtimeProvider>{children}</RealtimeProvider>
)

function res(status: number, body?: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as unknown as Response
}

beforeEach(() => {
  sendMock.mockClear()
  apiFetchMock.mockReset()
  wsStatus = "disconnected"
  capturedOpts = null
  mockWorkspaceId = "ws1"
})

describe("RealtimeProvider — getToken", () => {
  it("passes the page-derived /ws URL to useWebSocket", () => {
    renderHook(() => useRealtime(), { wrapper })
    expect(capturedOpts!.url).toMatch(/^wss?:\/\/.+\/ws$/)
  })

  it("returns the token from a 200 response", async () => {
    renderHook(() => useRealtime(), { wrapper })
    apiFetchMock.mockResolvedValueOnce(res(200, { token: "tok-1" }))
    await expect(capturedOpts!.getToken()).resolves.toBe("tok-1")
    expect(apiFetchMock).toHaveBeenCalledWith("/api/v1/ws-token")
  })

  it("returns null on 401 (auth dead — stop retrying)", async () => {
    renderHook(() => useRealtime(), { wrapper })
    apiFetchMock.mockResolvedValueOnce(res(401))
    await expect(capturedOpts!.getToken()).resolves.toBeNull()
  })

  it("returns null on 403", async () => {
    renderHook(() => useRealtime(), { wrapper })
    apiFetchMock.mockResolvedValueOnce(res(403))
    await expect(capturedOpts!.getToken()).resolves.toBeNull()
  })

  it("throws on non-auth non-2xx status (transient → backoff)", async () => {
    renderHook(() => useRealtime(), { wrapper })
    apiFetchMock.mockResolvedValueOnce(res(503))
    await expect(capturedOpts!.getToken()).rejects.toThrow(/503/)
  })

  it("throws when the response body is missing the token field", async () => {
    renderHook(() => useRealtime(), { wrapper })
    apiFetchMock.mockResolvedValueOnce(res(200, { nope: true }))
    await expect(capturedOpts!.getToken()).rejects.toThrow(/missing token/)
  })
})

describe("RealtimeProvider — message dispatch", () => {
  it("dispatches valid events to subscribers with typed payload + timestamp", () => {
    const { result } = renderHook(() => useRealtime(), { wrapper })
    const cb = vi.fn()
    act(() => {
      result.current.subscribe("run.started", cb)
    })
    act(() => {
      capturedOpts!.onMessage({ type: "run.started", payload: { run_id: "r1" } })
    })
    expect(cb).toHaveBeenCalledTimes(1)
    const event = cb.mock.calls[0][0]
    expect(event.type).toBe("run.started")
    expect(event.payload).toEqual({ run_id: "r1" })
    expect(event.timestamp).toBeInstanceOf(Date)
  })

  it("drops messages whose type is not in the realtime allowlist", () => {
    const { result } = renderHook(() => useRealtime(), { wrapper })
    const cb = vi.fn()
    act(() => {
      result.current.subscribe("run.started", cb)
    })
    act(() => {
      capturedOpts!.onMessage({ type: "totally.unknown", payload: {} })
    })
    expect(cb).not.toHaveBeenCalled()
  })

  it("normalizes a non-object payload to an empty object", () => {
    const { result } = renderHook(() => useRealtime(), { wrapper })
    const cb = vi.fn()
    act(() => {
      result.current.subscribe("agent.status", cb)
    })
    act(() => {
      capturedOpts!.onMessage({ type: "agent.status", payload: "raw-string" })
    })
    expect(cb).toHaveBeenCalledTimes(1)
    expect(cb.mock.calls[0][0].payload).toEqual({})
  })

  it("a throwing subscriber does not break other subscribers", () => {
    const { result } = renderHook(() => useRealtime(), { wrapper })
    const bad = vi.fn(() => {
      throw new Error("boom")
    })
    const good = vi.fn()
    act(() => {
      result.current.subscribe("inbox.updated", bad)
      result.current.subscribe("inbox.updated", good)
    })
    act(() => {
      capturedOpts!.onMessage({ type: "inbox.updated", payload: {} })
    })
    expect(bad).toHaveBeenCalledTimes(1)
    expect(good).toHaveBeenCalledTimes(1)
  })

  it("unsubscribe stops delivery", () => {
    const { result } = renderHook(() => useRealtime(), { wrapper })
    const cb = vi.fn()
    let unsub: () => void
    act(() => {
      unsub = result.current.subscribe("crew.updated", cb)
    })
    act(() => {
      capturedOpts!.onMessage({ type: "crew.updated", payload: {} })
    })
    expect(cb).toHaveBeenCalledTimes(1)
    act(() => {
      unsub()
      capturedOpts!.onMessage({ type: "crew.updated", payload: {} })
    })
    expect(cb).toHaveBeenCalledTimes(1)
  })
})

describe("RealtimeProvider — workspace channel subscription", () => {
  it("subscribes to workspace:{id} when connected, unsubscribes on unmount", () => {
    wsStatus = "connected"
    const { unmount } = renderHook(() => useRealtime(), { wrapper })
    expect(sendMock).toHaveBeenCalledWith({
      type: "subscribe",
      channel: "workspace:ws1",
    })
    unmount()
    expect(sendMock).toHaveBeenCalledWith({
      type: "unsubscribe",
      channel: "workspace:ws1",
    })
  })

  it("does not send any subscribe while disconnected", () => {
    wsStatus = "disconnected"
    renderHook(() => useRealtime(), { wrapper })
    expect(sendMock).not.toHaveBeenCalled()
  })

  it("does not subscribe when workspaceId is null even if connected", () => {
    wsStatus = "connected"
    mockWorkspaceId = null
    renderHook(() => useRealtime(), { wrapper })
    expect(sendMock).not.toHaveBeenCalled()
  })

  it("re-subscribes component-registered channels after (re)connect", () => {
    wsStatus = "disconnected"
    const { result, rerender } = renderHook(() => useRealtime(), { wrapper })
    act(() => {
      // Registered while offline: must NOT send yet, only remember.
      result.current.subscribeChannel("agent:a1")
    })
    expect(sendMock).not.toHaveBeenCalled()

    wsStatus = "connected"
    rerender()
    expect(sendMock).toHaveBeenCalledWith({
      type: "subscribe",
      channel: "workspace:ws1",
    })
    expect(sendMock).toHaveBeenCalledWith({
      type: "subscribe",
      channel: "agent:a1",
    })
  })
})

describe("RealtimeProvider — subscribeChannel", () => {
  it("sends subscribe immediately when connected and unsubscribe on cleanup", () => {
    wsStatus = "connected"
    const { result } = renderHook(() => useRealtime(), { wrapper })
    let unsub: () => void
    act(() => {
      unsub = result.current.subscribeChannel("session:s1")
    })
    expect(sendMock).toHaveBeenCalledWith({
      type: "subscribe",
      channel: "session:s1",
    })
    act(() => {
      unsub()
    })
    expect(sendMock).toHaveBeenCalledWith({
      type: "unsubscribe",
      channel: "session:s1",
    })
  })

  it("cleanup does not send unsubscribe when no longer connected", () => {
    wsStatus = "connected"
    const { result, rerender } = renderHook(() => useRealtime(), { wrapper })
    let unsub: () => void
    act(() => {
      unsub = result.current.subscribeChannel("session:s2")
    })
    sendMock.mockClear()

    wsStatus = "disconnected"
    rerender()
    sendMock.mockClear()
    act(() => {
      unsub()
    })
    const unsubCalls = sendMock.mock.calls.filter(
      (c) => c[0].type === "unsubscribe" && c[0].channel === "session:s2",
    )
    expect(unsubCalls).toHaveLength(0)
  })
})

describe("useRealtime — guard", () => {
  it("throws when used outside RealtimeProvider", () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => {})
    expect(() => renderHook(() => useRealtime())).toThrow(
      /must be used within a RealtimeProvider/,
    )
    spy.mockRestore()
  })
})

describe("useRealtimeEvent", () => {
  it("invokes the callback when the event fires", () => {
    const cb = vi.fn()
    renderHook(() => useRealtimeEvent("agent.updated", cb), { wrapper })
    act(() => {
      capturedOpts!.onMessage({ type: "agent.updated", payload: { id: "a1" } })
    })
    expect(cb).toHaveBeenCalledTimes(1)
    expect(cb.mock.calls[0][0].payload).toEqual({ id: "a1" })
  })

  it("always calls the LATEST callback (ref pattern, no resubscribe churn)", () => {
    const first = vi.fn()
    const second = vi.fn()
    const { rerender } = renderHook(
      ({ cb }: { cb: (e: unknown) => void }) => useRealtimeEvent("mission.updated", cb),
      { wrapper, initialProps: { cb: first } },
    )
    rerender({ cb: second })
    act(() => {
      capturedOpts!.onMessage({ type: "mission.updated", payload: {} })
    })
    expect(first).not.toHaveBeenCalled()
    expect(second).toHaveBeenCalledTimes(1)
  })

  it("stops delivering after unmount", () => {
    const cb = vi.fn()
    const { unmount } = renderHook(() => useRealtimeEvent("crew.created", cb), {
      wrapper,
    })
    unmount()
    act(() => {
      capturedOpts!.onMessage({ type: "crew.created", payload: {} })
    })
    expect(cb).not.toHaveBeenCalled()
  })
})

describe("useRealtimeChannel", () => {
  it("subscribes for the component lifetime and unsubscribes on unmount", () => {
    wsStatus = "connected"
    const { unmount } = renderHook(() => useRealtimeChannel("agent:42"), {
      wrapper,
    })
    expect(sendMock).toHaveBeenCalledWith({
      type: "subscribe",
      channel: "agent:42",
    })
    unmount()
    expect(sendMock).toHaveBeenCalledWith({
      type: "unsubscribe",
      channel: "agent:42",
    })
  })

  it("is a no-op for a null channel", () => {
    wsStatus = "connected"
    mockWorkspaceId = null // suppress the workspace subscribe noise
    renderHook(() => useRealtimeChannel(null), { wrapper })
    expect(sendMock).not.toHaveBeenCalled()
  })
})

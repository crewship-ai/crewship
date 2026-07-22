import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act, waitFor } from "@testing-library/react"

// Mock apiFetch before importing the hook — the hook fetches the short-lived
// WS ticket from /api/v1/ws-token on connect.
const apiFetchMock = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetchMock(...args),
}))

import { useAdminWebSocket } from "../use-admin-websocket"

let mockInstances: MockWebSocket[] = []

class MockWebSocket {
  url: string
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: ((event: { code: number; reason: string }) => void) | null = null
  sent: string[] = []

  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3

  constructor(url: string) {
    this.url = url
    mockInstances.push(this)
  }

  send(data: string) {
    this.sent.push(data)
  }

  close(code = 1000, reason = "") {
    this.readyState = MockWebSocket.CLOSED
    this.onclose?.({ code, reason })
  }

  simulateOpen() {
    this.readyState = MockWebSocket.OPEN
    this.onopen?.()
  }

  simulateMessage(data: unknown) {
    this.onmessage?.({ data: JSON.stringify(data) })
  }
}

function mockTokenResponse(token: string) {
  apiFetchMock.mockResolvedValue({
    ok: true,
    json: () => Promise.resolve({ token }),
  })
}

describe("useAdminWebSocket", () => {
  beforeEach(() => {
    mockInstances = []
    apiFetchMock.mockReset()
    vi.stubGlobal("WebSocket", MockWebSocket)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("connects to a bare /ws URL — the token must never ride the query string (FE1/B1, #1254 pt.2)", async () => {
    mockTokenResponse("secret-ticket")
    renderHook(() => useAdminWebSocket({ enabled: true, workspaceId: "ws-1" }))

    await waitFor(() => expect(mockInstances.length).toBe(1))
    const ws = mockInstances[0]
    expect(ws.url).not.toContain("token=")
    expect(ws.url).not.toContain("secret-ticket")
    expect(new URL(ws.url).pathname).toBe("/ws")
  })

  it("sends the auth frame as the FIRST message on open, then the keeper subscribe", async () => {
    mockTokenResponse("secret-ticket")
    const { result } = renderHook(() =>
      useAdminWebSocket({ enabled: true, workspaceId: "ws-1" }),
    )

    await waitFor(() => expect(mockInstances.length).toBe(1))
    const ws = mockInstances[0]
    act(() => ws.simulateOpen())

    expect(ws.sent.length).toBe(2)
    expect(JSON.parse(ws.sent[0])).toEqual({ type: "auth", token: "secret-ticket" })
    expect(JSON.parse(ws.sent[1])).toEqual({ type: "subscribe", channel: "keeper:ws-1" })
    expect(result.current.keeperWsStatus).toBe("connected")
  })

  it("collects keeper_event frames into keeperLiveEvents", async () => {
    mockTokenResponse("secret-ticket")
    const { result } = renderHook(() =>
      useAdminWebSocket({ enabled: true, workspaceId: "ws-1" }),
    )

    await waitFor(() => expect(mockInstances.length).toBe(1))
    const ws = mockInstances[0]
    act(() => ws.simulateOpen())
    act(() =>
      ws.simulateMessage({
        type: "keeper_event",
        payload: { request_id: "r1", decision: "approved" },
      }),
    )

    expect(result.current.keeperLiveEvents).toHaveLength(1)
    expect(result.current.keeperLiveEvents[0].request_id).toBe("r1")
  })

  it("does not connect when disabled or without a workspace", async () => {
    mockTokenResponse("secret-ticket")
    renderHook(() => useAdminWebSocket({ enabled: false, workspaceId: "ws-1" }))
    renderHook(() => useAdminWebSocket({ enabled: true, workspaceId: null }))

    // Flush any pending microtasks — no socket may have been opened.
    await act(async () => {})
    expect(apiFetchMock).not.toHaveBeenCalled()
    expect(mockInstances.length).toBe(0)
  })
})

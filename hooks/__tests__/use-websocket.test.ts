import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

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

  simulateError() {
    this.onerror?.()
  }
}

import { useWebSocket } from "@/hooks/use-websocket"

const tokenFn = (token: string | null) => () => Promise.resolve(token)

describe("useWebSocket", () => {
  beforeEach(() => {
    mockInstances = []
    vi.useFakeTimers()
    vi.stubGlobal("WebSocket", MockWebSocket)
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.stubGlobal("WebSocket", undefined)
  })

  it("does not open a socket when getToken returns null", async () => {
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken: tokenFn(null) }),
    )
    await act(async () => { await vi.runAllTimersAsync() })
    expect(mockInstances).toHaveLength(0)
    expect(result.current.status).toBe("error")
  })

  it("connects when getToken returns a string", async () => {
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken: tokenFn("test-token") }),
    )
    await act(async () => { await vi.runAllTimersAsync() })
    expect(mockInstances).toHaveLength(1)
    expect(mockInstances[0].url).toContain("token=test-token")
    expect(result.current.status).toBe("connecting")
  })

  it("sets connected status on open", async () => {
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken: tokenFn("test-token") }),
    )
    await act(async () => { await vi.runAllTimersAsync() })
    act(() => { mockInstances[0].simulateOpen() })
    expect(result.current.status).toBe("connected")
  })

  it("dispatches messages through onMessage", async () => {
    const onMessage = vi.fn()
    renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken: tokenFn("t"), onMessage }),
    )
    await act(async () => { await vi.runAllTimersAsync() })
    act(() => {
      mockInstances[0].simulateOpen()
      mockInstances[0].simulateMessage({ type: "hello", payload: "world" })
    })
    expect(onMessage).toHaveBeenCalledTimes(1)
    expect(onMessage.mock.calls[0][0]).toMatchObject({ type: "hello" })
  })

  it("close code 4401 emits auth:session-expired and stops retrying", async () => {
    const handler = vi.fn()
    window.addEventListener("auth:session-expired", handler)
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken: tokenFn("t") }),
    )
    await act(async () => { await vi.runAllTimersAsync() })
    act(() => { mockInstances[0].simulateOpen() })
    act(() => { mockInstances[0].close(4401, "session_revoked") })

    expect(handler).toHaveBeenCalledTimes(1)
    expect(result.current.status).toBe("error")

    // Even after the longest backoff window, no second connection attempt.
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
    expect(mockInstances).toHaveLength(1)
    window.removeEventListener("auth:session-expired", handler)
  })

  it("session_revoked frame stops retrying", async () => {
    const handler = vi.fn()
    window.addEventListener("auth:session-expired", handler)
    renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken: tokenFn("t") }),
    )
    await act(async () => { await vi.runAllTimersAsync() })
    act(() => {
      mockInstances[0].simulateOpen()
      mockInstances[0].simulateMessage({ type: "session_revoked", payload: { reason: "session_revoked" } })
    })
    expect(handler).toHaveBeenCalledTimes(1)
    window.removeEventListener("auth:session-expired", handler)
  })

  it("normal close triggers reconnect with backoff and re-fetches token", async () => {
    const tokens = ["t1", "t2"]
    const getToken = vi.fn(async () => tokens.shift() ?? null)
    renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken }),
    )
    await act(async () => { await vi.runAllTimersAsync() })
    expect(mockInstances).toHaveLength(1)
    expect(mockInstances[0].url).toContain("token=t1")

    act(() => { mockInstances[0].close(1006, "abnormal") })

    // First reconnect attempt: ~1-2s with jitter.
    await act(async () => { await vi.advanceTimersByTimeAsync(2_500) })
    await act(async () => { await vi.runAllTimersAsync() })
    expect(getToken).toHaveBeenCalledTimes(2)
    expect(mockInstances).toHaveLength(2)
    expect(mockInstances[1].url).toContain("token=t2")
  })

  it("reconnect attempts are capped — after MAX, fires session-expired", async () => {
    const handler = vi.fn()
    window.addEventListener("auth:session-expired", handler)
    renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken: tokenFn("t") }),
    )
    await act(async () => { await vi.runAllTimersAsync() })

    // Force-close repeatedly. Each backoff is capped at 30s + jitter,
    // so 35s per iteration safely advances past the next reconnect
    // window. After MAX_RECONNECT_ATTEMPTS (=8) the hook should give up.
    for (let i = 0; i < 9; i++) {
      const idx = mockInstances.length - 1
      if (idx < 0) break
      act(() => { mockInstances[idx].close(1006) })
      await act(async () => { await vi.advanceTimersByTimeAsync(35_000) })
    }

    expect(handler).toHaveBeenCalled()
    window.removeEventListener("auth:session-expired", handler)
  })

  it("send is a no-op until socket is OPEN", async () => {
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken: tokenFn("t") }),
    )
    await act(async () => { await vi.runAllTimersAsync() })
    act(() => { result.current.send({ type: "ping" }) })
    expect(mockInstances[0].sent).toHaveLength(0)

    act(() => {
      mockInstances[0].simulateOpen()
      result.current.send({ type: "ping" })
    })
    expect(mockInstances[0].sent).toHaveLength(1)
  })
})

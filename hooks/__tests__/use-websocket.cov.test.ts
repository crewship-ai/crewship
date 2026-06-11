import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

// Mirrors the MockWebSocket in use-websocket.test.ts; duplicated because
// existing test files must not be modified.
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

  simulateRawMessage(data: string) {
    this.onmessage?.({ data })
  }

  simulateError() {
    this.onerror?.()
  }
}

import { useWebSocket } from "@/hooks/use-websocket"

const tokenFn = (token: string | null) => () => Promise.resolve(token)

let authEventListeners: EventListener[] = []
function addAuthHandler(handler: EventListener) {
  window.addEventListener("auth:session-expired", handler)
  authEventListeners.push(handler)
  return handler
}

describe("useWebSocket — coverage extensions", () => {
  beforeEach(() => {
    mockInstances = []
    authEventListeners = []
    vi.useFakeTimers()
    vi.stubGlobal("WebSocket", MockWebSocket)
  })

  afterEach(() => {
    for (const h of authEventListeners) {
      window.removeEventListener("auth:session-expired", h)
    }
    authEventListeners = []
    vi.useRealTimers()
    vi.stubGlobal("WebSocket", undefined)
  })

  it("empty url falls back to a page-derived /ws URL", async () => {
    renderHook(() => useWebSocket({ url: "", getToken: tokenFn("t") }))
    await act(async () => { await vi.runAllTimersAsync() })
    expect(mockInstances).toHaveLength(1)
    const url = new URL(mockInstances[0].url)
    expect(url.protocol).toMatch(/^wss?:$/)
    expect(url.host).toBe(window.location.host)
    expect(url.pathname).toBe("/ws")
    expect(url.searchParams.get("token")).toBe("t")
  })

  it("socket error event flips status to 'error'", async () => {
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken: tokenFn("t") }),
    )
    await act(async () => { await vi.runAllTimersAsync() })
    act(() => { mockInstances[0].simulateOpen() })
    expect(result.current.status).toBe("connected")
    act(() => { mockInstances[0].simulateError() })
    expect(result.current.status).toBe("error")
  })

  it("schema-invalid frames are dropped before reaching onMessage", async () => {
    const onMessage = vi.fn()
    renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken: tokenFn("t"), onMessage }),
    )
    await act(async () => { await vi.runAllTimersAsync() })
    act(() => {
      mockInstances[0].simulateOpen()
      // type must be a string per the Zod schema.
      mockInstances[0].simulateMessage({ type: 123 })
      // payload must be a string or record.
      mockInstances[0].simulateMessage({ type: "ok-type", payload: 42 })
    })
    expect(onMessage).not.toHaveBeenCalled()
  })

  it("non-JSON frames are ignored without crashing", async () => {
    const onMessage = vi.fn()
    renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken: tokenFn("t"), onMessage }),
    )
    await act(async () => { await vi.runAllTimersAsync() })
    act(() => {
      mockInstances[0].simulateOpen()
      mockInstances[0].simulateRawMessage("definitely{not json")
    })
    expect(onMessage).not.toHaveBeenCalled()
  })

  it("manual reconnect() after disconnect() opens a fresh socket", async () => {
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken: tokenFn("t") }),
    )
    await act(async () => { await vi.runAllTimersAsync() })
    expect(mockInstances).toHaveLength(1)

    act(() => { result.current.disconnect() })
    await act(async () => {
      result.current.reconnect()
      await vi.runAllTimersAsync()
    })
    expect(mockInstances).toHaveLength(2)
  })

  it("reconnect() after auth termination is a hard no-op", async () => {
    const handler = vi.fn()
    addAuthHandler(handler)
    const getToken = vi.fn(async () => null)
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken }),
    )
    await act(async () => { await vi.runAllTimersAsync() })
    expect(handler).toHaveBeenCalledTimes(1)
    expect(getToken).toHaveBeenCalledTimes(1)

    await act(async () => {
      result.current.reconnect()
      await vi.runAllTimersAsync()
    })
    // terminated: connect() bails before even fetching a token.
    expect(getToken).toHaveBeenCalledTimes(1)
    expect(mockInstances).toHaveLength(0)
  })

  it("unmount while a reconnect is pending cancels the scheduled attempt", async () => {
    const { unmount } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken: tokenFn("t") }),
    )
    await act(async () => { await vi.runAllTimersAsync() })
    expect(mockInstances).toHaveLength(1)

    // Abnormal close schedules a backoff reconnect…
    act(() => { mockInstances[0].close(1006, "abnormal") })
    // …but unmounting must clear it.
    unmount()
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
    expect(mockInstances).toHaveLength(1)
  })

  it("token going null mid-reconnect terminates with session-expired and clears the timer", async () => {
    const handler = vi.fn()
    addAuthHandler(handler)
    const tokens: (string | null)[] = ["t1", null]
    const getToken = vi.fn(async () => tokens.shift() ?? null)
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken }),
    )
    await act(async () => { await vi.runAllTimersAsync() })
    expect(mockInstances).toHaveLength(1)

    // Reconnect path: backend restarted, the refreshed ticket comes back null.
    act(() => { mockInstances[0].close(1006, "abnormal") })
    await act(async () => { await vi.runAllTimersAsync() })

    expect(handler).toHaveBeenCalledTimes(1)
    expect(result.current.status).toBe("error")
    expect(mockInstances).toHaveLength(1)

    // No further attempts ever.
    await act(async () => { await vi.advanceTimersByTimeAsync(120_000) })
    expect(getToken).toHaveBeenCalledTimes(2)
  })

  it("persistently throwing getToken hits the retry cap → transport error, no logout", async () => {
    const handler = vi.fn()
    addAuthHandler(handler)
    const getToken = vi.fn(async () => {
      throw new Error("backend down")
    })
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken }),
    )
    // Drain the full backoff schedule (8 retries then terminate).
    await act(async () => { await vi.runAllTimersAsync() })

    expect(result.current.status).toBe("error")
    expect(handler).not.toHaveBeenCalled()
    expect(mockInstances).toHaveLength(0)
    // initial + 8 backoff retries; the 9th call hits the cap and terminates.
    expect(getToken).toHaveBeenCalledTimes(9)

    // Terminated: no further attempts even after more time passes.
    await act(async () => { await vi.advanceTimersByTimeAsync(120_000) })
    expect(getToken).toHaveBeenCalledTimes(9)
  })

  it("disconnect() while getToken is in flight and rejecting → no retry scheduled", async () => {
    let rejectToken!: (e: Error) => void
    const getToken = vi.fn(
      () => new Promise<string | null>((_res, rej) => { rejectToken = rej }),
    )
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken }),
    )
    await act(async () => { await Promise.resolve() })
    expect(getToken).toHaveBeenCalledTimes(1)

    act(() => { result.current.disconnect() })
    await act(async () => {
      rejectToken(new Error("aborted"))
      await Promise.resolve()
    })
    await act(async () => { await vi.advanceTimersByTimeAsync(120_000) })
    // The rejection during disconnect must not schedule a backoff retry.
    expect(getToken).toHaveBeenCalledTimes(1)
    expect(mockInstances).toHaveLength(0)
  })

  it("disconnect() while getToken resolves late → token discarded, no socket opened", async () => {
    let resolveToken!: (t: string | null) => void
    const getToken = vi.fn(
      () => new Promise<string | null>((res) => { resolveToken = res }),
    )
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken }),
    )
    await act(async () => { await Promise.resolve() })
    expect(getToken).toHaveBeenCalledTimes(1)

    act(() => { result.current.disconnect() })
    await act(async () => {
      resolveToken("late-token")
      await Promise.resolve()
    })
    // The late token must not open a leaked connection.
    expect(mockInstances).toHaveLength(0)
  })
})

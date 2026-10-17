import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

// "Retry now" on the reconnecting banner (Harbor 2 G09). Same MockWebSocket as
// the other use-websocket suites.
let sockets: MockWebSocket[] = []

class MockWebSocket {
  url: string
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: ((event: { code: number; reason: string }) => void) | null = null
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  constructor(url: string) { this.url = url; sockets.push(this) }
  send() {}
  close(code = 1000, reason = "") { this.readyState = MockWebSocket.CLOSED; this.onclose?.({ code, reason }) }
  open() { this.readyState = MockWebSocket.OPEN; this.onopen?.() }
}

import { useWebSocket } from "@/hooks/use-websocket"

const URL = "ws://localhost:8080/ws"

describe("useWebSocket retryNow", () => {
  beforeEach(() => { sockets = []; vi.useFakeTimers(); vi.stubGlobal("WebSocket", MockWebSocket) })
  afterEach(() => { vi.useRealTimers(); vi.stubGlobal("WebSocket", undefined) })

  it("skips the backoff wait and connects at once", async () => {
    const getToken = vi.fn(async () => "t")
    const { result } = renderHook(() => useWebSocket({ url: URL, getToken }))
    await act(async () => { await Promise.resolve(); await Promise.resolve() })
    expect(sockets).toHaveLength(1)
    act(() => { sockets[0].open() })
    act(() => { sockets[0].close(1006) })
    expect(result.current.status).toBe("disconnected")
    // A backoff timer is pending; nothing new yet.
    expect(sockets).toHaveLength(1)
    await act(async () => { result.current.retryNow(); await Promise.resolve(); await Promise.resolve() })
    expect(sockets).toHaveLength(2)
    // The cancelled timer does not open a third socket later.
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
    expect(sockets).toHaveLength(2)
  })

  it("starts a fresh budget after the automatic loop gave up", async () => {
    let failing = true
    const getToken = vi.fn(async () => { if (failing) throw new Error("backend down"); return "t" })
    const { result } = renderHook(() => useWebSocket({ url: URL, getToken }))
    await act(async () => { await vi.runAllTimersAsync() })
    expect(result.current.status).toBe("error")
    const calls = getToken.mock.calls.length
    failing = false
    await act(async () => { result.current.retryNow(); await Promise.resolve(); await Promise.resolve() })
    expect(getToken).toHaveBeenCalledTimes(calls + 1)
    expect(sockets).toHaveLength(1)
  })

  it("never opens a second socket while one is opening or open", async () => {
    const getToken = vi.fn(async () => "t")
    const { result } = renderHook(() => useWebSocket({ url: URL, getToken }))
    await act(async () => { await Promise.resolve(); await Promise.resolve() })
    act(() => { result.current.retryNow() })
    act(() => { sockets[0].open() })
    act(() => { result.current.retryNow() })
    await act(async () => { await Promise.resolve() })
    expect(sockets).toHaveLength(1)
    expect(getToken).toHaveBeenCalledTimes(1)
  })

  it("stays final after the session died", async () => {
    const getToken = vi.fn(async () => null)
    const { result } = renderHook(() => useWebSocket({ url: URL, getToken }))
    await act(async () => { await Promise.resolve(); await Promise.resolve() })
    expect(result.current.status).toBe("error")
    await act(async () => { result.current.retryNow(); await Promise.resolve() })
    expect(getToken).toHaveBeenCalledTimes(1)
  })
})

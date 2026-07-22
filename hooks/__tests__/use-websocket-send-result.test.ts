import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

// New test file (not an edit to use-websocket.test.ts / .cov.test.ts, which
// are left untouched per repo convention) covering the WS message-size
// guard work: send() must report success/failure instead of silently
// no-oping, and the byte-accounting helper must be correct for the
// large-paste scenario the guard exists for.
//
// Mirrors the MockWebSocket used by the existing use-websocket test files.
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
}

import { useWebSocket, encodedByteLength, WS_MAX_OUTBOUND_FRAME_BYTES } from "@/hooks/use-websocket"

const tokenFn = (token: string | null) => () => Promise.resolve(token)

describe("useWebSocket send() result", () => {
  beforeEach(() => {
    mockInstances = []
    vi.useFakeTimers()
    vi.stubGlobal("WebSocket", MockWebSocket)
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.stubGlobal("WebSocket", undefined)
  })

  it("returns false and does not touch the socket when not OPEN", async () => {
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken: tokenFn("t") }),
    )
    await act(async () => { await vi.runAllTimersAsync() })

    let ok: boolean | undefined
    act(() => { ok = result.current.send({ type: "ping" }) })

    expect(ok).toBe(false)
    expect(mockInstances[0].sent).toHaveLength(0)
  })

  it("returns true and forwards the frame once the socket is OPEN", async () => {
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken: tokenFn("t") }),
    )
    await act(async () => { await vi.runAllTimersAsync() })
    act(() => { mockInstances[0].simulateOpen() })

    let ok: boolean | undefined
    act(() => { ok = result.current.send({ type: "ping" }) })

    expect(ok).toBe(true)
    // simulateOpen sends the post-open auth frame first, then the ping.
    expect(mockInstances[0].sent).toHaveLength(2)
    expect(mockInstances[0].sent[1]).toBe(JSON.stringify({ type: "ping" }))
  })

  it("returns false again after the socket drops back to non-OPEN", async () => {
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", getToken: tokenFn("t") }),
    )
    await act(async () => { await vi.runAllTimersAsync() })
    act(() => { mockInstances[0].simulateOpen() })
    act(() => { mockInstances[0].close(1006, "abnormal") })

    let ok: boolean | undefined
    act(() => { ok = result.current.send({ type: "ping" }) })
    expect(ok).toBe(false)
  })
})

describe("encodedByteLength", () => {
  it("matches JS string length for plain ASCII", () => {
    expect(encodedByteLength("hello world")).toBe(11)
  })

  it("counts multi-byte UTF-8 characters correctly, not UTF-16 code units", () => {
    // A single emoji is a surrogate pair in JS (length 2) but 4 bytes on
    // the wire in UTF-8. This is exactly the miscount a naive
    // `.length`-based guard would get wrong.
    const emoji = "🚀"
    expect(emoji.length).toBe(2)
    expect(encodedByteLength(emoji)).toBe(4)
  })

  it("sizes a large multi-byte paste far above its JS .length", () => {
    const bigEmojiPaste = "🚀".repeat(20000) // 40,000 UTF-16 units, 80,000 bytes
    expect(encodedByteLength(bigEmojiPaste)).toBeGreaterThan(WS_MAX_OUTBOUND_FRAME_BYTES)
    expect(bigEmojiPaste.length).toBeLessThan(WS_MAX_OUTBOUND_FRAME_BYTES)
  })
})

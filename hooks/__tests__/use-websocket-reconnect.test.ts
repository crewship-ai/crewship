import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

// #3783: a lost socket reads as "connecting" while a retry is scheduled, never
// gives up for a transport failure, and comes back at once when the network,
// the tab or the window does.
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
  /** What a browser does when the server goes away: error, then close. */
  fail(code = 1006) { this.onerror?.(); this.close(code) }
}

import { useWebSocket, type WSStatus } from "@/hooks/use-websocket"

const URL = "ws://localhost:8080/ws"
const flush = async () => { await act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve() }) }
const last = () => sockets[sockets.length - 1]

let online = true

describe("useWebSocket reconnect states (#3783)", () => {
  beforeEach(() => {
    sockets = []
    online = true
    vi.useFakeTimers()
    vi.stubGlobal("WebSocket", MockWebSocket)
    vi.spyOn(navigator, "onLine", "get").mockImplementation(() => online)
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.stubGlobal("WebSocket", undefined)
    vi.restoreAllMocks()
  })

  async function connected(getToken: () => Promise<string | null> = async () => "t") {
    const seen: WSStatus[] = []
    const hook = renderHook(() => useWebSocket({ url: URL, getToken, onStatusChange: (s) => seen.push(s) }))
    await flush()
    act(() => { last().open() })
    expect(hook.result.current.status).toBe("connected")
    seen.length = 0
    return { ...hook, seen }
  }

  it("reads as connecting, not disconnected or error, while a retry is scheduled", async () => {
    const { result, seen } = await connected()
    act(() => { last().fail() })
    expect(result.current.status).toBe("connecting")
    expect(result.current.reason).toBe("retrying")
    expect(seen).not.toContain("error")
    expect(seen).not.toContain("disconnected")
  })

  it("marks a service restart (1012) or going away (1001) as a planned restart", async () => {
    for (const code of [1012, 1001]) {
      sockets = []
      const { result, unmount } = await connected()
      act(() => { last().close(code) })
      expect(result.current.status).toBe("connecting")
      expect(result.current.reason).toBe("restart")
      unmount()
    }
  })

  it("keeps retrying every 30 s after the fast budget, as offline, without flickering", async () => {
    let down = false
    const getToken = vi.fn(async () => { if (down) throw new Error("backend down"); return "t" })
    const { result, seen } = await connected(getToken)
    down = true
    act(() => { last().fail() })
    // The fast budget: 1+2+4+8+16+30+30+30 s plus jitter.
    await act(async () => { await vi.advanceTimersByTimeAsync(140_000) })
    expect(result.current.status).toBe("offline")
    expect(result.current.reason).toBe("unreachable")
    seen.length = 0
    const calls = getToken.mock.calls.length
    await act(async () => { await vi.advanceTimersByTimeAsync(31_000) })
    expect(getToken.mock.calls.length).toBe(calls + 1)
    await act(async () => { await vi.advanceTimersByTimeAsync(31_000) })
    expect(getToken.mock.calls.length).toBe(calls + 2)
    expect(result.current.status).toBe("offline")
    expect(seen.every((s) => s === "offline")).toBe(true)
    // …and recovers by itself once the backend is back.
    down = false
    await act(async () => { await vi.advanceTimersByTimeAsync(31_000) })
    act(() => { last().open() })
    expect(result.current.status).toBe("connected")
    expect(result.current.reason).toBeNull()
  })

  for (const [name, fire] of [
    ["online", () => window.dispatchEvent(new Event("online"))],
    ["visibilitychange to visible", () => document.dispatchEvent(new Event("visibilitychange"))],
    ["window focus", () => window.dispatchEvent(new Event("focus"))],
  ] as const) {
    it(`reconnects at once on ${name}`, async () => {
      await connected()
      act(() => { last().fail() })
      expect(sockets).toHaveLength(1)
      await act(async () => { fire(); await Promise.resolve(); await Promise.resolve() })
      expect(sockets).toHaveLength(2)
      // The cancelled backoff timer does not open a third socket later.
      await act(async () => { await vi.advanceTimersByTimeAsync(5_000) })
      expect(sockets).toHaveLength(2)
    })
  }

  it("reconnects at once on a manual retry()", async () => {
    const { result } = await connected()
    act(() => { last().fail() })
    await act(async () => { result.current.retry(); await Promise.resolve(); await Promise.resolve() })
    expect(sockets).toHaveLength(2)
  })

  it("ignores focus while the socket is open", async () => {
    await connected()
    await act(async () => { window.dispatchEvent(new Event("focus")); await Promise.resolve() })
    expect(sockets).toHaveLength(1)
  })

  it("says the browser is offline and waits for the online event", async () => {
    const getToken = vi.fn(async () => "t")
    const { result } = await connected(getToken)
    online = false
    act(() => { last().fail() })
    expect(result.current.status).toBe("offline")
    expect(result.current.reason).toBe("browser-offline")
    await act(async () => { await vi.advanceTimersByTimeAsync(300_000) })
    expect(getToken).toHaveBeenCalledTimes(1)
    expect(sockets).toHaveLength(1)
    online = true
    await act(async () => { window.dispatchEvent(new Event("online")); await Promise.resolve(); await Promise.resolve() })
    expect(sockets).toHaveLength(2)
    expect(result.current.status).toBe("connecting")
    act(() => { last().open() })
    expect(result.current.status).toBe("connected")
  })

  it("keeps the session-revoked close (4401) terminal", async () => {
    const { result } = await connected()
    act(() => { last().close(4401) })
    expect(result.current.status).toBe("error")
    await act(async () => { window.dispatchEvent(new Event("online")); await vi.advanceTimersByTimeAsync(300_000) })
    expect(sockets).toHaveLength(1)
  })
})

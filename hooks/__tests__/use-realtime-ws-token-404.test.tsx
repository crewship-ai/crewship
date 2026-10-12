// #2861 — a 404 from /api/v1/ws-token means realtime does not exist for this
// session (the restricted allowlist refuses the route). It is terminal: one
// request, status "unavailable", no reconnect loop, no banner, no logout.
// Before the fix it was treated as transient and retried forever behind a
// permanent "Reconnecting…" banner.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { act, render, renderHook, screen, waitFor } from "@testing-library/react"

const requests: string[] = []

function respond(status: number, body: unknown = {}): Response {
  return { ok: status >= 200 && status < 300, status, headers: new Headers(), json: async () => body } as unknown as Response
}

vi.mock("@/lib/api-fetch", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api-fetch")>()
  return {
    ...actual,
    apiFetch: vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      requests.push(url)
      // The list still reads trusted: the restriction landed after the tab
      // loaded it, so the 404 is the first sign.
      if (url === "/api/v1/workspaces") return respond(200, [{ id: "ws-a", name: "A", slug: "a", currentUserRole: "MEMBER" }])
      return respond(404)
    }),
  }
})

vi.mock("@/hooks/use-auth", () => ({
  useSessionSafe: () => ({ data: { user: { id: "u-1" } }, status: "authenticated" }),
}))

const sockets: string[] = []
class RecordingWebSocket {
  static OPEN = 1
  readyState = 0
  constructor(url: string) { sockets.push(url) }
  send() {}
  close() {}
}

import { RealtimeProvider, useRealtime } from "@/hooks/use-realtime"
import { WsUnavailableError, useWebSocket } from "@/hooks/use-websocket"
import { RealtimeStatusBanner } from "@/components/layout/realtime-status-banner"
import { _resetWorkspaceStoreForTests } from "@/hooks/use-workspace"

const NativeWebSocket = globalThis.WebSocket
const expired = vi.fn()
beforeEach(() => {
  _resetWorkspaceStoreForTests()
  requests.length = 0
  sockets.length = 0
  globalThis.WebSocket = RecordingWebSocket as unknown as typeof WebSocket
  window.addEventListener("auth:session-expired", expired)
})
afterEach(() => {
  globalThis.WebSocket = NativeWebSocket
  window.removeEventListener("auth:session-expired", expired)
  expired.mockReset()
})

function Probe() {
  const { status } = useRealtime()
  return <><span data-testid="status">{status}</span><RealtimeStatusBanner /></>
}

describe("getToken 404 is terminal", () => {
  it("asks for a ticket once, settles on 'unavailable', shows no banner and keeps the user signed in", async () => {
    render(<RealtimeProvider><Probe /></RealtimeProvider>)
    await waitFor(() => expect(requests).toContain("/api/v1/ws-token"))
    // Backoff would retry after ~1–2 s and again ~2–3 s later; wait past both
    // and past the banner's 3 s threshold.
    await act(async () => { await new Promise((r) => setTimeout(r, 4000)) })
    expect(requests.filter((r) => r === "/api/v1/ws-token")).toHaveLength(1)
    expect(screen.getByTestId("status").textContent).toBe("unavailable")
    expect(screen.queryByRole("status")).toBeNull()
    expect(sockets).toHaveLength(0)
    expect(expired).not.toHaveBeenCalled()
    // The 404 also re-reads the access mode (the session may just have become restricted).
    expect(requests.filter((r) => r === "/api/v1/workspaces").length).toBeGreaterThanOrEqual(2)
  })
})

describe("useWebSocket with WsUnavailableError", () => {
  it("stops for good with status 'unavailable' and no session-expired event", async () => {
    vi.useFakeTimers()
    try {
      const getToken = vi.fn(async () => { throw new WsUnavailableError() })
      const { result } = renderHook(() => useWebSocket({ url: "ws://x/ws", getToken }))
      await act(async () => { await vi.advanceTimersByTimeAsync(120_000) })
      expect(getToken).toHaveBeenCalledTimes(1)
      expect(result.current.status).toBe("unavailable")
      expect(expired).not.toHaveBeenCalled()
    } finally {
      vi.useRealTimers()
    }
  })

  it("tries again from a clean slate when the consumer re-enables it", async () => {
    let unavailable = true
    const getToken = vi.fn(async () => { if (unavailable) throw new WsUnavailableError(); return "tok" })
    const { result, rerender } = renderHook(({ enabled }) => useWebSocket({ enabled, url: "ws://x/ws", getToken }), { initialProps: { enabled: true } })
    await waitFor(() => expect(result.current.status).toBe("unavailable"))
    unavailable = false
    rerender({ enabled: false })
    rerender({ enabled: true })
    await waitFor(() => expect(sockets).toHaveLength(1))
    expect(getToken).toHaveBeenCalledTimes(2)
  })
})

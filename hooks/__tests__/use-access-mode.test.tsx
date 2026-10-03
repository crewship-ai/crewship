// #2861 — the access mode is a property of the SESSION, re-evaluated when the
// workspace list changes (workspace switch, access changed while open), and
// the realtime socket follows it.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { act, render, renderHook, screen, waitFor } from "@testing-library/react"

const requests: string[] = []
let workspacesPayload: unknown = []

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
      if (url === "/api/v1/workspaces") return respond(200, workspacesPayload)
      if (url === "/api/v1/ws-token") {
        const restricted = (workspacesPayload as { currentUserAccessMode?: string }[]).some((w) => w.currentUserAccessMode === "restricted")
        return restricted ? respond(404) : respond(200, { token: "tok" })
      }
      return respond(404)
    }),
  }
})

vi.mock("@/hooks/use-auth", () => ({
  useSessionSafe: () => ({ data: { user: { id: "u-1" } }, status: "authenticated" }),
}))

const sockets: RecordingWebSocket[] = []
class RecordingWebSocket {
  static OPEN = 1
  readyState = 0
  closed = false
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  onmessage: (() => void) | null = null
  constructor(public url: string) { sockets.push(this) }
  send() {}
  close() { this.closed = true }
}

import { deriveAccessMode, useAccessMode, useTrustedWorkspaceId } from "@/hooks/use-access-mode"
import { RealtimeProvider, useRealtime } from "@/hooks/use-realtime"
import { RealtimeStatusBanner } from "@/components/layout/realtime-status-banner"
import { _resetWorkspaceStoreForTests, useWorkspace, type WorkspaceData } from "@/hooks/use-workspace"

// The trusted projection carries no currentUserAccessMode at all.
const TRUSTED_ROW = (id: string): WorkspaceData => ({ id, name: id, slug: id, currentUserRole: "MEMBER" })
const ROW = (id: string, mode: "trusted" | "restricted") => ({ ...TRUSTED_ROW(id), currentUserAccessMode: mode })

const NativeWebSocket = globalThis.WebSocket
beforeEach(() => {
  _resetWorkspaceStoreForTests()
  requests.length = 0
  sockets.length = 0
  globalThis.WebSocket = RecordingWebSocket as unknown as typeof WebSocket
  ;(localStorage.getItem as ReturnType<typeof vi.fn>).mockImplementation(() => null)
})
afterEach(() => { globalThis.WebSocket = NativeWebSocket })

describe("deriveAccessMode", () => {
  it.each([
    ["no list yet", [], true, false, "loading"],
    ["cold load failed", [], false, true, "loading"],
    ["no memberships", [], false, false, "trusted"],
    ["trusted projection (field absent)", [TRUSTED_ROW("a")], false, false, "trusted"],
    ["every row trusted", [ROW("a", "trusted"), ROW("b", "trusted")], false, false, "trusted"],
    ["one restricted row restricts the session", [ROW("a", "trusted"), ROW("b", "restricted")], false, false, "restricted"],
    ["a background re-read keeps the held answer", [ROW("a", "restricted")], true, false, "restricted"],
  ] as const)("%s", (_name, rows, loading, error, want) => {
    expect(deriveAccessMode(rows, loading, error)).toBe(want)
  })
})

describe("useAccessMode", () => {
  it("is restricted in every workspace of a mixed-membership account, across a workspace switch", async () => {
    workspacesPayload = [ROW("ws-a", "restricted"), ROW("ws-b", "trusted")]
    const { result } = renderHook(() => ({ mode: useAccessMode(), trustedId: useTrustedWorkspaceId(), ws: useWorkspace() }))
    await waitFor(() => expect(result.current.mode).toBe("restricted"))
    expect(result.current.ws.workspaceId).toBe("ws-a")
    expect(result.current.trustedId).toBeNull()
    act(() => result.current.ws.setWorkspaceId("ws-b"))
    expect(result.current.ws.workspaceId).toBe("ws-b")
    expect(result.current.mode).toBe("restricted")
    expect(result.current.trustedId).toBeNull()
  })
})

function StatusProbe() {
  const { status } = useRealtime()
  const { refresh } = useWorkspace()
  return (
    <>
      <span data-testid="status">{status}</span>
      <button onClick={() => { void refresh() }}>recheck</button>
      <RealtimeStatusBanner />
    </>
  )
}

describe("RealtimeProvider follows the session access mode", () => {
  it("drops the socket when access becomes restricted while open, and reconnects when it is trusted again", async () => {
    workspacesPayload = [TRUSTED_ROW("ws-a")]
    render(<RealtimeProvider><StatusProbe /></RealtimeProvider>)
    await waitFor(() => expect(sockets).toHaveLength(1))
    expect(requests.filter((r) => r === "/api/v1/ws-token")).toHaveLength(1)

    // An admin restricts the membership while the tab is open.
    workspacesPayload = [ROW("ws-a", "restricted")]
    await act(async () => { screen.getByText("recheck").click() })
    await waitFor(() => expect(screen.getByTestId("status").textContent).toBe("unavailable"))
    expect(sockets[0].closed).toBe(true)
    const tokenCalls = requests.filter((r) => r === "/api/v1/ws-token").length
    await act(async () => { await new Promise((r) => setTimeout(r, 3500)) })
    expect(requests.filter((r) => r === "/api/v1/ws-token")).toHaveLength(tokenCalls)
    expect(sockets).toHaveLength(1)
    expect(screen.queryByRole("status")).toBeNull()

    // ... and lifts it again.
    workspacesPayload = [ROW("ws-a", "trusted")]
    await act(async () => { screen.getByText("recheck").click() })
    await waitFor(() => expect(sockets).toHaveLength(2))
    expect(requests.filter((r) => r === "/api/v1/ws-token")).toHaveLength(tokenCalls + 1)
  })

  it("requests no ticket while the access mode is loading", async () => {
    workspacesPayload = [ROW("ws-a", "restricted")]
    render(<RealtimeProvider><StatusProbe /></RealtimeProvider>)
    expect(screen.getByTestId("status").textContent).toBe("connecting")
    await waitFor(() => expect(screen.getByTestId("status").textContent).toBe("unavailable"))
    expect(requests).toEqual(["/api/v1/workspaces"])
    expect(sockets).toHaveLength(0)
  })
})

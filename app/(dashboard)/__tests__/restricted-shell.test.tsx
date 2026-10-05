// #2861 — a restricted account must get a shell that only talks to the
// restricted allowlist (internal/api/restricted_access.go). Every request the
// dashboard layout makes is recorded and judged by the same rule the server
// applies (lib/__tests__/restricted-route-oracle.ts reads the Go source).
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { act, render, screen } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import { serverAllowsRestricted } from "@/lib/__tests__/restricted-route-oracle"

const requests: string[] = []
let workspacesPayload: unknown = []
let workspacesPending = false

function respond(status: number, body: unknown = {}): Response {
  return { ok: status >= 200 && status < 300, status, headers: new Headers(), json: async () => body } as unknown as Response
}

vi.mock("@/lib/api-fetch", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api-fetch")>()
  return {
    ...actual,
    apiFetch: vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      requests.push(`${(init?.method ?? "GET").toUpperCase()} ${url}`)
      if (url.split("?")[0] === "/api/v1/workspaces" && (init?.method ?? "GET") === "GET") {
        if (workspacesPending) return new Promise<Response>(() => {})
        return respond(200, workspacesPayload)
      }
      // Everything else: what the restricted allowlist answers.
      return respond(404, { error: "Resource not found" })
    }),
  }
})

vi.mock("@/hooks/use-auth", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/use-auth")>()
  const session = { user: { id: "u-1", name: "Rita Restricted", email: "rita@example.test", avatar_url: "", is_instance_admin: false }, expires: "2099-01-01" }
  return {
    ...actual,
    useSession: () => ({ data: session, status: "authenticated" }),
    useSessionSafe: () => ({ data: session, status: "authenticated" }),
    useAuth: () => ({ session, status: "authenticated", signIn: vi.fn(), signOut: vi.fn(), refresh: vi.fn() }),
    useIsInstanceAdmin: () => false,
  }
})

let mobile = false
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mobile }))

const sockets: string[] = []
class RecordingWebSocket {
  static OPEN = 1
  readyState = 0
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  onmessage: (() => void) | null = null
  constructor(url: string) { sockets.push(url) }
  send() {}
  close() {}
}

import DashboardLayout from "../layout"
import RoutinesPage from "../routines/page"
import { _resetWorkspaceStoreForTests } from "@/hooks/use-workspace"

const RESTRICTED_A = { id: "ws-a", name: "Alpha", slug: "alpha", currentUserRole: "MEMBER", current_user_role: "MEMBER", currentUserAccessMode: "restricted" }
const TRUSTED_B = { id: "ws-b", name: "Beta", slug: "beta", currentUserRole: "MEMBER", current_user_role: "MEMBER", currentUserAccessMode: "trusted" }

function mount(children: React.ReactNode = <div data-testid="page">page</div>) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <DashboardLayout>{children}</DashboardLayout>
    </QueryClientProvider>,
  )
}

async function settle(ms: number) {
  await act(async () => { await new Promise((r) => setTimeout(r, ms)) })
}

function forbidden(): string[] {
  return requests.filter((r) => {
    const space = r.indexOf(" ")
    return !serverAllowsRestricted(r.slice(0, space), r.slice(space + 1))
  })
}

beforeEach(() => {
  _resetWorkspaceStoreForTests()
  requests.length = 0
  sockets.length = 0
  workspacesPayload = [RESTRICTED_A]
  workspacesPending = false
  mobile = false
  globalThis.WebSocket = RecordingWebSocket as unknown as typeof WebSocket
  ;(localStorage.getItem as ReturnType<typeof vi.fn>).mockImplementation(() => null)
})

const NativeWebSocket = globalThis.WebSocket
afterEach(() => {
  globalThis.WebSocket = NativeWebSocket
})

describe("dashboard shell for a restricted account (#2861)", () => {
  it("sends no request outside the restricted allowlist, opens no socket and shows no reconnect banner", async () => {
    mount()
    expect(await screen.findByTestId("page")).toBeInTheDocument()
    // The realtime banner appears after 3 s of "disconnected"; wait past it.
    await settle(3500)
    expect(forbidden()).toEqual([])
    expect(requests.some((r) => r.includes("/ws-token"))).toBe(false)
    expect(sockets).toEqual([])
    expect(screen.queryByText(/Reconnecting|Connection lost/)).toBeNull()
  })

  it("sends nothing but the workspace list while the access mode is still loading", async () => {
    workspacesPending = true
    mount()
    await settle(1000)
    expect(requests).toEqual(["GET /api/v1/workspaces"])
    expect(sockets).toEqual([])
    expect(screen.queryByTestId("page")).toBeNull()
  })

  it("stays restricted in a workspace where the membership is trusted (mixed membership)", async () => {
    workspacesPayload = [RESTRICTED_A, TRUSTED_B]
    ;(localStorage.getItem as ReturnType<typeof vi.fn>).mockImplementation(() => "ws-b")
    mount()
    expect(await screen.findByTestId("page")).toBeInTheDocument()
    await settle(800)
    expect(forbidden()).toEqual([])
    expect(sockets).toEqual([])
  })

  it("keeps the phone tab bar off the inbox count", async () => {
    mobile = true
    mount()
    expect(await screen.findByTestId("page")).toBeInTheDocument()
    await settle(800)
    expect(forbidden()).toEqual([])
  })

  it("renders the private routines surface in a trusted workspace of a restricted account", async () => {
    workspacesPayload = [RESTRICTED_A, TRUSTED_B]
    ;(localStorage.getItem as ReturnType<typeof vi.fn>).mockImplementation(() => "ws-b")
    mount(<RoutinesPage />)
    expect(await screen.findByText("Private routines")).toBeInTheDocument()
    await settle(800)
    expect(forbidden()).toEqual([])
  })
})

// Harbor 2 G01/G02: the shell before it has a session or a workspace list.
// No full-page spinner: the ground for a second, then the app frame as a
// skeleton; a workspace list that failed keeps the frame, shows the error
// card with Retry and leaves Log out reachable. A signed-out visitor keeps
// the screen they asked for through the login page.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import { loginRedirectUrl } from "@/lib/login-redirect"

const auth = vi.hoisted(() => ({ status: "loading" as string, signOut: vi.fn(async () => {}) }))
vi.mock("@/hooks/use-auth", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/use-auth")>()
  const session = { user: { id: "u-1", name: "Pavel", email: "p@example.test", avatar_url: "", is_instance_admin: false }, expires: "2099-01-01" }
  const data = () => (auth.status === "authenticated" ? session : null)
  return {
    ...actual,
    useSession: () => ({ data: data(), status: auth.status }),
    useSessionSafe: () => ({ data: data(), status: auth.status }),
    useAuth: () => ({ session: data(), status: auth.status, signIn: vi.fn(), signOut: auth.signOut, refresh: vi.fn() }),
    useIsInstanceAdmin: () => false,
  }
})

let workspacesStatus = 200
vi.mock("@/lib/api-fetch", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api-fetch")>()
  return {
    ...actual,
    apiFetch: vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input).split("?")[0]
      if (url === "/api/v1/workspaces") {
        return { ok: workspacesStatus < 300, status: workspacesStatus, headers: new Headers(), json: async () => ({ error: "down" }) } as unknown as Response
      }
      return new Promise<Response>(() => {})
    }),
  }
})
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))

import DashboardLayout from "../layout"
import { _resetWorkspaceStoreForTests } from "@/hooks/use-workspace"

let replaced: string | null = null
/** The address bar. The router mock (vitest.setup.ts) keeps saying "/", as
 *  Next does when the backend's SPA fallback hydrated the root page. */
function goTo(pathname: string, search = "") {
  Object.defineProperty(window, "location", {
    configurable: true,
    value: { pathname, search, href: "http://localhost" + pathname + search, replace: (u: string) => { replaced = u } },
  })
}
beforeEach(() => {
  vi.useFakeTimers()
  auth.status = "loading"
  auth.signOut.mockClear()
  workspacesStatus = 200
  _resetWorkspaceStoreForTests()
  replaced = null
  goTo("/issues/CRE-12", "?tab=activity")
})
afterEach(() => { cleanup(); vi.useRealTimers() })

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}><DashboardLayout><div data-testid="page">page</div></DashboardLayout></QueryClientProvider>)
}

describe("dashboard layout — before the session", () => {
  it("loading renders nothing for a second, then the frame skeleton — never a spinner", () => {
    mount()
    const frame = screen.getByRole("status")
    expect(frame).toHaveAttribute("data-state", "app-loading")
    expect(frame).toHaveAttribute("data-quiet")
    expect(document.querySelector(".app-frame-side")).toBeNull()
    act(() => { vi.advanceTimersByTime(1000) })
    expect(document.querySelector(".app-frame-side")).not.toBeNull()
    expect(document.querySelectorAll(".app-frame-row")).toHaveLength(8)
    expect(document.querySelector(".app-frame-top")).not.toBeNull()
    expect(document.querySelector('[data-slot="spinner"], .animate-spin')).toBeNull()
    expect(screen.queryByTestId("page")).toBeNull()
  })

  it("unauthenticated redirects with redirect=<path> so login brings the person back", () => {
    auth.status = "unauthenticated"
    mount()
    expect(replaced).toBe("/login?redirect=%2Fissues%2FCRE-12%3Ftab%3Dactivity")
  })

  it("an explicit Log out goes to the plain login page", () => {
    auth.status = "authenticated"
    workspacesStatus = 500
    const view = mount()
    auth.status = "unauthenticated"
    view.rerender(<QueryClientProvider client={new QueryClient()}><DashboardLayout><div /></DashboardLayout></QueryClientProvider>)
    expect(replaced).toBe("/login")
  })

  it("workspaces error renders the error card with Retry in the frame and keeps Log out reachable", async () => {
    vi.useRealTimers()
    auth.status = "authenticated"
    workspacesStatus = 500
    mount()
    const alert = await screen.findByRole("alert", undefined, { timeout: 3000 })
    expect(alert).toHaveAttribute("data-state", "error")
    expect(alert).toHaveTextContent("Your workspaces could not load")
    expect(alert).toHaveTextContent("The server did not answer. Nothing you saved is lost.")
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument()
    expect(document.querySelector(".app-frame-side")).not.toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Log out" }))
    expect(auth.signOut).toHaveBeenCalledOnce()
  })
})

// The static-export backend answers a path without a page with the ROOT
// index.html (SPA fallback), so this layout runs with the dashboard page as
// its child and the router reporting "/". It must show the not-found state.
describe("dashboard layout — a path the app has no page for", () => {
  it("signed out: the bare not-found state, no detour through login", () => {
    goTo("/does-not-exist")
    auth.status = "unauthenticated"
    mount()
    expect(replaced).toBeNull()
    expect(screen.getByText("This page doesn’t exist")).toBeInTheDocument()
    expect(screen.getByRole("main")).toHaveClass("bare-state")
    expect(screen.getByRole("link", { name: "Go to Dashboard" })).toHaveAttribute("href", "/")
    expect(screen.queryByTestId("page")).toBeNull()
  })

  it("signed in: the not-found state inside the shell, and the page under it never mounts", async () => {
    vi.useRealTimers()
    goTo("/does-not-exist")
    auth.status = "authenticated"
    workspacesStatus = 200
    mount()
    expect(await screen.findByText("This page doesn’t exist", undefined, { timeout: 3000 })).toBeInTheDocument()
    const state = screen.getByText("This page doesn’t exist").closest("[data-state=not-found]") as HTMLElement
    expect(within(state).getByRole("button", { name: "Search" })).toBeInTheDocument()
    expect(screen.queryByRole("main", { name: undefined })?.classList.contains("bare-state") ?? false).toBe(false)
    expect(document.querySelector("[data-slot=sidebar-container], [data-slot=sidebar]")).not.toBeNull()
    expect(screen.queryByTestId("page")).toBeNull()
    expect(replaced).toBeNull()
  })

  it("a known deep path still mounts its page", async () => {
    vi.useRealTimers()
    goTo("/issues/CRE-12")
    auth.status = "authenticated"
    mount()
    expect(await screen.findByTestId("page", undefined, { timeout: 3000 })).toBeInTheDocument()
    expect(screen.queryByText("This page doesn’t exist")).toBeNull()
  })
})

describe("loginRedirectUrl", () => {
  it.each([
    ["/issues/CRE-12", "", false, "/login?redirect=%2Fissues%2FCRE-12"],
    ["/", "", false, "/login"],
    ["/login", "?x=1", false, "/login"],
    ["//evil.example", "", false, "/login"],
    ["/issues", "", true, "/login"],
  ])("%s%s (had session: %s) → %s", (path, search, had, expected) => {
    expect(loginRedirectUrl(path, search, had)).toBe(expected)
  })
})

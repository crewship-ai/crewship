import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, act, within, waitFor } from "@testing-library/react"

// Harbor 2 M3 (#3350): the Admin console's sections live in the one
// SecondaryPanel — a column with a remembered collapse on a desktop, the top
// bar's bottom sheet on a phone (it used to be a hand-made aside and a left
// Sheet behind a hamburger on the sub-bar).

const h = vi.hoisted(() => ({ mobile: false, admin: true as boolean | null, replace: vi.fn() }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => h.mobile }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: h.replace, refresh: vi.fn() }),
  usePathname: () => "/admin",
  useSearchParams: () => new URLSearchParams(),
}))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => h.admin }))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-1", role: "OWNER", loading: false }),
}))
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async () => new Response("{}", { status: 200, headers: { "content-type": "application/json" } })),
}))
vi.mock("../hooks/use-admin-websocket", () => ({
  useAdminWebSocket: () => ({ keeperLiveEvents: [], keeperWsStatus: "idle" }),
}))
vi.mock("../tabs/overview-tab", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../tabs/overview-tab")>()),
  OverviewTab: () => <p>overview body</p>,
}))

import { useAppStore } from "@/lib/store"
import AdminPage from "../page"

let store: Map<string, string>
beforeEach(() => {
  cleanup()
  h.mobile = false
  h.admin = true
  h.replace.mockReset()
  store = new Map()
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  })
  window.history.replaceState(null, "", "/admin")
  act(() => {
    useAppStore.getState().setSecondaryPanel(null)
    useAppStore.getState().setSecondaryPanelOpen(false)
  })
})
afterEach(() => vi.unstubAllGlobals())

const panel = () => document.querySelector<HTMLElement>("[data-slot=secondary-panel]")

describe("Admin navigation panel", () => {
  it("renders inside the SecondaryPanel with the id admin", async () => {
    render(<AdminPage />)
    await screen.findByRole("region", { name: "Admin Overview" })
    const aside = panel()!
    expect(aside).toHaveAttribute("data-panel-id", "admin")
    expect(within(aside).getByRole("navigation", { name: "Admin sections" })).toBeInTheDocument()
    expect(within(aside).getByPlaceholderText("Search admin…")).toBeInTheDocument()
  })

  it("collapses to a rail and remembers it", async () => {
    render(<AdminPage />)
    await screen.findByRole("region", { name: "Admin Overview" })
    fireEvent.click(screen.getByRole("button", { name: "Hide Admin navigation" }))
    expect(panel()).toHaveAttribute("data-collapsed", "true")
    expect(screen.queryByRole("navigation", { name: "Admin sections" })).toBeNull()
    expect(store.get("crewship.pref.panel.admin.collapsed")).toBe("true")
  })

  it("on a phone opens on the list of sections; a pick opens the section with Back", async () => {
    h.mobile = true
    render(<AdminPage />)
    // PNL-05: no section named, so the list is the page.
    const list = await waitFor(() => { const el = panel(); if (!el) throw new Error("no panel"); return el })
    expect(list).toHaveAttribute("data-phone", "inline")
    expect(screen.queryByRole("region", { name: "Admin Overview" })).toBeNull()
    fireEvent.click(within(list).getByRole("button", { name: "Runtime" }))
    expect(await screen.findByRole("region", { name: "Admin Runtime" })).toBeInTheDocument()
    expect(new URLSearchParams(window.location.search).get("tab")).toBe("providers")
    // The list moved to the top bar's sheet; Back returns to it and drops ?tab=.
    expect(useAppStore.getState().secondaryPanel).toMatchObject({ id: "admin", label: "Admin navigation" })
    fireEvent.click(screen.getByRole("button", { name: "Back to Admin" }))
    await waitFor(() => expect(panel()).toHaveAttribute("data-phone", "inline"))
    expect(new URLSearchParams(window.location.search).has("tab")).toBe(false)
  })

  it("on a phone a link that names a section opens it straight away", async () => {
    h.mobile = true
    window.history.replaceState(null, "", "/admin?tab=ratelimits")
    render(<AdminPage />)
    expect(await screen.findByRole("region", { name: "Admin Limits" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Back to Admin" })).toBeInTheDocument()
  })
})

describe("Admin section head", () => {
  it("names the section with its tile and one sentence; Overview has none", async () => {
    window.history.replaceState(null, "", "/admin?tab=providers")
    render(<AdminPage />)
    const region = await screen.findByRole("region", { name: "Admin Runtime" })
    const head = region.querySelector("[data-slot=settings-page-head]") as HTMLElement
    expect(within(head).getByRole("heading", { level: 2, name: "Runtime" })).toBeInTheDocument()
    expect(head).toHaveTextContent("What the server runs agents on, and how much it logs")
    cleanup()
    window.history.replaceState(null, "", "/admin")
    render(<AdminPage />)
    const overview = await screen.findByRole("region", { name: "Admin Overview" })
    expect(overview.querySelector("[data-slot=settings-page-head]")).toBeNull()
  })
})

describe("Admin for someone who is not an instance administrator", () => {
  it("M01 non-admin sees no-access, not a redirect", async () => {
    h.admin = false
    render(<AdminPage />)
    expect(await screen.findByText("You can’t open the Admin console")).toBeInTheDocument()
    expect(screen.getByText(/needs an instance administrator/)).toBeInTheDocument()
    expect(h.replace).not.toHaveBeenCalled()
    // Nothing of the console is drawn for them.
    expect(screen.queryByRole("navigation", { name: "Admin sections" })).toBeNull()
  })

  it("an old ?tab= link still follows the move, after the gate", async () => {
    h.admin = false
    window.history.replaceState(null, "", "/admin?tab=users")
    render(<AdminPage />)
    await waitFor(() => expect(h.replace).toHaveBeenCalledWith("/admin/people"))
  })

  // admin-h2 e2e flaked when the move waited for two round trips first.
  it("an old ?tab= link moves before the session is known", async () => {
    h.admin = null
    window.history.replaceState(null, "", "/admin?tab=posture")
    render(<AdminPage />)
    await waitFor(() => expect(h.replace).toHaveBeenCalledWith("/admin/security"))
  })

  it("waits for the session instead of judging it", () => {
    h.admin = null
    render(<AdminPage />)
    expect(screen.getByRole("status", { name: "Loading the Admin console" })).toBeInTheDocument()
    expect(screen.queryByText(/You can’t open/)).toBeNull()
  })
})

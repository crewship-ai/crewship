import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, act, within, waitFor } from "@testing-library/react"

// Harbor 2 M3 (#3350): the Admin console's sections live in the one
// SecondaryPanel — a column with a remembered collapse on a desktop, the top
// bar's bottom sheet on a phone (it used to be a hand-made aside and a left
// Sheet behind a hamburger on the sub-bar).

const h = vi.hoisted(() => ({ mobile: false }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => h.mobile }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), refresh: vi.fn() }),
  usePathname: () => "/admin",
  useSearchParams: () => new URLSearchParams(),
}))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => true }))
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

  it("on a phone is the top bar's sheet, with no hamburger on the sub-bar, and a pick closes it", async () => {
    h.mobile = true
    render(<AdminPage />)
    await screen.findByRole("region", { name: "Admin Overview" })
    expect(panel()).toBeNull()
    expect(screen.queryByRole("button", { name: "Open admin navigation" })).toBeNull()
    expect(useAppStore.getState().secondaryPanel).toEqual({ id: "admin", label: "Admin navigation" })

    act(() => useAppStore.getState().setSecondaryPanelOpen(true))
    const sheet = screen.getByRole("dialog", { name: "Admin navigation" })
    fireEvent.click(within(sheet).getByRole("button", { name: "Runtime" }))
    await waitFor(() => expect(useAppStore.getState().secondaryPanelOpen).toBe(false))
    expect(new URLSearchParams(window.location.search).get("tab")).toBe("providers")
  })
})

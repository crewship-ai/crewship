import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, act, within, waitFor } from "@testing-library/react"

// Harbor 2 M3 (#3350): the Settings sections live in the one SecondaryPanel —
// a column with a remembered collapse on a desktop, the top bar's bottom
// sheet on a phone (it used to be a hand-made 280 px aside and a left Sheet
// behind a hamburger on the sub-bar).

const h = vi.hoisted(() => ({ mobile: false }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => h.mobile }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn(), push: vi.fn(), prefetch: vi.fn(), back: vi.fn(), forward: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(window.location.search),
  usePathname: () => "/settings",
}))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "w1", role: "OWNER", loading: false }),
}))
vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    session: { user: { id: "u1", name: "Ada", email: "ada@example.com" } },
    signOut: vi.fn().mockResolvedValue(undefined),
    refresh: vi.fn(),
  }),
}))
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => false }, hasCapability: () => false }),
}))
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async (url: string) => ({
    ok: true,
    status: 200,
    json: async () =>
      url.includes("/members")
        ? []
        : url.includes("/preferences")
          ? {}
          : { id: "w1", name: "Acme", slug: "acme", preferred_language: null, _count: { crews: 1, agents: 2, members: 3 } },
  })),
}))
vi.mock("../sections/profile-section", () => ({ ProfileSection: () => <div data-testid="profile-section" /> }))
vi.mock("../sections/general-section", () => ({ GeneralSection: () => <div data-testid="general-section" /> }))

import { useAppStore } from "@/lib/store"
import { SettingsNav } from "../settings-nav"
import { SettingsLayout } from "../settings-layout"

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
  window.history.replaceState(null, "", "/settings")
  act(() => {
    useAppStore.getState().setSecondaryPanel(null)
    useAppStore.getState().setSecondaryPanelOpen(false)
  })
})
afterEach(() => vi.unstubAllGlobals())

const panel = () => document.querySelector<HTMLElement>("[data-slot=secondary-panel]")

describe("Settings navigation panel", () => {
  it("renders inside the SecondaryPanel with the id settings", () => {
    render(<SettingsNav activeTab="profile" onTabChange={vi.fn()} role="OWNER" />)
    const aside = panel()!
    expect(aside).toHaveAttribute("data-panel-id", "settings")
    expect(within(aside).getByRole("navigation", { name: "Settings sections" })).toBeInTheDocument()
    // The search is the panel toolbar, with the panel's one collapse button.
    const search = within(aside).getByPlaceholderText("Search settings…")
    expect(search.closest(".secondary-panel-toolbar")).toContainElement(
      within(aside).getByRole("button", { name: "Hide Settings navigation" }),
    )
  })

  it("collapses to a rail and remembers it", () => {
    render(<SettingsNav activeTab="profile" onTabChange={vi.fn()} role="OWNER" />)
    fireEvent.click(screen.getByRole("button", { name: "Hide Settings navigation" }))
    expect(panel()).toHaveAttribute("data-collapsed", "true")
    expect(screen.queryByRole("navigation", { name: "Settings sections" })).toBeNull()
    expect(store.get("crewship.pref.panel.settings.collapsed")).toBe("true")
  })

  it("on a phone is the top bar's sheet, with no hamburger on the sub-bar, and a pick closes it", async () => {
    h.mobile = true
    render(<SettingsLayout />)
    await screen.findByTestId("profile-section")
    expect(panel()).toBeNull()
    expect(screen.queryByRole("button", { name: "Open settings navigation" })).toBeNull()
    expect(useAppStore.getState().secondaryPanel).toEqual({ id: "settings", label: "Settings navigation" })

    act(() => useAppStore.getState().setSecondaryPanelOpen(true))
    const sheet = screen.getByRole("dialog", { name: "Settings navigation" })
    fireEvent.click(within(sheet).getByRole("button", { name: "General" }))
    await waitFor(() => expect(useAppStore.getState().secondaryPanelOpen).toBe(false))
    expect(new URLSearchParams(window.location.search).get("tab")).toBe("general")
  })
})

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, act } from "@testing-library/react"

// Phone pilot, Settings (PILOT-TELEFON-STAV rows 3–4): /settings?tab=appearance
// opens Appearance; on a phone Back from a section goes to the section list
// — through the browser's history when the list is under it, by replacing
// the address after a direct link, never out of the app — and the browser's
// own Back from a section shows the list again. The list's search survives.

const h = vi.hoisted(() => ({ mobile: true, push: vi.fn(), replace: vi.fn() }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => h.mobile }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: h.replace, push: h.push, prefetch: vi.fn(), back: vi.fn(), forward: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(window.location.search),
  usePathname: () => "/settings",
}))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "w1", role: "OWNER", loading: false }),
}))
vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({ session: { user: { id: "u1", name: "Ada", email: "ada@example.com" } }, signOut: vi.fn(), refresh: vi.fn() }),
}))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ abilities: { can: () => false }, hasCapability: () => false }) }))
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async (url: string) => ({
    ok: true, status: 200,
    json: async () => url.includes("/members") ? [] : { id: "w1", name: "Acme", slug: "acme", preferred_language: null, _count: { crews: 1, agents: 2, members: 3 } },
  })),
}))
vi.mock("../sections/profile-section", () => ({ ProfileSection: () => <div data-testid="profile-section" /> }))
vi.mock("../sections/general-section", async () => {
  const { usePageSave } = await import("@/components/ui/page-save-bar")
  return {
    // A manual-save form with one edit pending.
    GeneralSection: () => {
      usePageSave({ label: "Workspace", count: 1, save: () => {}, discard: () => {} })
      return <div data-testid="general-section" />
    },
  }
})
vi.mock("../sections/appearance-section", () => ({ AppearanceSection: () => <div data-testid="appearance-section" /> }))

import { useAppStore } from "@/lib/store"
import { listDetailKey } from "@/hooks/use-phone-list-detail"
import { SettingsLayout } from "../settings-layout"

let slot: HTMLElement
beforeEach(() => {
  h.mobile = true
  h.push.mockReset()
  h.replace.mockReset()
  sessionStorage.clear()
  window.history.replaceState(null, "", "/settings")
  slot = document.createElement("div")
  slot.dataset.areaTitle = "Settings"
  document.body.appendChild(slot)
  act(() => {
    useAppStore.getState().setPageBarSlot(slot)
    useAppStore.getState().setSecondaryPanel(null)
    useAppStore.getState().setSecondaryPanelOpen(false)
  })
})
afterEach(() => {
  cleanup()
  slot.remove()
  act(() => useAppStore.getState().setPageBarSlot(null))
})

const go = (url: string, rerender: () => void) => {
  window.history.replaceState(null, "", url)
  rerender()
}

describe("Settings deep link", () => {
  it.each([true, false])("?tab=appearance opens Appearance (phone %s)", async (mobile) => {
    h.mobile = mobile
    window.history.replaceState(null, "", "/settings?tab=appearance")
    render(<SettingsLayout />)
    expect(await screen.findByTestId("appearance-section")).toBeInTheDocument()
    expect(screen.queryByTestId("profile-section")).toBeNull()
  })
})

describe("Settings on a phone: list → section → Back", () => {
  it("opening a section pushes it, so the browser's Back has the list under it", () => {
    render(<SettingsLayout />)
    fireEvent.click(screen.getByRole("button", { name: "Appearance" }))
    expect(h.push).toHaveBeenCalledWith("/settings?tab=appearance")
  })

  it("Back after a direct link replaces the section with the list", async () => {
    window.history.replaceState(null, "", "/settings?tab=appearance")
    render(<SettingsLayout />)
    await screen.findByTestId("appearance-section")
    fireEvent.click(screen.getByRole("button", { name: "Back to Settings" }))
    expect(h.replace).toHaveBeenCalledWith("/settings")
    expect(screen.getByRole("navigation", { name: "Settings sections" })).toBeInTheDocument()
  })

  it("Back after opening from the list goes back in history", () => {
    const view = render(<SettingsLayout />)
    fireEvent.click(screen.getByRole("button", { name: "Appearance" }))
    go("/settings?tab=appearance", () => view.rerender(<SettingsLayout />))
    const back = vi.spyOn(window.history, "back").mockImplementation(() => {})
    fireEvent.click(screen.getByRole("button", { name: "Back to Settings" }))
    expect(back).toHaveBeenCalledTimes(1)
    back.mockRestore()
  })

  it("the browser's Back to /settings shows the list again", async () => {
    window.history.replaceState(null, "", "/settings?tab=appearance")
    const view = render(<SettingsLayout />)
    await screen.findByTestId("appearance-section")
    go("/settings", () => view.rerender(<SettingsLayout />))
    expect(screen.getByRole("navigation", { name: "Settings sections" })).toBeInTheDocument()
    expect(screen.queryByTestId("appearance-section")).toBeNull()
  })

  it("keeps the section search when the list comes back after a section", () => {
    sessionStorage.setItem(listDetailKey("settings", "w1"), JSON.stringify({ detail: "/settings?tab=appearance", state: { query: "app" } }))
    render(<SettingsLayout />)
    expect((screen.getByPlaceholderText("Search settings…") as HTMLInputElement).value).toBe("app")
  })

  it("asks before Back drops a section's unsaved edits", async () => {
    window.history.replaceState(null, "", "/settings?tab=general")
    render(<SettingsLayout />)
    await screen.findByTestId("general-section")
    fireEvent.click(screen.getByRole("button", { name: "Back to Settings" }))
    expect(await screen.findByRole("alertdialog", { name: /Leave with 1 unsaved change/ })).toBeInTheDocument()
    expect(h.replace).not.toHaveBeenCalled()
    expect(screen.getByTestId("general-section")).toBeInTheDocument()
  })
})

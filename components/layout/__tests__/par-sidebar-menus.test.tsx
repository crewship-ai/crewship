import { beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { readFileSync } from "node:fs"
import path from "node:path"

// Parity with the v67 wireframe: sidebar insets (PAR-003), search field
// (PAR-004), user row (PAR-007), user menu (PAR-141), sidebar-mode menu (PAR-143).

const css = readFileSync(path.resolve(__dirname, "../../../app/globals.css"), "utf8")
function rule(selector: string): string {
  const i = css.indexOf(`${selector} {`)
  return i < 0 ? "" : css.slice(i, css.indexOf("}", i))
}

const menu = vi.hoisted(() => ({
  version: { current: "dev", commit: "a41d725abc", newer: true, latest: "0.9.0", url: "https://example.test/r" } as Record<string, unknown>,
}))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
  usePathname: () => "/chat",
  useSearchParams: () => new URLSearchParams(),
}))
vi.mock("@/hooks/use-access-mode", () => ({
  useAccessMode: () => "trusted",
  useRestrictedSurfaces: () => null,
  useTrustedWorkspaceId: () => "ws-test",
}))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/hooks/use-inbox", () => ({ useInboxUnreadCount: () => 0 }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({ session: { user: { id: "u1", name: "Pavel Srba", email: "pavel@acme.test" } }, signOut: vi.fn().mockResolvedValue(undefined) }),
  useIsInstanceAdmin: () => true,
  useAuthAvailable: () => true,
}))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", role: "OWNER", workspace: { name: "Acme Company" } }),
}))
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (p: string) =>
    Promise.resolve(
      p === "/api/v1/system/version" ? { ok: true, json: () => Promise.resolve(menu.version) }
        : p === "/api/v1/system/license" ? { ok: true, json: () => Promise.resolve({ edition: "community" }) }
          : p === "/api/v1/auth/sessions" ? { ok: true, json: () => Promise.resolve([{}, {}, {}]) }
            : { ok: false, json: () => Promise.resolve(null) },
    ),
}))
vi.mock("@/components/layout/workspace-switcher", () => ({ WorkspaceSwitcher: () => null }))
vi.mock("@/components/layout/sidebar-version", async (orig) => ({
  ...(await orig<typeof import("@/components/layout/sidebar-version")>()),
  SidebarVersion: () => null,
}))

import { AppSidebar } from "@/components/layout/app-sidebar"
import { UserMenu } from "@/components/layout/user-menu"
import { SidebarProvider } from "@/components/ui/sidebar"

beforeEach(() => {
  cleanup()
  localStorage.clear()
  for (const m of ["hasPointerCapture", "releasePointerCapture", "scrollIntoView"] as const) {
    if (!Element.prototype[m]) (Element.prototype as unknown as Record<string, unknown>)[m] = () => false
  }
})

describe("sidebar insets and search (PAR-003, PAR-004)", () => {
  it("keeps one 8 px inset: the rows add no second padding on top of the sidebar's own", () => {
    expect(rule(".rail-inset")).toMatch(/padding-inline:\s*0/)
  })
  it("sets the search placeholder at 12.5 px", () => {
    expect(rule(".rail-search")).toMatch(/font-size:\s*12\.5px/)
  })
})

describe("the sidebar user row (PAR-007)", () => {
  function row() {
    render(<SidebarProvider><AppSidebar /></SidebarProvider>)
    return screen.getByRole("button", { name: "User menu" })
  }
  it("reads the name at 13 px with the role under it", () => {
    const r = row()
    expect(within(r).getByText("Pavel Srba")).toHaveClass("type-nav", "font-semibold")
    expect(within(r).getByText("Owner")).toBeInTheDocument()
  })
  it("shows a presence dot on the avatar", () => {
    expect(row().querySelector('[data-slot="presence-dot"]')).not.toBeNull()
  })
  it("ends in the double chevron at 14 px", () => {
    const chev = row().querySelector("svg.lucide-chevrons-up-down")
    expect(chev).not.toBeNull()
    expect(chev).toHaveClass("size-3.5")
  })
})

describe("the user menu (PAR-141)", () => {
  function open() {
    render(<SidebarProvider><UserMenu placement="sidebar" /></SidebarProvider>)
    fireEvent.pointerDown(screen.getByRole("button", { name: /user menu/i }), { button: 0, ctrlKey: false })
  }
  it("is 300 px wide", () => {
    open()
    expect(screen.getByRole("menu").className).toContain("w-[300px]")
  })
  it("opens above the user row at the sidebar's left edge when the sidebar is open", () => {
    localStorage.setItem("crewship_sidebar_mode", "pinned")
    open()
    const m = screen.getByRole("menu")
    expect(m).toHaveAttribute("data-side", "top")
    expect(m).toHaveAttribute("data-align", "start")
  })
  it("opens to the right of the rail when the sidebar is a rail", () => {
    open()
    expect(screen.getByRole("menu")).toHaveAttribute("data-side", "right")
  })
  it("states the role in the workspace as one chip", () => {
    open()
    expect(screen.getByText("Owner in Acme Company")).toBeInTheDocument()
  })
  it("keeps Sessions & CLI tokens on one line beside its count", async () => {
    open()
    const label = screen.getByText("Sessions & CLI tokens")
    expect(label.className).toContain("whitespace-nowrap")
    expect(await screen.findByText("3 active")).toBeInTheDocument()
  })
  it("names the edition in the footer: Community Edition · dev · a41d725", async () => {
    open()
    const build = await screen.findByTestId("user-menu-build")
    expect(build).toHaveTextContent("Community Edition · dev · a41d725")
    expect(within(screen.getByTestId("user-menu-footer")).getByRole("link", { name: "Update available" })).toBeInTheDocument()
  })
})

describe("the sidebar mode menu (PAR-143)", () => {
  function open(mode?: string) {
    if (mode) localStorage.setItem("crewship_sidebar_mode", mode)
    render(<SidebarProvider><AppSidebar /></SidebarProvider>)
    fireEvent.pointerDown(screen.getByRole("button", { name: /^Sidebar:/ }), { button: 0, ctrlKey: false })
    return screen.getByRole("menu")
  }
  it("is 280 px wide with a tile icon, a bold title and a one-line description per row", () => {
    const m = open()
    expect(m.className).toContain("w-[280px]")
    const rows = within(m).getAllByRole("menuitemradio")
    expect(rows).toHaveLength(3)
    for (const r of rows) {
      expect(r.querySelector('[data-slot="mode-tile"]')).not.toBeNull()
      expect(r.querySelector("b, .font-semibold")).not.toBeNull()
    }
    expect(within(m).getByText("Icons, names under the pointer").className).toContain("truncate")
  })
  it("opens above the toggle at the sidebar's left edge when the sidebar is open", () => {
    const m = open("pinned")
    expect(m).toHaveAttribute("data-side", "top")
    expect(m).toHaveAttribute("data-align", "start")
  })
  it("shows the shortcut as a key chip in sans, not as mono text", () => {
    const m = open()
    const foot = within(m).getByText(/pins and unpins/)
    expect(foot.className).not.toContain("font-mono")
    expect(within(m).getByText("⌘B", { selector: "kbd" })).toBeInTheDocument()
  })
})

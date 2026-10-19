// Round 63 (#3784): one axis, a search icon like any other row, groups that
// fold in the open sidebar (remembered) but never in the rail.
import { describe, it, expect, vi, beforeEach } from "vitest"
import { readFileSync } from "node:fs"
import path from "node:path"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
  usePathname: () => "/chat",
  useSearchParams: () => new URLSearchParams(),
}))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/hooks/use-inbox", () => ({ useInboxUnreadCount: () => 0 }))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => true, useAuthAvailable: () => true }))
vi.mock("@/components/layout/workspace-switcher", () => ({ WorkspaceSwitcher: () => null }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/components/layout/user-menu", () => ({ UserMenu: () => null }))

import { AppSidebar } from "@/components/layout/app-sidebar"
import { SidebarProvider } from "@/components/ui/sidebar"

const css = readFileSync(path.resolve(__dirname, "../../../app/globals.css"), "utf8")
const rule = (sel: string) => {
  const at = css.indexOf(`${sel} {`)
  return at < 0 ? "" : css.slice(at, css.indexOf("}", at))
}

describe("one axis", () => {
  it("every row has a 28 px lead slot 6 px in, 40 × 36 in the rail", () => {
    expect(rule("  .rail-row")).toMatch(/height: 36px !important;[^}]*padding: 0 6px !important;/)
    expect(rule('  [data-collapsible="icon"] .rail-row')).toMatch(/width: 40px !important;[^}]*padding: 0 6px !important;/)
    expect(rule("  .rail-tile > svg")).toMatch(/width: 18px; height: 18px;/)
    // The panel's inset is 8 px, not p-2 (which follows --spacing): 8 + 6 + 14 = 28.
    expect(css).toMatch(/\[data-variant="inset"\] > \[data-slot="sidebar-container"\] \{ padding: 8px !important; \}/)
  })

  it("search has no border and wears a rail tile like the rows", () => {
    expect(rule("  .rail-search")).not.toMatch(/border:/)
    render(<SidebarProvider><AppSidebar /></SidebarProvider>)
    const search = screen.getByRole("button", { name: "Search" })
    expect(search.querySelector(".rail-tile svg")).not.toBeNull()
  })
})

describe("folding groups", () => {
  // vitest.setup.ts makes localStorage a set of mocks with no storage.
  const store = new Map<string, string>()
  beforeEach(() => {
    cleanup()
    store.clear()
    vi.mocked(localStorage.getItem).mockImplementation((k: string) => store.get(k) ?? null)
    vi.mocked(localStorage.setItem).mockImplementation((k: string, v: string) => { store.set(k, v) })
  })

  it("folds a group in the open sidebar and remembers it", () => {
    render(<SidebarProvider defaultOpen><AppSidebar /></SidebarProvider>)
    const work = screen.getByRole("button", { name: "Work" })
    expect(work).toHaveAttribute("aria-expanded", "true")
    expect(screen.getByRole("link", { name: "Issues" })).toBeInTheDocument()
    fireEvent.click(work)
    expect(work).toHaveAttribute("aria-expanded", "false")
    expect(screen.queryByRole("link", { name: "Issues" })).toBeNull()
    expect(JSON.parse(store.get("crewship.rail.folded-groups") ?? "[]")).toEqual(["Work"])
    cleanup()
    render(<SidebarProvider defaultOpen><AppSidebar /></SidebarProvider>)
    expect(screen.getByRole("button", { name: "Work" })).toHaveAttribute("aria-expanded", "false")
  })

  it("the rail shows every app even when a group is folded", () => {
    store.set("crewship.rail.folded-groups", JSON.stringify(["Work"]))
    store.set("crewship_sidebar_mode", "collapsed")
    render(<SidebarProvider defaultOpen={false}><AppSidebar /></SidebarProvider>)
    expect(screen.getByRole("link", { name: "Issues" })).toBeInTheDocument()
  })
})

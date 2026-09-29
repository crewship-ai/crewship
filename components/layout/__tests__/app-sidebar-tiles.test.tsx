// The rail wears the same icon tiles as a nested page's collapsed panel
// (DrillPage): every destination in a rounded tile, the page you are on
// tinted in its concept's colour, the rest neutral until hovered.
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
  usePathname: () => "/chat",
  useSearchParams: () => new URLSearchParams(),
}))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/hooks/use-inbox", () => ({ useInboxUnreadCount: () => 37 }))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => true }))
vi.mock("@/components/layout/workspace-switcher", () => ({ WorkspaceSwitcher: () => null }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/components/layout/sidebar-version", () => ({ SidebarVersion: () => null }))

import { AppSidebar } from "@/components/layout/app-sidebar"
import { SidebarProvider } from "@/components/ui/sidebar"

const tileOf = (name: string | RegExp) => screen.getByRole("link", { name }).querySelector("[data-slot=rail-tile]") as HTMLElement

describe("the rail's icon tiles", () => {
  beforeEach(() => cleanup())

  it("puts every destination in a tile", () => {
    render(<SidebarProvider><AppSidebar /></SidebarProvider>)
    for (const name of ["Dashboard", /^Inbox/, "Chat", "Crews", "Settings", "Admin"]) {
      expect(tileOf(name), name).not.toBeNull()
    }
  })

  it("tints the page you are on in its concept's colour, and only that one", () => {
    render(<SidebarProvider><AppSidebar /></SidebarProvider>)
    const chat = tileOf("Chat")
    expect(chat).toHaveAttribute("data-active", "true")
    expect(chat.style.getPropertyValue("--ic")).toBe("var(--notice)")
    expect(tileOf(/^Inbox/)).not.toHaveAttribute("data-active")
    expect(tileOf(/^Inbox/).style.getPropertyValue("--ic")).toBe("var(--info)")
  })
})

describe("one grid, one motion", () => {
  beforeEach(() => { cleanup(); vi.mocked(localStorage.setItem).mockClear() })
  // vitest.setup mocks localStorage: read what the rail WROTE for the mode.
  const mode = () => vi.mocked(localStorage.setItem).mock.calls.filter(([k]) => k === "crewship_sidebar_mode").at(-1)?.[1]

  it("keeps a group's heading as one row in both states, and the Inbox count on its tile", () => {
    render(<SidebarProvider><AppSidebar /></SidebarProvider>)
    expect(document.querySelectorAll("[data-slot=rail-group-head]")).toHaveLength(4)
    expect(tileOf(/^Inbox/).querySelector("[data-slot=rail-badge]")).toHaveTextContent("37")
    expect(screen.getByRole("link", { name: "Inbox, 37 unread" })).toBeInTheDocument()
  })

  it("pins and collapses with one button, back to the rail the person chose", () => {
    render(<SidebarProvider><AppSidebar /></SidebarProvider>)
    fireEvent.click(screen.getByRole("button", { name: "Pin open the sidebar" }))
    expect(mode()).toBe("pinned")
    fireEvent.click(screen.getByRole("button", { name: "Collapse the sidebar" }))
    expect(mode()).toBe("hover")
  })

  it("toggles with ⌘B too", () => {
    render(<SidebarProvider><AppSidebar /></SidebarProvider>)
    fireEvent.keyDown(window, { key: "b", metaKey: true })
    expect(mode()).toBe("pinned")
    fireEvent.keyDown(window, { key: "b", ctrlKey: true })
    expect(mode()).toBe("hover")
  })
})

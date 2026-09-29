// The rail wears the same icon tiles as a nested page's collapsed panel
// (DrillPage): every destination in a rounded tile, the page you are on
// tinted in its concept's colour, the rest neutral until hovered.
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
  usePathname: () => "/chat",
  useSearchParams: () => new URLSearchParams(),
}))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/hooks/use-inbox", () => ({ useInboxUnreadCount: () => 0 }))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => true }))
vi.mock("@/components/layout/workspace-switcher", () => ({ WorkspaceSwitcher: () => null }))
vi.mock("@/components/layout/sidebar-version", () => ({ SidebarVersion: () => null }))

import { AppSidebar } from "@/components/layout/app-sidebar"
import { SidebarProvider } from "@/components/ui/sidebar"

const tileOf = (name: string) => screen.getByRole("link", { name }).querySelector("[data-slot=rail-tile]") as HTMLElement

describe("the rail's icon tiles", () => {
  beforeEach(() => cleanup())

  it("puts every destination in a tile", () => {
    render(<SidebarProvider><AppSidebar /></SidebarProvider>)
    for (const name of ["Dashboard", "Inbox", "Chat", "Crews", "Settings", "Admin"]) {
      expect(tileOf(name), name).not.toBeNull()
    }
  })

  it("tints the page you are on in its concept's colour, and only that one", () => {
    render(<SidebarProvider><AppSidebar /></SidebarProvider>)
    const chat = tileOf("Chat")
    expect(chat).toHaveAttribute("data-active", "true")
    expect(chat.style.getPropertyValue("--ic")).toBe("var(--notice)")
    expect(tileOf("Inbox")).not.toHaveAttribute("data-active")
    expect(tileOf("Inbox").style.getPropertyValue("--ic")).toBe("var(--info)")
  })
})

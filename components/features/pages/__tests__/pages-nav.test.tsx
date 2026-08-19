/**
 * Nav registration — PRD §9b.5.
 *
 * "Pages belongs in Plan, after Routines — it is where a person goes to see
 * the state of their work, not a thing they build once." The position is the
 * assertion: Plan reads Dashboard · Inbox · Issues · Routines · Pages, and an
 * entry that drifts into Build or lands before Routines changes what the group
 * means.
 *
 * The icon comes from `CONCEPT_ICON`, pinned by
 * `lib/__tests__/concept-icons.test.ts` — the rail is the definition and every
 * other surface reads it rather than choosing again from memory.
 */
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, within } from "@testing-library/react"

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
  usePathname: () => "/pages",
  useSearchParams: () => new URLSearchParams(),
}))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/hooks/use-inbox", () => ({ useInboxUnreadCount: () => 0 }))
vi.mock("@/components/layout/workspace-switcher", () => ({ WorkspaceSwitcher: () => null }))

import { AppSidebar, NAV_ICONS } from "@/components/layout/app-sidebar"
import { SidebarProvider } from "@/components/ui/sidebar"
import { CONCEPT_ICON } from "@/lib/concept-icons"

function planGroup(): HTMLElement {
  const label = screen.getByText("Plan")
  const group = label.closest("[data-sidebar='group']") as HTMLElement | null
  if (!group) throw new Error("no Plan group")
  return group
}

describe("Pages in the nav rail", () => {
  beforeEach(() => cleanup())

  it("registers Pages in Plan, immediately after Routines", () => {
    render(
      <SidebarProvider>
        <AppSidebar />
      </SidebarProvider>,
    )
    const items = within(planGroup())
      .getAllByRole("link")
      .map((a) => a.textContent?.trim())
    expect(items).toEqual(["Dashboard", "Inbox", "Issues", "Routines", "Pages"])
  })

  it("points it at /pages", () => {
    render(
      <SidebarProvider>
        <AppSidebar />
      </SidebarProvider>,
    )
    const link = within(planGroup()).getByRole("link", { name: "Pages" })
    expect(link.getAttribute("href")).toBe("/pages")
  })

  it("takes its icon from CONCEPT_ICON, not from a second opinion", () => {
    expect(NAV_ICONS.pages).toBe(CONCEPT_ICON.pages)
    // And not the Dashboard's — two surfaces wearing one face is the drift
    // lib/concept-icons.ts exists to stop.
    expect(CONCEPT_ICON.pages).not.toBe(CONCEPT_ICON.dashboard)
  })
})

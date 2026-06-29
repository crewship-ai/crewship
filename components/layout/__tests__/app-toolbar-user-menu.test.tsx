import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"

// Radix DropdownMenu relies on pointer-capture + scrollIntoView, which
// happy-dom doesn't implement. Polyfill them so the menu can open in the
// test environment.
beforeEach(() => {
  if (!Element.prototype.hasPointerCapture) {
    Element.prototype.hasPointerCapture = () => false
  }
  if (!Element.prototype.releasePointerCapture) {
    Element.prototype.releasePointerCapture = () => {}
  }
  if (!Element.prototype.scrollIntoView) {
    Element.prototype.scrollIntoView = () => {}
  }
})

// usePathname is globally mocked to "/" in vitest.setup.ts.

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    session: { user: { name: "Demo User", email: "demo@crewship.ai" } },
    signOut: vi.fn().mockResolvedValue(undefined),
  }),
}))
vi.mock("@/hooks/use-realtime", () => ({ useRealtime: () => ({ status: "connected" }) }))
vi.mock("@/hooks/use-engine-status", () => ({ useEngineStatus: () => ({ status: "connected" }) }))
vi.mock("@/hooks/use-crews-status", () => ({ useCrewsStatus: () => null }))
vi.mock("@/hooks/use-provisioning-status", () => ({ useProvisioningStatus: () => null }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/lib/store", () => ({
  useAppStore: (selector: (s: { settingsTab: string | null; breadcrumbs: unknown[] }) => unknown) =>
    selector({ settingsTab: null, breadcrumbs: [] }),
}))

// Stub child widgets that pull their own data hooks — irrelevant to the menu.
vi.mock("@/components/features/notifications/notification-bell", () => ({ NotificationBell: () => null }))
vi.mock("@/components/features/inbox/inbox-bell", () => ({ InboxBell: () => null }))
vi.mock("@/components/features/activity/activity-bell", () => ({ ActivityBell: () => null }))
vi.mock("@/components/command-palette", () => ({ CommandPalette: () => null }))
vi.mock("../app-toolbar-provisioning", () => ({ ProvisioningBadge: () => null }))
vi.mock("@/components/ui/wifi", () => ({ WifiIcon: () => null }))

import { TooltipProvider } from "@/components/ui/tooltip"
import { AppToolbar } from "../app-toolbar"

function openUserMenu() {
  render(
    <TooltipProvider>
      <AppToolbar />
    </TooltipProvider>,
  )
  // Radix DropdownMenu opens on pointerdown (primary button), not click.
  const trigger = screen.getByRole("button", { name: /user menu/i })
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false })
}

describe("AppToolbar — user menu links", () => {
  beforeEach(() => cleanup())

  it.each([
    ["Profile & Settings", "/settings"],
    ["Help & Support", "https://github.com/crewship-ai/crewship/issues"],
    ["Documentation", "https://docs.crewship.ai"],
    ["GitHub", "https://github.com/crewship-ai/crewship"],
  ])("%s routes to %s instead of being a dead item", async (label, href) => {
    openUserMenu()
    const item = await screen.findByRole("menuitem", { name: new RegExp(label, "i") })
    // The item must be an anchor (asChild) carrying the destination — the
    // bug was that these were bare <div> items with no href, so clicking
    // them did nothing.
    expect(item).toHaveAttribute("href", href)
  })

  it("Log out stays actionable", async () => {
    openUserMenu()
    expect(await screen.findByRole("menuitem", { name: /log out/i })).toBeInTheDocument()
  })
})

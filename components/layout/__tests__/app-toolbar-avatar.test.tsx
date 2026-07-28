import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

// The toolbar drew initials in three places and never looked at avatar_url,
// so uploading a profile picture changed one row in Settings and nothing
// else in the product. These pin the avatar to every one of those places.

let avatarUrl = ""

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    session: { user: { id: "u1", name: "Demo User", email: "demo@crewship.ai", avatar_url: avatarUrl } },
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
  useAppStore: (selector: (s: { breadcrumbs: unknown[] }) => unknown) => selector({ breadcrumbs: [] }),
}))
vi.mock("@/components/features/notifications/notification-bell", () => ({ NotificationBell: () => null }))
vi.mock("@/components/features/inbox/inbox-bell", () => ({ InboxBell: () => null }))
vi.mock("@/components/features/activity/activity-bell", () => ({ ActivityBell: () => null }))
vi.mock("@/components/command-palette", () => ({ CommandPalette: () => null }))
vi.mock("../app-toolbar-provisioning", () => ({ ProvisioningBadge: () => null }))
vi.mock("@/components/ui/wifi", () => ({ WifiIcon: () => null }))

import { TooltipProvider } from "@/components/ui/tooltip"
import { AppToolbar } from "../app-toolbar"

function renderToolbar(url: string) {
  avatarUrl = url
  return render(
    <TooltipProvider>
      <AppToolbar />
    </TooltipProvider>,
  )
}

describe("AppToolbar — the user's avatar", () => {
  beforeEach(() => {
    cleanup()
    // Radix's dropdown needs pointer-capture + scrollIntoView, which
    // happy-dom does not implement.
    if (!Element.prototype.hasPointerCapture) Element.prototype.hasPointerCapture = () => false
    if (!Element.prototype.releasePointerCapture) Element.prototype.releasePointerCapture = () => {}
    if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
  })

  it("shows the uploaded picture instead of initials", () => {
    renderToolbar("/api/v1/users/u1/avatar?v=1700000000")
    const img = screen.getAllByRole("img", { name: /demo user/i })[0]
    expect(img).toHaveAttribute("src", "/api/v1/users/u1/avatar?v=1700000000")
    expect(screen.queryByText("DU")).toBeNull()
  })

  it("falls back to initials when the user has no picture", () => {
    renderToolbar("")
    expect(screen.getAllByText("DU").length).toBeGreaterThan(0)
    expect(screen.queryByRole("img", { name: /demo user/i })).toBeNull()
  })

  it("uses the picture in the menu header too, not just the trigger", async () => {
    renderToolbar("/api/v1/users/u1/avatar?v=1")
    // The dropdown body only mounts once opened, so the header avatar cannot
    // be asserted from the closed state. Radix opens on pointerdown.
    fireEvent.pointerDown(screen.getByRole("button", { name: /user menu/i }), { button: 0 })
    await screen.findByRole("menuitem", { name: /log out/i })
    // Radix marks everything outside the open menu aria-hidden, so a
    // role-based count only ever sees the menu's own copy — which is exactly
    // the one under test. A photo on the trigger and initials in the menu it
    // opens reads as a bug.
    expect(screen.getByRole("img", { name: /demo user/i })).toHaveAttribute(
      "src", "/api/v1/users/u1/avatar?v=1",
    )
    expect(screen.queryByText("DU")).toBeNull()
  })
})

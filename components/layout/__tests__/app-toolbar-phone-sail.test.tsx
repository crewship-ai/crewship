import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, cleanup } from "@testing-library/react"

// Phone pilot (PILOT-TELEFON-STAV row 1): a phone list header is the
// Crewship sail, the area's name and the global search — not the area tile.
// Pilot areas only (Inbox, Settings); every other area keeps its tile until
// the pilot is evaluated.

const m = vi.hoisted(() => ({ pathname: "/inbox", mobile: true }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), back: vi.fn(), prefetch: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => m.pathname,
}))
vi.mock("@/hooks/use-auth", () => ({
  useIsInstanceAdmin: () => true,
  useAuth: () => ({ session: { user: { name: "Demo User", email: "demo@crewship.ai" } }, signOut: vi.fn() }),
}))
vi.mock("@/hooks/use-realtime", () => ({ useRealtime: () => ({ status: "connected" }) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => m.mobile }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => "trusted" }))
vi.mock("@/lib/store", () => ({
  useAppStore: (selector: (s: Record<string, unknown>) => unknown) =>
    selector({ breadcrumbs: [], setPageBarSlot: () => {}, secondaryPanel: null, setSecondaryPanelOpen: () => {}, secondaryPanelOpen: false }),
}))
vi.mock("@/components/features/inbox/inbox-bell", () => ({ InboxBell: () => null }))
vi.mock("@/components/features/activity/activity-bell", () => ({ ActivityBell: () => null }))
vi.mock("@/components/command-palette", () => ({ CommandPalette: () => null }))
vi.mock("../mobile-more-sheet", () => ({ MobileMoreSheet: () => null }))

import { AppToolbar } from "../app-toolbar"

beforeEach(() => {
  cleanup()
  m.pathname = "/inbox"
  m.mobile = true
})

describe("phone list header", () => {
  it.each(["/inbox", "/settings"])("%s shows the Crewship sail instead of the area tile", (path) => {
    m.pathname = path
    const { container } = render(<AppToolbar />)
    const crumb = container.querySelector("[data-slot=area-crumb]")!
    expect(crumb.querySelector("[data-slot=area-sail]")).not.toBeNull()
    expect(crumb.querySelector(".area-tile")).toBeNull()
  })

  it("keeps the tile in an area outside the pilot", () => {
    m.pathname = "/issues"
    const { container } = render(<AppToolbar />)
    expect(container.querySelector("[data-slot=area-sail]")).toBeNull()
    expect(container.querySelector(".area-tile")).not.toBeNull()
  })

  it("keeps the tile on a desktop", () => {
    m.mobile = false
    const { container } = render(<AppToolbar />)
    expect(container.querySelector("[data-slot=area-sail]")).toBeNull()
    expect(container.querySelector(".area-tile")).not.toBeNull()
  })
})

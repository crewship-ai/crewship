import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, cleanup } from "@testing-library/react"

// The global next/navigation mock in vitest.setup.ts pins usePathname to "/".
// The toolbar title is *derived from the route*, so this suite needs to drive
// the pathname per case — hence a local mock over a mutable let.
let pathname = "/"
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), back: vi.fn(), prefetch: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => pathname,
}))

let settingsTab: string | null = null

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
    selector({ settingsTab, breadcrumbs: [] }),
}))

vi.mock("@/components/features/notifications/notification-bell", () => ({ NotificationBell: () => null }))
vi.mock("@/components/features/inbox/inbox-bell", () => ({ InboxBell: () => null }))
vi.mock("@/components/features/activity/activity-bell", () => ({ ActivityBell: () => null }))
vi.mock("@/components/command-palette", () => ({ CommandPalette: () => null }))
vi.mock("../app-toolbar-provisioning", () => ({ ProvisioningBadge: () => null }))
vi.mock("@/components/ui/wifi", () => ({ WifiIcon: () => null }))

import { TooltipProvider } from "@/components/ui/tooltip"
import { AppToolbar } from "../app-toolbar"

// The title lives in the toolbar's left-hand breadcrumb region. Scope the
// assertions to that <header> so a page name appearing anywhere else on the
// screen can't accidentally satisfy them.
function renderToolbarAt(route: string, tab: string | null = null) {
  pathname = route
  settingsTab = tab
  const { container } = render(
    <TooltipProvider>
      <AppToolbar />
    </TooltipProvider>,
  )
  const header = container.querySelector("header")
  if (!header) throw new Error("toolbar header not rendered")
  return header
}

describe("AppToolbar — the top bar never repeats the page name", () => {
  beforeEach(() => cleanup())

  // Every one of these routes renders a sub-bar (or its own header) one row
  // below carrying the page name already. Naming the page in the top bar too
  // printed it twice, stacked — "Crews & Agents" over "Crews & Agents · 3
  // crews" being the case that surfaced it.
  it.each([
    ["/", "Dashboard"],
    ["/crews", "Crews & Agents"],
    ["/skills", "Skills"],
    ["/credentials", "Credentials"],
    ["/settings", "Settings"],
  ])("%s shows Crewship, not %s", (route, pageName) => {
    const header = renderToolbarAt(route)
    expect(header).toHaveTextContent("Crewship")
    expect(header).not.toHaveTextContent(pageName)
  })

  // "Crews" alone can't be asserted as absent — it is a substring of
  // "Crewship". Pin the crews title exactly instead: the old map had
  // "/crews" -> "Crews" behind the "Crews & Agents" hardcode, so a partial
  // revert could leave that one behind and still pass the loop above.
  it("shows Crewship and nothing else on /crews", () => {
    const header = renderToolbarAt("/crews")
    // First child of the <header> is the left-hand breadcrumb region; the
    // second holds the status/search/bell cluster, which has semibold text
    // of its own.
    const crumbs = header.firstElementChild
    expect(crumbs).toHaveTextContent(/^Crewship$/)
  })

  // Routes that were already correct — guard against a regression that
  // reintroduces a per-page title map.
  it.each(["/inbox", "/issues", "/routines", "/integrations", "/admin", "/journal"])(
    "%s keeps showing Crewship",
    (route) => {
      expect(renderToolbarAt(route)).toHaveTextContent("Crewship")
    },
  )

  // Deep pages are the exception: their top-bar text is a *breadcrumb*, a
  // click-path back out of the detail view, not a restatement of the sub-bar.
  // Removing it would strand the user, so it stays.
  it("keeps the settings tab breadcrumb as a way back to Settings", () => {
    const header = renderToolbarAt("/settings", "audit")
    expect(header).toHaveTextContent("Settings")
    expect(header).toHaveTextContent("Audit Log")
  })

  it("keeps the chat breadcrumb pointing back at the agent's crew", () => {
    const header = renderToolbarAt("/chat/riley")
    expect(header).toHaveTextContent("Crews")
    expect(header).toHaveTextContent("riley")
    expect(header).toHaveTextContent("Chat")
  })
})

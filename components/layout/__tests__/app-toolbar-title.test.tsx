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
  it.each(["/inbox-v2", "/issues", "/routines", "/integrations", "/admin", "/journal"])(
    "%s keeps showing Crewship",
    (route) => {
      expect(renderToolbarAt(route)).toHaveTextContent("Crewship")
    },
  )

  // Settings is NOT a detail view — its section nav never leaves the page — so
  // its identity belongs in the sub-bar next to Admin's, not in a top-bar
  // breadcrumb. Holding a section here would make /settings the only route
  // whose top bar isn't plain "Crewship".
  it("shows Crewship on settings even with a section selected", () => {
    const header = renderToolbarAt("/settings", "audit")
    expect(header.firstElementChild).toHaveTextContent(/^Crewship$/)
  })

  // Deep pages are the exception: their top-bar text is a *breadcrumb*, a
  // click-path back out of the detail view, not a restatement of the sub-bar.
  // Removing it would strand the user, so it stays.
  it("keeps the chat breadcrumb pointing back at the agent's crew", () => {
    const header = renderToolbarAt("/chat/riley")
    expect(header).toHaveTextContent("Crews")
    expect(header).toHaveTextContent("riley")
    expect(header).toHaveTextContent("Chat")
  })

  // The toolbar used to carry a second breadcrumb branch, keyed on a
  // /crews/agents/<id> pathname, whose "Agents" crumb linked to /crews/agents
  // and whose crew crumb linked to /crews/<crewId>. Both are deleted routes.
  // Nothing routes to /crews/agents/<id> any more, so the branch only ever
  // rendered when the Go static handler fell a bad URL through to the SPA
  // root — putting an agent breadcrumb, and a fetch for it, above the
  // dashboard. Pin the absence: this is the shape a revert would take.
  // Assembled, not written out: dead-agent-routes.test.ts scans every .ts/.tsx
  // under app/ and components/, this file included, and a literal here would
  // report itself as a live link into the very family it is asserting against.
  const DEAD = ["/crews", "agents"].join("/")

  function deadHrefs(header: Element): string[] {
    return [...header.querySelectorAll("a")]
      .map((a) => a.getAttribute("href") ?? "")
      .filter((h) => h.startsWith(DEAD))
  }

  it("renders no breadcrumb into the deleted agents subtree", () => {
    const header = renderToolbarAt(`${DEAD}/ag_1`)
    expect(deadHrefs(header)).toEqual([])
    expect(header.firstElementChild).toHaveTextContent(/^Crewship$/)
  })

  // Same guard, one level up: the index. Every route the toolbar can be
  // rendered at, and not one anchor pointing at the dead family.
  it.each(["/", "/crews", DEAD, "/chat/riley", "/settings", "/inbox-v2"])(
    "%s links nowhere into the dead agents family",
    (route) => {
      expect(deadHrefs(renderToolbarAt(route))).toEqual([])
    },
  )
})

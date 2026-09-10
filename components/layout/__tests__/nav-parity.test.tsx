import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

import { CONCEPT_ICON } from "@/lib/concept-icons"

// =============================================================================
// The phone sheet used to be built from its own array, kept in step with the
// desktop rail by hand. Chat went missing from both once; by #2483 the sheet
// had also lost Inbox, Issues, Routines, Pages, Activity, Journal and
// Integrations — seven destinations a phone simply could not reach.
//
// Asserting that two arrays match is no longer meaningful (there is one array
// now), so this asserts the thing a person actually gets: open the sheet on a
// phone and every destination is in it.
// =============================================================================

beforeEach(() => {
  if (!Element.prototype.hasPointerCapture) Element.prototype.hasPointerCapture = () => false
  if (!Element.prototype.releasePointerCapture) Element.prototype.releasePointerCapture = () => {}
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
})

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
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", role: "OWNER", workspace: { name: "Example workspace" } }),
}))
vi.mock("@/hooks/use-inbox", () => ({ useInboxUnreadCount: () => 3 }))
// The sheet only renders when the toolbar believes it is on a phone.
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => true }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
const setMobileNavOpen = vi.fn()
vi.mock("@/lib/store", () => ({
  // The sheet is open for the whole of this file: what is under test is what
  // it offers, not how it is opened (that is mobile-tab-bar.test.tsx).
  useAppStore: (selector: (s: Record<string, unknown>) => unknown) =>
    selector({ settingsTab: null, breadcrumbs: [], mobileNavOpen: true, setMobileNavOpen }),
}))
vi.mock("@/components/features/inbox/inbox-bell", () => ({ InboxBell: () => null }))
vi.mock("@/components/features/activity/activity-bell", () => ({ ActivityBell: () => null }))
vi.mock("@/components/command-palette", () => ({ CommandPalette: () => null }))
vi.mock("../app-toolbar-provisioning", () => ({ ProvisioningBadge: () => null }))

import { TooltipProvider } from "@/components/ui/tooltip"
import { navSections, type NavItem } from "@/lib/nav-sections"
import { AppToolbar } from "../app-toolbar"

function flatten(): NavItem[] {
  return navSections.flatMap((s) => s.items)
}

/** Renders the toolbar on a phone with the navigation sheet already open. */
function openMobileNav() {
  cleanup()
  render(
    <TooltipProvider>
      <AppToolbar />
    </TooltipProvider>,
  )
}

describe("the phone navigation sheet offers the whole product", () => {
  it("renders a link for every destination the desktop rail carries", () => {
    openMobileNav()
    const rendered = new Set(
      screen.getAllByRole("link").map((a) => a.getAttribute("href")),
    )
    const missing = flatten()
      // FUTURE rows are announced, not built — they render as a disabled row
      // pointing at "#", so their href is deliberately not their destination.
      .filter((item) => item.badge !== "FUTURE")
      .map((item) => item.href)
      .filter((href) => !rendered.has(href))

    expect(missing, `unreachable from the phone sheet: ${missing.join(", ")}`).toEqual([])
  })

  it("labels each destination the way the rail labels it", () => {
    openMobileNav()
    for (const item of flatten()) {
      expect(
        screen.getAllByText(item.title).length,
        `no row titled "${item.title}" in the phone sheet`,
      ).toBeGreaterThan(0)
    }
  })

  it("carries the inbox unread count, which had no mobile home at all", () => {
    // The bell that shows this on desktop sits inside `hidden md:flex`, so
    // before #2483 a phone had no way to learn there was anything waiting.
    openMobileNav()
    const inbox = screen.getAllByRole("link").find((a) => a.getAttribute("href") === "/inbox")
    expect(inbox, "no /inbox row").toBeTruthy()
    expect(inbox!.textContent).toContain("3")
  })

  it("does not offer a FUTURE row to the keyboard or a screen reader", () => {
    // `pointer-events-none` blocks a mouse and nothing else: the row stayed
    // focusable, Enter followed href="#", and assistive technology announced
    // a link to a destination that does not exist.
    openMobileNav()
    const future = flatten().filter((i) => i.badge === "FUTURE")
    expect(future.length, "no FUTURE row to check").toBeGreaterThan(0)
    for (const item of future) {
      const row = screen.getByText(item.title).closest("[aria-disabled], a")
      expect(row, `no row for ${item.title}`).toBeTruthy()
      expect(row!.tagName, `${item.title} is still a link`).not.toBe("A")
      expect(row!.getAttribute("aria-disabled")).toBe("true")
    }
    expect(
      screen.getAllByRole("link").map((a) => a.getAttribute("href")),
      "a FUTURE row is still reachable as a link",
    ).not.toContain("#")
  })

  it("does not link at the per-agent chat route, which needs a slug nobody has yet", () => {
    openMobileNav()
    for (const a of screen.getAllByRole("link")) {
      expect(a.getAttribute("href") ?? "").not.toMatch(/^\/chat\/.+/)
    }
  })
})

describe("the navigation is defined once", () => {
  it("wears the product's conversation icon for Chat", () => {
    const chat = flatten().find((i) => i.href === "/chat")
    expect(chat, "no /chat entry").toBeTruthy()
    expect(chat!.icon).toBe(CONCEPT_ICON.sessions)
  })

  it("claims each route exactly once", () => {
    // Two rows for one route means one of them is always the wrong one to
    // click, and both light up as active.
    const hrefs = flatten().map((i) => i.href)
    expect(new Set(hrefs).size, `duplicate rows: ${hrefs.join(", ")}`).toBe(hrefs.length)
  })
})

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen, within } from "@testing-library/react"

// PAR-001 and the phone header (PAR-025, 052, 063, 092, 096, 136): the bar
// ends with Activity, Inbox and the Crewship sail (#5552); on a phone it is
// ONE row — area tile, title, the page's primary action, search, bell, sail.

const h = vi.hoisted(() => ({ mobile: false }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => h.mobile }))
vi.mock("@/hooks/use-access-mode", () => ({
  useAccessMode: () => "trusted",
  useRestrictedSurfaces: () => null,
  useTrustedWorkspaceId: () => "ws-1",
}))
vi.mock("@/hooks/use-auth", () => ({
  useIsInstanceAdmin: () => true,
  useAuth: () => ({ session: { user: { name: "Demo User", email: "demo@crewship.ai" } }, signOut: vi.fn() }),
}))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-1", role: "OWNER", workspace: { name: "Acme" } }) }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtime: () => ({ status: "disconnected" }) }))
vi.mock("@/hooks/use-engine-status", () => ({ useEngineStatus: () => ({ status: "offline" }) }))
vi.mock("@/hooks/use-crews-status", () => ({ useCrewsStatus: () => null }))
vi.mock("@/hooks/use-inbox", () => ({ useInboxUnreadCount: () => 0 }))
vi.mock("@/hooks/use-live-run-count", () => ({ useLiveRunCount: () => 0 }))
vi.mock("@/hooks/use-provisioning-status", () => ({
  useProvisioningStatus: () => ({
    total: 1, building: 0, failed: 0, needsProvision: 1, pendingRestart: 0, recentlyCompleted: 0,
    detail: [{ id: "c1", slug: "q", name: "Quality", status: "needs_provision", featureIds: [] }],
    acknowledge: vi.fn(),
  }),
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => ({ ok: false, json: async () => null })) }))
vi.mock("@/components/features/inbox/inbox-bell", () => ({ InboxBell: () => <button type="button" aria-label="Inbox" /> }))
vi.mock("@/components/features/activity/activity-bell", () => ({ ActivityBell: () => <button type="button" aria-label="Activity" /> }))
vi.mock("@/components/command-palette", () => ({ CommandPalette: () => null }))
vi.mock("@/components/ui/wifi", () => ({ WifiIcon: () => null }))

import { TooltipProvider } from "@/components/ui/tooltip"
import { AppToolbar } from "../app-toolbar"

function bar() {
  render(<TooltipProvider><AppToolbar /></TooltipProvider>)
  return screen.getByRole("banner")
}

beforeEach(() => { h.mobile = false })
afterEach(cleanup)

describe("the top bar's right edge (PAR-001)", () => {
  it("has no provisioning chip and no connection button, only Activity, Inbox and the sail", () => {
    const header = bar()
    expect(within(header).queryByRole("button", { name: /crew images/i })).toBeNull()
    expect(header.querySelector('[data-testid="system-status-pill"]')).toBeNull()
    expect(within(header).getByRole("button", { name: "Activity" })).toBeInTheDocument()
    expect(within(header).getByRole("button", { name: "Inbox" })).toBeInTheDocument()
    expect(within(header).getByRole("button", { name: "Crewship" })).toBeInTheDocument()
  })

  it("ends with the Crewship sail, after Activity and Inbox (#5552)", () => {
    const header = bar()
    const names = within(header).getAllByRole("button").map((b) => b.getAttribute("aria-label"))
    expect(names.slice(-3)).toEqual(["Activity", "Inbox", "Crewship"])
    // A rule sets it apart from the bells, on every width (owner, 19 Nov).
    const rule = header.querySelector('[data-slot="crewship-sail-rule"]') as HTMLElement
    expect(rule).not.toBeNull()
    expect(rule.className).not.toMatch(/(^|\s)hidden(\s|$)/)
    const sail = within(header).getByRole("button", { name: "Crewship" })
    expect(rule.compareDocumentPosition(sail) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    const inbox = within(header).getByRole("button", { name: "Inbox" })
    expect(inbox.compareDocumentPosition(rule) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})

describe("the phone's top bar (PAR-025 ... PAR-136)", () => {
  it("is one row: title, the page's actions slot, search and the activity bell", () => {
    h.mobile = true
    const header = bar()
    const slot = header.querySelector('[data-slot="page-bar"]') as HTMLElement
    // The slot holds the page's primary action (icon-only) on a phone: it is shown.
    expect(slot.className).not.toMatch(/(^|\s)hidden(\s|$)/)
    expect(within(header).getByRole("button", { name: "Search" })).toBeInTheDocument()
    expect(within(header).getByRole("button", { name: "Activity" })).toBeInTheDocument()
    expect(within(header).getByRole("button", { name: "Crewship" })).toBeInTheDocument()
  })

  it("keeps Inbox for the tab bar, and the account for the More sheet", () => {
    h.mobile = true
    const header = bar()
    // Hidden below md by its wrapper (no stylesheet in jsdom): Inbox is a tab.
    expect(within(header).getByRole("button", { name: "Inbox" }).parentElement).toHaveClass("hidden", "md:flex")
    expect(within(header).queryByRole("button", { name: /user menu/i })).toBeNull()
  })
})

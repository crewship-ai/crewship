import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen, within } from "@testing-library/react"

// CHR-01 (#3482), chrome-states G11: the phone's More is the v67 `pop-more`
// sheet - one grid of tiles for every destination the person may open, then a
// row for the user and a row for what is running.

const ctx = vi.hoisted(() => ({
  role: "OWNER" as string,
  instanceAdmin: false,
  surfaces: null as string[] | null,
  live: 0,
  unread: 0,
  open: true,
  setOpen: vi.fn(),
}))

vi.mock("next/navigation", () => ({ usePathname: () => "/issues", useRouter: () => ({ push: vi.fn() }), useSearchParams: () => new URLSearchParams() }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: ctx.role }) }))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => ctx.instanceAdmin }))
vi.mock("@/hooks/use-access-mode", () => ({
  useRestrictedSurfaces: () => ctx.surfaces,
  useTrustedWorkspaceId: () => (ctx.surfaces ? null : "ws-test"),
}))
vi.mock("@/hooks/use-inbox", () => ({ useInboxUnreadCount: () => ctx.unread }))
vi.mock("@/hooks/use-live-run-count", () => ({ useLiveRunCount: () => ctx.live }))
vi.mock("@/lib/store", () => ({
  useAppStore: (selector: (s: Record<string, unknown>) => unknown) =>
    selector({ mobileNavOpen: ctx.open, setMobileNavOpen: ctx.setOpen }),
}))
vi.mock("../workspace-switcher", () => ({ MobileWorkspaceSwitcher: () => <div data-testid="ws-switcher" /> }))
vi.mock("../user-menu", () => ({ UserMenu: ({ placement }: { placement?: string }) => <div data-testid="user-row" data-placement={placement} /> }))

import { MobileMoreSheet } from "../mobile-more-sheet"

beforeEach(() => {
  ctx.role = "OWNER"; ctx.instanceAdmin = false; ctx.surfaces = null; ctx.live = 0; ctx.unread = 0; ctx.open = true
})
afterEach(cleanup)

const grid = () => screen.getByRole("list", { name: "Navigation" })
const tiles = () => within(grid()).getAllByRole("link").map((a) => a.textContent?.replace(/\d+$/, "").trim())

describe("the phone More sheet grid", () => {
  it("is one grid of tiles in navigation order, Settings last, no Admin for a non-admin", () => {
    render(<MobileMoreSheet />)
    expect(tiles()).toEqual([
      "Dashboard", "Inbox", "Chat", "Activity", "Issues", "Routines", "Artifacts", "Calendar",
      "Crews", "Skills", "Vault", "Integrations", "Settings",
    ])
  })

  it("adds Admin at the end for an instance administrator", () => {
    ctx.instanceAdmin = true
    render(<MobileMoreSheet />)
    expect(tiles().at(-1)).toBe("Admin")
    expect(tiles().at(-2)).toBe("Settings")
  })

  it("draws a concept tile per destination and no group headings", () => {
    render(<MobileMoreSheet />)
    for (const a of within(grid()).getAllByRole("link")) {
      expect(a.querySelector('[data-slot="concept-tile"]'), `no tile on ${a.textContent}`).toBeTruthy()
    }
    expect(screen.queryByText("Work")).toBeNull()
    expect(screen.queryByText("Build")).toBeNull()
  })

  it("marks the current page", () => {
    render(<MobileMoreSheet />)
    expect(within(grid()).getByRole("link", { name: /^Issues/ }).getAttribute("aria-current")).toBe("page")
  })

  it("carries the inbox unread count on its tile", () => {
    ctx.unread = 4
    render(<MobileMoreSheet />)
    expect(within(grid()).getByRole("link", { name: /^Inbox/ }).textContent).toContain("4")
  })

  it("filters the grid for a Guest to the granted screens", () => {
    ctx.role = "VIEWER"
    ctx.surfaces = ["chat", "issues", "account_security"]
    render(<MobileMoreSheet />)
    expect(tiles()).toEqual(["Chat", "Issues", "Settings"])
  })
})

describe("the rows under the grid", () => {
  it("opens the user menu from a row (the same menu, not a second copy)", () => {
    render(<MobileMoreSheet />)
    expect(screen.getByTestId("user-row").getAttribute("data-placement")).toBe("row")
  })

  it("names what is running and links to Activity", () => {
    ctx.live = 3
    render(<MobileMoreSheet />)
    const row = screen.getByRole("link", { name: /3 agents running/ })
    expect(row.getAttribute("href")).toBe("/activity")
  })

  it("says 1 agent running in the singular", () => {
    ctx.live = 1
    render(<MobileMoreSheet />)
    expect(screen.getByRole("link", { name: /1 agent running/ })).toBeTruthy()
  })

  it("has no running row when nothing runs, and none for a Guest", () => {
    render(<MobileMoreSheet />)
    expect(screen.queryByText(/running/)).toBeNull()
    cleanup()
    ctx.live = 3; ctx.surfaces = ["chat"]
    render(<MobileMoreSheet />)
    expect(screen.queryByText(/running/)).toBeNull()
  })
})

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen } from "@testing-library/react"

// =============================================================================
// The admin console's content pane scrolls, and on the default section
// (Overview) it holds nothing focusable — no button, no link, no field. A
// scroll container that cannot be focused and contains nothing focusable is
// unreachable by keyboard: the content below the fold simply does not exist
// for anyone not using a mouse (axe: scrollable-region-focusable).
//
// The sibling <nav> was not the culprit — it is full of rows a keyboard can
// reach. This test pins the pane that actually failed.
// =============================================================================

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), refresh: vi.fn() }),
  usePathname: () => "/admin",
  useSearchParams: () => new URLSearchParams(),
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-1", role: "OWNER", loading: false }),
}))

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async () => new Response("{}", { status: 200, headers: { "content-type": "application/json" } })),
}))

vi.mock("../hooks/use-admin-websocket", () => ({
  useAdminWebSocket: () => ({ keeperLiveEvents: [], keeperWsStatus: "idle" }),
}))

// The tab bodies are not what is under test, and each one fetches. Stub the
// default section to something that is deliberately NOT focusable, which is
// the condition the fix exists for.
vi.mock("../tabs/overview-tab", () => ({
  OverviewTab: () => <p>overview body</p>,
}))

import AdminPage from "../page"

describe("admin console content pane", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/admin")
  })

  it("is a focusable, named region so a keyboard can scroll it", async () => {
    render(<AdminPage />)

    const region = await screen.findByRole("region", { name: /^Admin / })
    expect(region.className).toContain("overflow-y-auto")
    expect(region).toHaveAttribute("tabindex", "0")
  })

  it("names the region after the section on screen, not just 'content'", async () => {
    render(<AdminPage />)
    // Overview is the default section (an unknown or absent ?tab= lands here),
    // so that is the name a landmark list should show.
    expect(await screen.findByRole("region", { name: "Admin Overview" })).toBeInTheDocument()
  })

  it("puts the tab stop on the scroll container, not on the nav beside it", async () => {
    render(<AdminPage />)
    const region = await screen.findByRole("region", { name: /^Admin / })
    const nav = screen.getByRole("navigation", { name: "Admin sections" })
    expect(nav).not.toHaveAttribute("tabindex")
    expect(nav.contains(region)).toBe(false)
  })
})

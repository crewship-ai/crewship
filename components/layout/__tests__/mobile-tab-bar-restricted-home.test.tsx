import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

const surfaces = vi.hoisted(() => ({ value: null as string[] | null, pathname: "/" }))
vi.mock("next/navigation", () => ({ usePathname: () => surfaces.pathname }))
vi.mock("@/hooks/use-inbox", () => ({ useInboxUnreadCount: () => 0 }))
vi.mock("@/hooks/use-access-mode", () => ({
  useRestrictedSurfaces: () => surfaces.value,
  useTrustedWorkspaceId: () => (surfaces.value ? null : "ws-test"),
}))
vi.mock("@/lib/store", () => ({
  useAppStore: (selector: (s: Record<string, unknown>) => unknown) => selector({ setMobileNavOpen: vi.fn() }),
}))

import { MobileTabBar } from "../mobile-tab-bar"

const tabs = () => screen.getAllByRole("link").map((a) => [a.textContent, a.getAttribute("href")])

beforeEach(() => {
  cleanup()
  surfaces.value = null
  surfaces.pathname = "/"
})

// DSH-02 / SHL-07: a restricted session has no Dashboard, so Home leads to
// its own home (restrictedHome), never to "/" and never disappears.
describe("phone Home tab for a restricted session", () => {
  it("trusted: Home is the Dashboard", () => {
    render(<MobileTabBar />)
    expect(tabs()[0]).toEqual(["Home", "/"])
  })

  it("restricted with chat: Home leads to /chat and Chat is not offered twice", () => {
    surfaces.value = ["chat", "issues"]
    render(<MobileTabBar />)
    expect(tabs()).toEqual([["Home", "/chat"], ["Issues", "/issues"]])
  })

  it("restricted without chat: Home leads to the first allowed surface", () => {
    surfaces.value = ["routines", "pages"]
    render(<MobileTabBar />)
    expect(tabs()).toEqual([["Home", "/routines"]])
  })

  it("restricted Home is current on its home surface", () => {
    surfaces.value = ["chat"]
    surfaces.pathname = "/chat/abc"
    render(<MobileTabBar />)
    expect(screen.getByRole("link", { name: "Home" })).toHaveAttribute("aria-current", "page")
  })

  it("restricted with nothing allowed: no Home tab, only More", () => {
    surfaces.value = []
    render(<MobileTabBar />)
    expect(screen.queryAllByRole("link")).toHaveLength(0)
    expect(screen.getByRole("button", { name: /^more$/i })).toBeTruthy()
  })
})

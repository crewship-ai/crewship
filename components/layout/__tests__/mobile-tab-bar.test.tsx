import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))
vi.mock("@/hooks/use-inbox", () => ({ useInboxUnreadCount: () => 4 }))

const setMobileNavOpen = vi.fn()
vi.mock("@/lib/store", () => ({
  useAppStore: (selector: (s: Record<string, unknown>) => unknown) =>
    selector({ mobileNavOpen: false, setMobileNavOpen }),
}))

import { navSections, PHONE_TAB_HREFS, phoneTabs } from "@/lib/nav-sections"
import { MobileTabBar } from "../mobile-tab-bar"

beforeEach(() => {
  cleanup()
  setMobileNavOpen.mockClear()
})

describe("the phone tab bar", () => {
  it("offers exactly the declared tabs, plus a way to the rest", () => {
    render(<MobileTabBar />)
    const hrefs = screen.getAllByRole("link").map((a) => a.getAttribute("href"))
    expect(hrefs).toEqual([...PHONE_TAB_HREFS])
    expect(screen.getByRole("button", { name: /^more$/i })).toBeTruthy()
  })

  it("takes its labels and icons from the one nav definition, never a copy", () => {
    // A tab that hard-coded "Inbox" would keep saying it after the rail was
    // renamed, which is the whole failure this module exists to prevent.
    render(<MobileTabBar />)
    for (const tab of phoneTabs()) {
      expect(screen.getByText(tab.title), `no tab titled ${tab.title}`).toBeTruthy()
    }
  })

  it("opens the full navigation from More rather than duplicating it", () => {
    render(<MobileTabBar />)
    fireEvent.click(screen.getByRole("button", { name: /^more$/i }))
    expect(setMobileNavOpen).toHaveBeenCalledWith(true)
  })

  it("shows a waiting marker on Inbox, the one row with a live count", () => {
    render(<MobileTabBar />)
    expect(screen.getByLabelText("4 unread")).toBeTruthy()
  })

  it("names only destinations the navigation actually carries", () => {
    const known = new Set(navSections.flatMap((s) => s.items).map((i) => i.href))
    for (const href of PHONE_TAB_HREFS) {
      expect(known.has(href), `${href} is a tab but not a nav destination`).toBe(true)
    }
    // phoneTabs throws rather than rendering a blank tab if that ever breaks.
    expect(() => phoneTabs()).not.toThrow()
  })
})

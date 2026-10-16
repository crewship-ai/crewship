import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, act } from "@testing-library/react"

// Harbor 2 (#3350): one 48 px top bar. A page's SubBar renders its identity
// and actions into the top bar's slot; a title that only repeats the area
// name stays the page's heading for assistive technology but is not shown.

let pathname = "/issues"
let mobile = false
vi.mock("next/navigation", () => ({ usePathname: () => pathname }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mobile }))

import { useAppStore } from "@/lib/store"
import { SubBar, SubBarPrimary } from "../sub-bar"

function withSlot(area = "Issues") {
  const slot = document.createElement("div")
  slot.dataset.areaTitle = area
  slot.setAttribute("data-testid", "slot")
  document.body.appendChild(slot)
  act(() => useAppStore.getState().setPageBarSlot(slot))
  return slot
}

beforeEach(() => {
  cleanup()
  document.body.innerHTML = ""
  act(() => useAppStore.getState().setPageBarSlot(null))
  pathname = "/issues"
  mobile = false
})

describe("SubBar in the top bar", () => {
  it("renders its actions into the top bar slot and keeps tabs on the page", () => {
    const slot = withSlot()
    render(<SubBar title="Issues" actions={<SubBarPrimary>New issue</SubBarPrimary>} tabs={[{ id: "a", label: "All" }]} activeTab="a" />)
    expect(slot).toContainElement(screen.getByRole("button", { name: "New issue" }))
    expect(slot).not.toContainElement(screen.getByRole("tab", { name: "All" }))
  })

  it("does not show a title that repeats the area, but keeps it as the heading", () => {
    withSlot()
    render(<SubBar title="Issues" />)
    const h1 = screen.getByRole("heading", { level: 1, name: "Issues" })
    expect(h1.className).toMatch(/sr-only/)
  })

  it("shows a title that differs from the area after a chevron", () => {
    withSlot("Settings")
    render(<SubBar title="Members" />)
    expect(screen.getByRole("heading", { level: 1, name: "Members" }).className).not.toMatch(/sr-only/)
  })

  it("stays in place on a phone and where no slot exists", () => {
    render(<SubBar title="Issues" actions={<SubBarPrimary>New issue</SubBarPrimary>} />)
    expect(screen.getByRole("heading", { level: 1, name: "Issues" })).toBeVisible()
    cleanup()
    const slot = withSlot()
    mobile = true
    render(<SubBar title="Issues" actions={<SubBarPrimary>New issue</SubBarPrimary>} />)
    expect(slot).not.toContainElement(screen.getByRole("button", { name: "New issue" }))
  })
})

import { beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"

// Phone pilot (PILOT-TELEFON-STAV rows 1–2): the detail header is Back, the
// item's name in at most two lines, and ⋯ — the page's secondary actions in a
// MenuSheet — instead of a row of four icons.

const mobile = vi.hoisted(() => ({ value: true }))
vi.mock("next/navigation", () => ({ usePathname: () => "/inbox" }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mobile.value }))

import { useAppStore } from "@/lib/store"
import { MenuSheetItem } from "@/components/ui/menu-sheet"
import { SubBar, SubBarBack } from "../sub-bar"

function withSlot(area = "Inbox") {
  const slot = document.createElement("div")
  slot.dataset.areaTitle = area
  document.body.appendChild(slot)
  act(() => useAppStore.getState().setPageBarSlot(slot))
  return slot
}

beforeEach(() => {
  cleanup()
  document.body.innerHTML = ""
  mobile.value = true
  act(() => useAppStore.getState().setPageBarSlot(null))
})

const LONG = "Approve production deploy of billing-service v2.14 for Finance & Marketing"

describe("SubBarBack", () => {
  it("is a named 44 × 44 tap button that calls back", () => {
    const onBack = vi.fn()
    render(<SubBarBack label="Back to inbox" onClick={onBack} />)
    const btn = screen.getByRole("button", { name: "Back to inbox" })
    expect(btn).toHaveAttribute("data-tap", "")
    fireEvent.click(btn)
    expect(onBack).toHaveBeenCalledTimes(1)
  })
})

describe("SubBar menu on a phone", () => {
  it("renders ⋯ in the top bar and opens the secondary actions as a sheet", () => {
    const slot = withSlot()
    const archive = vi.fn()
    render(
      <SubBar
        title="Inbox"
        section={LONG}
        leading={<SubBarBack label="Back to inbox" onClick={() => {}} />}
        menu={<MenuSheetItem onSelect={archive}>Archive</MenuSheetItem>}
      />,
    )
    const more = screen.getByRole("button", { name: "More actions" })
    expect(slot).toContainElement(more)
    expect(more).toHaveAttribute("data-tap", "")
    fireEvent.click(more)
    const sheet = screen.getByRole("dialog", { name: "More actions" })
    fireEvent.click(sheet.querySelector("[data-menu-row]")!)
    expect(archive).toHaveBeenCalledTimes(1)
  })

  it("shows the detail's name in at most two lines, with the whole name as its title", () => {
    const slot = withSlot()
    render(<SubBar title="Inbox" section={LONG} leading={<SubBarBack label="Back to inbox" onClick={() => {}} />} />)
    const name = slot.querySelector("[data-slot=sub-bar-detail-title]") as HTMLElement
    expect(name).toHaveTextContent(LONG)
    expect(name.className).toContain("line-clamp-2")
    expect(name).toHaveAttribute("title", LONG)
  })

  it("has no ⋯ without a menu", () => {
    withSlot()
    render(<SubBar title="Inbox" leading={<SubBarBack label="Back" onClick={() => {}} />} />)
    expect(screen.queryByRole("button", { name: "More actions" })).toBeNull()
  })
})

describe("SubBar menu on a desktop", () => {
  it("renders ⋯ beside the actions as a popover trigger", () => {
    mobile.value = false
    const slot = withSlot()
    render(<SubBar title="Inbox" section="Approval" menu={<MenuSheetItem>Archive</MenuSheetItem>} />)
    expect(slot).toContainElement(screen.getByRole("button", { name: "More actions" }))
  })
})

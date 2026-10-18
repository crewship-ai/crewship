import { beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, render, screen } from "@testing-library/react"
import { Download, Plus } from "lucide-react"

// The phone header pattern (v67): ONE row — the area tile and title, the
// page's primary action as a blue icon button, search, bell — and no second
// row for the breadcrumb or the page's buttons. Views and filters are chips
// under it (the SubBar's tabs row).

vi.mock("next/navigation", () => ({ usePathname: () => "/routines" }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => true }))

import { useAppStore } from "@/lib/store"
import { SubBar, SubBarPrimary, SubBarSecondary } from "../sub-bar"

function withSlot(area = "Routines") {
  const slot = document.createElement("div")
  slot.dataset.areaTitle = area
  document.body.appendChild(slot)
  act(() => useAppStore.getState().setPageBarSlot(slot))
  return slot
}

beforeEach(() => {
  cleanup()
  document.body.innerHTML = ""
  act(() => useAppStore.getState().setPageBarSlot(null))
})

describe("SubBar on a phone", () => {
  it("puts the page's actions in the top bar, not in a row of their own", () => {
    const slot = withSlot()
    const { container } = render(<SubBar title="Routines" actions={<SubBarPrimary icon={Plus}>New routine</SubBarPrimary>} />)
    expect(slot).toContainElement(screen.getByRole("button", { name: "New routine" }))
    expect(container.querySelector("[data-slot=sub-bar-row]")).toBeNull()
    expect(container.querySelector(".min-h-9")).toBeNull()
  })

  it("draws the primary action icon-only, keeping its name for screen readers", () => {
    withSlot()
    render(<SubBar title="Routines" actions={<SubBarPrimary icon={Plus}>New routine</SubBarPrimary>} />)
    const btn = screen.getByRole("button", { name: "New routine" })
    // Icon-only below 1280: phone and tablet (v67 index.html:275).
    expect(btn.querySelector("span.max-xl\\:sr-only")).toHaveTextContent("New routine")
  })

  it("gives a text-only create action a plus on a phone", () => {
    withSlot()
    render(<SubBar title="Routines" actions={<SubBarPrimary>New routine</SubBarPrimary>} />)
    const btn = screen.getByRole("button", { name: "New routine" })
    expect(btn.querySelector("svg.md\\:hidden")).not.toBeNull()
  })

  it("keeps a labelled non-create action as text", () => {
    withSlot()
    render(<SubBar title="Routines" actions={<SubBarPrimary>Customize</SubBarPrimary>} />)
    expect(screen.getByRole("button", { name: "Customize" }).querySelector(".max-md\\:sr-only")).toBeNull()
  })

  it("shows a secondary action as an icon, or hides it when it has none", () => {
    withSlot()
    render(
      <SubBar
        title="Routines"
        actions={<><SubBarSecondary icon={Download}>Export</SubBarSecondary><SubBarSecondary>Import</SubBarSecondary></>}
      />,
    )
    expect(screen.getByRole("button", { name: "Export" }).querySelector(".max-xl\\:sr-only")).not.toBeNull()
    expect(screen.getByRole("button", { name: "Import" }).className).toContain("max-md:hidden")
  })

  it("keeps the view tabs under the bar as chips", () => {
    withSlot()
    render(<SubBar title="Routines" tabs={[{ id: "all", label: "All routines" }]} activeTab="all" />)
    expect(screen.getByRole("tab", { name: "All routines" })).toBeVisible()
  })

  it("still draws a title that names something other than the area, without the actions", () => {
    const slot = withSlot("Settings")
    render(<SubBar title="Members" actions={<SubBarPrimary icon={Plus}>Invite</SubBarPrimary>} />)
    expect(screen.getByRole("heading", { level: 1, name: "Members" })).not.toHaveClass("sr-only")
    expect(slot).toContainElement(screen.getByRole("button", { name: "Invite" }))
  })

  // PAR-052 (v67 core.js topbar): a phone header names only the area. The
  // current view of an area page (Routines / Overview) is not a second row;
  // the page's h1 keeps the full path for assistive technology.
  it("draws no row for the area's current view, and keeps the full path as the h1", () => {
    withSlot()
    const { container } = render(<SubBar title="Routines" section="Overview" />)
    expect(container.querySelector("[data-slot=sub-bar-row]")).toBeNull()
    const h1 = screen.getByRole("heading", { level: 1, name: "Routines / Overview" })
    expect(h1).toHaveClass("sr-only")
  })

  it("draws no row when only the leading control would have kept it", () => {
    const slot = withSlot()
    const { container } = render(<SubBar title="Routines" leading={<button type="button">Panel</button>} />)
    expect(slot).toContainElement(screen.getByRole("button", { name: "Panel" }))
    expect(container.querySelector("[data-slot=sub-bar-row]")).toBeNull()
    expect(screen.getByRole("heading", { level: 1, name: "Routines" })).toHaveClass("sr-only")
  })

  it("keeps the leading control (the explorer toggle) in the top bar too", () => {
    const slot = withSlot()
    render(<SubBar title="Routines" leading={<button type="button">Panel</button>} />)
    expect(slot).toContainElement(screen.getByRole("button", { name: "Panel" }))
  })
})

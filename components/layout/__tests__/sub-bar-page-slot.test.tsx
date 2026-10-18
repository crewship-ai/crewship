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
import { SubBar, SubBarActions, SubBarPrimary } from "../sub-bar"

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

  it("stays in place where no slot exists, and on a phone puts its actions in the top bar (v67: one header row)", () => {
    render(<SubBar title="Issues" actions={<SubBarPrimary>New issue</SubBarPrimary>} />)
    expect(screen.getByRole("heading", { level: 1, name: "Issues" })).toBeVisible()
    cleanup()
    const slot = withSlot()
    mobile = true
    render(<SubBar title="Issues" actions={<SubBarPrimary>New issue</SubBarPrimary>} />)
    expect(slot).toContainElement(screen.getByRole("button", { name: "New issue" }))
  })

  it("on a phone does not repeat the area named in the top bar, but keeps the actions", () => {
    withSlot("Routines")
    mobile = true
    render(<SubBar title="Routines" actions={<SubBarPrimary>New routine</SubBarPrimary>} />)
    expect(screen.getByRole("heading", { level: 1, name: "Routines" }).className).toMatch(/sr-only/)
    expect(screen.getByRole("button", { name: "New routine" })).toBeVisible()
  })

  it("on a phone shows a title that differs from the area", () => {
    withSlot("Settings")
    mobile = true
    render(<SubBar title="Members" />)
    expect(screen.getByRole("heading", { level: 1, name: "Members" }).className).not.toMatch(/sr-only/)
  })

  it("on a phone leaves no empty row when nothing visible is left in it", () => {
    withSlot("Dashboard")
    mobile = true
    const { container } = render(<SubBar title="Dashboard" />)
    const h1 = screen.getByRole("heading", { level: 1, name: "Dashboard" })
    expect(h1.className).toMatch(/sr-only/)
    // The heading stays for assistive technology, but the 36 px row is gone.
    expect(container.querySelector(".min-h-9")).toBeNull()
  })

  // COM-06 (#3481): Dashboard passes its window control as an action hidden
  // below md; the row then held nothing visible and drew an empty 36 px strip.
  it("on a phone draws no row of its own for the actions, and keeps hidden ones mounted in the top bar", () => {
    const slot = withSlot("Dashboard")
    mobile = true
    const { container } = render(
      <SubBar title="Dashboard" actions={<button type="button" style={{ display: "none" }}>Last 7 days</button>} />,
    )
    expect(container.querySelector("[data-slot=sub-bar-row]")).toBeNull()
    expect(screen.getByRole("heading", { level: 1, name: "Dashboard", hidden: true })).toBeInTheDocument()
    // A display:none button has no accessible name to query by; its text is
    // still in the document, so it stays mounted.
    expect(slot).toContainElement(screen.getByText("Last 7 days"))
    expect(screen.getByText("Last 7 days").closest("[data-slot=sub-bar-actions]")).not.toBeNull()
  })

  it("on a phone keeps the visible action in the top bar, with no row of its own", () => {
    const slot = withSlot("Dashboard")
    mobile = true
    const { container } = render(
      <SubBar
        title="Dashboard"
        actions={<><button type="button" style={{ display: "none" }}>Window</button><SubBarPrimary>Customize</SubBarPrimary></>}
      />,
    )
    expect(container.querySelector("[data-slot=sub-bar-row]")).toBeNull()
    expect(slot).toContainElement(screen.getByRole("button", { name: "Customize" }))
  })

  // COM-12 (#3481): a page deeper in the tree than its layout's SubBar (the
  // routine page under the Routines layout) puts its actions in the bar
  // through the kit, not through a portal target the layout hands down.
  it("SubBarActions puts a page's actions into the SubBar rendered elsewhere, after the bar's own", () => {
    const slot = withSlot("Routines")
    const { rerender } = render(
      <>
        <SubBar title="Weekly scan" actions={<SubBarPrimary>Edit</SubBarPrimary>} />
        <section><SubBarActions><button type="button">Run now</button></SubBarActions></section>
      </>,
    )
    const actions = slot.querySelector("[data-slot=sub-bar-actions]")
    expect(actions).not.toBeNull()
    expect(actions).toContainElement(screen.getByRole("button", { name: "Run now" }))
    expect([...actions!.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["Edit", "Run now"])
    rerender(<SubBar title="Weekly scan" actions={<SubBarPrimary>Edit</SubBarPrimary>} />)
    expect(screen.queryByRole("button", { name: "Run now" })).toBeNull()
  })

  it("SubBarActions works on a phone, in the top bar", () => {
    const slot = withSlot("Routines")
    mobile = true
    render(
      <>
        <SubBar title="Weekly scan" />
        <SubBarActions><button type="button">Run now</button></SubBarActions>
      </>,
    )
    const actions = slot.querySelector("[data-slot=sub-bar-actions]")
    expect(actions).toContainElement(screen.getByRole("button", { name: "Run now" }))
  })

  it("SubBarActions renders nothing while no SubBar is on screen", () => {
    render(<SubBarActions><button type="button">Run now</button></SubBarActions>)
    expect(screen.queryByRole("button", { name: "Run now" })).toBeNull()
  })

  it("leaves the page's one h1 to a page that shows its own heading", () => {
    withSlot("Activity")
    render(<SubBar title="Activity" section="Could not finish" heading={false} />)
    expect(screen.queryByRole("heading", { level: 1 })).toBeNull()
    expect(screen.getByText("Could not finish")).toBeVisible()
  })

  it("on a phone a page with its own heading gets no extra h1 either", () => {
    withSlot("Dashboard")
    mobile = true
    render(<SubBar title="Dashboard" heading={false} />)
    expect(screen.queryByRole("heading", { level: 1 })).toBeNull()
  })
})

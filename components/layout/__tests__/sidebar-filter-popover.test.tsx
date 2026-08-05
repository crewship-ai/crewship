import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, waitForElementToBeRemoved } from "@testing-library/react"

import { SidebarFilterPopover, SidebarFacet, SidebarFacetOption } from "../sidebar-kit"

/**
 * The behaviours here are the whole reason the panel moved into the kit (#1777).
 *
 * Every surface had already adopted `SidebarFilterButton`, so the trigger was
 * never what drifted — the panel was. Credentials in particular closed itself on
 * every pick AND cleared the sibling facet, which makes combining two facets
 * impossible; Issues had fixed both, privately, in its own copy. These tests pin
 * the fixed behaviour to the shared component so the next adopter inherits it
 * instead of re-deciding it.
 */

/** Two facets side by side — the only way to prove a pick leaves its neighbour alone. */
function TwoFacets({
  onStatusToggle = vi.fn(),
  onCrewToggle = vi.fn(),
  onStatusReset = vi.fn(),
  onClear,
  activeCount = 0,
}: {
  onStatusToggle?: () => void
  onCrewToggle?: () => void
  onStatusReset?: () => void
  onClear?: () => void
  activeCount?: number
} = {}) {
  return (
    <SidebarFilterPopover label="Filter issues" activeCount={activeCount} onClear={onClear}>
      <SidebarFacet label="Status" resetLabel="Any status" resetActive onReset={onStatusReset} first>
        <SidebarFacetOption active={false} onToggle={onStatusToggle}>
          Todo
        </SidebarFacetOption>
      </SidebarFacet>
      <SidebarFacet label="Crews" resetLabel="All crews" resetActive onReset={vi.fn()}>
        <SidebarFacetOption active onToggle={onCrewToggle}>
          Platform
        </SidebarFacetOption>
      </SidebarFacet>
    </SidebarFilterPopover>
  )
}

const openPanel = () => fireEvent.click(screen.getByRole("button", { name: /filter/i }))
const panel = () => screen.queryByRole("group", { name: /filter issues/i })

describe("SidebarFilterPopover", () => {
  beforeEach(() => cleanup())

  it("keeps the panel closed until the trigger is clicked", () => {
    render(<TwoFacets />)
    expect(panel()).toBeNull()
    openPanel()
    expect(panel()).toBeTruthy()
  })

  it("surfaces the active count on its trigger", () => {
    render(<TwoFacets activeCount={2} />)
    expect(screen.getByRole("button", { name: /filter/i }).textContent).toContain("2")
  })

  it("marks the trigger expanded only while the panel is open", () => {
    render(<TwoFacets />)
    const trigger = screen.getByRole("button", { name: /filter/i })
    expect(trigger.getAttribute("aria-expanded")).toBe("false")
    openPanel()
    expect(trigger.getAttribute("aria-expanded")).toBe("true")
  })

  // NOTE — assert on aria-expanded, not on the panel node. The panel plays an
  // exit animation, so it is still in the DOM for a frame after it starts
  // closing: `expect(panel()).toBeTruthy()` passes even against an
  // implementation that closes on every pick, which is the exact bug this test
  // exists to catch. aria-expanded flips synchronously.
  it("STAYS OPEN after a facet pick — combining two facets depends on it", () => {
    const onStatusToggle = vi.fn()
    render(<TwoFacets onStatusToggle={onStatusToggle} />)
    openPanel()
    fireEvent.click(screen.getByRole("button", { name: /todo/i }))
    expect(onStatusToggle).toHaveBeenCalled()
    expect(screen.getByRole("button", { name: /filter/i }).getAttribute("aria-expanded")).toBe("true")
    expect(panel()).toBeTruthy()
  })

  it("leaves a sibling facet untouched when one facet is picked", () => {
    const onCrewToggle = vi.fn()
    render(<TwoFacets onCrewToggle={onCrewToggle} />)
    openPanel()
    fireEvent.click(screen.getByRole("button", { name: /todo/i }))
    expect(onCrewToggle).not.toHaveBeenCalled()
  })

  it("stays open after a facet reset too", () => {
    const onStatusReset = vi.fn()
    render(<TwoFacets onStatusReset={onStatusReset} />)
    openPanel()
    fireEvent.click(screen.getByRole("button", { name: /any status/i }))
    expect(onStatusReset).toHaveBeenCalled()
    expect(screen.getByRole("button", { name: /filter/i }).getAttribute("aria-expanded")).toBe("true")
    expect(panel()).toBeTruthy()
  })

  it("offers Clear all only when something is active", () => {
    const onClear = vi.fn()
    const { rerender } = render(<TwoFacets activeCount={0} onClear={onClear} />)
    openPanel()
    expect(screen.queryByRole("button", { name: /clear all/i })).toBeNull()
    rerender(<TwoFacets activeCount={2} onClear={onClear} />)
    fireEvent.click(screen.getByRole("button", { name: /clear all/i }))
    expect(onClear).toHaveBeenCalled()
  })

  // The panel plays its exit before unmounting, so assert on removal rather
  // than on the frame right after the event — same as bar-menu.test.tsx.
  it("closes on click-away", async () => {
    render(<TwoFacets />)
    openPanel()
    fireEvent.click(screen.getByTestId("sidebar-filter-dismiss"))
    expect(screen.getByRole("button", { name: /filter/i }).getAttribute("aria-expanded")).toBe("false")
    await waitForElementToBeRemoved(panel)
  })

  it("closes on Escape", async () => {
    render(<TwoFacets />)
    openPanel()
    fireEvent.keyDown(document, { key: "Escape" })
    expect(screen.getByRole("button", { name: /filter/i }).getAttribute("aria-expanded")).toBe("false")
    await waitForElementToBeRemoved(panel)
  })
})

describe("SidebarFacet / SidebarFacetOption", () => {
  beforeEach(() => cleanup())

  it("renders the facet label and its reset row", () => {
    render(
      <SidebarFacet label="Priority" resetLabel="Any priority" resetActive onReset={vi.fn()} first>
        <SidebarFacetOption active={false} onToggle={vi.fn()}>
          Urgent
        </SidebarFacetOption>
      </SidebarFacet>,
    )
    expect(screen.getByText("Priority")).toBeTruthy()
    expect(screen.getByRole("button", { name: /any priority/i })).toBeTruthy()
  })

  it("reports the reset row's own state through aria-pressed", () => {
    const { rerender } = render(
      <SidebarFacet label="Priority" resetLabel="Any priority" resetActive onReset={vi.fn()} first>
        <span />
      </SidebarFacet>,
    )
    expect(screen.getByRole("button", { name: /any priority/i }).getAttribute("aria-pressed")).toBe("true")
    rerender(
      <SidebarFacet label="Priority" resetLabel="Any priority" resetActive={false} onReset={vi.fn()} first>
        <span />
      </SidebarFacet>,
    )
    expect(screen.getByRole("button", { name: /any priority/i }).getAttribute("aria-pressed")).toBe("false")
  })

  it("reports an option's selected state through aria-pressed, and toggles it", () => {
    const onToggle = vi.fn()
    const { rerender } = render(
      <SidebarFacetOption active onToggle={onToggle}>
        Urgent
      </SidebarFacetOption>,
    )
    const opt = screen.getByRole("button", { name: /urgent/i })
    expect(opt.getAttribute("aria-pressed")).toBe("true")
    fireEvent.click(opt)
    expect(onToggle).toHaveBeenCalled()
    rerender(
      <SidebarFacetOption active={false} onToggle={onToggle}>
        Urgent
      </SidebarFacetOption>,
    )
    expect(screen.getByRole("button", { name: /urgent/i }).getAttribute("aria-pressed")).toBe("false")
  })
})

import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, within } from "@testing-library/react"

import { SidebarFilterButton } from "../sidebar-kit"
import {
  SidebarFacetBlock,
  SidebarInlineFacet,
  useSidebarFacets,
  type SidebarFacetValue,
} from "../sidebar-facets"

// COM-01 (#3481): the facets open INSIDE the secondary panel, under the
// Filter button (v67 `panel()` → `pk-fx`, as the Inbox composes by hand in
// #3392), never in a floating popover. A facet lists at most six values and
// offers the rest through "All N…".

const STATUS: SidebarFacetValue[] = [
  { value: "active", label: "Active", count: 4 },
  { value: "paused", label: "Paused", count: 2 },
  { value: "draft", label: "Draft", count: 0 },
]

const CREWS: SidebarFacetValue[] = Array.from({ length: 9 }, (_, i) => ({
  value: `crew-${i + 1}`,
  label: `Crew ${i + 1}`,
  count: 9 - i,
}))

function Harness({
  status = [],
  crews = [],
  onStatus = () => {},
  onCrew = () => {},
  onClear = () => {},
}: {
  status?: string[]
  crews?: string[]
  onStatus?: (v: string) => void
  onCrew?: (v: string) => void
  onClear?: () => void
}) {
  const facets = useSidebarFacets()
  const active = status.length + crews.length
  return (
    <>
      <SidebarFilterButton activeCount={active} {...facets.triggerProps}>Filter</SidebarFilterButton>
      <SidebarFacetBlock {...facets.blockProps} label="Filter routines" activeCount={active} onClear={onClear}>
        <SidebarInlineFacet label="Status" values={STATUS} selected={status} onToggle={onStatus} onClear={() => {}} />
        <SidebarInlineFacet label="Crew" values={CREWS} selected={crews} onToggle={onCrew} onClear={() => {}} defaultOpen />
      </SidebarFacetBlock>
      <p>list below</p>
    </>
  )
}

function Stateful() {
  const [status, setStatus] = React.useState<string[]>([])
  const toggle = (v: string) => setStatus((s) => (s.includes(v) ? s.filter((x) => x !== v) : [...s, v]))
  return <Harness status={status} onStatus={toggle} />
}

describe("sidebar facet block (COM-01)", () => {
  beforeEach(() => {
    cleanup()
    // jsdom lacks what cmdk's list calls; environment only.
    Element.prototype.scrollIntoView ??= vi.fn()
    globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver
  })

  it("stays out of the panel until Filter opens it, and the trigger names the block it opens", () => {
    render(<Harness />)
    const trigger = screen.getByRole("button", { name: /^Filter/ })
    expect(trigger).toHaveAttribute("aria-expanded", "false")
    expect(screen.queryByRole("group", { name: "Filter routines" })).toBeNull()
    fireEvent.click(trigger)
    expect(trigger).toHaveAttribute("aria-expanded", "true")
    const block = screen.getByRole("group", { name: "Filter routines" })
    expect(trigger.getAttribute("aria-controls")).toBe(block.id)
    // Inline in the document flow, not a floating layer with a dismiss overlay.
    expect(screen.queryByTestId("sidebar-filter-dismiss")).toBeNull()
    expect(block.className).not.toMatch(/\babsolute\b|\bfixed\b/)
  })

  it("heads the block with Filter, the active count and Clear while something is set", () => {
    const onClear = vi.fn()
    const { rerender } = render(<Harness onClear={onClear} />)
    fireEvent.click(screen.getByRole("button", { name: /^Filter/ }))
    let block = screen.getByRole("group", { name: "Filter routines" })
    expect(within(block).queryByRole("button", { name: "Clear all filters" })).toBeNull()
    rerender(<Harness status={["active"]} crews={["crew-2"]} onClear={onClear} />)
    block = screen.getByRole("group", { name: "Filter routines" })
    expect(within(block).getByTestId("facet-block-count")).toHaveTextContent("2")
    fireEvent.click(within(block).getByRole("button", { name: "Clear all filters" }))
    expect(onClear).toHaveBeenCalledOnce()
  })

  it("lists at most six values and offers the rest as All N…", () => {
    render(<Harness />)
    fireEvent.click(screen.getByRole("button", { name: /^Filter/ }))
    const crew = screen.getByRole("group", { name: "Crew" })
    const options = within(crew).getAllByRole("button", { pressed: false })
    expect(options.map((o) => o.textContent)).toEqual(
      ["Crew 1", "Crew 2", "Crew 3", "Crew 4", "Crew 5", "Crew 6"].map((n, i) => `${n}${9 - i}`),
    )
    expect(within(crew).getByRole("button", { name: "All 9…" })).toBeInTheDocument()
  })

  it("does not offer All N… when every value fits", () => {
    render(<Harness status={["paused"]} />)
    fireEvent.click(screen.getByRole("button", { name: /^Filter/ }))
    const status = screen.getByRole("group", { name: "Status" })
    expect(within(status).queryByRole("button", { name: /^All \d/ })).toBeNull()
  })

  it("keeps a chosen value past the sixth in view", () => {
    render(<Harness crews={["crew-8"]} />)
    fireEvent.click(screen.getByRole("button", { name: /^Filter/ }))
    const crew = screen.getByRole("group", { name: "Crew" })
    expect(within(crew).getByRole("button", { name: /Crew 8/, pressed: true })).toBeInTheDocument()
  })

  it("starts a facet folded unless it has a value, and folds from its header", () => {
    render(<Harness crews={[]} />)
    fireEvent.click(screen.getByRole("button", { name: /^Filter/ }))
    const header = screen.getByRole("button", { name: /^Status/, expanded: false })
    expect(screen.queryByRole("group", { name: "Status" })).toBeNull()
    fireEvent.click(header)
    expect(header).toHaveAttribute("aria-expanded", "true")
    expect(screen.getByRole("group", { name: "Status" })).toBeInTheDocument()
  })

  it("summarises the facet in its header and clears just that facet", () => {
    const onClear = vi.fn()
    const { rerender } = render(
      <SidebarInlineFacet label="Status" values={STATUS} selected={["paused"]} onToggle={() => {}} onClear={onClear} />,
    )
    expect(screen.getByRole("button", { name: /^Status/ })).toHaveTextContent("StatusPaused")
    fireEvent.click(screen.getByRole("button", { name: "Clear Status" }))
    expect(onClear).toHaveBeenCalledOnce()
    rerender(<SidebarInlineFacet label="Status" values={STATUS} selected={["paused", "active"]} onToggle={() => {}} onClear={onClear} />)
    expect(screen.getByRole("button", { name: /^Status/ })).toHaveTextContent("Status2 selected")
    rerender(<SidebarInlineFacet label="Status" values={STATUS} selected={[]} onToggle={() => {}} onClear={onClear} />)
    expect(screen.queryByRole("button", { name: "Clear Status" })).toBeNull()
  })

  it("accepts a single value as the selection", () => {
    render(<SidebarInlineFacet label="Status" values={STATUS} selected="active" onToggle={() => {}} onClear={() => {}} />)
    expect(screen.getByRole("button", { name: /Active/, pressed: true })).toBeInTheDocument()
  })

  it("toggles one value and keeps the block open, so a second facet is one click away", () => {
    render(<Stateful />)
    fireEvent.click(screen.getByRole("button", { name: /^Filter/ }))
    fireEvent.click(screen.getByRole("button", { name: /^Status/ }))
    fireEvent.click(screen.getByRole("button", { name: /Paused/, pressed: false }))
    // The header now names the choice too, so ask for the value by its state.
    expect(screen.getByRole("button", { name: /Paused/, pressed: true })).toBeInTheDocument()
    expect(screen.getByRole("group", { name: "Filter routines" })).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /Active/, pressed: false }))
    expect(screen.getByRole("button", { name: /^Status/ })).toHaveTextContent("2 selected")
  })

  it("All N… opens a searchable list of every value", async () => {
    const onCrew = vi.fn()
    render(<Harness onCrew={onCrew} />)
    fireEvent.click(screen.getByRole("button", { name: /^Filter/ }))
    fireEvent.click(screen.getByRole("button", { name: "All 9…" }))
    const search = await screen.findByPlaceholderText("Find a crew…")
    fireEvent.change(search, { target: { value: "Crew 9" } })
    const listbox = screen.getByRole("listbox")
    expect(within(listbox).getAllByRole("option").map((o) => o.textContent)).toEqual(["Crew 91"])
    fireEvent.click(within(listbox).getByRole("option", { name: /Crew 9/ }))
    expect(onCrew).toHaveBeenCalledWith("crew-9")
  })

  it("Escape inside the block closes it and returns focus to Filter", () => {
    render(<Harness />)
    const trigger = screen.getByRole("button", { name: /^Filter/ })
    fireEvent.click(trigger)
    const block = screen.getByRole("group", { name: "Filter routines" })
    fireEvent.keyDown(within(block).getByRole("button", { name: /^Status/ }), { key: "Escape" })
    expect(screen.queryByRole("group", { name: "Filter routines" })).toBeNull()
    expect(document.activeElement).toBe(trigger)
  })

  // ACT-06 / RT-02 (#3481): Filter opens the block and keeps focus on itself,
  // so Escape right after opening it comes from the trigger. The popover it
  // replaced closed on Escape from anywhere; the block must close from its
  // own trigger too.
  it("Escape on the Filter button closes the open block and keeps focus there", () => {
    render(<Harness />)
    const trigger = screen.getByRole("button", { name: /^Filter/ })
    fireEvent.click(trigger)
    trigger.focus()
    expect(screen.getByRole("group", { name: "Filter routines" })).toBeInTheDocument()
    fireEvent.keyDown(trigger, { key: "Escape" })
    expect(screen.queryByRole("group", { name: "Filter routines" })).toBeNull()
    expect(trigger).toHaveAttribute("aria-expanded", "false")
    expect(document.activeElement).toBe(trigger)
  })
})

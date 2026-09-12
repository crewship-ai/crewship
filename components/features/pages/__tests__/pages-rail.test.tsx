/**
 * The /pages filter rail — PRD §9b.1.
 *
 * The point of these tests is not that the rail renders. It is that Pages is
 * the SECOND surface on the shared filter panel and not the sixth hand-rolled
 * one (#1776): the panel stays open after a pick, a pick never touches a
 * sibling facet, and both facets are multi-select. Credentials'
 * `set({category}); setFilterOpen(false)` is the behaviour being ruled out, and
 * it is invisible to a test that only asserts what is on screen at rest.
 */
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, within, act } from "@testing-library/react"

import { PagesRail } from "@/components/features/pages/pages-rail"
import * as sidebarKit from "@/components/layout/sidebar-kit"
import { toPageView, EMPTY_PAGE_FILTERS, type PageFilters } from "@/hooks/use-pages"

const PAGES = [
  toPageView({
    id: "p1",
    slug: "fleet-201",
    name: "Flotila .201",
    owner: "crew/lookout",
    panels: [
      { id: "a", schema: "status.v1", state: "stale" },
      { id: "b", schema: "metric.v1", state: "fresh" },
    ],
  }),
  toPageView({
    id: "p2",
    slug: "nightly-close",
    name: "Nightly close",
    owner: "crew/finance",
    panels: [{ id: "a", schema: "metric.v1", state: "fresh" }],
  }),
]

const ME = "u1"

/**
 * The five pages the grouping tests share. `reach` is what the server says
 * about the CALLER (P0b): `crew:lookout` means "you reach this through your
 * membership of lookout", which is the only membership signal the list
 * carries — there is no roster request, and this file must not add one.
 */
const GROUPED = [
  toPageView({ id: "g1", slug: "nightly-close", name: "Nightly close", owner: "crew/finance", reach: ["role"], panels: [] }),
  toPageView({ id: "g2", slug: "anna-board", name: "Anna's board", owner: "user/u2", reach: ["grant"], panels: [] }),
  toPageView({ id: "g3", slug: "fleet-201", name: "Flotila .201", owner: "crew/lookout", reach: ["crew:lookout"], panels: [] }),
  toPageView({ id: "g4", slug: "my-notes", name: "My notes", owner: `user/${ME}`, reach: ["owner"], panels: [] }),
  toPageView({ id: "g5", slug: "alpha-ops", name: "Alpha ops", owner: "crew/alpha", reach: ["role"], panels: [] }),
]

const STORAGE_KEY = `pages-rail:ws-1:${ME}`

/** Group headers are the kit's collapsible section buttons. Not "every button
 *  with aria-expanded": the Filter trigger carries one too. */
const groupHeaders = () =>
  screen.getAllByRole("button").filter((b) => b.hasAttribute("data-rail-header"))
const groupHeader = (label: string) => {
  const hit = groupHeaders().find((b) => (b.textContent ?? "").startsWith(label))
  if (!hit) throw new Error(`no group header starting with ${JSON.stringify(label)}`)
  return hit
}
const rowOf = (name: string) => screen.getByText(name).closest('[role="button"]') as HTMLElement

function railProps(over: Partial<React.ComponentProps<typeof PagesRail>> = {}) {
  return {
    pages: PAGES,
    workspaceId: "ws-1",
    currentUserId: ME,
    search: "",
    onSearchChange: vi.fn(),
    filters: EMPTY_PAGE_FILTERS as PageFilters,
    onFiltersChange: vi.fn(),
    selectedSlug: null,
    onSelectPage: vi.fn(),
    ...over,
  }
}

function renderRail(over: Partial<React.ComponentProps<typeof PagesRail>> = {}) {
  const props = railProps(over)
  const view = render(<PagesRail {...props} />)
  return {
    onFiltersChange: props.onFiltersChange,
    onSelectPage: props.onSelectPage,
    onSearchChange: props.onSearchChange,
    rerender: view.rerender,
  }
}

// `^Filter` and not `/filter/i`: the active-filter chips carry "Remove filter"
// buttons, and a loose match would grab one of those instead of the trigger.
const openPanel = () => fireEvent.click(screen.getByRole("button", { name: /^Filter/ }))
const panel = () => screen.queryByRole("group", { name: /filter pages/i })

describe("PagesRail", () => {
  beforeEach(() => cleanup())

  it("is built from the shared kit rather than a private popover", () => {
    // A structural assertion on purpose: the drift #1776 tracks is a surface
    // that stops importing the kit, and that is not visible in the DOM.
    expect(typeof sidebarKit.SidebarFilterPopover).toBe("function")
    expect(typeof sidebarKit.SidebarFacet).toBe("function")
    expect(typeof sidebarKit.SidebarFacetOption).toBe("function")
    renderRail()
    // The panel the kit renders carries role="group" + its accessible name.
    openPanel()
    expect(panel()).toBeTruthy()
  })

  it("offers exactly the STATUS options §9b.1 names", () => {
    renderRail()
    openPanel()
    const p = panel()!
    // "All" is the facet's reset row (exact name — "All crews" is the OWNER
    // facet's own reset); the four states carry their count in the name.
    expect(within(p).getByRole("button", { name: "All" })).toBeTruthy()
    for (const label of ["Fresh", "Stale", "Failed", "Never produced"]) {
      expect(within(p).getByRole("button", { name: new RegExp(`^${label}`) })).toBeTruthy()
    }
  })

  it("keeps the panel open after a pick", () => {
    renderRail()
    openPanel()
    fireEvent.click(within(panel()!).getByRole("button", { name: /^Stale/i }))
    expect(panel()).toBeTruthy()
  })

  it("adds to a facet instead of replacing it, and leaves its sibling alone", () => {
    const { onFiltersChange } = renderRail({ filters: { states: ["stale"], owners: ["crew/finance"], shared: false } })
    openPanel()
    fireEvent.click(within(panel()!).getByRole("button", { name: /^Fresh/i }))
    expect(onFiltersChange).toHaveBeenCalledWith({
      states: ["stale", "fresh"],
      owners: ["crew/finance"], // untouched — the whole point of #1776
      shared: false,
    })
  })

  it("toggles a picked option off without clearing the rest", () => {
    const { onFiltersChange } = renderRail({ filters: { states: ["stale", "fresh"], owners: [], shared: false } })
    openPanel()
    fireEvent.click(within(panel()!).getByRole("button", { name: /^Stale/i }))
    expect(onFiltersChange).toHaveBeenCalledWith({ states: ["fresh"], owners: [], shared: false })
  })

  it("resets one facet without touching the other", () => {
    const { onFiltersChange } = renderRail({ filters: { states: ["stale"], owners: ["crew/finance"], shared: false } })
    openPanel()
    fireEvent.click(within(panel()!).getByRole("button", { name: "All" }))
    expect(onFiltersChange).toHaveBeenCalledWith({ states: [], owners: ["crew/finance"], shared: false })
  })

  it("builds the OWNER facet per crew, from the loaded pages", () => {
    const { onFiltersChange } = renderRail()
    openPanel()
    const p = panel()!
    expect(within(p).getByRole("button", { name: /^lookout/ })).toBeTruthy()
    fireEvent.click(within(p).getByRole("button", { name: /^finance/ }))
    expect(onFiltersChange).toHaveBeenCalledWith({ states: [], owners: ["crew/finance"], shared: false })
  })

  it("counts facet options over the whole list, not the filtered view", () => {
    // With "stale" picked, "Fresh" must still report the 2 pages it would
    // match — a menu whose unpicked options all read 0 argues with itself.
    renderRail({ filters: { states: ["stale"], owners: [], shared: false } })
    openPanel()
    const fresh = within(panel()!).getByRole("button", { name: /^Fresh/i })
    expect(fresh.textContent).toContain("2")
  })

  it("shows what is narrowing the list as removable chips", () => {
    const { onFiltersChange } = renderRail({ filters: { states: ["stale"], owners: ["crew/lookout"], shared: false } })
    expect(screen.getByText("Stale")).toBeTruthy()
    // "lookout" is also a group header now; the chip is the one beside Stale.
    expect(screen.getAllByText("lookout").length).toBeGreaterThan(0)
    fireEvent.click(screen.getAllByRole("button", { name: /remove filter/i })[0])
    expect(onFiltersChange).toHaveBeenCalledWith({ states: [], owners: ["crew/lookout"], shared: false })
  })

  it("lists the pages the filter leaves, and opens one on click", () => {
    const { onSelectPage } = renderRail({ filters: { states: ["stale"], owners: [], shared: false } })
    expect(screen.getByText("Flotila .201")).toBeTruthy()
    expect(screen.queryByText("Nightly close")).toBeNull()
    fireEvent.click(screen.getByText("Flotila .201"))
    expect(onSelectPage).toHaveBeenCalledWith("fleet-201")
  })

  it("never renders a blank list — an empty result names the next action (§9b.3)", () => {
    renderRail({ filters: { states: ["failed"], owners: [], shared: false } })
    expect(screen.getByText(/clear a facet/i)).toBeTruthy()

    cleanup()
    renderRail({ pages: [] })
    expect(screen.getByText(/crewship page create/i)).toBeTruthy()
  })
})

// ── groups (#2523) ──────────────────────────────────────────────────────────
//
// The rail is grouped by OWNER: Mine, the crews I belong to, the other crews,
// and the personal pages of other people I can reach. Membership comes from
// `reach` on the pages themselves — there is no roster request here, so on a
// server that does not send `reach` every crew sorts alphabetically.

describe("PagesRail groups", () => {
  const storage = globalThis.localStorage as unknown as {
    getItem: ReturnType<typeof vi.fn>
    setItem: ReturnType<typeof vi.fn>
  }

  beforeEach(() => {
    cleanup()
    storage.getItem.mockReset()
    storage.setItem.mockReset()
    storage.getItem.mockImplementation(() => null)
  })

  const collapsedInStorage = (keys: string[]) =>
    storage.getItem.mockImplementation((k: string) => (k === STORAGE_KEY ? JSON.stringify(keys) : null))

  it("orders the groups Mine, my crews, other crews A→Z, Owned by others — and counts each", () => {
    renderRail({ pages: GROUPED })
    expect(groupHeaders().map((h) => h.textContent)).toEqual([
      "Mine1",
      "lookout1",
      "alpha1",
      "finance1",
      "Owned by others1",
    ])
    // The owner is said once, in the header — a row does not repeat it.
    expect(rowOf("Flotila .201").textContent).not.toMatch(/lookout/)
    expect(rowOf("Flotila .201").querySelector("[title]")?.getAttribute("title")).toBe("Flotila .201")
  })

  it("renders no header for a group with nothing in it", () => {
    renderRail({ pages: PAGES, currentUserId: ME })
    expect(groupHeaders().map((h) => h.textContent)).toEqual(["finance1", "lookout1"])
    expect(screen.queryByText(/^Mine/)).toBeNull()
    expect(screen.queryByText(/Owned by others/)).toBeNull()
  })

  it("reads the saved collapse state per workspace and user, and writes a toggle back", () => {
    collapsedInStorage(["crew/finance"])
    renderRail({ pages: GROUPED })
    expect(storage.getItem).toHaveBeenCalledWith(STORAGE_KEY)
    expect(groupHeader("finance").getAttribute("aria-expanded")).toBe("false")
    expect(screen.queryByText("Nightly close")).toBeNull()
    expect(screen.getByText("Flotila .201")).toBeTruthy()

    fireEvent.click(groupHeader("lookout"))
    expect(screen.queryByText("Flotila .201")).toBeNull()
    expect(storage.setItem).toHaveBeenLastCalledWith(STORAGE_KEY, JSON.stringify(["crew/finance", "crew/lookout"]))

    fireEvent.click(groupHeader("finance"))
    expect(screen.getByText("Nightly close")).toBeTruthy()
    expect(storage.setItem).toHaveBeenLastCalledWith(STORAGE_KEY, JSON.stringify(["crew/lookout"]))
  })

  it("defaults to everything expanded when storage is empty, throws or holds junk", () => {
    storage.getItem.mockImplementation(() => {
      throw new Error("SecurityError")
    })
    storage.setItem.mockImplementation(() => {
      throw new Error("QuotaExceededError")
    })
    renderRail({ pages: GROUPED })
    expect(groupHeaders().every((h) => h.getAttribute("aria-expanded") === "true")).toBe(true)
    // A toggle still works in memory when it cannot be saved.
    fireEvent.click(groupHeader("alpha"))
    expect(screen.queryByText("Alpha ops")).toBeNull()

    cleanup()
    storage.getItem.mockImplementation(() => "{not json")
    renderRail({ pages: GROUPED })
    expect(groupHeaders().every((h) => h.getAttribute("aria-expanded") === "true")).toBe(true)
  })

  it("opens collapsed groups to their matches while searching, hides the rest, and restores on clear", () => {
    collapsedInStorage(["crew/finance"])
    const { rerender } = renderRail({ pages: GROUPED })
    expect(screen.queryByText("Nightly close")).toBeNull()

    rerender(<PagesRail {...railProps({ pages: GROUPED, search: "night" })} />)
    expect(groupHeaders().map((h) => h.textContent)).toEqual(["finance1"])
    expect(groupHeader("finance").getAttribute("aria-expanded")).toBe("true")
    expect(screen.getByText("Nightly close")).toBeTruthy()
    // A search is not a toggle: nothing was written to storage.
    expect(storage.setItem).not.toHaveBeenCalled()

    rerender(<PagesRail {...railProps({ pages: GROUPED, search: "" })} />)
    expect(groupHeader("finance").getAttribute("aria-expanded")).toBe("false")
    expect(screen.queryByText("Nightly close")).toBeNull()
    expect(groupHeaders()).toHaveLength(5)
  })

  it("opens the group of the selected page so the active row is always visible", () => {
    collapsedInStorage(["crew/finance"])
    const { rerender } = renderRail({ pages: GROUPED })
    expect(screen.queryByText("Nightly close")).toBeNull()

    rerender(<PagesRail {...railProps({ pages: GROUPED, selectedSlug: "nightly-close" })} />)
    expect(groupHeader("finance").getAttribute("aria-expanded")).toBe("true")
    expect(rowOf("Nightly close").getAttribute("aria-pressed")).toBe("true")
    expect(storage.setItem).toHaveBeenLastCalledWith(STORAGE_KEY, JSON.stringify([]))

    // The person may still fold it afterwards; the selection does not pin it open.
    fireEvent.click(groupHeader("finance"))
    expect(screen.queryByText("Nightly close")).toBeNull()
  })

  it("keeps the focused row across a selection (the rail is one node, not a rebuild)", () => {
    const { rerender } = renderRail({ pages: GROUPED })
    const row = rowOf("Flotila .201")
    act(() => row.focus())
    rerender(<PagesRail {...railProps({ pages: GROUPED, selectedSlug: "fleet-201" })} />)
    expect(document.activeElement).toBe(row)
    expect(rowOf("Flotila .201")).toBe(row)
  })

  it("walks headers and visible rows with Up/Down/Home/End, folds with Left and unfolds with Right", () => {
    // A tree walk, as WAI-ARIA draws it: the arrows visit every visible node,
    // group headers included, so a group can be reached and folded without
    // leaving the arrow keys. Nothing wraps at either end.
    const { onSelectPage } = renderRail({ pages: GROUPED })
    const notes = rowOf("My notes")
    act(() => notes.focus())

    fireEvent.keyDown(notes, { key: "ArrowUp" })
    expect(document.activeElement).toBe(groupHeader("Mine"))
    fireEvent.keyDown(document.activeElement!, { key: "ArrowUp" })
    expect(document.activeElement).toBe(groupHeader("Mine")) // the top does not wrap
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" })
    expect(document.activeElement).toBe(notes)
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" })
    expect(document.activeElement).toBe(groupHeader("lookout"))
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" })
    expect(document.activeElement).toBe(rowOf("Flotila .201"))
    fireEvent.keyDown(document.activeElement!, { key: "End" })
    expect(document.activeElement).toBe(rowOf("Anna's board"))
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" })
    expect(document.activeElement).toBe(rowOf("Anna's board")) // nor the bottom
    fireEvent.keyDown(document.activeElement!, { key: "Home" })
    expect(document.activeElement).toBe(groupHeader("Mine"))

    // Left on a row folds ITS group and lands on the header, so focus never
    // falls off a row that just vanished; Right reopens it.
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" })
    fireEvent.keyDown(document.activeElement!, { key: "ArrowLeft" })
    expect(document.activeElement).toBe(groupHeader("Mine"))
    expect(groupHeader("Mine").getAttribute("aria-expanded")).toBe("false")
    expect(screen.queryByText("My notes")).toBeNull()
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" })
    expect(document.activeElement).toBe(groupHeader("lookout"))
    fireEvent.keyDown(document.activeElement!, { key: "ArrowUp" })
    fireEvent.keyDown(document.activeElement!, { key: "ArrowRight" })
    expect(groupHeader("Mine").getAttribute("aria-expanded")).toBe("true")
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" })
    expect(document.activeElement).toBe(rowOf("My notes"))

    // Enter opens — the row's own contract, unchanged.
    fireEvent.keyDown(document.activeElement!, { key: "Enter" })
    expect(onSelectPage).toHaveBeenCalledWith("my-notes")
  })

  it("sorts every crew alphabetically when the server sends no reach, and says nothing about membership", () => {
    const noReach = GROUPED.map((p) => ({ ...p, reach: null }))
    renderRail({ pages: noReach })
    expect(groupHeaders().map((h) => h.textContent)).toEqual([
      "Mine1",
      "alpha1",
      "finance1",
      "lookout1",
      "Owned by others1",
    ])
  })
})

describe("PagesRail 'Shared with me' facet", () => {
  beforeEach(() => cleanup())

  it("is absent on a server that sends no reach — never a dead switch", () => {
    renderRail({ pages: PAGES })
    openPanel()
    expect(within(panel()!).queryByRole("button", { name: /shared with me/i })).toBeNull()
  })

  it("toggles `shared` without touching the other facets, and shows as a chip", () => {
    const { onFiltersChange } = renderRail({
      pages: GROUPED,
      filters: { states: ["stale"], owners: [], shared: false },
    })
    openPanel()
    fireEvent.click(within(panel()!).getByRole("button", { name: /shared with me/i }))
    expect(onFiltersChange).toHaveBeenCalledWith({ states: ["stale"], owners: [], shared: true })

    cleanup()
    const second = renderRail({ pages: GROUPED, filters: { states: [], owners: [], shared: true } })
    expect(screen.getByText("Anna's board")).toBeTruthy()
    expect(screen.queryByText("Flotila .201")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: /remove filter/i }))
    expect(second.onFiltersChange).toHaveBeenCalledWith({ states: [], owners: [], shared: false })
  })

  it("counts as one active filter and is cleared with the rest", () => {
    const { onFiltersChange } = renderRail({ pages: GROUPED, filters: { states: [], owners: [], shared: true } })
    expect(screen.getByRole("button", { name: /^Filter/ }).textContent).toContain("1")
    openPanel()
    fireEvent.click(within(panel()!).getByRole("button", { name: /^clear/i }))
    expect(onFiltersChange).toHaveBeenCalledWith(EMPTY_PAGE_FILTERS)
  })
})

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"

import { IssuesPanel, panelKeyOrder, selectionLabel, type IssuesPanelProps } from "../issues-panel"
import { NO_FILTERS } from "@/hooks/use-issue-filters"
import type { Mission, Project, SavedView } from "@/lib/types/mission"
import type { CrewSummary } from "@/lib/types/orchestration"

// The panel's filter is facets that AND together: each one toggles its own
// value and leaves the others alone, and every active one shows as a chip
// that removes just itself.

const issue = (over: Partial<Mission>): Mission =>
  ({ id: "x", title: "x", status: "TODO", crew_id: "c1", created_at: "", updated_at: "", ...over }) as Mission

const ISSUES = [
  issue({ id: "i1", status: "BACKLOG", priority: "high", delegate: { id: "a1", name: "Scout" }, owner: { id: "u1", name: "Pavel Srba" }, labels: [{ id: "l1", name: "docs", color: "#0aa", label_group: null }] }),
  issue({ id: "i2", status: "TODO", priority: "low", work_mode: "human", worker_user_id: "u2", worker_name: "Jana", owner: { id: "u2", name: "Jana" } }),
]
const CREWS: CrewSummary[] = [{ id: "c1", name: "Engineering", slug: "engineering", color: "blue", icon: "users" } as CrewSummary]
const PROJECTS = [{ id: "p1", name: "Launch v2", icon: "rocket", color: "violet", health: "at_risk", issue_count: 4, progress: 40 } as Project]
const VIEWS = [{ id: "v1", name: "Launch blockers", filters_json: "{}", sort_json: null, view_type: "list", is_default: false, shared: true, created_at: "" } as SavedView]

function setup(over: Partial<IssuesPanelProps> = {}) {
  const props: IssuesPanelProps = {
    issues: ISSUES,
    quickCounts: { mine: 1, all: 2 },
    projects: PROJECTS,
    savedViews: VIEWS,
    crews: CREWS,
    search: "",
    onSearchChange: vi.fn(),
    filters: NO_FILTERS,
    onFilters: vi.fn(),
    onClearFilters: vi.fn(),
    selection: { kind: "quick", id: "all" },
    onSelect: vi.fn(),
    onNewProject: vi.fn(),
    onSaveView: vi.fn(),
    ...over,
  }
  render(<IssuesPanel {...props} />)
  return props
}
const openFilters = () => fireEvent.click(screen.getByRole("button", { name: /^filter/i }))
const panel = () => within(screen.getByRole("group", { name: "Filter issues" }))
/** Facets fold (COM-01); a facet with a value starts open. */
const unfold = (facet: string) => {
  const header = panel().getByRole("button", { name: new RegExp(`^${facet}`) })
  if (header.getAttribute("aria-expanded") !== "true") fireEvent.click(header)
}

describe("IssuesPanel", () => {
  beforeEach(() => vi.clearAllMocks())

  it("orders the sections Views → Projects → Saved views", () => {
    setup()
    const text = document.body.textContent ?? ""
    expect(text.indexOf("Views")).toBeLessThan(text.indexOf("Projects"))
    expect(text.indexOf("Projects")).toBeLessThan(text.indexOf("Saved views"))
    expect(screen.getByText("My work")).toBeInTheDocument()
    expect(screen.getByText("All issues")).toBeInTheDocument()
    expect(screen.getByText("Launch v2")).toBeInTheDocument()
    expect(screen.getByLabelText("Health: at risk")).toBeInTheDocument()
    expect(screen.getByText("Launch blockers")).toBeInTheDocument()
  })

  it("Filter opens the facets inline in the panel (COM-01), not in a floating popover", () => {
    setup()
    const trigger = screen.getByRole("button", { name: /^filter/i })
    expect(trigger).toHaveAttribute("aria-expanded", "false")
    expect(screen.queryByRole("group", { name: "Filter issues" })).toBeNull()
    openFilters()
    const block = screen.getByRole("group", { name: "Filter issues" })
    expect(block).toHaveAttribute("data-slot", "facet-block")
    expect(trigger).toHaveAttribute("aria-expanded", "true")
    expect(trigger.getAttribute("aria-controls")).toBe(block.id)
    expect(screen.queryByRole("dialog")).toBeNull()
    // In the panel's flow, above the views.
    expect(block.compareDocumentPosition(screen.getByText("My work")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it("a facet lists at most six values and the rest under All N…", () => {
    setup()
    openFilters()
    unfold("Status")
    const status = within(panel().getByRole("group", { name: "Status" }))
    expect(status.getAllByRole("button", { pressed: false })).toHaveLength(6)
    expect(status.getByRole("button", { name: "All 7…" })).toBeInTheDocument()
  })

  it("offers the six v67 facets plus Crew, with counts from the loaded issues", () => {
    setup()
    openFilters()
    for (const f of ["Status", "Priority", "Delegate", "Owner", "Who holds the work", "Labels", "Crew"]) {
      expect(panel().getByText(f)).toBeInTheDocument()
    }
    unfold("Delegate")
    unfold("Who holds the work")
    expect(panel().getByRole("button", { name: /Scout/ })).toHaveTextContent("1")
    expect(panel().getByRole("button", { name: /A person/ })).toHaveTextContent("1")
  })

  it("a pick patches only its own facet and keeps the panel open", () => {
    const p = setup({ filters: { ...NO_FILTERS, crewId: "c1" } })
    openFilters()
    unfold("Priority")
    fireEvent.click(panel().getByRole("button", { name: /^High/ }))
    expect(p.onFilters).toHaveBeenLastCalledWith({ priority: "high" })
    unfold("Owner")
    fireEvent.click(panel().getByRole("button", { name: /Pavel Srba/ }))
    expect(p.onFilters).toHaveBeenLastCalledWith({ ownerId: "u1" })
    expect(screen.getByRole("group", { name: "Filter issues" })).toBeInTheDocument()
  })

  it("picking the active value again clears just that facet", () => {
    const p = setup({ filters: { ...NO_FILTERS, label: "docs" } })
    openFilters()
    fireEvent.click(panel().getByRole("button", { name: /^docs/ }))
    expect(p.onFilters).toHaveBeenLastCalledWith({ label: null })
  })

  it("adds a status to the multi-select without dropping the others", () => {
    const p = setup({ filters: { ...NO_FILTERS, statuses: ["TODO"] } })
    openFilters()
    unfold("Status")
    fireEvent.click(panel().getByRole("button", { name: /^In review/ }))
    expect(p.onFilters).toHaveBeenLastCalledWith({ statuses: ["TODO", "REVIEW"] })
  })

  it("shows each active facet as a chip that removes only itself", () => {
    const p = setup({ filters: { ...NO_FILTERS, crewId: "c1", holder: "person" } })
    expect(screen.getByText("Crew: Engineering")).toBeInTheDocument()
    const chip = screen.getByText("Held by a person").closest("span")!
    fireEvent.click(within(chip).getByRole("button", { name: "Remove filter" }))
    expect(p.onFilters).toHaveBeenLastCalledWith({ holder: null })
  })

  it("selects views, projects and saved views, and offers New project and Save view", () => {
    const p = setup()
    fireEvent.click(screen.getByText("My work"))
    expect(p.onSelect).toHaveBeenLastCalledWith({ kind: "quick", id: "mine" })
    fireEvent.click(screen.getByText("Launch v2"))
    expect(p.onSelect).toHaveBeenLastCalledWith({ kind: "project", id: "p1" })
    fireEvent.click(screen.getByText("Launch blockers"))
    expect(p.onSelect).toHaveBeenLastCalledWith({ kind: "saved", id: "v1" })
    fireEvent.click(screen.getByRole("button", { name: "New project" }))
    expect(p.onNewProject).toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Save current view" }))
    expect(p.onSaveView).toHaveBeenCalled()
  })
})

describe("panel keys", () => {
  it("1–9 walk Views, then saved views, then projects", () => {
    expect(panelKeyOrder(PROJECTS, VIEWS)).toEqual([
      { kind: "quick", id: "mine" },
      { kind: "quick", id: "all" },
      { kind: "saved", id: "v1" },
      { kind: "project", id: "p1" },
    ])
  })

  it("names the selection for the top bar", () => {
    expect(selectionLabel({ kind: "quick", id: "mine" }, PROJECTS, VIEWS)).toBe("My work")
    expect(selectionLabel({ kind: "saved", id: "v1" }, PROJECTS, VIEWS)).toBe("Launch blockers")
    expect(selectionLabel({ kind: "project", id: "p1" }, PROJECTS, VIEWS)).toBe("Launch v2")
  })
})

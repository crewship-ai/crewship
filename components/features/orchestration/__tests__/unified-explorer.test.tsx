import { describe, it, expect, vi, beforeEach, type Mock } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"

import { UnifiedExplorer } from "../unified-explorer"
import type { Mission } from "@/lib/types/mission"
import type { CrewSummary } from "@/lib/types/orchestration"

// The explorer's filter dropdown used to be three switches wearing a filter's
// clothes: every pick cleared the others, so "Engineering crew AND High
// priority" could not be expressed at all. These tests pin the combining
// behaviour, the per-facet clear, and the fact that status — which the reader
// goes looking for here first — is one of the facets.

function issue(over: Partial<Mission>): Mission {
  return {
    id: "i-default",
    title: "default",
    workspace_id: "ws-1",
    crew_id: "c1",
    lead_agent_id: "",
    trace_id: "",
    status: "TODO",
    ...over,
  } as Mission
}

const CREWS: CrewSummary[] = [
  { id: "c1", name: "Engineering", slug: "engineering", color: "blue", icon: "users" },
  { id: "c2", name: "Design", slug: "design", color: "purple", icon: "users" },
]

// One issue per interesting combination, so an over-eager filter shows up as a
// missing row rather than as an identical list.
const ISSUES: Mission[] = [
  issue({ id: "i1", identifier: "ENG-1", title: "Engineering high", crew_id: "c1", assignee_id: "a1", assignee_name: "Robin", priority: "high", status: "BACKLOG" }),
  issue({ id: "i2", identifier: "ENG-2", title: "Engineering low", crew_id: "c1", assignee_id: "a2", assignee_name: "Sam", priority: "low", status: "TODO" }),
  issue({ id: "i3", identifier: "DES-1", title: "Design high", crew_id: "c2", assignee_id: "a2", assignee_name: "Sam", priority: "high", status: "BACKLOG" }),
]

type ExplorerProps = React.ComponentProps<typeof UnifiedExplorer>

// Mocks carry the component's own handler signatures so a renamed or
// retyped callback fails here, not only at runtime.
interface Handlers {
  onCrewFilter: Mock<ExplorerProps["onCrewFilter"]>
  onAgentFilter: Mock<ExplorerProps["onAgentFilter"]>
  onPriorityFilter: Mock<NonNullable<ExplorerProps["onPriorityFilter"]>>
  onStatusFilter: Mock<NonNullable<ExplorerProps["onStatusFilter"]>>
}

function setup(props: Partial<ExplorerProps> = {}) {
  const handlers: Handlers = {
    onCrewFilter: vi.fn(),
    onAgentFilter: vi.fn(),
    onPriorityFilter: vi.fn(),
    onStatusFilter: vi.fn(),
  }
  const view = render(
    <UnifiedExplorer
      issues={ISSUES}
      projects={[]}
      search=""
      onSearchChange={() => {}}
      selectedIssue={null}
      selectedProjectId={null}
      onProjectSelect={() => {}}
      onIssueSelect={() => {}}
      crews={CREWS}
      missions={[]}
      onTaskSelect={() => {}}
      filterCrewId={null}
      filterAgentId={null}
      filterPriority={null}
      filterStatuses={[]}
      {...handlers}
      {...props}
    />,
  )
  return { ...view, handlers }
}

function openFilters() {
  fireEvent.click(screen.getByRole("button", { name: /filter/i }))
}

/**
 * Queries scoped to the dropdown. The issue list underneath carries the same
 * words — a row titled "Design high", an avatar labelled "Robin" — so an
 * unscoped query picks up whichever the filter did not narrow away.
 */
function panel() {
  return within(screen.getByRole("group", { name: "Filter issues" }))
}

describe("UnifiedExplorer — filters combine", () => {
  beforeEach(() => vi.clearAllMocks())

  it("picking a priority does not wipe the crew that is already picked", () => {
    // The defect: `onPriorityFilter(p)` also called `onCrewFilter(null)` and
    // `onAgentFilter(null)`, so the second pick always erased the first.
    const { handlers } = setup({ filterCrewId: "c1" })
    openFilters()
    fireEvent.click(panel().getByRole("button", { name: /^High$/ }))

    expect(handlers.onPriorityFilter).toHaveBeenCalledWith("high")
    expect(handlers.onCrewFilter).not.toHaveBeenCalled()
    expect(handlers.onAgentFilter).not.toHaveBeenCalled()
  })

  it("picking a crew does not wipe the agent or the priority", () => {
    const { handlers } = setup({ filterAgentId: "a2", filterPriority: "high" })
    openFilters()
    fireEvent.click(panel().getByRole("button", { name: /Design/ }))

    expect(handlers.onCrewFilter).toHaveBeenCalledWith("c2")
    expect(handlers.onAgentFilter).not.toHaveBeenCalled()
    expect(handlers.onPriorityFilter).not.toHaveBeenCalled()
  })

  it("picking an agent does not wipe the crew or the priority", () => {
    const { handlers } = setup({ filterCrewId: "c1", filterPriority: "high" })
    openFilters()
    fireEvent.click(panel().getByRole("button", { name: /Robin/ }))

    expect(handlers.onAgentFilter).toHaveBeenCalledWith("a1")
    expect(handlers.onCrewFilter).not.toHaveBeenCalled()
    expect(handlers.onPriorityFilter).not.toHaveBeenCalled()
  })

  it("stays open after a pick, so a second facet can be added", () => {
    setup()
    openFilters()
    fireEvent.click(panel().getByRole("button", { name: /Design/ }))
    // Closing on select is what made combining impossible by hand: two
    // facets meant two trips through the trigger, and the second pick had
    // already dropped the first.
    expect(panel().getByRole("button", { name: /^High$/ })).toBeInTheDocument()
  })

  it("clicking the selected value again clears just that facet", () => {
    const { handlers } = setup({ filterCrewId: "c2", filterPriority: "high" })
    openFilters()
    fireEvent.click(panel().getByRole("button", { name: /Design/ }))

    expect(handlers.onCrewFilter).toHaveBeenCalledWith(null)
    expect(handlers.onPriorityFilter).not.toHaveBeenCalled()
  })

  it("the per-facet reset row clears only its own facet", () => {
    const { handlers } = setup({ filterCrewId: "c1", filterAgentId: "a1", filterPriority: "high" })
    openFilters()
    fireEvent.click(panel().getByRole("button", { name: /All crews/ }))

    expect(handlers.onCrewFilter).toHaveBeenCalledWith(null)
    expect(handlers.onAgentFilter).not.toHaveBeenCalled()
    expect(handlers.onPriorityFilter).not.toHaveBeenCalled()
  })

  it("Clear all drops every facet at once", () => {
    const { handlers } = setup({
      filterCrewId: "c1",
      filterAgentId: "a1",
      filterPriority: "high",
      filterStatuses: ["BACKLOG"],
    })
    openFilters()
    fireEvent.click(panel().getByRole("button", { name: /Clear all/ }))

    expect(handlers.onCrewFilter).toHaveBeenCalledWith(null)
    expect(handlers.onAgentFilter).toHaveBeenCalledWith(null)
    expect(handlers.onPriorityFilter).toHaveBeenCalledWith(null)
    expect(handlers.onStatusFilter).toHaveBeenCalledWith([])
  })
})

describe("UnifiedExplorer — status is a facet here too", () => {
  beforeEach(() => vi.clearAllMocks())

  it("offers status in the dropdown", () => {
    // The owner went looking for Backlog here and found crews, agents and
    // priorities only — status lived exclusively in the chip row above the
    // board, which is not even rendered when an issue detail is open.
    setup()
    openFilters()
    expect(panel().getByRole("button", { name: /^Backlog$/ })).toBeInTheDocument()
  })

  it("toggles a status into and out of the shared multi-select", () => {
    const { handlers, rerender } = setup()
    openFilters()
    fireEvent.click(panel().getByRole("button", { name: /^Backlog$/ }))
    expect(handlers.onStatusFilter).toHaveBeenCalledWith(["BACKLOG"])

    rerender(
      <UnifiedExplorer
        issues={ISSUES}
        projects={[]}
        search=""
        onSearchChange={() => {}}
        selectedIssue={null}
        selectedProjectId={null}
        onProjectSelect={() => {}}
        onIssueSelect={() => {}}
        crews={CREWS}
        missions={[]}
        onTaskSelect={() => {}}
        filterCrewId={null}
        filterAgentId={null}
        filterPriority={null}
        filterStatuses={["BACKLOG"]}
        onCrewFilter={handlers.onCrewFilter}
        onAgentFilter={handlers.onAgentFilter}
        onPriorityFilter={handlers.onPriorityFilter}
        onStatusFilter={handlers.onStatusFilter}
      />,
    )
    openFilters()
    fireEvent.click(panel().getByRole("button", { name: /^Backlog$/ }))
    expect(handlers.onStatusFilter).toHaveBeenLastCalledWith([])
  })
})

describe("UnifiedExplorer — the badge and the list agree with the filters", () => {
  beforeEach(() => vi.clearAllMocks())

  it("counts every active facet in the trigger badge", () => {
    setup({
      filterCrewId: "c1",
      filterAgentId: "a1",
      filterPriority: "high",
      filterStatuses: ["BACKLOG", "TODO"],
    })
    // crew + agent + priority + two statuses.
    expect(screen.getByRole("button", { name: /filter/i })).toHaveTextContent("5")
  })

  it("intersects crew, priority and status in the rendered list", () => {
    // Before this, the sidebar list applied crew and agent only: a priority
    // or status picked in the dropdown changed the board and left the list
    // beside it showing rows the filter had excluded.
    setup({ filterCrewId: "c1", filterPriority: "high", filterStatuses: ["BACKLOG"] })
    expect(screen.getByText("Engineering high")).toBeInTheDocument()
    expect(screen.queryByText("Engineering low")).toBeNull()
    expect(screen.queryByText("Design high")).toBeNull()
  })
})

describe("UnifiedExplorer — generated issue families fold", () => {
  const family = [
    issue({ id: "f1", identifier: "INF-1", title: "infra-prehled/verdikt stopped reporting" }),
    issue({ id: "f2", identifier: "INF-2", title: "infra-prehled/verdikt: disk > 85" }),
    issue({ id: "f3", identifier: "INF-3", title: "infra-prehled/verdikt stopped reporting" }),
  ]

  it("shows a run of generated issues as one row that opens to the rest of each title", () => {
    setup({ issues: [...ISSUES, ...family] })
    const row = screen.getByRole("button", { name: "infra-prehled/verdikt: 3 issues" })
    expect(row).toHaveAttribute("aria-expanded", "false")
    expect(screen.queryByText("INF-2")).not.toBeInTheDocument()
    fireEvent.click(row)
    expect(row).toHaveAttribute("aria-expanded", "true")
    expect(screen.getByText("INF-2")).toBeInTheDocument()
    expect(screen.getByText("disk > 85")).toBeInTheDocument()
  })

  it("never hides the selected issue inside a closed family", () => {
    setup({ issues: family, selectedIssue: family[1] })
    expect(screen.getByRole("button", { name: "infra-prehled/verdikt: 3 issues" })).toHaveAttribute("aria-expanded", "true")
    expect(screen.getByText("INF-2")).toBeInTheDocument()
  })

  it("lists every hit flat while searching", () => {
    setup({ issues: family, search: "verdikt" })
    expect(screen.queryByRole("button", { name: /3 issues/ })).not.toBeInTheDocument()
    expect(screen.getByText("INF-1")).toBeInTheDocument()
  })
})

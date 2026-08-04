import { describe, it, expect, vi, beforeEach } from "vitest"
import { useState } from "react"
import { render, screen, fireEvent, cleanup, within } from "@testing-library/react"

import { IssuesStatusChips } from "../issues-status-chips"
import { useFilteredIssues } from "@/hooks/use-filtered-issues"
import type { Mission, MissionStatus } from "@/lib/types/mission"

function issue(overrides: Partial<Mission>): Mission {
  return {
    id: "i-default",
    title: "default",
    workspace_id: "ws-1",
    crew_id: "c1",
    lead_agent_id: "agent-1",
    trace_id: "trace-1",
    status: "TODO",
    ...overrides,
  } as Mission
}

// 6 backlog, 4 todo, 3 in-progress, 2 done — spread over two crews so the
// non-status filters have something to bite on.
const issues: Mission[] = [
  ...Array.from({ length: 6 }, (_, n) =>
    issue({ id: `b${n}`, status: "BACKLOG" as MissionStatus, crew_id: n < 4 ? "c1" : "c2" }),
  ),
  ...Array.from({ length: 4 }, (_, n) =>
    issue({ id: `t${n}`, status: "TODO" as MissionStatus, crew_id: n < 3 ? "c1" : "c2" }),
  ),
  ...Array.from({ length: 3 }, (_, n) =>
    issue({ id: `p${n}`, status: "IN_PROGRESS" as MissionStatus, crew_id: "c1" }),
  ),
  ...Array.from({ length: 2 }, (_, n) =>
    issue({ id: `d${n}`, status: "COMPLETED" as MissionStatus, crew_id: "c2" }),
  ),
]

/**
 * The composition /issues actually renders: one `useFilteredIssues` call
 * driving both the board and the chip row. The chips must count the set
 * filtered by everything *except* status — feeding them the fully filtered
 * list makes every unselected chip read 0, and a chip that reads 0 is not
 * rendered at all (issues-status-chips.tsx), so the second status can never
 * be picked.
 */
function Page({
  initialStatuses = [],
  filterCrewId = null,
}: {
  initialStatuses?: MissionStatus[]
  filterCrewId?: string | null
}) {
  const [filterStatuses, setFilterStatuses] = useState<MissionStatus[]>(initialStatuses)
  const { visible, statusFacet } = useFilteredIssues({
    issues,
    search: "",
    selectedProjectId: null,
    filterProjectId: null,
    filterCrewId,
    filterAgentId: null,
    filterStatuses,
    filterPriority: null,
  })
  return (
    <>
      <IssuesStatusChips
        issues={statusFacet}
        selected={filterStatuses}
        onToggle={(s) =>
          setFilterStatuses((prev) => (prev.includes(s) ? prev.filter((x) => x !== s) : [...prev, s]))
        }
        onClear={() => setFilterStatuses([])}
      />
      <div data-testid="visible-count">{visible.length}</div>
    </>
  )
}

/** The count badge a chip carries, e.g. "4" on the Todo chip. */
function chipCount(label: string): string {
  const chip = screen.getByRole("button", { name: new RegExp(`^${label}`) })
  return chip.textContent?.replace(label, "").trim() ?? ""
}

describe("IssuesStatusChips — multi-select", () => {
  beforeEach(cleanup)

  it("counts every status when nothing is selected", () => {
    render(<Page />)
    expect(chipCount("Backlog")).toBe("6")
    expect(chipCount("Todo")).toBe("4")
    expect(chipCount("In Progress")).toBe("3")
    expect(chipCount("Done")).toBe("2")
    expect(within(screen.getByRole("button", { name: /^All/ })).queryByText("15")).toBeTruthy()
  })

  // The defect: with the chips fed the already-status-filtered list, picking
  // Backlog drops every other chip's count to 0, the zero-count chips stop
  // rendering, and a second status can never be added to the filter.
  it("keeps the other chips — at their true counts — after one status is picked", () => {
    render(<Page />)

    fireEvent.click(screen.getByRole("button", { name: /^Backlog/ }))

    // The list itself narrowed…
    expect(screen.getByTestId("visible-count").textContent).toBe("6")
    // …but the chips still describe the whole (non-status-filtered) set.
    expect(chipCount("Todo")).toBe("4")
    expect(chipCount("In Progress")).toBe("3")
    expect(chipCount("Done")).toBe("2")
    expect(screen.getByRole("button", { name: /^Backlog/ }).getAttribute("aria-pressed")).toBe("true")
  })

  it("adds a second status to the filter (OR-composed)", () => {
    render(<Page />)

    fireEvent.click(screen.getByRole("button", { name: /^Backlog/ }))
    fireEvent.click(screen.getByRole("button", { name: /^Todo/ }))

    expect(screen.getByTestId("visible-count").textContent).toBe("10")
    expect(screen.getByRole("button", { name: /^Todo/ }).getAttribute("aria-pressed")).toBe("true")
  })

  it("the All pill shows the real total, not the filtered one", () => {
    render(<Page initialStatuses={["BACKLOG"]} />)
    expect(within(screen.getByRole("button", { name: /^All/ })).queryByText("15")).toBeTruthy()
    expect(screen.getByRole("button", { name: /^All/ }).getAttribute("aria-pressed")).toBe("false")
  })

  it("still honours the non-status filters in its counts", () => {
    // Crew c1 owns 4 backlog + 3 todo + 3 in-progress = 10, and no Done rows.
    render(<Page filterCrewId="c1" initialStatuses={["BACKLOG"]} />)

    expect(chipCount("Backlog")).toBe("4")
    expect(chipCount("Todo")).toBe("3")
    expect(chipCount("In Progress")).toBe("3")
    expect(within(screen.getByRole("button", { name: /^All/ })).queryByText("10")).toBeTruthy()
    // Nothing in c1 is Done, and Done is not selected — the chip stays hidden.
    expect(screen.queryByRole("button", { name: /^Done/ })).toBeNull()
  })

  it("clearing restores the All pill", () => {
    const onClear = vi.fn()
    render(
      <IssuesStatusChips issues={issues} selected={["BACKLOG"]} onToggle={vi.fn()} onClear={onClear} />,
    )
    fireEvent.click(screen.getByRole("button", { name: /^All/ }))
    expect(onClear).toHaveBeenCalledOnce()
  })
})

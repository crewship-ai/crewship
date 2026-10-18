// PAR-041 / PAR-042 / PAR-043 (and the PAR-153 numerals): the Issues panel
// project bar, the List | Board toggle and the board column headers follow v67.

import { describe, it, expect, vi } from "vitest"
import { render, screen, within } from "@testing-library/react"

import { IssuesBoardView } from "../issues-board-view"
import { IssuesViewToggle } from "../issues-view-toggle"
import { IssuesPanel, type IssuesPanelProps } from "@/components/features/orchestration/issues-panel"
import { NO_FILTERS } from "@/hooks/use-issue-filters"
import { SecondaryPanelContext } from "@/components/layout/secondary-panel"
import type { Mission, Project } from "@/lib/types/mission"

const issue = (i: number): Mission =>
  ({ id: `m${i}`, identifier: `ENG-${i}`, title: `Issue ${i}`, status: "BACKLOG", priority: "none", created_at: "", updated_at: "" }) as unknown as Mission

describe("Issues v67 details", () => {
  it("PAR-043: board column counts and the fold link are sans, not mono", () => {
    render(<IssuesBoardView issues={Array.from({ length: 10 }, (_, i) => issue(i))} onIssueClick={() => {}} />)
    const backlog = screen.getByRole("region", { name: "Backlog" })
    const count = within(backlog).getByText("10")
    expect(count.className).not.toContain("font-mono")
    expect(count.className).toContain("tabular-nums")
    expect(screen.getByTestId("board-fold-BACKLOG").className).not.toContain("font-mono")
  })

  it("PAR-041: the project row carries a progress bar with a track you can see", () => {
    const projects = [{ id: "p1", name: "Launch v2", icon: "rocket", color: "violet", health: "on_track", issue_count: 6, progress: 40 } as Project]
    const props: IssuesPanelProps = {
      issues: [], quickCounts: { mine: 0, all: 0 }, projects, savedViews: [], crews: [], search: "", onSearchChange: vi.fn(),
      filters: NO_FILTERS, onFilters: vi.fn(), onClearFilters: vi.fn(), selection: null, onSelect: vi.fn(), onNewProject: vi.fn(),
    }
    const { container } = render(<IssuesPanel {...props} />)
    const track = container.querySelector(".group\\/proj [aria-hidden].h-\\[3px\\]") as HTMLElement
    expect(track).not.toBeNull()
    // The track is a foreground tint: the old surface-subtle track equals the panel ground.
    expect(track.className).toContain("bg-foreground/[0.08]")
    expect((track.firstElementChild as HTMLElement).style.width).toBe("40%")
    // Counts in the panel are sans tabular too.
    expect(screen.getByText("6").className).not.toContain("font-mono")
  })

  it("PAR-042: List | Board is one 28 px segmented control (inner = control height - 6 px)", () => {
    render(<IssuesViewToggle value="list" onChange={vi.fn()} />)
    const group = screen.getByRole("radiogroup", { name: "View" })
    expect(group).toHaveAttribute("data-slot", "segmented")
    // The segments read --control-xs; this group sets it to 6 px under the 28 px control height
    // (2 px padding + 1 px border on each side), so the whole control is as tall as the buttons beside it.
    expect(group.className).toContain("[--control-xs:calc(var(--control-sm)-6px)]")
    // A coarse pointer keeps the 44 px target.
    for (const radio of within(group).getAllByRole("radio")) expect(radio.className).toContain("coarse:h-[44px]")
    expect(within(group).getByRole("radio", { name: "List" })).toHaveAttribute("aria-checked", "true")
  })

  it("PAR-040: inside the secondary panel the Filter button shows its word next to the funnel", () => {
    const props: IssuesPanelProps = {
      issues: [], quickCounts: { mine: 0, all: 0 }, projects: [], savedViews: [], crews: [], search: "", onSearchChange: vi.fn(),
      filters: NO_FILTERS, onFilters: vi.fn(), onClearFilters: vi.fn(), selection: null, onSelect: vi.fn(), onNewProject: vi.fn(),
    }
    render(
      <SecondaryPanelContext.Provider value={{ toggle: null, claim: () => () => {} }}>
        <IssuesPanel {...props} />
      </SecondaryPanelContext.Provider>,
    )
    const button = screen.getByRole("button", { name: "Filter issues" })
    expect(button).toHaveTextContent("Filter")
    expect(button.querySelector(".sr-only")).toBeNull()
  })
})

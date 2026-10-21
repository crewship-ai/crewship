import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"

import { IssueRow } from "../issue-row"
import { DEFAULT_ROW_FIELDS } from "../issues-view-model"
import type { Mission } from "@/lib/types/mission"

function issue(over: Partial<Mission> = {}): Mission {
  return {
    id: "m1",
    identifier: "ENG-7",
    title: "Ship the pricing page",
    status: "IN_PROGRESS",
    priority: "high",
    created_at: "2026-10-01T10:00:00Z",
    updated_at: new Date(Date.now() - 3 * 3600_000).toISOString(),
    sub_issues_count: 4,
    labels: [{ id: "l1", name: "docs", color: "#14b8a6", label_group: null }],
    due_date: new Date().toISOString(),
    owner: { id: "u1", name: "Pavel Srba" },
    delegate: { id: "a1", name: "Scout" },
    ...over,
  } as Mission
}

describe("IssueRow", () => {
  it("renders display-option fields and hides disabled ones", () => {
    const { rerender } = render(<IssueRow issue={issue()} onOpen={() => {}} />)
    expect(screen.getByText("ENG-7")).toBeInTheDocument()
    expect(screen.getByText("docs")).toBeInTheDocument()
    expect(screen.getByTitle("4 sub-issues")).toBeInTheDocument()
    expect(screen.getByText("Today")).toBeInTheDocument()
    expect(screen.getByText("3h")).toBeInTheDocument()
    expect(screen.getByTitle("Owner · Pavel Srba")).toBeInTheDocument()

    rerender(
      <IssueRow
        issue={issue()}
        onOpen={() => {}}
        fields={{ ...DEFAULT_ROW_FIELDS, id: false, labels: false, sub: false, due: false, owner: false, updated: false }}
      />,
    )
    expect(screen.queryByText("ENG-7")).toBeNull()
    expect(screen.queryByText("docs")).toBeNull()
    expect(screen.queryByTitle("4 sub-issues")).toBeNull()
    expect(screen.queryByText("Today")).toBeNull()
    expect(screen.queryByText("3h")).toBeNull()
    expect(screen.queryByTitle("Owner · Pavel Srba")).toBeNull()
    // The title and who does the work never switch off.
    expect(screen.getByText("Ship the pricing page")).toBeInTheDocument()
    expect(screen.getByTitle("Delegate · Scout")).toBeInTheDocument()
  })

  it("marks an open issue that is due today as late", () => {
    render(<IssueRow issue={issue()} onOpen={() => {}} />)
    expect(screen.getByText("Today").className).toContain("text-destructive")
  })

  it("says nobody is delegated instead of leaving a gap", () => {
    render(<IssueRow issue={issue({ delegate: null, assignee_id: null })} onOpen={() => {}} />)
    expect(screen.getByLabelText("No delegate")).toBeInTheDocument()
  })

  it("opens on click and selects through its own checkbox", () => {
    const onOpen = vi.fn()
    const onChecked = vi.fn()
    render(<IssueRow issue={issue()} onOpen={onOpen} onCheckedChange={onChecked} />)
    fireEvent.click(screen.getByRole("checkbox", { name: "Select ENG-7" }))
    expect(onChecked).toHaveBeenCalledWith(true)
    expect(onOpen).not.toHaveBeenCalled()
    fireEvent.click(screen.getByText("Ship the pricing page"))
    expect(onOpen).toHaveBeenCalledTimes(1)
  })

  it("names the status for a screen reader, not only as a glyph", () => {
    render(<IssueRow issue={issue({ status: "REVIEW" })} onOpen={() => {}} />)
    expect(screen.getByText("In review")).toHaveClass("sr-only")
  })
})

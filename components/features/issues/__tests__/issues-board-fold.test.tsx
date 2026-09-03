import { describe, it, expect } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"

import { BOARD_COLUMN_CAP, IssuesBoardView, foldColumns } from "@/components/features/issues/issues-board-view"
import type { Mission, MissionStatus } from "@/lib/types/mission"

// The board is designed for 1 and for 1 000 (README §4): each column shows
// six cards and folds the rest behind "N more · Show all". Five fixed
// 280px columns used to scroll sideways at 1440 with Done off screen, and
// a backlog of 982 was 982 cards.

function issue(i: number, status: MissionStatus): Mission {
  return {
    id: `m_${status}_${i}`,
    identifier: `ENG-${i}`,
    title: `Issue ${i}`,
    status,
    priority: "none",
    created_at: "2026-09-03T10:00:00Z",
    updated_at: "2026-09-03T10:00:00Z",
  } as unknown as Mission
}

describe("foldColumns", () => {
  it("caps each column and counts what is hidden", () => {
    const issues = [...Array.from({ length: 9 }, (_, i) => issue(i, "BACKLOG")), issue(99, "TODO")]
    const cols = foldColumns(issues, ["BACKLOG", "TODO", "IN_PROGRESS"], BOARD_COLUMN_CAP, new Set())
    expect(cols.map((c) => [c.status, c.all.length, c.shown.length, c.hidden])).toEqual([
      ["BACKLOG", 9, 6, 3],
      ["TODO", 1, 1, 0],
      ["IN_PROGRESS", 0, 0, 0],
    ])
  })

  it("shows the whole column once it is opened", () => {
    const issues = Array.from({ length: 9 }, (_, i) => issue(i, "BACKLOG"))
    const [col] = foldColumns(issues, ["BACKLOG"], BOARD_COLUMN_CAP, new Set(["BACKLOG"]))
    expect(col.shown.length).toBe(9)
    expect(col.hidden).toBe(0)
  })
})

describe("IssuesBoardView", () => {
  it("folds a long column and opens it on demand", () => {
    const issues = Array.from({ length: 10 }, (_, i) => issue(i, "BACKLOG"))
    render(<IssuesBoardView issues={issues} onIssueClick={() => {}} />)
    const backlog = screen.getByRole("region", { name: "Backlog" })
    expect(within(backlog).getAllByRole("button", { name: /^Issue / })).toHaveLength(BOARD_COLUMN_CAP)
    const fold = screen.getByTestId("board-fold-BACKLOG")
    expect(fold).toHaveTextContent("4 more · Show all")
    fireEvent.click(fold)
    expect(within(backlog).getAllByRole("button", { name: /^Issue / })).toHaveLength(10)
    expect(fold).toHaveTextContent("Show fewer")
  })

  it("does not scroll sideways: the columns are a grid that stacks below md", () => {
    const { container } = render(<IssuesBoardView issues={[issue(1, "TODO")]} onIssueClick={() => {}} />)
    const grid = container.querySelector(".grid-cols-1.lg\\:grid-cols-5")
    expect(grid).not.toBeNull()
    expect(container.querySelector(".overflow-x-auto")).toBeNull()
    expect(container.querySelector(".w-\\[280px\\]")).toBeNull()
  })

  it("names the CLI action when there are no issues at all", () => {
    render(<IssuesBoardView issues={[]} onIssueClick={() => {}} onCreateClick={() => {}} />)
    expect(screen.getByText(/crewship issue create/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /new issue/i })).toBeInTheDocument()
  })
})

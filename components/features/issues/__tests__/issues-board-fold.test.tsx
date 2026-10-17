import { describe, it, expect } from "vitest"
import { render, screen, fireEvent, within, waitFor } from "@testing-library/react"
import { vi, beforeEach } from "vitest"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))
const toastError = vi.fn()
vi.mock("@/lib/toast", () => ({ toast: { error: (...a: unknown[]) => toastError(...a), success: vi.fn() } }))

import { BOARD_COLUMN_CAP, IssuesBoardView, boardColumnTemplate, foldColumns } from "@/components/features/issues/issues-board-view"
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
    const grid = container.querySelector('[data-slot="issues-board-grid"]')
    expect(grid).not.toBeNull()
    expect(grid?.className).toContain("grid-cols-1")
    expect(container.querySelector(".overflow-x-auto")).toBeNull()
    expect(container.querySelector(".w-\\[280px\\]")).toBeNull()
  })

  it("shows the six columns, an empty one as a drop target", () => {
    render(<IssuesBoardView issues={[issue(1, "TODO")]} onIssueClick={() => {}} />)
    expect(screen.getAllByRole("region").map((r) => r.getAttribute("aria-label"))).toEqual(["Backlog", "Todo", "In progress", "In review", "Done", "Failed"])
    const done = screen.getByRole("region", { name: "Done" })
    expect(done).toHaveAttribute("data-empty", "true")
    expect(within(done).getByText("Drop issues here")).toBeInTheDocument()
    expect(screen.getByRole("region", { name: "Todo" })).toHaveAttribute("data-empty", "false")
  })

  it("puts DONE issues (and the retired COMPLETED) in Done, Planning in Todo", () => {
    render(<IssuesBoardView issues={[issue(1, "DONE"), issue(2, "COMPLETED"), issue(3, "PLANNING")]} onIssueClick={() => {}} />)
    expect(within(screen.getByRole("region", { name: "Done" })).getAllByRole("button", { name: /^Issue / })).toHaveLength(2)
    expect(within(screen.getByRole("region", { name: "Todo" })).getAllByRole("button", { name: /^Issue / })).toHaveLength(1)
  })

  it("a column's + opens New issue with its status", () => {
    const create = vi.fn()
    render(<IssuesBoardView issues={[]} onIssueClick={() => {}} onCreateClick={create} />)
    fireEvent.click(screen.getByRole("button", { name: "New issue in In progress" }))
    expect(create).toHaveBeenCalledWith("IN_PROGRESS")
  })

  // POST /crews/{c}/issues takes BACKLOG or one step from it; a + on In
  // review, Done or Failed would make a Backlog issue in disguise.
  it("offers + only on columns an issue can be created in", () => {
    render(<IssuesBoardView issues={[]} onIssueClick={() => {}} onCreateClick={vi.fn()} />)
    for (const name of ["Backlog", "Todo", "In progress"]) expect(screen.getByRole("button", { name: `New issue in ${name}` })).toBeInTheDocument()
    for (const name of ["In review", "Done", "Failed"]) expect(screen.queryByRole("button", { name: `New issue in ${name}` })).toBeNull()
  })
})

describe("IssuesBoardView drag and drop", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    toastError.mockReset()
  })
  const card = { ...issue(1, "TODO"), crew_id: "c1" } as Mission
  const drop = (status: string) => {
    const data = new Map<string, string>()
    const dataTransfer = { setData: (k: string, v: string) => data.set(k, v), getData: (k: string) => data.get(k) ?? "", effectAllowed: "" }
    fireEvent.dragStart(screen.getByRole("button", { name: /^Issue ENG-1/ }), { dataTransfer })
    fireEvent.drop(screen.getByRole("region", { name: status }), { dataTransfer })
  }

  it("a drop PATCHes the issue's status", async () => {
    apiFetch.mockResolvedValue(new Response("{}", { status: 200 }))
    const moved = vi.fn()
    render(<IssuesBoardView issues={[card]} onIssueClick={() => {}} workspaceId="ws" onMoved={moved} />)
    drop("In progress")
    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(
        "/api/v1/crews/c1/issues/ENG-1?workspace_id=ws",
        expect.objectContaining({ method: "PATCH", body: JSON.stringify({ status: "IN_PROGRESS" }) }),
      ),
    )
    await waitFor(() => expect(moved).toHaveBeenCalled())
  })

  it("a drop on Done asks first, as the issue page does", async () => {
    apiFetch.mockResolvedValue(new Response("{}", { status: 200 }))
    render(<IssuesBoardView issues={[card]} onIssueClick={() => {}} workspaceId="ws" />)
    drop("Done")
    expect(screen.getByRole("alertdialog")).toHaveTextContent("Move ENG-1 to Done?")
    expect(apiFetch).not.toHaveBeenCalled()
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Move to Done" }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalled())
  })

  it("a refused move leaves the card and says why", async () => {
    apiFetch.mockResolvedValue(new Response(JSON.stringify({ detail: "2 sub-issues are still open" }), { status: 409 }))
    const moved = vi.fn()
    render(<IssuesBoardView issues={[card]} onIssueClick={() => {}} workspaceId="ws" onMoved={moved} />)
    drop("In progress")
    await waitFor(() => expect(toastError).toHaveBeenCalledWith("ENG-1 stays in Todo: 2 sub-issues are still open"))
    expect(moved).not.toHaveBeenCalled()
    expect(within(screen.getByRole("region", { name: "Todo" })).getByRole("button", { name: /^Issue ENG-1/ })).toBeInTheDocument()
  })
})

// Drag and drop has no keyboard path, so each card carries a "Move to"
// menu that goes through the same PATCH and the same confirmation.
describe("IssuesBoardView Move to (keyboard)", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    toastError.mockReset()
  })
  const card = { ...issue(1, "TODO"), crew_id: "c1" } as Mission
  const openMenu = () => {
    const trigger = screen.getByRole("button", { name: "Move ENG-1 to…" })
    trigger.focus()
    fireEvent.keyDown(trigger, { key: "Enter" })
    return screen.getByRole("menu")
  }

  it("moves a card from the keyboard with the same PATCH a drop sends", async () => {
    apiFetch.mockResolvedValue(new Response("{}", { status: 200 }))
    const moved = vi.fn()
    render(<IssuesBoardView issues={[card]} onIssueClick={() => {}} workspaceId="ws" onMoved={moved} />)
    const menu = openMenu()
    // The column it is already in is not offered.
    expect(within(menu).queryByRole("menuitem", { name: "Todo" })).toBeNull()
    fireEvent.click(within(menu).getByRole("menuitem", { name: "In progress" }))
    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(
        "/api/v1/crews/c1/issues/ENG-1?workspace_id=ws",
        expect.objectContaining({ method: "PATCH", body: JSON.stringify({ status: "IN_PROGRESS" }) }),
      ),
    )
    await waitFor(() => expect(moved).toHaveBeenCalled())
  })

  it("asks before Done, like a drop", () => {
    render(<IssuesBoardView issues={[card]} onIssueClick={() => {}} workspaceId="ws" />)
    fireEvent.click(within(openMenu()).getByRole("menuitem", { name: "Done" }))
    expect(screen.getByRole("alertdialog")).toHaveTextContent("Move ENG-1 to Done?")
    expect(apiFetch).not.toHaveBeenCalled()
  })

  it("has no Move to menu when the board cannot move cards", () => {
    render(<IssuesBoardView issues={[card]} onIssueClick={() => {}} />)
    expect(screen.queryByRole("button", { name: "Move ENG-1 to…" })).toBeNull()
  })
})

// Empty columns narrow to a rail on wide screens, so the populated ones get
// the width: 33 backlog cards next to three empty 1fr columns was a board
// that was mostly "No issues".
describe("boardColumnTemplate", () => {
  it.each([
    [[3, 1, 0, 0, 0, 0], "minmax(0,1fr) minmax(0,1fr) minmax(0,9rem) minmax(0,9rem) minmax(0,9rem) minmax(0,9rem)"],
    [[1, 1, 1, 1, 1], "repeat(5,minmax(0,1fr))"],
    [[0, 0, 0, 0, 2], "minmax(0,9rem) minmax(0,9rem) minmax(0,9rem) minmax(0,9rem) minmax(0,1fr)"],
    [[0, 0, 0, 0, 0], "repeat(5,minmax(0,1fr))"],
  ])("counts %j → %s", (counts, want) => {
    expect(boardColumnTemplate(counts)).toBe(want)
  })
})

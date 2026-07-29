import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"
import { CircleDot } from "lucide-react"

import { DetailCell, type DetailCellItem } from "@/components/features/crews/canvas/detail-cell"

const items: DetailCellItem[] = [
  { id: "1", icon: CircleDot, title: "OPS-6 Map container ceiling", subtitle: "in progress", meta: "2 h", tag: "run" },
  { id: "2", icon: CircleDot, title: "OPS-1 Draft a runbook", subtitle: "in review", meta: "Riley", tag: "run" },
  { id: "3", icon: CircleDot, title: "OPS-2 Harborlight brief", subtitle: "todo", meta: "—", tag: "todo" },
  { id: "4", icon: CircleDot, title: "OPS-9 Audit base image", subtitle: "backlog", meta: "—", tag: "todo" },
]

const filters = [
  { id: "all", label: "All" },
  { id: "run", label: "Running" },
  { id: "todo", label: "Todo" },
]

function renderCell(overrides: Partial<React.ComponentProps<typeof DetailCell>> = {}) {
  return render(
    <DetailCell title="Issues" count={items.length} filters={filters} items={items} {...overrides} />,
  )
}

describe("<DetailCell>", () => {
  it("renders every item and reports the total", () => {
    renderCell()
    expect(screen.getByText("OPS-6 Map container ceiling")).toBeInTheDocument()
    expect(screen.getByText("OPS-9 Audit base image")).toBeInTheDocument()
    expect(screen.getByTestId("cell-count")).toHaveTextContent("4 items")
  })

  it("narrows to the selected filter and reports the narrowed count", () => {
    renderCell()
    fireEvent.click(screen.getByRole("button", { name: "Todo" }))

    expect(screen.queryByText("OPS-6 Map container ceiling")).not.toBeInTheDocument()
    expect(screen.getByText("OPS-2 Harborlight brief")).toBeInTheDocument()
    expect(screen.getByTestId("cell-count")).toHaveTextContent("2 of 4")
  })

  it("returns to the full list when the all-filter is picked again", () => {
    renderCell()
    fireEvent.click(screen.getByRole("button", { name: "Todo" }))
    fireEvent.click(screen.getByRole("button", { name: "All" }))
    expect(screen.getByTestId("cell-count")).toHaveTextContent("4 items")
  })

  it("filters by free text across title and subtitle, case-insensitively", () => {
    renderCell()
    fireEvent.click(screen.getByRole("button", { name: /search/i }))
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "RUNBOOK" } })

    expect(screen.getByText("OPS-1 Draft a runbook")).toBeInTheDocument()
    expect(screen.queryByText("OPS-6 Map container ceiling")).not.toBeInTheDocument()

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "backlog" } })
    expect(screen.getByText("OPS-9 Audit base image")).toBeInTheDocument()
  })

  it("combines the chip filter with the search term", () => {
    renderCell()
    fireEvent.click(screen.getByRole("button", { name: "Todo" }))
    fireEvent.click(screen.getByRole("button", { name: /search/i }))
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "harborlight" } })

    expect(screen.getByTestId("cell-count")).toHaveTextContent("1 of 4")
  })

  it("shows the empty state when nothing matches and hides it again after clearing", () => {
    renderCell()
    fireEvent.click(screen.getByRole("button", { name: /search/i }))
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "zzz-none" } })
    expect(screen.getByText(/nothing matches/i)).toBeInTheDocument()

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "" } })
    expect(screen.queryByText(/nothing matches/i)).not.toBeInTheDocument()
  })

  it("clears the search term when the search box is toggled shut", () => {
    renderCell()
    const toggle = screen.getByRole("button", { name: /search/i })
    fireEvent.click(toggle)
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "runbook" } })
    expect(screen.getByTestId("cell-count")).toHaveTextContent("1 of 4")

    fireEvent.click(toggle)
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument()
    expect(screen.getByTestId("cell-count")).toHaveTextContent("4 items")
  })

  it("invokes onSelect for the clicked row", () => {
    const onSelect = vi.fn()
    renderCell({ items: items.map((i) => ({ ...i, onSelect: () => onSelect(i.id) })) })
    fireEvent.click(screen.getByText("OPS-1 Draft a runbook"))
    expect(onSelect).toHaveBeenCalledWith("2")
  })

  it("renders the footer link with the destination label", () => {
    renderCell({ footerLabel: "Open filtered by agent:morgan", footerHref: "/issues?assignee=morgan" })
    const footer = screen.getByRole("link", { name: /open filtered/i })
    expect(footer).toHaveAttribute("href", "/issues?assignee=morgan")
  })

  it("renders an empty list without crashing and reports zero", () => {
    renderCell({ items: [], count: 0 })
    expect(screen.getByTestId("cell-count")).toHaveTextContent("0 items")
    expect(screen.getByText(/nothing matches/i)).toBeInTheDocument()
  })

  it("marks the cell header count as needing attention when warn is set", () => {
    renderCell({ warn: true })
    expect(screen.getByTestId("cell-badge")).toHaveAttribute("data-warn", "true")
  })
})

describe("<DetailCell> keyboard", () => {
  it("activates a row with Enter", () => {
    const onSelect = vi.fn()
    renderCell({ items: [{ ...items[0], onSelect }] })
    const row = screen.getByRole("button", { name: /OPS-6/ })
    fireEvent.keyDown(row, { key: "Enter" })
    expect(onSelect).toHaveBeenCalled()
  })

  it("keeps rows reachable by tab order", () => {
    renderCell({ items: [{ ...items[0], onSelect: vi.fn() }] })
    const list = screen.getByTestId("cell-body")
    const row = within(list).getByRole("button", { name: /OPS-6/ })
    expect(row).toHaveAttribute("tabindex", "0")
  })
})

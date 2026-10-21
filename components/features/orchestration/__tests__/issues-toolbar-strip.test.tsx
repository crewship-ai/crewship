import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { IssuesToolbarStrip, issuesShowingLabel, formatCount, type IssuesToolbarStripProps } from "../issues-toolbar-strip"
afterEach(cleanup)
const defaults: IssuesToolbarStripProps = { statuses: [], onToggleStatus: vi.fn(), onClearStatuses: vi.fn(), shown: 13, groupBy: "status" }

it("offers All plus the four statuses work moves through, and says what is shown", () => {
  render(<IssuesToolbarStrip {...defaults} />)
  const group = screen.getByRole("group", { name: "Filter issues by status" })
  expect([...group.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["All", "In progress", "In review", "Todo", "Backlog"])
  expect(screen.getByRole("button", { name: "All" })).toHaveAttribute("aria-pressed", "true")
  expect(screen.getByTestId("issues-count")).toHaveTextContent("13 issues · grouped by status")
})
it("toggles a status chip and clears through All", () => {
  const toggle = vi.fn()
  const clear = vi.fn()
  render(<IssuesToolbarStrip {...defaults} statuses={["REVIEW"]} onToggleStatus={toggle} onClearStatuses={clear} />)
  expect(screen.getByRole("button", { name: "In review" })).toHaveAttribute("aria-pressed", "true")
  expect(screen.getByRole("button", { name: "All" })).toHaveAttribute("aria-pressed", "false")
  fireEvent.click(screen.getByRole("button", { name: "Todo" }))
  expect(toggle).toHaveBeenCalledWith("TODO")
  fireEvent.click(screen.getByRole("button", { name: "All" }))
  expect(clear).toHaveBeenCalled()
})
it("names the grouping the list uses, and the board's own", () => {
  const { rerender } = render(<IssuesToolbarStrip {...defaults} shown={1} groupBy="project" />)
  expect(screen.getByTestId("issues-count")).toHaveTextContent("1 issue · grouped by project")
  rerender(<IssuesToolbarStrip {...defaults} groupBy="none" />)
  expect(screen.getByTestId("issues-count")).toHaveTextContent(/^13 issues$/)
  rerender(<IssuesToolbarStrip {...defaults} groupBy="none" board />)
  expect(screen.getByTestId("issues-count")).toHaveTextContent("grouped by status")
})
it("phone: a Filters chip opens the panel and counts the active facets", () => {
  const open = vi.fn()
  render(<IssuesToolbarStrip {...defaults} onOpenFilters={open} activeFilters={2} />)
  fireEvent.click(screen.getByRole("button", { name: /Filters/ }))
  expect(open).toHaveBeenCalled()
  expect(screen.getByRole("button", { name: /Filters/ })).toHaveTextContent("2")
})
it("presents server totals and prevents another page request while loading", () => {
  const load = vi.fn()
  const { rerender } = render(<IssuesToolbarStrip {...defaults} loaded={100} total={1015} hasMore onLoadMore={load} />)
  expect(screen.getByTestId("issues-showing")).toHaveTextContent("Showing newest 100 of 1 015")
  fireEvent.click(screen.getByRole("button", { name: "Load 100 more" }))
  expect(load).toHaveBeenCalledTimes(1)
  rerender(<IssuesToolbarStrip {...defaults} loaded={100} total={1015} hasMore onLoadMore={load} loadingMore />)
  expect(screen.getByRole("button", { name: "Load 100 more" })).toBeDisabled()
  fireEvent.click(screen.getByRole("button", { name: "Load 100 more" }))
  expect(load).toHaveBeenCalledTimes(1)
})
it.each([{ hasMore: false, onLoadMore: vi.fn() }, { hasMore: true, onLoadMore: undefined }])("keeps a partial-count label without inventing a page action", props => {
  render(<IssuesToolbarStrip {...defaults} loaded={100} total={200} {...props} />)
  expect(screen.getByTestId("issues-showing")).toHaveTextContent("Showing newest 100 of 200")
  expect(screen.queryByRole("button", { name: "Load 100 more" })).not.toBeInTheDocument()
})
it.each([null, 0, 99, 100])("does not misrepresent a complete/unknown total %s as a partial page", total => {
  expect(issuesShowingLabel(100, total)).toBeNull()
  render(<IssuesToolbarStrip {...defaults} loaded={100} total={total} />)
  expect(screen.queryByTestId("issues-showing")).not.toBeInTheDocument()
})
it("formats large totals consistently without locale dependence", () => {
  expect(formatCount(1000000)).toBe("1 000 000")
  expect(formatCount(0)).toBe("0")
})

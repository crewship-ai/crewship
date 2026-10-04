import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, it } from "vitest"
import { CardGridSkeleton, DetailPanelSkeleton, ListRowSkeleton, TableRowSkeleton } from "../skeletons"
afterEach(cleanup)
it("keeps list loading placeholders at the requested row count", () => {
  const { rerender } = render(<ListRowSkeleton />)
  expect(screen.getAllByRole("listitem")).toHaveLength(4)
  rerender(<ListRowSkeleton rows={2} className="compact-list" />)
  expect(screen.getAllByRole("listitem")).toHaveLength(2)
  expect(screen.getByRole("list")).toHaveClass("compact-list")
  rerender(<ListRowSkeleton rows={0} />)
  expect(screen.queryAllByRole("listitem")).toHaveLength(0)
})
it("reserves four blocks for detail metadata and forwards layout classes", () => {
  const { container } = render(<DetailPanelSkeleton className="detail-loading" />)
  expect(container.firstChild).toHaveClass("detail-loading")
  expect(container.querySelectorAll(".h-12")).toHaveLength(4)
  expect(container.querySelectorAll(".animate-pulse")).toHaveLength(7)
})
it("preserves table dimensions with default and explicit row/column counts", () => {
  const { container, rerender } = render(<TableRowSkeleton />)
  expect(container.firstElementChild?.children).toHaveLength(6)
  expect(container.querySelectorAll(".animate-pulse")).toHaveLength(24)
  rerender(<TableRowSkeleton rows={2} columns={3} />)
  expect(container.firstElementChild?.children).toHaveLength(2)
  expect(container.querySelectorAll(".animate-pulse")).toHaveLength(6)
  rerender(<TableRowSkeleton rows={0} columns={3} />)
  expect(container.querySelectorAll(".animate-pulse")).toHaveLength(0)
})
it.each([2, 3, 4] as const)("lays out requested cards in %s columns", columns => {
  const { container } = render(<CardGridSkeleton cards={5} columns={columns} />)
  expect(container.firstChild).toHaveClass(`grid-cols-${columns}`)
  expect(container.firstElementChild?.children).toHaveLength(5)
})
it("defaults the loading card grid to six cards in three columns", () => {
  const { container } = render(<CardGridSkeleton />)
  expect(container.firstChild).toHaveClass("grid-cols-3")
  expect(container.firstElementChild?.children).toHaveLength(6)
})

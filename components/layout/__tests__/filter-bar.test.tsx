import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { FilterBar } from "@/components/layout/filter-bar"
afterEach(cleanup)
it("defaults to the first filter and reports the selected label", () => {
  const choose = vi.fn()
  const { rerender } = render(<FilterBar filters={["All", "Running"]} onFilter={choose} />)
  expect(screen.getByRole("button", { name: "All" })).toHaveClass("bg-accent")
  fireEvent.click(screen.getByRole("button", { name: "Running" }))
  expect(choose).toHaveBeenCalledWith("Running")
  rerender(<FilterBar filters={["All", "Running"]} active="Running" onFilter={choose} />)
  expect(screen.getByRole("button", { name: "Running" })).toHaveClass("bg-accent")
  expect(screen.getByRole("button", { name: "All" })).not.toHaveClass("bg-accent")
})
it("supports display-only filters and an empty list", () => {
  const { rerender } = render(<FilterBar filters={["All"]} />)
  fireEvent.click(screen.getByRole("button", { name: "All" }))
  rerender(<FilterBar filters={[]} />)
  expect(screen.queryByRole("button")).not.toBeInTheDocument()
})

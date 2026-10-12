import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { PropertyRow, SectionHeader } from "@/components/features/issues/property-row"

afterEach(cleanup)
it("announces collapsed state and keeps the action separate from toggling", () => {
  const toggle = vi.fn(), action = vi.fn()
  const { rerender } = render(<SectionHeader title="Properties" open={false} onToggle={toggle} action={<button onClick={action}>Add property</button>} />)
  const header = screen.getByRole("button", { name: "Properties" })
  expect(header).toHaveAttribute("aria-expanded", "false")
  fireEvent.click(header)
  expect(toggle).toHaveBeenCalledOnce()
  fireEvent.click(screen.getByRole("button", { name: "Add property" }))
  expect(action).toHaveBeenCalledOnce()
  expect(toggle).toHaveBeenCalledOnce()
  rerender(<SectionHeader title="Properties" open onToggle={toggle} />)
  expect(screen.getByRole("button", { name: "Properties" })).toHaveAttribute("aria-expanded", "true")
  expect(screen.queryByRole("button", { name: "Add property" })).not.toBeInTheDocument()
})
it("supports named and unnamed property values without invalid block nesting", () => {
  const { container, rerender } = render(<PropertyRow label="Owner" className="custom-property"><div>Team A</div></PropertyRow>)
  expect(screen.getByText("Owner")).toBeInTheDocument()
  expect(container.firstElementChild).toHaveClass("custom-property")
  expect(screen.getByText("Team A").parentElement?.tagName).toBe("DIV")
  rerender(<PropertyRow><button>Choose owner</button></PropertyRow>)
  expect(screen.queryByText("Owner")).not.toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Choose owner" })).toBeInTheDocument()
})

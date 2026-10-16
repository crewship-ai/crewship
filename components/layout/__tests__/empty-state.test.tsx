import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { Workflow } from "lucide-react"

import { EmptyState } from "@/components/layout/empty-state"

// The merged EmptyState (#3344) keeps every prop either former component
// offered: icon/title/description/size/className/children from the layout
// one, `action` and an optional description from the ui/detail one.

describe("<EmptyState>", () => {
  it("wraps in a card by default and renders children", () => {
    const { container } = render(
      <EmptyState icon={Workflow} title="No crews" description="Create one.">
        <button>Create crew</button>
      </EmptyState>,
    )
    expect(screen.getByRole("heading", { name: "No crews" })).toBeInTheDocument()
    expect(screen.getByText("Create one.")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Create crew" })).toBeInTheDocument()
    expect(container.querySelector("[data-slot=card]")).not.toBeNull()
  })

  it("renders flush with size=inline", () => {
    const { container } = render(<EmptyState icon={Workflow} title="Nothing" description="Here" size="inline" className="x-inline" />)
    expect(container.firstElementChild).toHaveClass("x-inline")
    expect(container.querySelector("[data-slot=card]")).toBeNull()
  })

  it("compact keeps the former detail-kit register: title, description and action", () => {
    render(
      <EmptyState icon={Workflow} size="compact" title="Nothing yet" description="Description" action={<button>Add</button>} />,
    )
    expect(screen.getByText("Nothing yet")).toHaveClass("text-body", "font-medium")
    expect(screen.getByText("Description")).toHaveClass("type-row")
    expect(screen.getByRole("button", { name: "Add" }).parentElement).toHaveClass("mt-4")
  })

  it("description is optional in every size", () => {
    for (const size of ["card", "inline", "compact"] as const) {
      const { container, unmount } = render(<EmptyState icon={Workflow} size={size} title="Only a title" />)
      expect(screen.getByText("Only a title")).toBeInTheDocument()
      expect(container.querySelector("p")).toBeNull()
      unmount()
    }
  })

  it("renders action under the copy outside compact too", () => {
    render(<EmptyState icon={Workflow} size="inline" title="Empty" description="d" action={<button>Go</button>} />)
    expect(screen.getByRole("button", { name: "Go" }).parentElement).toHaveClass("mt-4")
  })
})

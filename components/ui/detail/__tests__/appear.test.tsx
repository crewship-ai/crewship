import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { CircleDot } from "lucide-react"

import { Appear } from "@/components/ui/detail"
import { DetailCell } from "@/components/features/crews/canvas/detail-cell"

// =============================================================================
// The entrance wrapper introduces a div between the grid and the card, which
// is exactly the kind of change that silently breaks a layout: put the column
// span on the card and the card is no longer the grid item, so the span does
// nothing. These pin the contract — spans land on the wrapper, content still
// renders, and the wrapper never swallows children.
// =============================================================================

describe("<Appear>", () => {
  it("renders its children and keeps the caller's classes", () => {
    render(<Appear className="col-span-2 min-w-0"><p>inside</p></Appear>)
    const el = screen.getByText("inside").parentElement!
    expect(el.className).toContain("col-span-2")
    expect(el.className).toContain("min-w-0")
  })
})

describe("<DetailCell> as a grid item", () => {
  const props = {
    title: "Issues",
    filters: [{ id: "all", label: "All" }],
    items: [{ id: "1", icon: CircleDot, title: "OPS-2 Draft the incident note", tag: "all" }],
  }

  it("puts the span classes on the outer grid item, not the card", () => {
    const { container } = render(
      <div className="grid">
        <DetailCell {...props} className="@10xl:col-span-2" />
      </div>,
    )
    const gridItem = container.querySelector(".grid")!.firstElementChild!
    expect(gridItem.className).toContain("@10xl:col-span-2")
  })

  it("still renders the list through the wrapper", () => {
    render(<DetailCell {...props} order={3} />)
    expect(screen.getByText("OPS-2 Draft the incident note")).toBeInTheDocument()
    expect(screen.getByTestId("cell-count")).toHaveTextContent("1 items")
  })
})

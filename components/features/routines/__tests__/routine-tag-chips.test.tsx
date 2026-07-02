import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { RunTagChips } from "@/components/features/routines/routine-tag-chips"

describe("RunTagChips", () => {
  it("renders one chip per tag", () => {
    render(<RunTagChips tags={["prod", "nightly"]} />)
    expect(screen.getByText("prod")).toBeInTheDocument()
    expect(screen.getByText("nightly")).toBeInTheDocument()
  })

  it("renders nothing when tags is undefined", () => {
    const { container } = render(<RunTagChips />)
    expect(container).toBeEmptyDOMElement()
  })

  it("renders nothing when tags is an empty array", () => {
    const { container } = render(<RunTagChips tags={[]} />)
    expect(container).toBeEmptyDOMElement()
  })

  it("renders nothing when tags is null", () => {
    const { container } = render(<RunTagChips tags={null} />)
    expect(container).toBeEmptyDOMElement()
  })

  it("filters out blank/whitespace-only entries defensively", () => {
    const { container } = render(<RunTagChips tags={["", "  ", "real-tag"]} />)
    expect(screen.getByText("real-tag")).toBeInTheDocument()
    expect(container.querySelectorAll("[title^='Run tag:']").length).toBe(1)
  })
})

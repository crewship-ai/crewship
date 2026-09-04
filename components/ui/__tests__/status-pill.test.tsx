import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { StatusPill } from "@/components/ui/status-pill"

describe("StatusPill", () => {
  it("renders a word next to the dot, never the raw enum", () => {
    render(<StatusPill status="IN_PROGRESS" />)
    expect(screen.getByText("In progress")).toBeTruthy()
    expect(screen.queryByText("IN_PROGRESS")).toBeNull()
  })

  it("takes its tone from the status and lets a label override the word only", () => {
    const { container } = render(<StatusPill status="FAILED" label="2 failed" />)
    expect(screen.getByText("2 failed")).toBeTruthy()
    expect(container.querySelector('[data-tone="danger"]')).not.toBeNull()
  })

  it("pulses only when told the thing is live", () => {
    const { container, rerender } = render(<StatusPill status="RUNNING" />)
    expect(container.querySelector(".animate-pulse")).toBeNull()
    rerender(<StatusPill status="RUNNING" live />)
    expect(container.querySelector(".animate-pulse")).not.toBeNull()
  })
})

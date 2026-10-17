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

describe("StatusPill icon", () => {
  it("renders an icon in place of the dot when one is given", () => {
    const { container } = render(<StatusPill status="FAILED" icon={<svg data-testid="glyph" />} />)
    expect(screen.getByTestId("glyph")).toBeTruthy()
    expect(container.querySelector('[data-slot="status-pill-dot"]')).toBeNull()
  })

  it("keeps the dot when there is no icon", () => {
    const { container } = render(<StatusPill status="FAILED" />)
    expect(container.querySelector('[data-slot="status-pill-dot"]')).not.toBeNull()
  })
})

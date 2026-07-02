import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { WakeGateChip } from "@/components/features/routines/routine-wake-gate-chip"

describe("WakeGateChip", () => {
  it("renders the wake gate label with the probe routine slug", () => {
    render(<WakeGateChip wakePipelineSlug="check-inbox-probe" />)
    expect(screen.getByText("Wake gate: check-inbox-probe")).toBeInTheDocument()
  })

  it("carries a tooltip explaining the token-zero probe", () => {
    render(<WakeGateChip wakePipelineSlug="probe" />)
    const chip = screen.getByText("Wake gate: probe").closest('[data-slot="badge"]')
    expect(chip?.getAttribute("title")).toMatch(/token-zero probe/i)
  })

  it("renders nothing when there is no wake pipeline", () => {
    const { container } = render(<WakeGateChip />)
    expect(container).toBeEmptyDOMElement()
  })

  it("renders nothing for an empty string slug", () => {
    const { container } = render(<WakeGateChip wakePipelineSlug="" />)
    expect(container).toBeEmptyDOMElement()
  })
})

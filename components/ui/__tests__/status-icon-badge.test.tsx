import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { StatusIconBadge } from "@/components/ui/status-icon-badge"
import { RUN_STATUS_CONFIG } from "@/lib/status-config"

// The icon badge is a StatusPill with a glyph: its tone comes from the entry
// and renders on the chip tokens, never a per-feature palette class.
describe("StatusIconBadge", () => {
  it("renders the entry label and a default icon inside a status pill", () => {
    const { container } = render(<StatusIconBadge entry={RUN_STATUS_CONFIG.COMPLETED} />)
    expect(screen.getByText("Completed")).toBeInTheDocument()
    const pill = container.querySelector('[data-slot="status-pill"]')!
    expect(pill).toBeTruthy()
    expect(pill.querySelector("svg")).toBeTruthy()
    expect(pill.getAttribute("data-tone")).toBe("success")
  })

  it("takes the entry tone and the chip token classes", () => {
    const { container } = render(<StatusIconBadge entry={RUN_STATUS_CONFIG.FAILED} />)
    const pill = container.querySelector('[data-slot="status-pill"]')!
    expect(pill.getAttribute("data-tone")).toBe("danger")
    expect(pill.classList.contains("bg-chip-danger-bg")).toBe(true)
    expect(pill.className).not.toMatch(/\b(?:bg|text)-(?:red|emerald|blue|amber)-\d/)
  })

  it("honors an explicit icon override", () => {
    const { container } = render(
      <StatusIconBadge entry={RUN_STATUS_CONFIG.RUNNING} icon={<span data-testid="glyph" />} />,
    )
    expect(screen.getByTestId("glyph")).toBeInTheDocument()
    expect(container.querySelector("svg")).toBeNull()
    expect(screen.getByText("Running")).toBeInTheDocument()
  })

  it("pulses the pill dot instead of a glyph when live", () => {
    const { container } = render(<StatusIconBadge entry={RUN_STATUS_CONFIG.RUNNING} live />)
    expect(container.querySelector("svg")).toBeNull()
    expect(container.querySelector('[data-slot="status-pill-dot"].animate-pulse')).not.toBeNull()
  })
})

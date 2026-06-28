import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { StatusIconBadge } from "@/components/ui/status-icon-badge"
import { RUN_STATUS_CONFIG, STATUS_STYLES } from "@/lib/status-config"

// Pins the rendered contract (label + icon + className) of the shared icon
// badge renderer. Features migrate their local STATUS_CONFIG maps onto this
// primitive only if it produces byte-identical output, so this is the
// reference those migrations are checked against. Changing a class string
// here is a deliberate visual change, not an incidental one.

describe("StatusIconBadge", () => {
  it("renders the entry label and a default icon", () => {
    const { container } = render(<StatusIconBadge entry={RUN_STATUS_CONFIG.COMPLETED} />)
    expect(screen.getByText("Completed")).toBeInTheDocument()
    // Default icon is the entry's own component, rendered as an svg.
    expect(container.querySelector("svg")).toBeTruthy()
  })

  it("emits the outline variant with gap-1 border-0 + the entry color classes", () => {
    const { container } = render(<StatusIconBadge entry={RUN_STATUS_CONFIG.FAILED} />)
    const badge = container.querySelector('[data-slot="badge"]')!
    expect(badge.getAttribute("data-variant")).toBe("outline")
    expect(badge.classList.contains("gap-1")).toBe(true)
    expect(badge.classList.contains("border-0")).toBe(true)
    // red entry color (STATUS_STYLES.red) survives the merge.
    for (const cls of STATUS_STYLES.red.split(" ")) {
      expect(badge.classList.contains(cls)).toBe(true)
    }
  })

  it("honors a custom gap and an explicit icon override", () => {
    const { container } = render(
      <StatusIconBadge
        entry={RUN_STATUS_CONFIG.RUNNING}
        gap="gap-1.5"
        icon={<span data-testid="pulse" />}
      />,
    )
    const badge = container.querySelector('[data-slot="badge"]')!
    expect(badge.classList.contains("gap-1.5")).toBe(true)
    expect(badge.classList.contains("gap-1")).toBe(false)
    // The override replaces the default icon entirely (no svg rendered).
    expect(screen.getByTestId("pulse")).toBeInTheDocument()
    expect(container.querySelector("svg")).toBeNull()
    expect(screen.getByText("Running")).toBeInTheDocument()
  })
})

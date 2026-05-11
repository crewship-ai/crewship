import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { CrewIconPickerDialog } from "@/components/features/crews/crew-icon-picker-dialog"
import { CREW_ICONS, GRADIENT_PALETTES } from "@/lib/entities"

describe("<CrewIconPickerDialog>", () => {
  const baseProps = {
    open: true,
    onOpenChange: vi.fn(),
    crewName: "Research",
    icon: "telescope",
    color: "fuchsia",
    onSave: vi.fn().mockResolvedValue(undefined),
  }

  beforeEach(() => {
    baseProps.onOpenChange.mockClear()
    baseProps.onSave.mockClear()
  })

  it("renders all gradient palettes as color swatches", () => {
    render(<CrewIconPickerDialog {...baseProps} />)
    // Each palette gets a swatch button with title equal to palette id.
    for (const p of GRADIENT_PALETTES) {
      const swatch = document.querySelector(`button[title="${p.id}"]`)
      expect(swatch).toBeInTheDocument()
    }
  })

  it("icon grid renders all CREW_ICONS by default", () => {
    render(<CrewIconPickerDialog {...baseProps} />)
    // Grid items are buttons with title=icon.name. Check a known icon.
    const briefcaseBtn = document.querySelector('button[title="briefcase"]')
    expect(briefcaseBtn).toBeInTheDocument()
    // Counter shows N of M
    expect(screen.getByText(new RegExp(`of ${CREW_ICONS.length}`))).toBeInTheDocument()
  })

  it("search filters the icon grid", () => {
    render(<CrewIconPickerDialog {...baseProps} />)
    const searchInput = document.querySelector('input[placeholder*="Search icons"]') as HTMLInputElement
    expect(searchInput).toBeInTheDocument()
    fireEvent.change(searchInput, { target: { value: "telescope" } })
    // Only icons whose name includes "telescope" should remain.
    expect(document.querySelector('button[title="telescope"]')).toBeInTheDocument()
    // briefcase shouldn't be visible if the search filters it out (it doesn't contain "telescope")
    expect(document.querySelector('button[title="briefcase"]')).toBeNull()
  })

  it("clicking a color swatch and Save dispatches the right shape", async () => {
    render(<CrewIconPickerDialog {...baseProps} />)
    // Click "amber" swatch
    fireEvent.click(document.querySelector('button[title="amber"]')!)
    fireEvent.click(screen.getByRole("button", { name: /^Save$/ }))

    await waitFor(() => expect(baseProps.onSave).toHaveBeenCalled())
    const arg = baseProps.onSave.mock.calls[0][0]
    expect(arg.color).toBe("amber")
    expect(arg.icon).toBe("telescope") // unchanged from initial
  })

  it("clicking an icon and Save dispatches the right shape", async () => {
    render(<CrewIconPickerDialog {...baseProps} />)
    fireEvent.click(document.querySelector('button[title="rocket"]')!)
    fireEvent.click(screen.getByRole("button", { name: /^Save$/ }))

    await waitFor(() => expect(baseProps.onSave).toHaveBeenCalled())
    const arg = baseProps.onSave.mock.calls[0][0]
    expect(arg.icon).toBe("rocket")
    expect(arg.color).toBe("fuchsia") // unchanged
  })

  it("Cancel does not call onSave", () => {
    render(<CrewIconPickerDialog {...baseProps} />)
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(baseProps.onSave).not.toHaveBeenCalled()
    expect(baseProps.onOpenChange).toHaveBeenCalledWith(false)
  })

  it("re-opening with new props resets the draft state", () => {
    const { rerender } = render(<CrewIconPickerDialog {...baseProps} icon="briefcase" color="blue" />)
    // Pick something, then re-render with original closed→open.
    fireEvent.click(document.querySelector('button[title="rocket"]')!)
    rerender(<CrewIconPickerDialog {...baseProps} open={false} icon="briefcase" color="blue" />)
    rerender(<CrewIconPickerDialog {...baseProps} open={true} icon="briefcase" color="blue" />)
    // After re-open, the active icon should be briefcase again, not rocket.
    const briefcaseBtn = document.querySelector('button[title="briefcase"]')
    expect(briefcaseBtn?.className).toContain("border-blue-400")
  })
})

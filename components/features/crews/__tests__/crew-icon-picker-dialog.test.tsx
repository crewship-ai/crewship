import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { CrewIconPickerDialog } from "@/components/features/crews/crew-icon-picker-dialog"
import { CREW_ICONS, GRADIENT_PALETTES, getCrewIconDef } from "@/lib/entities"

/**
 * The dialog now renders the kit's CreateSurfacePicker rather than its own
 * preview + palette row + search-and-grid, so the DOM contract these tests
 * read moved with it:
 *
 *   swatches   title="blue"          → role="radio" aria-label="blue"
 *   icon tiles title="telescope"     → role="radio" aria-label="Telescope"
 *                                      (the def's LABEL, not the slug)
 *   counter    "345 of 345"          → the result count alone
 *
 * The assertions below are the same ones, re-pointed. Using the shared picker
 * is the change; keeping a bespoke one alive to satisfy a `title` attribute
 * would have been the tail wagging the dog.
 */
describe("<CrewIconPickerDialog>", () => {
  const baseProps = {
    open: true,
    onOpenChange: vi.fn(),
    crewName: "Research",
    icon: "telescope",
    color: "fuchsia",
    onSave: vi.fn().mockResolvedValue(undefined),
  }

  const swatch = (id: string) => screen.getByRole("radio", { name: id })
  const tile = (iconName: string) =>
    screen.queryByRole("radio", { name: getCrewIconDef(iconName).label })

  beforeEach(() => {
    baseProps.onOpenChange.mockClear()
    baseProps.onSave.mockClear()
  })

  it("wears the shared shell, not a bare dialog on the darkest background", () => {
    // Radix's DialogContent is bg-background — oklch(0.10), the darkest
    // surface in the palette — while every create surface is bg-card at
    // oklch(0.155). Opened from a page built on the lighter card, the old
    // dialog read as a hole rather than a panel.
    render(<CrewIconPickerDialog {...baseProps} />)
    const content = document.querySelector('[data-slot="dialog-content"]')!
    expect(content.className).toContain("bg-card")
    expect(content.className).not.toContain("bg-background")
    // The shell's phone geometry, which the hand-rolled dialog never had.
    expect(content.className).toContain("max-sm:rounded-t-2xl")
  })

  it("renders all gradient palettes as color swatches", () => {
    render(<CrewIconPickerDialog {...baseProps} />)
    for (const p of GRADIENT_PALETTES) {
      expect(swatch(p.id)).toBeInTheDocument()
    }
  })

  it("offers every icon by default, and says how many", () => {
    render(<CrewIconPickerDialog {...baseProps} />)
    expect(tile("briefcase")).toBeInTheDocument()
    expect(screen.getByText(String(CREW_ICONS.length))).toBeInTheDocument()
  })

  it("search filters the icon grid", () => {
    render(<CrewIconPickerDialog {...baseProps} />)
    fireEvent.change(screen.getByRole("textbox", { name: /Search icons/i }), {
      target: { value: "telescope" },
    })
    expect(tile("telescope")).toBeInTheDocument()
    expect(tile("briefcase")).toBeNull()
  })

  it("browses by category, which the hand-rolled grid could not do", () => {
    // 345 icons behind a name-substring search and nothing else was the
    // reason this needed the kit's picker rather than a recolour.
    render(<CrewIconPickerDialog {...baseProps} />)
    const categories = screen.getAllByRole("button").filter((b) =>
      b.getAttribute("aria-pressed") !== null,
    )
    expect(categories.length).toBeGreaterThan(1)
  })

  it("clicking a color swatch and Save dispatches the right shape", async () => {
    render(<CrewIconPickerDialog {...baseProps} />)
    fireEvent.click(swatch("amber"))
    fireEvent.click(screen.getByRole("button", { name: /^Save$/ }))

    await waitFor(() => expect(baseProps.onSave).toHaveBeenCalled())
    const arg = baseProps.onSave.mock.calls[0][0]
    expect(arg.color).toBe("amber")
    expect(arg.icon).toBe("telescope") // unchanged from initial
  })

  it("clicking an icon and Save dispatches the right shape", async () => {
    render(<CrewIconPickerDialog {...baseProps} />)
    fireEvent.click(tile("rocket")!)
    fireEvent.click(screen.getByRole("button", { name: /^Save$/ }))

    await waitFor(() => expect(baseProps.onSave).toHaveBeenCalled())
    const arg = baseProps.onSave.mock.calls[0][0]
    expect(arg.icon).toBe("rocket")
    expect(arg.color).toBe("fuchsia") // unchanged
  })

  it("Cancel does not call onSave, and does not stop to ask", () => {
    // guardCancel is off: nothing reaches the server until Save, so the
    // shell's discard confirmation would be protecting work that was never
    // at risk.
    render(<CrewIconPickerDialog {...baseProps} />)
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(baseProps.onSave).not.toHaveBeenCalled()
    expect(baseProps.onOpenChange).toHaveBeenCalledWith(false)
  })

  it("re-opening with new props resets the draft state", () => {
    const { rerender } = render(
      <CrewIconPickerDialog {...baseProps} icon="briefcase" color="blue" />,
    )
    fireEvent.click(tile("rocket")!)
    rerender(<CrewIconPickerDialog {...baseProps} open={false} icon="briefcase" color="blue" />)
    rerender(<CrewIconPickerDialog {...baseProps} open icon="briefcase" color="blue" />)

    expect(tile("briefcase")).toHaveAttribute("aria-checked", "true")
    expect(tile("rocket")).toHaveAttribute("aria-checked", "false")
  })
})

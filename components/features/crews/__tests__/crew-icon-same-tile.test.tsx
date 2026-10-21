import { describe, it, expect, vi, afterEach } from "vitest"
import { render, cleanup } from "@testing-library/react"

import { CrewIconPickerDialog } from "@/components/features/crews/crew-icon-picker-dialog"
import { CrewTile } from "../crew-tile"
import { StepIdentity } from "../create-crew/step-identity"
import { INITIAL_STATE } from "../create-crew/types"

afterEach(cleanup)

// The icon you see while picking must be the icon you get. The header (48 px)
// and the panel (20 px) draw a crew as CrewTile: a solid square in the crew
// colour with a white glyph. The picker's preview used CrewIcon (a 15 % tint
// with a coloured glyph), so a saved crew never looked like its preview.
describe("crew icon preview matches the saved crew tile", () => {
  it("draws the picker preview with CrewTile at the header size and colour", () => {
    render(
      <CrewIconPickerDialog
        open
        onOpenChange={vi.fn()}
        crewName="Research"
        icon="telescope"
        color="fuchsia"
        onSave={vi.fn()}
      />,
    )
    const preview = document.querySelector("[data-slot=crew-tile]") as HTMLElement | null
    expect(preview, "preview must be a CrewTile").not.toBeNull()

    const { container } = render(<CrewTile icon="telescope" color="fuchsia" size={48} />)
    const saved = container.querySelector("[data-slot=crew-tile]") as HTMLElement

    expect(preview!.style.backgroundColor).toBe(saved.style.backgroundColor)
    expect(preview!.style.width).toBe(saved.style.width)
    expect(preview!.style.height).toBe(saved.style.height)
    expect(preview!.querySelector("svg")?.getAttribute("class")).toBe(saved.querySelector("svg")?.getAttribute("class"))
  })

  it("draws the edit dialog's identity tile with CrewTile too", () => {
    render(<StepIdentity state={{ ...INITIAL_STATE, icon: "telescope", color: "fuchsia" }} setState={vi.fn()} onPickIcon={vi.fn()} />)
    const tile = document.querySelector("[data-slot=crew-tile]") as HTMLElement | null
    expect(tile, "identity tile must be a CrewTile").not.toBeNull()
    expect(tile!.style.width).toBe("48px")
  })
})

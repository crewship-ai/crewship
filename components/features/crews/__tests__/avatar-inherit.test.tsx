import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

import { AvatarPickerDialog } from "@/components/features/crews/avatar-picker-dialog"

// =============================================================================
// "Inherit" is not a style, and the picker has to stop pretending it is.
//
// It sat in the style grid as a twelfth tile, and its preview renders
// crewStyle ?? DEFAULT_AVATAR_STYLE. The default is bottts-neutral, whose
// label in the grid is "Robots" — so for any agent whose crew has no style
// set, two adjacent tiles showed the identical face with different words under
// them, and nothing on screen explained why. It reads as a duplicate.
//
// It is a REFERENCE: follow whatever the crew uses. So it says what it
// currently resolves to, and it sits outside the grid of actual styles.
// =============================================================================

const base = {
  open: true,
  onOpenChange: vi.fn(),
  agentName: "Sam",
  seed: "2tj0n38kyx",
  onSave: vi.fn(),
}

describe("avatar style — inherit", () => {
  it("names the crew style it is following", () => {
    render(<AvatarPickerDialog {...base} style={null} crewStyle="pixel-art" />)
    expect(screen.getByRole("button", { name: /Follow the crew/i })).toHaveTextContent(/Pixel Art/i)
  })

  it("admits when following the crew just means the default", () => {
    // The case that looked like a duplicate: no crew style, so inheriting
    // resolves to the same Robots face the grid shows one tile over.
    render(<AvatarPickerDialog {...base} style={null} crewStyle={null} />)
    const tile = screen.getByRole("button", { name: /Follow the crew/i })
    expect(tile).toHaveTextContent(/Robots/i)
    expect(tile).toHaveTextContent(/default/i)
  })

  it("is not one of the style tiles", () => {
    // The dialog renders in a portal, so query the document, not the container.
    render(<AvatarPickerDialog {...base} style={null} crewStyle={null} />)
    const grid = screen.getByTestId("avatar-style-grid")
    expect(grid.textContent).not.toMatch(/Follow the crew/i)
    expect(grid.textContent).toMatch(/Robots/)
  })
})

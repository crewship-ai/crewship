// Sheets and workbooks carry the green Excel identity, through the --sheet token.
import { expect, it } from "vitest"
import { render } from "@testing-library/react"
import { SheetIcon } from "../sheet-icon"

it("draws the Sheet icon in the Sheet colour, from the token, on its soft tile", () => {
  const { container } = render(<SheetIcon />)
  const tile = container.firstElementChild as HTMLElement
  expect(tile.className).toMatch(/\bbg-sheet-soft\b/)
  expect(tile.className).toMatch(/\btext-sheet\b/)
  expect(tile).toHaveAttribute("aria-hidden", "true")
  expect(container.innerHTML).not.toMatch(/#[0-9a-f]{6}|green-\d/i)
})

import { describe, it, expect, afterEach } from "vitest"
import { render, cleanup } from "@testing-library/react"

import { CrewTile } from "../crew-tile"

afterEach(cleanup)

// PAR-065 / PAR-069: v67's crew tile is a solid square in the crew colour with
// a white glyph, 48 px on the crew page and 20 px in the panel.
describe("CrewTile", () => {
  it("is a solid tile in the crew colour with a white glyph", () => {
    render(<CrewTile icon="search" color="#0F8C8C" size={48} />)
    const tile = document.querySelector("[data-slot=crew-tile]") as HTMLElement
    expect(tile.style.backgroundColor).toBe("rgb(15, 140, 140)")
    expect(tile.style.width).toBe("48px")
    expect(tile.style.height).toBe("48px")
    expect(tile.querySelector("svg")?.getAttribute("class")).toMatch(/text-white/)
  })
  it("resolves a palette id and falls back to a default colour", () => {
    render(<CrewTile icon="code" color={null} size={20} />)
    const tile = document.querySelector("[data-slot=crew-tile]") as HTMLElement
    expect(tile.style.backgroundColor).not.toBe("")
    expect(tile.style.width).toBe("20px")
  })
})

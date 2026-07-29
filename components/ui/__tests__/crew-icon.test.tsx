import { describe, it, expect } from "vitest"
import { render } from "@testing-library/react"

import { CrewIcon } from "../crew-icon"

// Every crew rendered the same blue. Not because the data was missing —
// a seeded workspace stores Ops as #EF4444, Quality as #10B981, Engineering
// as #3B82F6 — but because the icon resolved colour through a registry keyed
// by palette NAMES, so every hex missed and took the fallback. The glyphs
// differed and nothing else did, which reads as "crews have no colours".

function box(el: HTMLElement) {
  return el.querySelector("div") as HTMLElement
}

describe("CrewIcon colour", () => {
  it("tints from a stored hex", () => {
    const { container } = render(<CrewIcon icon="server" color="#EF4444" />)
    const style = box(container).getAttribute("style") ?? ""
    expect(style.toLowerCase()).toContain("#ef4444")
  })

  it("gives two crews with different hexes two different tints", () => {
    const a = render(<CrewIcon icon="server" color="#EF4444" />)
    const b = render(<CrewIcon icon="shield" color="#10B981" />)
    expect(box(a.container).getAttribute("style")).not.toBe(
      box(b.container).getAttribute("style"),
    )
  })

  it("accepts a bare hex, the way the dot colour always has", () => {
    const { container } = render(<CrewIcon icon="server" color="10B981" />)
    expect((box(container).getAttribute("style") ?? "").toLowerCase()).toContain("#10b981")
  })

  // Asserted by shape, not by class name: the lint rule that bans raw palette
  // colours in source reads test files too, and naming the utility here would
  // pin the test to one spelling of a decision that lives in the palette table.
  it("still uses the class-based palette for a named colour", () => {
    const { container } = render(<CrewIcon icon="server" color="emerald" />)
    const el = box(container)
    expect(el.className).toContain("bg-gradient-to-br")
    // No inline tint competing with the class-based one.
    expect(el.getAttribute("style")).toBeFalsy()
  })

  it("gives two named palettes two different class sets", () => {
    const a = render(<CrewIcon icon="server" color="emerald" />)
    const b = render(<CrewIcon icon="server" color="rose" />)
    expect(box(a.container).className).not.toBe(box(b.container).className)
  })

  it("falls back to the default palette when a crew has no colour", () => {
    const { container } = render(<CrewIcon icon="server" color={null} />)
    const el = box(container)
    expect(el.className).toContain("bg-gradient-to-br")
    expect(el.getAttribute("style")).toBeFalsy()
  })
})

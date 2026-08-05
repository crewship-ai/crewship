import { describe, it, expect } from "vitest"

import { iconColorProps, GRADIENT_PALETTES } from "@/lib/crew-icons"

// A crew's or project's colour is stored as EITHER a palette id ("blue") or a
// raw hex ("#22C55E"), depending on which picker wrote it. `getGradientPalette`
// only knows ids and falls back to GRADIENT_PALETTES[0] — which is blue — so
// every hex-coloured entity rendered blue wherever a caller reached for
// `.text` directly.
//
// That is not a hypothetical: File Operations is stored #22C55E and showed a
// green icon on its own card (which routes through CrewIconPopover, and gets
// it right) while the sidebar row beside it drew the same project blue.
//
// crewColorHex already existed to solve this and its doc comment prescribes
// the pattern — "callers that can style inline ask here first". The bug kept
// coming back because that is a rule callers have to remember. This helper
// makes it a thing they call instead.

const BLUE_FALLBACK = GRADIENT_PALETTES[0]

describe("iconColorProps", () => {
  it("tints a hex inline and uses no palette class", () => {
    const p = iconColorProps("#22C55E")
    expect(p.style).toEqual({ color: "#22C55E" })
    expect(p.className).toBeUndefined()
  })

  it("never renders a hex as the blue fallback", () => {
    // The whole bug in one assertion.
    for (const hex of ["#22C55E", "#EC4899", "#EF4444", "#F97316", "#06B6D4"]) {
      expect(iconColorProps(hex).className).not.toBe(BLUE_FALLBACK.text)
    }
  })

  it("keeps distinct hexes distinct", () => {
    // Five projects stored as five colours must not collapse into one.
    const colours = ["#22C55E", "#EC4899", "#EF4444", "#F97316", "#06B6D4"]
    const rendered = colours.map((c) => JSON.stringify(iconColorProps(c)))
    expect(new Set(rendered).size).toBe(colours.length)
  })

  it("uses the palette class for a palette id, with no inline style", () => {
    const p = iconColorProps("emerald")
    const emerald = GRADIENT_PALETTES.find((g) => g.id === "emerald")!
    expect(p.className).toBe(emerald.text)
    expect(p.style).toBeUndefined()
  })

  it("accepts a bare hex without the hash", () => {
    expect(iconColorProps("22C55E").style).toEqual({ color: "#22C55E" })
  })

  it("falls back to the default palette for nothing, and for nonsense", () => {
    for (const bad of [null, undefined, "", "not-a-colour", "#12"]) {
      const p = iconColorProps(bad)
      expect(p.className).toBe(BLUE_FALLBACK.text)
      expect(p.style).toBeUndefined()
    }
  })
})

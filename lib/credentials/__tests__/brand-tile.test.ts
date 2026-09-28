// Provider logos in pickers and lists are app-icon tiles: the brand's own
// ground, a white glyph. These pin the grounds the Harbor spec names and the
// fallbacks for brands whose official colour is white or near-black.

import { describe, it, expect } from "vitest"
import { brandTileColors } from "../brand-tile"

describe("brandTileColors", () => {
  it.each([
    ["ANTHROPIC", "#D97757", "#FFFFFF"],
    ["CLAUDE_CODE", "#D97757", "#FFFFFF"],
    ["OPENAI", "#0D0D0D", "#FFFFFF"],
    ["OLLAMA", "#FFFFFF", "#0D0D0D"],
    ["GOOGLE", "linear-gradient(135deg, #4285F4, #9B72CB, #D96570)", "#FFFFFF"],
  ])("%s gets its named ground", (key, background, glyph) => {
    expect(brandTileColors(key, "#123456")).toEqual({ background, glyph })
  })

  it("puts a white-branded mark on the dark ground rather than white on white", () => {
    expect(brandTileColors("OPENCODE_GO", "#FFFFFF")).toEqual({ background: "#0D0D0D", glyph: "#FFFFFF" })
  })

  it("uses the brand hex as the ground for everyone else", () => {
    expect(brandTileColors("ZAI_CODING_PLAN", "#3E4AC8")).toEqual({ background: "#3E4AC8", glyph: "#FFFFFF" })
  })

  it("switches to a dark glyph on a pale brand ground", () => {
    expect(brandTileColors("SOMETHING", "#F7DF1E")).toEqual({ background: "#F7DF1E", glyph: "#0D0D0D" })
  })
})

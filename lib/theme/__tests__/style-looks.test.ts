import { describe, it, expect } from "vitest"

import { STYLES } from "@/lib/theme/appearance-style"
import { STYLE_LOOKS, lookOf } from "@/lib/theme/appearance-style"

// Settings › Appearance groups the styles by how they look, not by industry.
describe("style looks", () => {
  it("has the four looks in the prototype's order, with its counts", () => {
    expect(STYLE_LOOKS.map((l) => [l.label, l.styles.length])).toEqual([
      ["Neutral", 17],
      ["Warm", 9],
      ["Violet", 12],
      ["Made for dark", 7],
    ])
  })

  it("puts every style in exactly one look", () => {
    const all = STYLE_LOOKS.flatMap((l) => l.styles)
    expect(all).toHaveLength(STYLES.length)
    expect(new Set(all).size).toBe(all.length)
    expect([...all].sort()).toEqual(STYLES.map((s) => s.id).sort())
  })

  it("maps the prototype's names to the product's ids", () => {
    expect(lookOf("ink")).toBe("neutral")
    expect(lookOf("pewter")).toBe("neutral")
    expect(lookOf("terracotta")).toBe("warm")
    expect(lookOf("slate")).toBe("violet")
    expect(lookOf("harbor-night")).toBe("dark")
    expect(lookOf("afterglow")).toBe("dark")
    expect(lookOf("nope")).toBeUndefined()
  })

  it("lists only dark-capable styles under Made for dark that are dark-leaning, and single-scheme dark styles are all there", () => {
    const dark = STYLE_LOOKS.find((l) => l.id === "dark")!.styles
    for (const s of STYLES.filter((x) => x.schemes.length === 1 && x.schemes[0] === "dark")) expect(dark).toContain(s.id)
  })
})

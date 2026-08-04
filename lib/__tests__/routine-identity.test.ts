import { describe, it, expect } from "vitest"

import { routineIcon, routineColor } from "@/lib/routine-identity"
import { getCrewIconDef, GRADIENT_PALETTES } from "@/lib/crew-icons"

// A routine has no stored icon, so one is derived from its slug. The
// derivation lives here rather than in a component because two surfaces
// use it — the sidebar row and the detail header — and the same routine
// showing two different icons would be worse than showing none.

describe("routineIcon / routineColor", () => {
  it("is stable: the same slug always resolves the same way", () => {
    const a = routineIcon("monthly-accounting-pack")
    const b = routineIcon("monthly-accounting-pack")
    expect(a).toBe(b)
    expect(routineColor("monthly-accounting-pack")).toBe(routineColor("monthly-accounting-pack"))
  })

  it("spreads a realistic workspace across the pools", () => {
    // The point is telling routines apart in a list of thirty. All-one
    // icon would be no better than the status dot we already had.
    const slugs = Array.from({ length: 30 }, (_, i) => `routine-${i}-example`)
    expect(new Set(slugs.map(routineIcon)).size).toBeGreaterThan(4)
    expect(new Set(slugs.map(routineColor)).size).toBeGreaterThan(3)
  })

  it("only ever names an icon the kit can render", () => {
    const slugs = Array.from({ length: 200 }, (_, i) => `slug-${i}`)
    for (const s of slugs) {
      const name = routineIcon(s)
      expect(getCrewIconDef(name), `${name} is not in the crew icon registry`).toBeTruthy()
    }
  })

  it("only ever names a palette the kit defines", () => {
    const ids = new Set(GRADIENT_PALETTES.map((p) => p.id))
    for (let i = 0; i < 200; i++) {
      expect(ids.has(routineColor(`slug-${i}`))).toBe(true)
    }
  })

  it("does not throw on the degenerate cases", () => {
    expect(() => routineIcon("")).not.toThrow()
    expect(() => routineColor("")).not.toThrow()
    expect(typeof routineIcon("")).toBe("string")
  })
})

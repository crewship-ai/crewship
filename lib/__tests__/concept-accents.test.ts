import { describe, it, expect } from "vitest"

import { CONCEPT_ICON } from "../concept-icons"
import { ACCENT, CONCEPT_ACCENT, accentFor } from "../concept-accents"

// =============================================================================
// The icon map and the accent map describe the same set of concepts. The whole
// point of `concept-icons.ts` was that a concept must not wear a different face
// per screen; an accent map that has drifted out of step with it reintroduces
// exactly that, one property down.
// =============================================================================

describe("concept accents", () => {
  it("gives every concept in the icon map an accent", () => {
    for (const key of Object.keys(CONCEPT_ICON)) {
      expect(
        (CONCEPT_ACCENT as Record<string, string>)[key],
        `"${key}" has an icon but no accent`,
      ).toBeTruthy()
    }
  })

  it("does not name an accent for a concept that has no icon", () => {
    for (const key of Object.keys(CONCEPT_ACCENT)) {
      expect(
        (CONCEPT_ICON as Record<string, unknown>)[key],
        `"${key}" has an accent but no icon`,
      ).toBeTruthy()
    }
  })

  it("resolves every named accent to a real class set", () => {
    for (const [key, name] of Object.entries(CONCEPT_ACCENT)) {
      const tone = ACCENT[name]
      expect(tone, `"${key}" points at unknown accent "${name}"`).toBeTruthy()
      expect(tone.fg).toMatch(/^text-/)
      expect(tone.chip).toMatch(/^bg-/)
    }
  })

  it("keeps a rail group's concepts visually distinct", () => {
    // Colour only helps if adjacent rows differ. These are the four Build rows,
    // which is the group where two grey glyphs used to be indistinguishable.
    const build = ["crews", "skills", "credentials", "integrations"] as const
    const hues = new Set(build.map((c) => CONCEPT_ACCENT[c]))
    expect(hues.size).toBe(build.length)
  })

  it("gives a concept the same accent as the screen it opens", () => {
    // `runs` links to the journal and already borrows the journal's icon; a
    // different colour would break the promise the icon makes.
    expect(CONCEPT_ACCENT.runs).toBe(CONCEPT_ACCENT.journal)
    expect(CONCEPT_ACCENT.tools).toBe(CONCEPT_ACCENT.integrations)
  })

  it("falls back to the neutral accent rather than throwing", () => {
    // Callers pass strings that are not always concepts (a page title, a
    // provider key). Returning slate beats crashing a dialog header.
    expect(accentFor(undefined)).toBe(ACCENT.slate)
    expect(accentFor("not-a-concept")).toBe(ACCENT.slate)
    expect(accentFor("issues")).toBe(ACCENT[CONCEPT_ACCENT.issues])
  })

  it("uses no chart token", () => {
    // globals.css records that --chart-1..5 are identical between light and
    // dark (a known bug) and are a data-series scale, not a UI palette.
    for (const tone of Object.values(ACCENT)) {
      expect(`${tone.fg} ${tone.chip} ${tone.soft}`).not.toContain("chart")
    }
  })
})

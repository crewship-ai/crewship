import { describe, it, expect, beforeEach } from "vitest"
import { render, cleanup } from "@testing-library/react"
import { KeyRound } from "lucide-react"

import { ConceptIcon } from "../concept-icon"
import { ACCENT, CONCEPT_ACCENT } from "@/lib/concept-accents"

describe("ConceptIcon", () => {
  beforeEach(() => cleanup())

  it("colours a concept from its own accent", () => {
    const { container } = render(<ConceptIcon concept="crews" />)
    const svg = container.querySelector("svg")
    expect(svg).not.toBeNull()
    expect(svg!.getAttribute("class")).toContain(ACCENT[CONCEPT_ACCENT.crews].fg)
  })

  it("draws no chip in the default variant", () => {
    // Most icons in the product sit inline next to text, where a chip would be
    // a box around every word.
    const { container } = render(<ConceptIcon concept="issues" />)
    expect(container.querySelector("span")).toBeNull()
  })

  it("draws the tinted chip when asked", () => {
    const { container } = render(<ConceptIcon concept="issues" variant="chip" />)
    const chip = container.querySelector("span")
    expect(chip).not.toBeNull()
    expect(chip!.getAttribute("class")).toContain(ACCENT[CONCEPT_ACCENT.issues].chip)
  })

  it("lets an explicit accent win over the concept's own", () => {
    const { container } = render(<ConceptIcon concept="issues" accent="red" />)
    expect(container.querySelector("svg")!.getAttribute("class")).toContain(ACCENT.red.fg)
  })

  it("accepts a glyph that is not a product concept", () => {
    // Brand marks and one-off glyphs still need the colour and the sizing.
    const { container } = render(<ConceptIcon icon={KeyRound} accent="amber" />)
    const svg = container.querySelector("svg")
    expect(svg).not.toBeNull()
    expect(svg!.getAttribute("class")).toContain(ACCENT.amber.fg)
  })

  it("renders nothing when it has neither a concept nor a glyph", () => {
    // A caller that passes an unknown string should get a gap, not a crash in
    // a dialog header.
    const { container } = render(<ConceptIcon concept="not-a-concept" />)
    expect(container).toBeEmptyDOMElement()
  })

  it("scales the glyph with the chip", () => {
    const { container } = render(<ConceptIcon concept="skills" variant="chip" size="lg" />)
    expect(container.querySelector("span")!.getAttribute("class")).toContain("h-10")
    expect(container.querySelector("svg")!.getAttribute("class")).toContain("h-5")
  })
})

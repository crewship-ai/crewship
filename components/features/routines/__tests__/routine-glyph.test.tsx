import { describe, it, expect, afterEach } from "vitest"
import { render, cleanup } from "@testing-library/react"

import { RoutineGlyph } from "../routine-glyph"
import { getCrewDotColor } from "@/lib/crew-icons"
import { routineColor } from "@/lib/routine-identity"

afterEach(cleanup)

// A routine's colour is part of its identity, like a crew's: the list, the
// dashboard and the calendar paint the glyph in the routine's own colour,
// whether that colour was stored as a hex, a palette id, or derived from the
// slug when none was chosen.
const glyph = (container: HTMLElement) => container.querySelector<HTMLElement>('[data-slot="routine-glyph"]')!

describe("RoutineGlyph colour", () => {
  it.each([
    ["a stored hex", { slug: "coolify-ingest", color: "#22C55E" }, "#22C55E"],
    ["a palette id", { slug: "infra-sber", color: "green" }, getCrewDotColor("green")],
    ["no colour (derived from the slug)", { slug: "workspace-digest" }, getCrewDotColor(routineColor("workspace-digest"))],
  ])("tile tints with %s", (_, routine, want) => {
    const { container } = render(<RoutineGlyph routine={routine} />)
    expect(glyph(container).style.getPropertyValue("--ic").toLowerCase()).toBe(want.toLowerCase())
  })

  it("bare glyph is drawn in the routine colour", () => {
    const { container } = render(<RoutineGlyph routine={{ slug: "coolify-ingest", color: "#22C55E" }} variant="bare" />)
    expect(glyph(container).style.color.replace(/\s/g, "")).toMatch(/#22c55e|rgb\(34,197,94\)/i)
  })
})

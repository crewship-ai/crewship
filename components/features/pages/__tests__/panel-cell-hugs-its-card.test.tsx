import { readFileSync } from "node:fs"
import { join } from "node:path"

import { describe, expect, it } from "vitest"

// The arrival ring is painted on the grid CELL, and a grid cell stretches to
// the tallest panel in its row while the card inside keeps its natural height.
// So a short panel beside a tall one drew its "data just arrived" ring around
// the card *and* the empty space below it — a cue claiming something happened
// in a region where nothing did, which on a live page reads as a rendering
// fault rather than as a signal.
//
// One alignment token fixes it, which is exactly why it needs pinning: it is
// invisible in every state except the flash, so nothing else in the suite would
// notice a grid refactor dropping it. This reads the source rather than
// rendering, the same way panel-registry.test.tsx pins the
// no-dangerouslySetInnerHTML rule — the claim is about what the component
// declares, and a render test would need a full PageDetail fixture to assert
// one class name.
const SOURCE = readFileSync(
  join(process.cwd(), "components/features/pages/page-view.tsx"),
  "utf8",
)

describe("the cell the arrival cue is painted on hugs its card", () => {
  it("declares self-start on the panel cell", () => {
    const cell = SOURCE.slice(SOURCE.indexOf("function PanelCell"))
    const decl = cell.slice(0, cell.indexOf("</div>"))
    expect(decl).toContain("self-start")
  })

  it("does not stretch it back", () => {
    // `stretch` is the grid default, so naming it explicitly anywhere in this
    // component would restore the bug without removing the token above.
    expect(SOURCE).not.toContain("self-stretch")
    expect(SOURCE).not.toContain("items-stretch")
  })

  it("keeps the cue and the radius on the same box", () => {
    // A box-shadow follows the border radius of the box it is painted on, so
    // the cell has to carry the card's radius or the ring renders square
    // around a rounded card.
    const cell = SOURCE.slice(SOURCE.indexOf("function PanelCell"))
    const decl = cell.slice(0, cell.indexOf("</div>"))
    expect(decl).toContain("rounded-xl")
    expect(decl).toContain('data-panel-arrival')
  })
})

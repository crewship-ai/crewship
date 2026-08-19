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
//
// COMMENTS ARE STRIPPED FIRST, and that is not tidiness. The first version of
// this file searched the raw source, and PanelCell explains `self-start` in
// prose directly above the class list — so the assertion passed on the comment
// and would have kept passing if somebody deleted the class and left the
// paragraph describing it. A test that reads documentation is not a test.
const SOURCE = readFileSync(
  join(process.cwd(), "components/features/pages/page-view.tsx"),
  "utf8",
)

/** The source with `//` and block comments removed, so an assertion can only
 *  match code. Deliberately simple: this file has no string literal containing
 *  `//` or `/*`, and a regex that tried to handle that case would be a parser
 *  nobody reviews. */
function withoutComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:])\/\/.*$/gm, "$1")
}

/** PanelCell's body, comments removed. */
function panelCellCode(): string {
  const code = withoutComments(SOURCE)
  const start = code.indexOf("function PanelCell")
  expect(start, "PanelCell was renamed — update this test rather than deleting it").toBeGreaterThan(-1)
  const end = code.indexOf("</div>", start)
  expect(end, "PanelCell's element could not be found").toBeGreaterThan(start)
  return code.slice(start, end)
}

describe("the cell the arrival cue is painted on hugs its card", () => {
  it("declares self-start on the panel cell", () => {
    expect(panelCellCode()).toContain("self-start")
  })

  it("does not stretch it back", () => {
    // `stretch` is the grid default, so naming it explicitly anywhere in this
    // component would restore the bug without removing the token above.
    const code = withoutComments(SOURCE)
    expect(code).not.toContain("self-stretch")
    expect(code).not.toContain("items-stretch")
  })

  it("keeps the cue and the radius on the same box", () => {
    // A box-shadow follows the border radius of the box it is painted on, so
    // the cell has to carry the card's radius or the ring renders square
    // around a rounded card.
    const cell = panelCellCode()
    expect(cell).toContain("rounded-xl")
    expect(cell).toContain("data-panel-arrival")
  })

  it("is a test that reads code, not prose", () => {
    // The guard on the guard. If comment stripping ever stops working, the
    // three assertions above go back to passing on the paragraph that explains
    // the class — silently, and for as long as the paragraph survives.
    const stripped = withoutComments("const a = 1 // self-start lives here\n")
    expect(stripped).not.toContain("self-start")
  })
})

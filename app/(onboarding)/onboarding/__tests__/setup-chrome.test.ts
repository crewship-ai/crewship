import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import { join } from "node:path"

// Source-level, for the same reason app/(auth)/__tests__/auth-branding.test.ts
// is: these are layout properties of one 1100-line client component with a
// wizard's worth of data dependencies, and the thing worth pinning is the
// structural decision, not any one step's render.
//
// Each of these was a defect found by walking the real wizard on a freshly
// nuked instance, so each assertion carries the symptom it prevents.

const PAGE = readFileSync(
  join(process.cwd(), "app", "(onboarding)", "onboarding", "page.tsx"),
  "utf8"
)
const PREVIEW = readFileSync(
  join(process.cwd(), "components", "features", "onboarding", "onboarding-preview.tsx"),
  "utf8"
)

/**
 * The className of a column's own container — the first `<div className="…">`
 * after the marker comment, and nothing inside it.
 *
 * Scanning the whole block instead was the first version of this file and it
 * was wrong twice over: the form column contains plenty of legitimately
 * centred inner rows, and the explanatory comments quote the very class names
 * the assertions forbid.
 */
function columnClass(marker: string): string {
  const from = PAGE.indexOf(marker)
  expect(from, `${marker} moved or was renamed`).toBeGreaterThan(-1)
  const after = PAGE.slice(from)
  const open = after.indexOf("*/}")
  const match = after.slice(open).match(/<div className="([^"]+)"/)
  expect(match, `no container div follows ${marker}`).not.toBeNull()
  return match![1]
}

const leftColumn = () => columnClass("{/* LEFT: form")
const rightColumn = () => columnClass("{/* RIGHT: live preview")

describe("the setup wizard's chrome holds still", () => {
  it("anchors the form column to the top so the lockup cannot drift", () => {
    // Centring the column made the lockup and stepper slide as the step
    // content changed height — y=101 on Workspace, y=137 on Crew, y=66 on
    // Adapter. The logo visibly jumped on every Continue.
    const left = leftColumn()
    expect(left).toMatch(/flex items-start/)
    expect(left).not.toMatch(/flex items-center/)
  })

  it("anchors the preview column to the top for the same reason", () => {
    // The preview grows downward as you fill things in; centring made the
    // workspace card drift while it did.
    expect(rightColumn()).toMatch(/flex items-start/)
  })

  it("gives the preview a surface of its own", () => {
    // bg-muted/20 sat within a hair of the form column's background, so the
    // split read as one page with a hairline down it instead of two panes.
    const right = rightColumn()
    expect(right).not.toMatch(/bg-muted\/20/)
    expect(right).toMatch(/radial-gradient/)
  })
})

describe("the setup lockup wears the mark the sign-in screen wears", () => {
  it("uses the bare cropped mark, not the tile", () => {
    // Nested inside both the tile's padding and the viewBox's, the sails
    // stopped being legible at lockup size.
    expect(PAGE).toMatch(/<CrewshipLogo\s+tight/)
    expect(PAGE).not.toMatch(/CrewshipLogoTile/)
  })
})

describe("the preview's empty state is a promise, not a gap", () => {
  it("reserves the height of the card that lands there", () => {
    // A thin strip left the pane looking ~85% empty on step one — which
    // reads as a failed render — and the layout jumped when the real crew
    // card arrived. The floor is the header plus four agent rows.
    const empty = PREVIEW.slice(PREVIEW.indexOf('key="empty"'))
    expect(empty).toMatch(/min-h-\[24\d px?\]|min-h-\[248px\]/)
  })

  it("says what will land there rather than pointing at a control", () => {
    expect(PREVIEW).toMatch(/Your crew lands here/)
  })
})

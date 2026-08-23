import { describe, it, expect } from "vitest"
import { readFileSync, readdirSync, existsSync } from "node:fs"
import { join } from "node:path"

// Every unauthenticated page is a first impression, and two of them were
// wearing a generic lucide <Ship /> instead of the product mark. Login,
// signup and bootstrap used CrewshipLogoTile; reset-password and
// forgot-password did not — so the page an invited colleague lands on to
// set their password showed a stock sailboat.
//
// A source-level check rather than a render test: these are five separate
// route components with five different data dependencies, and the property
// worth pinning ("nobody hand-rolls a brand mark here") is about the import,
// not about any one page's runtime.

const AUTH_DIR = join(process.cwd(), "app", "(auth)")

function authPages(): { name: string; source: string }[] {
  return readdirSync(AUTH_DIR, { withFileTypes: true })
    .filter((e) => e.isDirectory() && !e.name.startsWith("__"))
    .map((e) => ({ name: e.name, file: join(AUTH_DIR, e.name, "page.tsx") }))
    .filter((p) => existsSync(p.file))
    .map((p) => ({ name: p.name, source: readFileSync(p.file, "utf8") }))
}

// A page satisfies the property either by rendering the tile itself or by
// sitting inside AuthSplitShell, which renders it in the lockup. The second
// route appeared when login moved to the split shell; the shell's own use of
// the tile is pinned below, so the chain stays checked end to end.
const SHELL = "AuthSplitShell"
const TILE = "CrewshipLogoTile"
// The shell shows the bare mark rather than the tile: nested inside both the
// tile's padding and the viewBox's, the sails stop being readable at lockup
// size. Either component satisfies the property — both come from the shared
// branding module, which is the thing being pinned.
const SHARED_MARK = /Crewship(Logo|LogoTile)\b/

describe("auth pages wear the product mark", () => {
  const pages = authPages()

  it("finds the auth routes at all (guards against a silent rename)", () => {
    expect(pages.length).toBeGreaterThanOrEqual(4)
  })

  it.each(pages.map((p) => p.name))("%s uses the shared logo component", (name) => {
    const page = pages.find((p) => p.name === name)!
    expect(
      page.source.includes(TILE) || page.source.includes(SHELL),
      `${name} renders neither ${TILE} nor ${SHELL}`
    ).toBe(true)
  })

  it("the shared shell is what puts the mark on the pages that delegate to it", () => {
    const shell = readFileSync(
      join(process.cwd(), "components", "branding", "auth-split-shell.tsx"),
      "utf8"
    )
    expect(shell).toMatch(SHARED_MARK)
  })

  it("shows the mark cropped to its own bounds when it stands without a tile", () => {
    // Without `tight` the mark renders inside the tile's padding with no tile
    // around it, which is what made it read as a few grey pixels at 28px.
    const shell = readFileSync(
      join(process.cwd(), "components", "branding", "auth-split-shell.tsx"),
      "utf8"
    )
    // Matched as a whole element rather than as the literal "<CrewshipLogo ".
    // A prop moving to its own line — a formatter's decision, not an author's
    // — turned the guard off silently: the substring stopped matching, the
    // `if` never entered, and an un-cropped mark would have sailed through a
    // green test. A guard that a line break can disable is not a guard.
    const usages = shell.match(/<CrewshipLogo\b[\s\S]*?\/?>/g) ?? []
    for (const usage of usages) {
      expect(usage).toMatch(/\btight\b/)
    }
  })

  it.each(pages.map((p) => p.name))("%s does not hand-roll a lucide ship", (name) => {
    const page = pages.find((p) => p.name === name)!
    // The specific stand-in that shipped. A generic icon is worse than no
    // icon here: it reads as someone else's product.
    expect(page.source).not.toMatch(/\bShip\b/)
  })
})

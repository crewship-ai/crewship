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

// Reading JSX with a regex is what made the two previous versions of the
// `tight` guard unreliable, each in its own way: a substring on one line that
// a formatter could disable, then `[\s\S]*?\/?>` which stops at the first `>`
// — including the one in `onClick={() => …}` — and `\btight\b`, which is just
// as happy inside `className="tight-logo"`. Neither is a parse.
//
// This is: it walks the source tracking quotes and brace depth, so an element
// ends at the `>` that actually closes it and an attribute is a token in the
// attribute list rather than a substring anywhere inside it. Small enough to
// read, which matters more here than generality — a guard nobody can verify is
// the thing being guarded against.

// crewshipLogoUsages returns each `<CrewshipLogo …>` element as written.
// `<CrewshipLogoTile>` is a different component and is deliberately not one:
// the tile supplies its own padding, and `tight` would be wrong on it.
function crewshipLogoUsages(source: string): string[] {
  const usages: string[] = []
  const TAG = "<CrewshipLogo"
  for (let i = source.indexOf(TAG); i !== -1; i = source.indexOf(TAG, i + 1)) {
    // The tag name ends here, so `<CrewshipLogoTile` is not a match.
    if (/[A-Za-z0-9_$]/.test(source[i + TAG.length] ?? "")) continue
    const end = elementEnd(source, i + TAG.length)
    if (end === -1) continue
    usages.push(source.slice(i, end + 1))
  }
  return usages
}

// elementEnd finds the `>` closing the tag opened before `from`, skipping any
// that sit inside a string literal or a `{…}` expression.
function elementEnd(source: string, from: number): number {
  let depth = 0
  let quote: string | null = null
  for (let i = from; i < source.length; i++) {
    const ch = source[i]
    if (quote) {
      if (ch === "\\") i++
      else if (ch === quote) quote = null
      continue
    }
    if (ch === '"' || ch === "'" || ch === "`") quote = ch
    else if (ch === "{") depth++
    else if (ch === "}") depth--
    else if (ch === ">" && depth === 0) return i
  }
  return -1
}

// hasAttribute reports whether `name` appears as an attribute of the element,
// not merely as text somewhere inside it. Quoted values and brace expressions
// are blanked first, so `className="tight-logo"` and `title={tight}` do not
// count as the `tight` prop.
function hasAttribute(element: string, name: string): boolean {
  let out = ""
  let depth = 0
  let quote: string | null = null
  for (let i = 0; i < element.length; i++) {
    const ch = element[i]
    if (quote) {
      if (ch === "\\") i++
      else if (ch === quote) quote = null
      continue
    }
    if (ch === '"' || ch === "'" || ch === "`") {
      quote = ch
      continue
    }
    if (ch === "{") {
      depth++
      continue
    }
    if (ch === "}") {
      depth--
      continue
    }
    // A blank keeps token boundaries intact where an expression was removed.
    out += depth > 0 ? " " : ch
  }
  return new RegExp(`(^|\\s)${name}(\\s|=|/|>|$)`).test(out)
}

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
    const usages = crewshipLogoUsages(shell)
    // A guard that finds nothing must fail, not pass vacuously — the third
    // version of the same lesson this test keeps learning. An empty match list
    // means the component was renamed or the file restructured, which is
    // exactly when the property stops being checked.
    expect(usages.length).toBeGreaterThan(0)
    for (const usage of usages) {
      expect(
        hasAttribute(usage, "tight"),
        `<CrewshipLogo> without \`tight\`: ${usage}`
      ).toBe(true)
    }
  })

  it.each(pages.map((p) => p.name))("%s does not hand-roll a lucide ship", (name) => {
    const page = pages.find((p) => p.name === name)!
    // The specific stand-in that shipped. A generic icon is worse than no
    // icon here: it reads as someone else's product.
    expect(page.source).not.toMatch(/\bShip\b/)
  })
})

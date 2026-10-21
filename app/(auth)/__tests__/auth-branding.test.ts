import { describe, it, expect } from "vitest"
import { readFileSync, readdirSync, existsSync } from "node:fs"
import { join } from "node:path"

// Every unauthenticated page is a first impression. Two of them once wore a
// generic lucide <Ship /> instead of the product mark, and until Harbor 2 the
// six routes used two different shells. Now there is one: the auth card
// (components/branding/auth-card.tsx), which carries the lockup.
//
// A source-level check rather than a render test: these are six separate
// route components with six different data dependencies, and the properties
// worth pinning ("every page uses the one shell", "nobody hand-rolls a brand
// mark or a raw size here") are about the source, not about any one runtime.

const AUTH_DIR = join(process.cwd(), "app", "(auth)")

function authPages(): { name: string; source: string }[] {
  return readdirSync(AUTH_DIR, { withFileTypes: true })
    .filter((e) => e.isDirectory() && !e.name.startsWith("__"))
    .map((e) => ({ name: e.name, file: join(AUTH_DIR, e.name, "page.tsx") }))
    .filter((p) => existsSync(p.file))
    .map((p) => ({ name: p.name, source: readFileSync(p.file, "utf8") }))
}

// Every page renders inside AuthScreen, which draws the lockup; the card's own
// use of the mark is pinned below, so the chain stays checked end to end.
const SHELL = "AuthScreen"
const CARD_FILE = join(process.cwd(), "components", "branding", "auth-card.tsx")
// The lockup shows the bare mark rather than the tile: nested inside both the
// tile's padding and the viewBox's, the sails stop being readable at lockup
// size.
const SHARED_MARK = /CrewshipLogo\b/
// Harbor 2 token rules (lib/__tests__/design-token-guard.test.ts) at zero for
// these files, plus the hex gradient and clamp() sizes the old shell carried
// (a colour has a letter in it; an issue reference like #3410 does not).
const RAW = [/\btext-\[/, /\brounded(?:-[trblse]{1,2})?-\[/, /\brounded(?:-[trblse]{1,2})?-(?:2xl|3xl)\b/, /#(?=[0-9]*[a-fA-F])[0-9a-fA-F]{3,8}\b/, /\bclamp\(/]

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
  const card = readFileSync(CARD_FILE, "utf8")

  it("finds the auth routes at all (guards against a silent rename)", () => {
    expect(pages.length).toBeGreaterThanOrEqual(6)
  })

  it.each(pages.map((p) => p.name))("%s renders in the shared auth card", (name) => {
    const page = pages.find((p) => p.name === name)!
    expect(page.source, `${name} does not render ${SHELL}`).toContain(SHELL)
    expect(page.source).toContain("@/components/branding/auth-card")
    // The retired two-pane shell and the shadcn Card it competed with.
    expect(page.source).not.toMatch(/AuthSplitShell|@\/components\/ui\/card/)
  })

  it.each(pages.map((p) => p.name))("%s draws the sail panel beside the form", (name) => {
    // Owner decision 2026-10-18 (#3583): the animated sails from main stay on
    // every auth screen. A page without `panel` would fall back to the plain card.
    const page = pages.find((p) => p.name === name)!
    for (const tag of page.source.match(/<AuthScreen\b[^>]*>/g) ?? []) {
      expect(tag, `${name}: ${tag}`).toMatch(/\bpanel="/)
    }
  })

  it("the panel is the animated mark", () => {
    expect(card).toMatch(/<AnimatedMark\b/)
  })

  it("the auth card is what puts the mark on the pages", () => {
    expect(card).toMatch(SHARED_MARK)
  })

  it("shows the mark cropped to its own bounds when it stands without a tile", () => {
    // Without `tight` the mark renders inside the tile's padding with no tile
    // around it, which is what made it read as a few grey pixels at 28px.
    const usages = crewshipLogoUsages(card)
    // A guard that finds nothing must fail, not pass vacuously — an empty
    // match list means the component was renamed or the file restructured,
    // which is exactly when the property stops being checked.
    expect(usages.length).toBeGreaterThan(0)
    for (const usage of usages) {
      expect(hasAttribute(usage, "tight"), `<CrewshipLogo> without \`tight\`: ${usage}`).toBe(true)
    }
  })

  it.each(pages.map((p) => p.name))("%s does not hand-roll a lucide ship", (name) => {
    const page = pages.find((p) => p.name === name)!
    // The specific stand-in that shipped. A generic icon is worse than no
    // icon here: it reads as someone else's product.
    expect(page.source).not.toMatch(/\bShip\b/)
  })

  it.each([...pages.map((p) => [p.name, p.source]), ["auth-card", card]])(
    "%s uses tokens only (no raw sizes, radii, hex or clamp)",
    (_name, source) => {
      for (const rx of RAW) expect(source).not.toMatch(rx)
    },
  )
})

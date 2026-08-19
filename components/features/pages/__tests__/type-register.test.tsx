/**
 * The Pages type register — `app/globals.css`, "The Pages register".
 *
 * Pages had left the type system without saying so: `text-[11px]` written out
 * in eleven files, `text-[10px]` in three — under the 11px `--typo-micro`
 * documents as *"smallest allowed"* — plus two different 14px idioms for the
 * same job in two panels. `globals.css` names that exact failure in the comment
 * above the navigation register: *"the moment a surface spells out
 * `text-[0.8125rem]` for itself, it has left the system, and the next surface
 * copies the number instead of the reason."*
 *
 * These tests are the fence around the repair, and they are deliberately not
 * written against pixels. Three things have to stay true:
 *
 *  1. **The register exists and is made of house tokens.** A role that spells a
 *     length out is the original defect wearing a class name; a `var(--typo-*)`
 *     moves when the product's scale moves.
 *  2. **Its value and label roles still agree with `PropertyRow`**, the
 *     product's canonical label/value pair — one scale, checked through the CSS
 *     rather than by comparing two class strings, so a rename on either side
 *     cannot make the test lie.
 *  3. **No Pages source spells a size out again.** Read off the source, the way
 *     `panel-registry.test.tsx` greps for `dangerouslySetInnerHTML`, because
 *     the rule is about what may be WRITTEN — a behavioural test only sees the
 *     components somebody remembered to render.
 */
import { describe, it, expect } from "vitest"
import { render } from "@testing-library/react"

import { PropertyRow } from "@/components/layout/property-row"
import {
  PAGE_TYPE_ROLES,
  declaration,
  fontSizeToken,
  pagesSources,
  remToPx,
  sizeUtility,
  tailwindSizeToken,
  typoValue,
} from "./type-register"

describe("the Pages register is declared once, in house tokens", () => {
  it.each(PAGE_TYPE_ROLES)("%s exists and is sized from a --typo-* token", (role) => {
    expect(fontSizeToken(role)).toMatch(/^--typo-[a-z]+$/)
  })

  /**
   * The floor, stated as a number exactly once — here, against the token's own
   * declared value. `--typo-micro` is commented "11px – smallest allowed", and
   * the sub-floor `text-[10px]` that used to sit in the rail and on three
   * badges is the reason this test is not "every role is a token" alone.
   */
  it.each(PAGE_TYPE_ROLES)("%s sits at or above the documented 11px floor", (role) => {
    const px = remToPx(typoValue(fontSizeToken(role)!))
    expect(px).toBeGreaterThanOrEqual(remToPx(typoValue("--typo-micro")))
  })

  /**
   * Two roles are AT the floor, and both are text you find rather than text you
   * read line by line: the uppercase label you aim at, and the machine stamp
   * under a panel. Everything that carries a sentence is above it. Pinning that
   * split here is what stops the next edit from quietly putting a hint or a
   * status word back at 11px "to match the label".
   */
  it("keeps only the label and the stamp at the floor", () => {
    const floor = remToPx(typoValue("--typo-micro"))
    const atFloor = PAGE_TYPE_ROLES.filter(
      (role) => remToPx(typoValue(fontSizeToken(role)!)) === floor,
    )
    expect([...atFloor].sort()).toEqual(["type-page-label", "type-page-stamp"])
  })

  it("carries the label's case and the stamp's family in the register, not at the call site", () => {
    // "12px semibold uppercase wide-tracked muted" was always the rule, and
    // leaving three of those four to memory is how the surfaces drifted —
    // globals.css says so above the roles. The register carries all of it.
    expect(declaration("type-page-label", "text-transform")).toBe("uppercase")
    expect(declaration("type-page-label", "letter-spacing")).toBeTruthy()
    expect(declaration("type-page-label", "font-weight")).toBeTruthy()
    expect(declaration("type-page-stamp", "font-family")).toBe("var(--font-mono)")
    expect(declaration("type-page-metric", "font-variant-numeric")).toBe("tabular-nums")
  })
})

/**
 * The parity that `table-panel.test.tsx` used to assert by comparing class
 * strings against `PropertyRow`'s DOM. It is asserted here instead, and one
 * level deeper: the card list now names the register's roles rather than the
 * house utilities, so a string comparison would only prove the two files spell
 * things the same way. What actually has to hold is that they resolve to the
 * same `--typo-*` token — which survives a rename on either side and fails, as
 * it should, the moment somebody gives Pages a scale of its own.
 */
describe("the register's content roles are PropertyRow's, through the tokens", () => {
  function houseTokens(): { label: string; value: string } {
    const { container } = render(<PropertyRow label="house">value</PropertyRow>)
    const row = container.firstElementChild!
    // The row carries the value size; the label overrides it on its own child.
    const label = sizeUtility(row.children[0]) ?? sizeUtility(row)!
    const value = sizeUtility(row.children[1]) ?? sizeUtility(row)!
    return { label: tailwindSizeToken(label)!, value: tailwindSizeToken(value)! }
  }

  it("sizes the value role from the same token as PropertyRow's value", () => {
    expect(fontSizeToken("type-page-value")).toBe(houseTokens().value)
  })

  it("sizes the meta role from the same token as PropertyRow's label", () => {
    expect(fontSizeToken("type-page-meta")).toBe(houseTokens().label)
  })
})

/**
 * The 14px question, settled. Two idioms for one job had grown inside Pages:
 * `.type-row` (14px / 1.3rem) in `status-panel.tsx` and `text-body`
 * (14px / 1.25rem) in the collapsed table cards. `text-body` won — it is the
 * token that tracks `--typo-body`, where `.type-row` hard-codes 0.875rem and
 * would sit still through a scale change, and it is what `PropertyRow` is
 * written in.
 */
describe("Pages has exactly one 14px", () => {
  it("writes its value role in --typo-body, not in a literal", () => {
    expect(fontSizeToken("type-page-value")).toBe("--typo-body")
    expect(declaration("type-page-value", "line-height")).toBe("var(--typo-body-lh)")
  })

  it("uses no other 14px idiom anywhere in Pages", () => {
    const offenders = pagesSources()
      .filter((s) => /\btype-row\b/.test(s.code))
      .map((s) => s.file)
    expect(offenders, `type-row in ${offenders.join(", ")}`).toEqual([])
  })
})

/**
 * The lint. Arbitrary values only: `text-xs` and friends are a SHARED scale a
 * surface can be asked to move off, whereas `text-[11px]` is a private one
 * nothing can reach — and it is the form globals.css names as the moment a
 * surface leaves the system. Sixty-two of them existed across ten files when
 * the register was declared.
 */
describe("no Pages source spells a font size out (globals.css, the register comment)", () => {
  const sources = pagesSources()

  it("scans the whole feature, not one directory of it", () => {
    const files = sources.map((s) => s.file)
    expect(files.length).toBeGreaterThanOrEqual(18)
    expect(files.some((f) => f.includes("/panels/"))).toBe(true)
    expect(files.some((f) => f.includes("/public/"))).toBe(true)
    expect(files).toContain("components/features/pages/panels/table-panel.tsx")
  })

  it("carries no arbitrary text size", () => {
    const offenders = sources
      .filter((s) => /\btext-\[[\d.]+(px|rem|em)\]/.test(s.code))
      .map((s) => s.file)
    expect(offenders, `arbitrary text size in ${offenders.join(", ")}`).toEqual([])
  })

  it("carries no arbitrary leading either — leading is half of a size", () => {
    const offenders = sources
      .filter((s) => /\bleading-\[[\d.]+(px|rem|em)?\]/.test(s.code))
      .map((s) => s.file)
    expect(offenders, `arbitrary leading in ${offenders.join(", ")}`).toEqual([])
  })

  /**
   * The register is only worth having if it is used. A Pages source that
   * carries no role at all is either chrome-free or has quietly gone back to
   * inheriting whatever its parent happened to be.
   */
  it("draws its type through the roles", () => {
    const users = sources.filter((s) => /\btype-page-[a-z]+\b/.test(s.code))
    expect(users.length).toBeGreaterThanOrEqual(10)
  })
})

import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import path from "node:path"

import { buttonVariants } from "@/components/ui/button"

// Harbor 2 control sizes and touch targets (#3333).
// Heights are 24 / 28 / 32 px at Compact (13 px text, a 13/14 root) and,
// being rem, scale with the user's text size like everything else.
// With a coarse pointer every control gets at least a 44 × 44 px target,
// whatever the window width (a tablet is a finger too).

const css = readFileSync(path.resolve(__dirname, "../../../app/globals.css"), "utf8")
const COMPACT_ROOT_PX = 16 * 0.92857

describe("control height tokens", () => {
  it.each([
    ["control-xs", 24],
    ["control-sm", 28],
    ["control-md", 32],
  ])("--%s is %i px at Compact", (name, px) => {
    const m = css.match(new RegExp(`--${name}:\\s*([\\d.]+)rem;`))
    expect(m, `--${name} defined in rem`).toBeTruthy()
    expect(Number((m as RegExpMatchArray)[1]) * COMPACT_ROOT_PX).toBeCloseTo(px, 0)
  })

  it("buttons use the tokens", () => {
    expect(buttonVariants({ size: "default" })).toContain("h-[var(--control-md)]")
    expect(buttonVariants({ size: "sm" })).toContain("h-[var(--control-sm)]")
    expect(buttonVariants({ size: "xs" })).toContain("h-[var(--control-xs)]")
    expect(buttonVariants({ size: "icon" })).toContain("size-[var(--control-md)]")
  })

  it("the primary button is flat: no glow, no lift; hover goes deeper", () => {
    const pri = buttonVariants({ variant: "default" })
    expect(pri).not.toMatch(/shadow-\[|translate-y/)
    expect(pri).toContain("hover:bg-primary-strong-hover")
  })

  it("inputs and select triggers share the field height", () => {
    const input = readFileSync(path.resolve(__dirname, "../../../components/ui/input.tsx"), "utf8")
    const select = readFileSync(path.resolve(__dirname, "../../../components/ui/select.tsx"), "utf8")
    expect(input).toContain("h-[var(--control-md)]")
    expect(select).toContain("data-[size=default]:h-[var(--control-md)]")
    expect(select).toContain("data-[size=sm]:h-[var(--control-sm)]")
  })
})

describe("coarse pointer targets", () => {
  const coarse = [...css.matchAll(/@media \(pointer: coarse\) \{([\s\S]*?)\n\}/g)].map((m) => m[1]).join("\n")

  it("every button-like control, field and select gets min-height 44px", () => {
    expect(coarse).toMatch(/:where\([^)]*button[^{]*\{\s*min-height:\s*44px;/)
    for (const role of ["button", "tab", "menuitem", "option", "combobox"]) expect(coarse).toContain(`[role="${role}"]`)
  })

  it("links laid out as boxes (card links, list rows) are 44px tall too; inline text links are unaffected", () => {
    const rule = /:where\(([^{]*)\)[^{]*\{\s*min-height:\s*44px;/.exec(coarse)
    expect(rule?.[1]).toContain("a[href]")
  })

  it("buttons are at least 44px wide, but checkboxes, radios and switches are not stretched", () => {
    expect(coarse).toMatch(/min-width:\s*44px;/)
    expect(coarse).toMatch(/:not\(\[role="checkbox"\], \[role="radio"\], \[role="switch"\]\)/)
  })

  it("checkboxes and radios get a 44px hit area without growing", () => {
    expect(coarse).toMatch(/\[role="checkbox"\][^{]*::after|::after[^{]*\[role="checkbox"\]/)
    expect(coarse).toMatch(/width:\s*44px;\s*height:\s*44px;/)
  })
})

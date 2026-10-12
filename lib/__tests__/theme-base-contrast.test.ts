import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import path from "node:path"

import { ACCENTS, accentBlock, type AccentId } from "@/lib/theme/accents"
import { BASES, BASE_TOKENS, DEFAULT_BASE, baseBlock, type BaseId } from "@/lib/theme/bases"
import { blend, contrast, hexToRgb, oklchToRgb, type Rgb } from "@/lib/theme/derive-accent"

// Every non-default base palette (app/styles/bases.css), under every preset
// accent, in both modes, held to the floors theme-contrast.test.ts holds the
// default (Cool) ground to. A base only replaces neutral tokens; anything it
// does not define falls through to globals.css, which this resolves the same
// way the browser does.

const root = path.resolve(__dirname, "../..")
const globals = readFileSync(path.join(root, "app/globals.css"), "utf8")
const accentsCss = readFileSync(path.join(root, "app/styles/accents.css"), "utf8")
const basesCss = readFileSync(path.join(root, "app/styles/bases.css"), "utf8")

type Mode = "light" | "dark"
const GLOBAL_BLOCKS: Record<Mode, [string, string]> = {
  light: [":root {\n  /* ── Light surfaces", "\n.dark {"],
  dark: ["\n.dark {", "@theme inline"],
}

function globalToken(mode: Mode, name: string): string {
  const [from, to] = GLOBAL_BLOCKS[mode]
  const start = globals.indexOf(from)
  const block = globals.slice(start, globals.indexOf(to, start + 1))
  const m = block.match(new RegExp(`--${name}:\\s*([^;]+);`))
  expect(m, `--${name} in globals ${mode}`).toBeTruthy()
  return (m as RegExpMatchArray)[1].trim()
}

function resolve(mode: Mode, base: BaseId, accent: AccentId, name: string): Rgb {
  const fromBase = baseBlock(basesCss, base, mode).get(name)
  let value = fromBase ?? globalToken(mode, name)
  const ref = value.match(/^var\(--(brand[a-z-]*)\)$/)
  if (ref) value = accentBlock(accentsCss, accent, mode).get(ref[1]) as string
  if (/^#[0-9a-f]{6}$/i.test(value)) return hexToRgb(value)
  const ok = value.match(/^oklch\(([\d.]+)\s+([\d.]+)\s+([\d.]+)\)$/)
  expect(ok, `--${name} (${base}/${mode}) is hex or simple oklch: ${value}`).toBeTruthy()
  const [, l, c, h] = ok as RegExpMatchArray
  return oklchToRgb(Number(l), Number(c), Number(h))
}

const OTHER_BASES = BASES.map((b) => b.id).filter((id) => id !== DEFAULT_BASE)

describe("base registry", () => {
  it("starts with the default, Cool, which has no block of its own", () => {
    expect(BASES[0].id).toBe(DEFAULT_BASE)
    expect(baseBlock(basesCss, DEFAULT_BASE, "light").size).toBe(0)
    expect(baseBlock(basesCss, DEFAULT_BASE, "dark").size).toBe(0)
  })

  it.each(OTHER_BASES)("%s defines every base token in light AND dark", (id) => {
    // A token defined only in light would leak into dark: the light block
    // (0,2,0) outranks globals' .dark (0,1,0).
    for (const mode of ["light", "dark"] as const) {
      expect([...baseBlock(basesCss, id, mode).keys()].sort(), `${id} ${mode}`).toEqual([...BASE_TOKENS].sort())
    }
  })

  it("the preview swatches are the palette's own ground and card", () => {
    for (const b of BASES.filter((x) => x.id !== DEFAULT_BASE)) {
      for (const mode of ["light", "dark"] as const) {
        const block = baseBlock(basesCss, b.id, mode)
        expect([block.get("background"), block.get("card")]).toEqual([...b[mode]])
      }
    }
  })
})

const CASES = OTHER_BASES.flatMap((base) =>
  ACCENTS.flatMap((a) => (["light", "dark"] as const).map((mode) => [base, a.id, mode] as const)),
)

describe.each(CASES)("base %s · accent %s · %s — WCAG AA", (base, accent, mode) => {
  const rgb = (name: string) => resolve(mode, base, accent, name)
  const grounds = ["background", "card"] as const

  it("foreground and muted text on background and card ≥ 4.5:1", () => {
    for (const fg of ["foreground", "card-foreground", "muted-foreground", "muted-foreground-soft"]) {
      for (const g of grounds) expect(contrast(rgb(fg), rgb(g)), `${fg} on ${g}`).toBeGreaterThanOrEqual(4.5)
    }
  })

  it("primary and links (primary-hover) on background and card ≥ 4.5:1", () => {
    for (const fg of ["primary", "primary-hover"]) {
      for (const g of grounds) expect(contrast(rgb(fg), rgb(g)), `${fg} on ${g}`).toBeGreaterThanOrEqual(4.5)
    }
  })

  it("form control borders against the ground ≥ 3:1 (WCAG 1.4.11)", () => {
    for (const g of grounds) expect(contrast(rgb("control-border"), rgb(g)), `control-border on ${g}`).toBeGreaterThanOrEqual(3)
  })

  it("brand chip text on bg-primary/15–25 over the ground ≥ 4.5:1", () => {
    if (mode === "light") return // light chips use the opaque brand-tint, checked in theme-contrast
    for (const [alpha, g] of [[0.15, "card"], [0.2, "card"], [0.25, "card"], [0.25, "background"]] as const) {
      expect(contrast(rgb("primary-hover"), blend(rgb("primary"), alpha, rgb(g))), `on primary/${alpha * 100} over ${g}`).toBeGreaterThanOrEqual(4.5)
    }
  })
})

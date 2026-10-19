import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import path from "node:path"

import { ACCENTS, accentBlock, isAccentId, type AccentId, type StyleAccentId } from "@/lib/theme/accents"
import { STYLES, HARBOR } from "@/lib/theme/appearance-style"

// STY-04 (PRD): every style passes the same AA gate as Harbor, or it does not
// ship. lib/__tests__/theme-contrast.test.ts holds Harbor itself; this holds
// each style in app/styles/styles.css, in each scheme it supports, with every
// accent the user can put on it. A style is its token block laid over
// Harbor's: a token the block does not set is Harbor's.

const root = path.resolve(__dirname, "../..")
const globals = readFileSync(path.join(root, "app/globals.css"), "utf8")
const stylesCss = readFileSync(path.join(root, "app/styles/styles.css"), "utf8")
const accentsCss = readFileSync(path.join(root, "app/styles/accents.css"), "utf8")

const lin = (c: number) => (c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4))
type RGB = [number, number, number]
const lum = ([r, g, b]: RGB) => 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b)
const hex = (h: string): RGB => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16) / 255) as RGB
function oklch(l: number, c: number, hd: number): RGB {
  const h = (hd * Math.PI) / 180
  const a = c * Math.cos(h)
  const b = c * Math.sin(h)
  const l_ = l + 0.3963377774 * a + 0.2158037573 * b
  const m_ = l - 0.1055613458 * a - 0.0638541728 * b
  const s_ = l - 0.0894841775 * a - 1.291485548 * b
  const [L, M, S] = [l_ ** 3, m_ ** 3, s_ ** 3]
  const g = (x: number) => {
    const v = Math.min(1, Math.max(0, x))
    return v <= 0.0031308 ? 12.92 * v : 1.055 * Math.pow(v, 1 / 2.4) - 0.055
  }
  return [g(4.0767416621 * L - 3.3077115913 * M + 0.2309699292 * S), g(-1.2684380046 * L + 2.6097574011 * M - 0.3413193965 * S), g(-0.0041960863 * L - 0.7034186147 * M + 1.707614701 * S)]
}
function rgbOf(value: string): RGB {
  if (/^#[0-9a-fA-F]{6}$/.test(value)) return hex(value)
  const m = value.match(/^oklch\(([\d.]+)\s+([\d.]+)\s+([\d.]+)\)$/)
  if (!m) throw new Error(`not a simple colour: ${value}`)
  return oklch(Number(m[1]), Number(m[2]), Number(m[3]))
}
const ratio = (a: RGB, b: RGB) => {
  const [x, y] = [lum(a), lum(b)]
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05)
}
const blend = (fg: RGB, alpha: number, bg: RGB): RGB => fg.map((v, i) => alpha * v + (1 - alpha) * bg[i]) as RGB

function declarations(body: string): Map<string, string> {
  const out = new Map<string, string>()
  for (const m of body.matchAll(/--([a-z0-9-]+):\s*([^;]+);/g)) out.set(m[1], m[2].trim())
  return out
}
const baseBlock = (from: string, to: string) => {
  const start = globals.indexOf(from)
  return declarations(globals.slice(start, globals.indexOf(to, start + 1)))
}
const BASE = {
  light: baseBlock(":root {\n  /* ── Light surfaces", "\n.dark {"),
  dark: baseBlock("\n.dark {", "@theme inline"),
}

function styleBlock(id: string, scheme: "light" | "dark"): Map<string, string> {
  const selector = scheme === "dark" ? `.dark[data-style="${id}"]` : `[data-style="${id}"]:not(.dark)`
  const bare = stylesCss.replace(/\/\*[\s\S]*?\*\//g, "")
  const found = [...bare.matchAll(/([^{}]+)\{([^{}]*)\}/g)].find(([, sel]) => sel.trim() === selector)
  return found ? declarations(found[2]) : new Map()
}

const SURFACES = ["background", "sidebar", "card", "muted", "surface-subtle", "surface-raised"]
const TEXT = ["foreground", "muted-foreground", "muted-foreground-soft", "success", "warn", "destructive"]
const SEMANTIC_ON_CARD = ["info", "notice", "gold", "indigo", "rose", "lime", "orange", "azure", "purple-hover"]

const shipped = STYLES.filter((s) => s.id !== HARBOR.id)

describe("the shipped styles", () => {
  it("there is a first batch beyond Harbor", () => {
    expect(shipped.length).toBeGreaterThanOrEqual(12)
  })
})

describe.each(shipped.flatMap((s) => s.schemes.map((scheme) => [s.id, scheme, s.accent] as const)))("style %s — %s", (id, scheme, ownAccent) => {
  const own = styleBlock(id, scheme)
  const tokens = new Map([...BASE[scheme], ...own])
  const get = (name: string): RGB => rgbOf(tokens.get(name) ?? (() => { throw new Error(`--${name} missing`) })())
  const fg = () => get("foreground")
  const hovered = (surface: string) => blend(fg(), 0.04, get(surface))

  it("defines its own palette block", () => {
    for (const name of ["sidebar", "background", "card", "muted", "foreground", "muted-foreground", "muted-foreground-soft", "icon-faint", "control-border", "border", "line-strong", "nav-selected"]) {
      expect(own.has(name), `--${name}`).toBe(true)
    }
  })

  it.each(TEXT)("text %s ≥ 4.5:1 on every surface and on its hover", (text) => {
    for (const s of SURFACES) {
      expect(ratio(get(text), get(s)), `${text} on ${s}`).toBeGreaterThanOrEqual(4.5)
      expect(ratio(get(text), hovered(s)), `${text} on hovered ${s}`).toBeGreaterThanOrEqual(4.5)
    }
  })

  it("semantic and concept text colours ≥ 4.5:1 on the card", () => {
    for (const name of SEMANTIC_ON_CARD) expect(ratio(get(name), get("card")), name).toBeGreaterThanOrEqual(4.5)
    for (const tone of ["ok", "warn", "danger", "violet"]) expect(ratio(get(`chip-${tone}-fg`), get("card")), `chip ${tone}`).toBeGreaterThanOrEqual(4.5)
  })

  it("icons, control borders ≥ 3:1 and chart series ≥ 3:1 on every surface", () => {
    for (const s of SURFACES) {
      expect(ratio(get("icon-faint"), get(s)), `icon-faint on ${s}`).toBeGreaterThanOrEqual(3)
      expect(ratio(get("control-border"), get(s)), `control-border on ${s}`).toBeGreaterThanOrEqual(3)
      for (let i = 1; i <= 8; i++) expect(ratio(get(`series-${i}`), get(s)), `series-${i} on ${s}`).toBeGreaterThanOrEqual(3)
    }
  })

  it("banner text on the warn and ok fills ≥ 4.5:1", () => {
    for (const tone of ["warn", "ok"]) expect(ratio(fg(), get(`chip-${tone}-bg`)), tone).toBeGreaterThanOrEqual(4.5)
  })

  // Every picker accent, plus the hidden accent this style carries.
  it.each([...ACCENTS.map((a) => a.id as string), ...(isAccentId(ownAccent) ? [] : [ownAccent as string])])("accent %s: brand text on background and card, text on the selection tint ≥ 4.5:1", (accent) => {
    const brand = accentBlock(accentsCss, accent as AccentId | StyleAccentId, scheme)
    const rgb = (n: string) => rgbOf(brand.get(n)!)
    for (const n of ["brand", "brand-ink"]) {
      for (const s of ["background", "card"]) expect(ratio(rgb(n), get(s)), `${n} on ${s}`).toBeGreaterThanOrEqual(4.5)
    }
    const tint = rgb("brand-tint")
    for (const text of TEXT) expect(ratio(get(text), tint), `${text} on selection`).toBeGreaterThanOrEqual(4.5)
    expect(ratio(fg(), tint), "info banner").toBeGreaterThanOrEqual(4.5)
  })
})

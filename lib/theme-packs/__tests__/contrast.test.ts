import { describe, it, expect } from "vitest"
import { PACKS } from "@/lib/theme-packs"

// Every pack holds the same AA bar as the built-in styles (STY-04): text 4.5:1
// on every surface it can sit on, icons and control borders 3:1. A pack that
// fails does not ship. Afterglow's measured figures from the design are pinned
// as well, so a token edit cannot quietly lower them.

type RGB = [number, number, number]
const lin = (c: number) => (c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4))
const hex = (h: string): RGB => {
  if (!/^#[0-9A-Fa-f]{6}$/.test(h)) throw new Error(`not a plain hex colour: ${h}`)
  return [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16) / 255) as RGB
}
const lum = ([r, g, b]: RGB) => 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b)
const ratio = (a: string, b: string) => {
  const [x, y] = [lum(hex(a)), lum(hex(b))]
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05)
}

const SURFACES = ["background", "sidebar", "card", "popover", "muted", "surface-subtle", "surface-raised"]
const TEXT = ["foreground", "muted-foreground", "muted-foreground-soft", "success", "warn", "destructive", "primary-hover", "chip-active-fg"]

describe.each(PACKS.flatMap((p) => p.schemes.map((s) => [p.id, s] as const)))("pack %s — %s", (id, scheme) => {
  const t = PACKS.find((p) => p.id === id)!.tokens[scheme]!
  const get = (n: string) => t[n] ?? (() => { throw new Error(`--${n} missing`) })()

  it.each(TEXT)("text %s ≥ 4.5:1 on every surface", (text) => {
    for (const s of SURFACES) expect(ratio(get(text), get(s)), `${text} on ${s}`).toBeGreaterThanOrEqual(4.5)
  })

  it("primary label ≥ 4.5:1 on the primary fill and its hover; destructive label on its fill", () => {
    for (const fill of ["primary", "primary-strong", "primary-strong-hover", "sidebar-primary"]) {
      expect(ratio(get(fill === "sidebar-primary" ? "sidebar-primary-foreground" : "primary-foreground"), get(fill)), fill).toBeGreaterThanOrEqual(4.5)
    }
    expect(ratio(get("destructive-foreground"), get("destructive"))).toBeGreaterThanOrEqual(4.5)
  })

  it("every chip's text ≥ 4.5:1 on its own fill and on the card", () => {
    for (const tone of ["ok", "warn", "danger", "neutral", "info", "violet"]) {
      expect(ratio(get(`chip-${tone}-fg`), get(`chip-${tone}-bg`)), `chip ${tone}`).toBeGreaterThanOrEqual(4.5)
    }
    for (const tone of ["ok", "warn", "danger", "violet", "neutral"]) expect(ratio(get(`chip-${tone}-fg`), get("card")), `chip ${tone} on card`).toBeGreaterThanOrEqual(4.5)
  })

  it("text on selection, hover and accent fills ≥ 4.5:1", () => {
    for (const fill of ["selection-bg", "nav-selected", "accent", "sidebar-accent"]) {
      expect(ratio(get("foreground"), get(fill)), `foreground on ${fill}`).toBeGreaterThanOrEqual(4.5)
      expect(ratio(get("muted-foreground"), get(fill)), `muted-foreground on ${fill}`).toBeGreaterThanOrEqual(4.5)
    }
    expect(ratio(get("sidebar-accent-foreground"), get("sidebar-accent"))).toBeGreaterThanOrEqual(4.5)
  })

  it("icons, control borders, focus ring and chart series ≥ 3:1 on every surface", () => {
    for (const s of SURFACES) {
      for (const n of ["icon-faint", "control-border", "ring"]) expect(ratio(get(n), get(s)), `${n} on ${s}`).toBeGreaterThanOrEqual(3)
      for (let i = 1; i <= 5; i++) expect(ratio(get(`series-${i}`), get(s)), `series-${i} on ${s}`).toBeGreaterThanOrEqual(3)
    }
  })

  it("the status colours stay apart: working, waiting, idle and failed are four different tokens", () => {
    const four = [get("warn"), get("chip-active-fg"), get("chip-idle-fg"), get("destructive")]
    expect(new Set(four.map((c) => c.toUpperCase())).size).toBe(4)
  })
})

describe("Afterglow's measured figures (design canvas)", () => {
  const t = PACKS.find((p) => p.id === "afterglow")!.tokens.dark!
  it("text 15.5, lavender 10.4, muted 6.1, amber button 9.9 on Night; neon 6.8 on Ground", () => {
    const night = "#18182D"
    expect(ratio("#F4F0FF", night)).toBeGreaterThanOrEqual(15)
    expect(ratio("#CCC2F5", night)).toBeGreaterThanOrEqual(10)
    expect(ratio("#9C93C7", night)).toBeGreaterThanOrEqual(6)
    expect(ratio(t["primary-foreground"], t["primary"])).toBeGreaterThanOrEqual(9.5)
    expect(ratio("#FF5FA2", t["background"])).toBeGreaterThanOrEqual(6.5)
  })
})

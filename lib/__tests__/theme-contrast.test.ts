import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import path from "node:path"

// Guards the WCAG AA (4.5:1) contrast of the dark-theme brand tokens in
// app/globals.css. The app renders dark-only (<html className="dark">),
// so these pairs are exactly what axe's color-contrast rule measures in
// e2e/a11y.spec.ts — this test pins the same math at unit level so a
// token regression fails fast without a Playwright run.
//
// History: --primary-foreground used to be white on #1E7BFE (3.95:1),
// which forced the color-contrast axe rule to stay disabled.

const css = readFileSync(path.resolve(__dirname, "../../app/globals.css"), "utf8")

// ── minimal color math (sRGB + OKLCH → relative luminance) ──────────────

function srgbChannelToLinear(c: number): number {
  return c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4)
}

function luminanceFromRgb([r, g, b]: [number, number, number]): number {
  return (
    0.2126 * srgbChannelToLinear(r) +
    0.7152 * srgbChannelToLinear(g) +
    0.0722 * srgbChannelToLinear(b)
  )
}

function hexToRgb(hex: string): [number, number, number] {
  const h = hex.replace("#", "")
  return [
    parseInt(h.slice(0, 2), 16) / 255,
    parseInt(h.slice(2, 4), 16) / 255,
    parseInt(h.slice(4, 6), 16) / 255,
  ]
}

// OKLCH → linear sRGB (Björn Ottosson's reference transform).
function oklchToRgb(l: number, c: number, hDeg: number): [number, number, number] {
  const h = (hDeg * Math.PI) / 180
  const a = c * Math.cos(h)
  const b = c * Math.sin(h)
  const l_ = l + 0.3963377774 * a + 0.2158037573 * b
  const m_ = l - 0.1055613458 * a - 0.0638541728 * b
  const s_ = l - 0.0894841775 * a - 1.291485548 * b
  const L = l_ ** 3
  const M = m_ ** 3
  const S = s_ ** 3
  const rLin = 4.0767416621 * L - 3.3077115913 * M + 0.2309699292 * S
  const gLin = -1.2684380046 * L + 2.6097574011 * M - 0.3413193965 * S
  const bLin = -0.0041960863 * L - 0.7034186147 * M + 1.707614701 * S
  const toGamma = (x: number) => {
    const v = Math.min(1, Math.max(0, x))
    return v <= 0.0031308 ? 12.92 * v : 1.055 * Math.pow(v, 1 / 2.4) - 0.055
  }
  return [toGamma(rLin), toGamma(gLin), toGamma(bLin)]
}

function contrast(fgLum: number, bgLum: number): number {
  const [hi, lo] = fgLum > bgLum ? [fgLum, bgLum] : [bgLum, fgLum]
  return (hi + 0.05) / (lo + 0.05)
}

// Alpha-composite fg over bg in gamma sRGB space (how CSS resolves
// translucent backgrounds like bg-primary/15 before axe measures them).
function blend(
  fg: [number, number, number],
  alpha: number,
  bg: [number, number, number],
): [number, number, number] {
  return [
    alpha * fg[0] + (1 - alpha) * bg[0],
    alpha * fg[1] + (1 - alpha) * bg[1],
    alpha * fg[2] + (1 - alpha) * bg[2],
  ]
}

// ── token extraction, per theme block ─────────────────────────────────────
//
// Harbor ships three palettes: :root (Day), .dark (Night, the default) and
// .dusk. Each is a block in globals.css ending where the next one starts; a
// token missing from a block is a test failure, not a silent fallback.

const THEMES = {
  day: [":root {\n  /* ── Light surfaces", "\n.dark {"],
  night: ["\n.dark {", "\n.dusk {"],
  dusk: ["\n.dusk {", "@theme inline"],
} as const
type Theme = keyof typeof THEMES

function block(theme: Theme): string {
  const [from, to] = THEMES[theme]
  const start = css.indexOf(from)
  const end = css.indexOf(to, start + 1)
  expect(start, `${theme} block present`).toBeGreaterThan(-1)
  expect(end, `${theme} block terminated`).toBeGreaterThan(start)
  return css.slice(start, end)
}

function token(theme: Theme, name: string): string {
  const m = block(theme).match(new RegExp(`--${name}:\\s*([^;]+);`))
  expect(m, `--${name} present in the ${theme} theme`).toBeTruthy()
  return (m as RegExpMatchArray)[1].trim()
}

function tokenRgb(theme: Theme, name: string): [number, number, number] {
  const value = token(theme, name)
  const hex = value.match(/^#([0-9a-fA-F]{6})$/)
  if (hex) return hexToRgb(value)
  const ok = value.match(/^oklch\(([\d.]+)\s+([\d.]+)\s+([\d.]+)\)$/)
  expect(ok, `--${name} is hex or simple oklch (got: ${value})`).toBeTruthy()
  const [, l, c, h] = ok as RegExpMatchArray
  return oklchToRgb(Number(l), Number(c), Number(h))
}

const lum = (theme: Theme, name: string) => luminanceFromRgb(tokenRgb(theme, name))
const WHITE = luminanceFromRgb([1, 1, 1])

// The dark themes carry the full axe-parity suite (the app is dark by default).
describe.each(["night", "dusk"] as const)("%s theme WCAG AA contrast (axe color-contrast parity)", (theme) => {
  it("primary-foreground on primary (bg-primary fills) ≥ 4.5:1", () => {
    expect(contrast(lum(theme, "primary-foreground"), lum(theme, "primary"))).toBeGreaterThanOrEqual(4.5)
  })

  it("primary as text on background and card ≥ 4.5:1", () => {
    const primary = lum(theme, "primary")
    expect(contrast(primary, lum(theme, "background"))).toBeGreaterThanOrEqual(4.5)
    expect(contrast(primary, lum(theme, "card"))).toBeGreaterThanOrEqual(4.5)
  })

  it("primary-hover as chip text on bg-primary/15 and /20 over card ≥ 4.5:1", () => {
    const hover = lum(theme, "primary-hover")
    const primary = tokenRgb(theme, "primary")
    const card = tokenRgb(theme, "card")
    for (const alpha of [0.15, 0.2]) {
      const tinted = luminanceFromRgb(blend(primary, alpha, card))
      expect(contrast(hover, tinted), `text-primary-hover on bg-primary/${alpha * 100}`).toBeGreaterThanOrEqual(4.5)
    }
  })

  // The dim metadata tier. Replaces text-muted-foreground/40–/70, which
  // composited to 1.74–3.21:1 on the dark background — every one of
  // those alpha variants failed AA for normal-size text.
  it("muted-foreground-soft on background and card ≥ 4.5:1", () => {
    const soft = lum(theme, "muted-foreground-soft")
    expect(contrast(soft, lum(theme, "background"))).toBeGreaterThanOrEqual(4.5)
    expect(contrast(soft, lum(theme, "card"))).toBeGreaterThanOrEqual(4.5)
  })

  it("primary-foreground on primary-hover ≥ 4.5:1", () => {
    expect(contrast(lum(theme, "primary-foreground"), lum(theme, "primary-hover"))).toBeGreaterThanOrEqual(4.5)
  })

  // crew-policy-controls save button: hover tint capped at bg-primary/25
  // (was /30 → 4.26:1 over card with text-primary-hover).
  it("primary-hover as text on bg-primary/25 over card and background ≥ 4.5:1", () => {
    const hover = lum(theme, "primary-hover")
    const primary = tokenRgb(theme, "primary")
    for (const surface of ["card", "background"] as const) {
      const tinted = luminanceFromRgb(blend(primary, 0.25, tokenRgb(theme, surface)))
      expect(contrast(hover, tinted), `text-primary-hover on bg-primary/25 over ${surface}`).toBeGreaterThanOrEqual(4.5)
    }
  })
})

// What every Harbor theme, Day included, must hold.
describe.each(["day", "night", "dusk"] as const)("%s theme Harbor contrast", (theme) => {
  it("muted-foreground on background and card ≥ 4.5:1", () => {
    const muted = lum(theme, "muted-foreground")
    expect(contrast(muted, lum(theme, "background"))).toBeGreaterThanOrEqual(4.5)
    expect(contrast(muted, lum(theme, "card"))).toBeGreaterThanOrEqual(4.5)
  })

  it("muted-foreground-soft on card ≥ 4.5:1", () => {
    expect(contrast(lum(theme, "muted-foreground-soft"), lum(theme, "card"))).toBeGreaterThanOrEqual(4.5)
  })

  // The filled button: white label on primary-strong, at rest and on hover.
  it("white on primary-strong and primary-strong-hover ≥ 4.5:1", () => {
    expect(contrast(WHITE, lum(theme, "primary-strong"))).toBeGreaterThanOrEqual(4.5)
    expect(contrast(WHITE, lum(theme, "primary-strong-hover"))).toBeGreaterThanOrEqual(4.5)
  })

  it("links (primary-hover) on background and card ≥ 4.5:1", () => {
    const ink = lum(theme, "primary-hover")
    expect(contrast(ink, lum(theme, "background"))).toBeGreaterThanOrEqual(4.5)
    expect(contrast(ink, lum(theme, "card"))).toBeGreaterThanOrEqual(4.5)
  })

  it.each(["ok", "warn", "danger", "neutral", "info", "violet"])("chip %s text on its fill ≥ 4.5:1", (tone) => {
    expect(contrast(lum(theme, `chip-${tone}-fg`), lum(theme, `chip-${tone}-bg`))).toBeGreaterThanOrEqual(4.5)
  })
})

// Day is the first light palette the app shows. The semantic tokens double as
// text colours (text-success, text-destructive …) and were tuned for a dark
// ground, where lightness ≥ 0.72 reads; on white those fell to ~2–3:1.
describe("day theme semantic text contrast", () => {
  it.each(["success", "warn", "destructive", "info", "notice", "gold"])("text-%s on card ≥ 4.5:1", (name) => {
    expect(contrast(lum("day", name), lum("day", "card"))).toBeGreaterThanOrEqual(4.5)
  })
})

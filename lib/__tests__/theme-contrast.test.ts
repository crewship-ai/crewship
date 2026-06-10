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

// ── token extraction from the .dark block ────────────────────────────────

function darkBlock(): string {
  const start = css.indexOf(".dark {")
  const end = css.indexOf("@theme inline")
  expect(start).toBeGreaterThan(-1)
  return css.slice(start, end)
}

function token(name: string): string {
  const m = darkBlock().match(new RegExp(`--${name}:\\s*([^;]+);`))
  expect(m, `--${name} present in .dark theme`).toBeTruthy()
  return (m as RegExpMatchArray)[1].trim()
}

function tokenRgb(name: string): [number, number, number] {
  const value = token(name)
  const hex = value.match(/^#([0-9a-fA-F]{6})$/)
  if (hex) return hexToRgb(value)
  const ok = value.match(/^oklch\(([\d.]+)\s+([\d.]+)\s+([\d.]+)\)$/)
  expect(ok, `--${name} is hex or simple oklch (got: ${value})`).toBeTruthy()
  const [, l, c, h] = ok as RegExpMatchArray
  return oklchToRgb(Number(l), Number(c), Number(h))
}

describe("dark theme WCAG AA contrast (axe color-contrast parity)", () => {
  it("primary-foreground on primary (default buttons) ≥ 4.5:1", () => {
    const ratio = contrast(
      luminanceFromRgb(tokenRgb("primary-foreground")),
      luminanceFromRgb(tokenRgb("primary")),
    )
    expect(ratio).toBeGreaterThanOrEqual(4.5)
  })

  it("primary as text on background and card ≥ 4.5:1", () => {
    const primary = luminanceFromRgb(tokenRgb("primary"))
    expect(contrast(primary, luminanceFromRgb(tokenRgb("background")))).toBeGreaterThanOrEqual(4.5)
    expect(contrast(primary, luminanceFromRgb(tokenRgb("card")))).toBeGreaterThanOrEqual(4.5)
  })

  it("primary-hover as chip text on bg-primary/15 and /20 over card ≥ 4.5:1", () => {
    const hover = luminanceFromRgb(tokenRgb("primary-hover"))
    const primary = tokenRgb("primary")
    const card = tokenRgb("card")
    for (const alpha of [0.15, 0.2]) {
      const tinted = luminanceFromRgb(blend(primary, alpha, card))
      expect(
        contrast(hover, tinted),
        `text-primary-hover on bg-primary/${alpha * 100}`,
      ).toBeGreaterThanOrEqual(4.5)
    }
  })

  it("muted-foreground on background and card ≥ 4.5:1", () => {
    const muted = luminanceFromRgb(tokenRgb("muted-foreground"))
    expect(contrast(muted, luminanceFromRgb(tokenRgb("background")))).toBeGreaterThanOrEqual(4.5)
    expect(contrast(muted, luminanceFromRgb(tokenRgb("card")))).toBeGreaterThanOrEqual(4.5)
  })
})

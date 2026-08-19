import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import path from "node:path"

// Guards the --chart-1..5 tokens in app/globals.css against two regressions:
//
//  1. Light and dark using the SAME palette. They did until this test was
//     added — the .dark block was a byte-for-byte copy of :root's chart
//     tokens, so one ground was always rendering a palette tuned for the
//     other.
//  2. A palette edit that lets two slots become too close to tell apart.
//     ΔE is checked in OKLab (Euclidean distance, scaled x100 onto a
//     familiar CIE-Lab-ish range) both for normal vision and after
//     simulating protanopia / deuteranopia (Machado 2009 "reduced"
//     matrices, applied in linear sRGB) — a pair can look fine normally
//     and still collapse for a color-blind viewer.
//
// Mirrors theme-contrast.test.ts's approach of parsing tokens straight out
// of globals.css so the CSS file stays the single source of truth.

const css = readFileSync(path.resolve(__dirname, "../../app/globals.css"), "utf8")

// ── minimal color math ──────────────────────────────────────────────────

function srgbChannelToLinear(c: number): number {
  return c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4)
}

function hexToLinearRgb(hex: string): [number, number, number] {
  const h = hex.replace("#", "")
  return [
    srgbChannelToLinear(parseInt(h.slice(0, 2), 16) / 255),
    srgbChannelToLinear(parseInt(h.slice(2, 4), 16) / 255),
    srgbChannelToLinear(parseInt(h.slice(4, 6), 16) / 255),
  ]
}

// OKLCH -> linear sRGB (Björn Ottosson's reference transform, same as
// theme-contrast.test.ts's oklchToRgb but stopping before gamma-encoding).
function oklchToLinearRgb(l: number, c: number, hDeg: number): [number, number, number] {
  const h = (hDeg * Math.PI) / 180
  const a = c * Math.cos(h)
  const b = c * Math.sin(h)
  const l_ = l + 0.3963377774 * a + 0.2158037573 * b
  const m_ = l - 0.1055613458 * a - 0.0638541728 * b
  const s_ = l - 0.0894841775 * a - 1.291485548 * b
  const L = l_ ** 3
  const M = m_ ** 3
  const S = s_ ** 3
  return [
    4.0767416621 * L - 3.3077115913 * M + 0.2309699292 * S,
    -1.2684380046 * L + 2.6097574011 * M - 0.3413193965 * S,
    -0.0041960863 * L - 0.7034186147 * M + 1.707614701 * S,
  ]
}

// linear sRGB -> OKLab
function linearRgbToOklab([r, g, b]: [number, number, number]): [number, number, number] {
  const l = 0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b
  const m = 0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b
  const s = 0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b
  const l_ = Math.cbrt(l)
  const m_ = Math.cbrt(m)
  const s_ = Math.cbrt(s)
  return [
    0.2104542553 * l_ + 0.793617785 * m_ - 0.0040720468 * s_,
    1.9779984951 * l_ - 2.428592205 * m_ + 0.4505937099 * s_,
    0.0259040371 * l_ + 0.7827717662 * m_ - 0.808675766 * s_,
  ]
}

function deltaEOklab(a: [number, number, number], b: [number, number, number]): number {
  const dL = a[0] - b[0]
  const da = a[1] - b[1]
  const db = a[2] - b[2]
  return Math.sqrt(dL * dL + da * da + db * db) * 100
}

// Machado, Oliveira & Fitipaldi 2009 "reduced" dichromacy simulation
// matrices, applied directly in linear sRGB. Severity 1.0 (full dichromacy)
// — the worst case, and the one a spec-compliance check should hold to.
const PROTANOPIA = [
  [0.152286, 1.052583, -0.204868],
  [0.114503, 0.786281, 0.099216],
  [-0.003882, -0.048116, 1.051998],
] as const
const DEUTERANOPIA = [
  [0.367322, 0.860646, -0.227968],
  [0.280085, 0.672501, 0.047413],
  [-0.01182, 0.04294, 0.968881],
] as const

function applyMatrix(
  m: readonly (readonly number[])[],
  [r, g, b]: [number, number, number],
): [number, number, number] {
  return [
    m[0][0] * r + m[0][1] * g + m[0][2] * b,
    m[1][0] * r + m[1][1] * g + m[1][2] * b,
    m[2][0] * r + m[2][1] * g + m[2][2] * b,
  ]
}

// ── token extraction ─────────────────────────────────────────────────────

function lightBlock(): string {
  const start = css.indexOf(":root {")
  const end = css.indexOf(".dark {")
  expect(start).toBeGreaterThan(-1)
  expect(end).toBeGreaterThan(start)
  return css.slice(start, end)
}

function darkBlock(): string {
  const start = css.indexOf(".dark {")
  const end = css.indexOf("@theme inline")
  expect(start).toBeGreaterThan(-1)
  return css.slice(start, end)
}

function chartTokens(block: string): string[] {
  return [1, 2, 3, 4, 5].map((n) => {
    const m = block.match(new RegExp(`--chart-${n}:\\s*([^;]+);`))
    expect(m, `--chart-${n} present`).toBeTruthy()
    return (m as RegExpMatchArray)[1].trim()
  })
}

function toLinearRgb(value: string): [number, number, number] {
  const hex = value.match(/^#([0-9a-fA-F]{6})$/)
  if (hex) return hexToLinearRgb(value)
  const ok = value.match(/^oklch\(([\d.]+)\s+([\d.]+)\s+([\d.]+)\)$/)
  expect(ok, `chart token is hex or simple oklch (got: ${value})`).toBeTruthy()
  const [, l, c, h] = ok as RegExpMatchArray
  return oklchToLinearRgb(Number(l), Number(c), Number(h))
}

function inGamut([r, g, b]: [number, number, number], margin = 0.005): boolean {
  return [r, g, b].every((v) => v >= -margin && v <= 1 + margin)
}

const lightTokens = chartTokens(lightBlock())
const darkTokens = chartTokens(darkBlock())

describe("chart palette (--chart-1..5)", () => {
  it("light and dark palettes are not identical (regression: were byte-for-byte the same)", () => {
    expect(lightTokens).not.toEqual(darkTokens)
  })

  it("every light chart token differs from its dark counterpart", () => {
    for (let i = 0; i < 5; i++) {
      expect(lightTokens[i], `chart-${i + 1}`).not.toBe(darkTokens[i])
    }
  })

  it.each([
    ["light", lightTokens],
    ["dark", darkTokens],
  ])("%s palette: all 5 colours render inside the sRGB gamut", (_name, tokens) => {
    for (const t of tokens) {
      expect(inGamut(toLinearRgb(t)), `${t} in gamut`).toBe(true)
    }
  })

  it.each([
    ["light", lightTokens],
    ["dark", darkTokens],
  ])(
    "%s palette: every pair clears ΔE ≥ 15 (OKLab) under normal vision, protanopia and deuteranopia sim",
    (_name, tokens) => {
      const rgbs = tokens.map(toLinearRgb)
      const normal = rgbs.map(linearRgbToOklab)
      const prot = rgbs.map((c) => linearRgbToOklab(applyMatrix(PROTANOPIA, c)))
      const deut = rgbs.map((c) => linearRgbToOklab(applyMatrix(DEUTERANOPIA, c)))
      for (let i = 0; i < 5; i++) {
        for (let j = i + 1; j < 5; j++) {
          expect(deltaEOklab(normal[i], normal[j]), `slot ${i + 1} vs ${j + 1}, normal vision`).toBeGreaterThanOrEqual(15)
          expect(deltaEOklab(prot[i], prot[j]), `slot ${i + 1} vs ${j + 1}, protanopia`).toBeGreaterThanOrEqual(15)
          expect(deltaEOklab(deut[i], deut[j]), `slot ${i + 1} vs ${j + 1}, deuteranopia`).toBeGreaterThanOrEqual(15)
        }
      }
    },
  )

  it("slot 1 stays the brand blue in both themes (hue within 5deg of #1E7BFE)", () => {
    const brandHue = (() => {
      const [, a, b] = linearRgbToOklab(hexToLinearRgb("#1E7BFE"))
      return (Math.atan2(b, a) * 180) / Math.PI
    })()
    for (const tokens of [lightTokens, darkTokens]) {
      const [, a, b] = linearRgbToOklab(toLinearRgb(tokens[0]))
      const hue = (Math.atan2(b, a) * 180) / Math.PI
      const diff = Math.min(Math.abs(hue - brandHue), 360 - Math.abs(hue - brandHue))
      expect(diff, `chart-1 hue vs brand blue hue`).toBeLessThan(5)
    }
  })
})

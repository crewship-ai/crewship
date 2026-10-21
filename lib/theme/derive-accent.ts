/**
 * An accent derived from one colour — the workspace's brand colour
 * (Settings › General › Pages appearance › Brand accent).
 *
 * A preset accent in app/styles/accents.css is seven hand-tuned tokens per
 * mode, each pinned to WCAG AA by lib/__tests__/theme-contrast.test.ts. A
 * colour an admin typed has no such guarantee — #9FE5BD (the old Pages mint)
 * under white button text is 1.4:1 — so it is never used as-is. Its HUE and
 * CHROMA are kept and each token's LIGHTNESS is searched, in OKLCH, until the
 * token meets the same floor against every ground it can be drawn on (the
 * theme's background and card). The result is the admin's colour,
 * taken as light or as dark as legibility requires.
 *
 * Contrast floors, mirroring theme-contrast.test.ts:
 *   both modes  white on brand-strong / brand-strong-hover ≥ 4.5
 *               brand and brand-ink on every ground ≥ 4.5
 *               brand-ink on brand-tint (chip) ≥ 4.5
 *   dark        brand-foreground (near-black) on brand and on brand-ink ≥ 4.5
 *               brand-ink on brand/15–25 over every ground ≥ 4.5
 *   light       brand-foreground (white) on brand ≥ 4.5 (brand = strong)
 */


export type Rgb = [number, number, number]
export interface AccentTokens {
  brand: string
  "brand-foreground": string
  "brand-ink": string
  "brand-strong": string
  "brand-strong-hover": string
  "brand-glow": string
  "brand-tint": string
}

// ── colour math ──────────────────────────────────────────────────────

export function hexToRgb(hex: string): Rgb {
  const h = hex.replace("#", "")
  return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16) / 255) as Rgb
}

export function rgbToHex([r, g, b]: Rgb): string {
  const c = (v: number) => Math.round(Math.min(1, Math.max(0, v)) * 255).toString(16).padStart(2, "0")
  return `#${c(r)}${c(g)}${c(b)}`.toUpperCase()
}

const toLinear = (c: number) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4)
const toGamma = (c: number) => (c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055)

export function luminance(rgb: Rgb): number {
  const [r, g, b] = rgb.map(toLinear)
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

export function contrast(a: Rgb, b: Rgb): number {
  const x = luminance(a)
  const y = luminance(b)
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05)
}

export function blend(fg: Rgb, alpha: number, bg: Rgb): Rgb {
  return [0, 1, 2].map((i) => alpha * fg[i] + (1 - alpha) * bg[i]) as Rgb
}

/** sRGB (gamma) → OKLCH [L, C, h°]. */
export function rgbToOklch(rgb: Rgb): [number, number, number] {
  const [r, g, b] = rgb.map(toLinear)
  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b)
  const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b)
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b)
  const L = 0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s
  const A = 1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s
  const B = 0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s
  const C = Math.hypot(A, B)
  const h = ((Math.atan2(B, A) * 180) / Math.PI + 360) % 360
  return [L, C, h]
}

/** OKLCH → linear-then-gamma sRGB, unclamped (out-of-gamut channels exceed 0..1). */
function oklchToRgbRaw(L: number, C: number, hDeg: number): Rgb {
  const h = (hDeg * Math.PI) / 180
  const a = C * Math.cos(h)
  const b = C * Math.sin(h)
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3
  const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3
  const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3
  return [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ]
}

/** OKLCH → sRGB, reducing chroma until the colour is in gamut. */
export function oklchToRgb(L: number, C: number, h: number): Rgb {
  let c = C
  for (let i = 0; i < 40; i++) {
    const lin = oklchToRgbRaw(L, c, h)
    if (lin.every((v) => v >= -1e-4 && v <= 1 + 1e-4)) return lin.map((v) => toGamma(Math.min(1, Math.max(0, v)))) as Rgb
    c *= 0.9
  }
  return oklchToRgbRaw(L, 0, h).map((v) => toGamma(Math.min(1, Math.max(0, v)))) as Rgb
}

// ── grounds ──────────────────────────────────────────────────────────

/** The background and card a brand token can sit on, per mode — the
 *  --background and --card of app/globals.css (pinned by its contrast test). */
export const GROUNDS = {
  light: ["#F7F8FA", "#FFFFFF"],
  dark: ["#0A0B0E", "#121318"],
} as const

function grounds(mode: "light" | "dark"): Rgb[] {
  return GROUNDS[mode].map(hexToRgb)
}

const WHITE: Rgb = [1, 1, 1]
/** The dark-mode brand-foreground every preset uses: text on a light fill. */
const DARK_FG_HEX = "#0A0B0E"
const DARK_FG = hexToRgb(DARK_FG_HEX)
const FLOOR = 4.5
// A hair above AA, so a colour that lands exactly on the line after hex
// rounding cannot dip below it.
const TARGET = FLOOR + 0.05

/**
 * The lightness nearest to `start` (moving in `dir`) whose colour satisfies
 * `ok`. Steps of 0.005 L; falls back to the extreme when nothing passes
 * (which the floors below never need: L=0/1 is black/white).
 */
function searchL(start: number, dir: 1 | -1, C: number, h: number, ok: (rgb: Rgb) => boolean): number {
  for (let L = start; L >= 0 && L <= 1; L += 0.005 * dir) {
    if (ok(oklchToRgb(L, C, h))) return L
  }
  return dir === 1 ? 1 : 0
}

const glow = (rgb: Rgb, alpha: number) =>
  `rgb(${rgb.map((v) => Math.round(v * 255)).join(" ")} / ${alpha})`

/** The seven brand tokens for both modes, from one hex colour. */
export function deriveAccent(hex: string): { light: AccentTokens; dark: AccentTokens } {
  const [L0, C0, h] = rgbToOklch(hexToRgb(hex))
  // A near-grey input has no meaningful hue; keep it grey rather than letting
  // a rounding-error hue tint every token.
  const C = C0 < 0.02 ? 0 : C0

  // Light: brand is both the fill under white text and text on the ground.
  const lightG = grounds("light")
  const lBrandL = searchL(Math.min(L0, 0.62), -1, C, h, (rgb) =>
    contrast(WHITE, rgb) >= TARGET && lightG.every((g) => contrast(rgb, g) >= TARGET))
  const lBrand = oklchToRgb(lBrandL, C, h)
  const lHover = oklchToRgb(Math.max(0, lBrandL - 0.05), C, h)
  const lTint = oklchToRgb(0.955, Math.min(C, 0.035), h)
  const lInkL = searchL(Math.max(0, lBrandL - 0.1), -1, C, h, (rgb) =>
    contrast(rgb, lTint) >= TARGET && lightG.every((g) => contrast(rgb, g) >= TARGET))
  const lInk = oklchToRgb(lInkL, C, h)

  // Dark: brand is a light fill under near-black text, and text on the ground.
  const darkG = grounds("dark")
  const dBrandL = searchL(Math.max(L0, 0.62), 1, C, h, (rgb) =>
    contrast(DARK_FG, rgb) >= TARGET && darkG.every((g) => contrast(rgb, g) >= TARGET))
  const dBrand = oklchToRgb(dBrandL, C, h)
  const dTint = oklchToRgb(0.24, Math.min(C, 0.06), h)
  const dInkL = searchL(Math.max(dBrandL + 0.08, 0.78), 1, C, h, (rgb) =>
    contrast(DARK_FG, rgb) >= TARGET &&
    contrast(rgb, dTint) >= TARGET &&
    darkG.every((g) => contrast(rgb, g) >= TARGET &&
      [0.15, 0.2, 0.25].every((a) => contrast(rgb, blend(dBrand, a, g)) >= TARGET)))
  const dInk = oklchToRgb(dInkL, C, h)
  // The filled button keeps white text in dark mode too.
  const dStrongL = searchL(Math.min(L0, 0.58), -1, C, h, (rgb) => contrast(WHITE, rgb) >= TARGET)
  const dStrong = oklchToRgb(dStrongL, C, h)
  const dStrongHover = oklchToRgb(Math.max(0, dStrongL - 0.05), C, h)

  return {
    light: {
      brand: rgbToHex(lBrand),
      "brand-foreground": "#FFFFFF",
      "brand-ink": rgbToHex(lInk),
      "brand-strong": rgbToHex(lBrand),
      "brand-strong-hover": rgbToHex(lHover),
      "brand-glow": glow(lBrand, 0.5),
      "brand-tint": rgbToHex(lTint),
    },
    dark: {
      brand: rgbToHex(dBrand),
      "brand-foreground": DARK_FG_HEX,
      "brand-ink": rgbToHex(dInk),
      "brand-strong": rgbToHex(dStrong),
      "brand-strong-hover": rgbToHex(dStrongHover),
      "brand-glow": glow(dStrong, 0.55),
      "brand-tint": rgbToHex(dTint),
    },
  }
}

/** The stylesheet that paints a derived accent under data-accent="workspace". */
export function workspaceAccentCss(hex: string): string {
  const { light, dark } = deriveAccent(hex)
  const decls = (t: AccentTokens) => Object.entries(t).map(([k, v]) => `--${k}:${v};`).join("")
  return `:root[data-accent="workspace"]{${decls(light)}}:root.dark[data-accent="workspace"]{${decls(dark)}}`
}

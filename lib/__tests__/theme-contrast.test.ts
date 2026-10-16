import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import path from "node:path"

import { ACCENTS, DEFAULT_ACCENT, accentBlock, type AccentId } from "@/lib/theme/accents"

// Guards the WCAG AA (4.5:1) contrast of the dark-theme brand tokens in
// app/globals.css. The app renders dark-only (<html className="dark">),
// so these pairs are exactly what axe's color-contrast rule measures in
// e2e/a11y.spec.ts — this test pins the same math at unit level so a
// token regression fails fast without a Playwright run.
//
// History: --primary-foreground used to be white on #1E7BFE (3.95:1),
// which forced the color-contrast axe rule to stay disabled.

const css = readFileSync(path.resolve(__dirname, "../../app/globals.css"), "utf8")
const accentsCss = readFileSync(path.resolve(__dirname, "../../app/styles/accents.css"), "utf8")

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
// Harbor ships two palettes: :root (light) and .dark (the default). Each is a
// block in globals.css ending where the next one starts; a token missing from
// a block is a test failure, not a silent fallback.
//
// Brand-coloured tokens (--primary, --ring, links, blue chips …) hold
// `var(--brand-*)`; the colour itself lives in app/styles/accents.css, one
// block per accent and mode. Every check below runs for every accent in
// ACCENTS, so a new accent cannot ship below AA.

const THEMES = {
  day: [":root {\n  /* ── Light surfaces", "\n.dark {"],
  night: ["\n.dark {", "@theme inline"],
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

function token(theme: Theme, name: string, accent: AccentId = DEFAULT_ACCENT): string {
  const m = block(theme).match(new RegExp(`--${name}:\\s*([^;]+);`))
  expect(m, `--${name} present in the ${theme} theme`).toBeTruthy()
  const value = (m as RegExpMatchArray)[1].trim()
  const ref = value.match(/^var\(--(brand[a-z-]*)\)$/)
  if (!ref) return value
  const brand = accentBlock(accentsCss, accent, theme === "night" ? "dark" : "light").get(ref[1])
  expect(brand, `--${ref[1]} defined for accent ${accent} (${theme})`).toBeTruthy()
  return brand as string
}

function tokenRgb(theme: Theme, name: string, accent?: AccentId): [number, number, number] {
  const value = token(theme, name, accent)
  const hex = value.match(/^#([0-9a-fA-F]{6})$/)
  if (hex) return hexToRgb(value)
  const ok = value.match(/^oklch\(([\d.]+)\s+([\d.]+)\s+([\d.]+)\)$/)
  expect(ok, `--${name} is hex or simple oklch (got: ${value})`).toBeTruthy()
  const [, l, c, h] = ok as RegExpMatchArray
  return oklchToRgb(Number(l), Number(c), Number(h))
}

const WHITE = luminanceFromRgb([1, 1, 1])
const ACCENT_IDS = ACCENTS.map((a) => a.id)

// The dark theme carries the full axe-parity suite (the app is dark by default).
describe.each(ACCENT_IDS)("accent %s — dark theme WCAG AA contrast (axe color-contrast parity)", (accent) => {
  const theme = "night" as const
  const lum = (name: string) => luminanceFromRgb(tokenRgb(theme, name, accent))

  it("primary-foreground on primary (bg-primary fills) ≥ 4.5:1", () => {
    expect(contrast(lum("primary-foreground"), lum("primary"))).toBeGreaterThanOrEqual(4.5)
  })

  it("primary as text on background and card ≥ 4.5:1", () => {
    expect(contrast(lum("primary"), lum("background"))).toBeGreaterThanOrEqual(4.5)
    expect(contrast(lum("primary"), lum("card"))).toBeGreaterThanOrEqual(4.5)
  })

  it("primary-hover as chip text on bg-primary/15, /20 over card and /25 over both ≥ 4.5:1", () => {
    const hover = lum("primary-hover")
    const primary = tokenRgb(theme, "primary", accent)
    for (const [alpha, surface] of [[0.15, "card"], [0.2, "card"], [0.25, "card"], [0.25, "background"]] as const) {
      const tinted = luminanceFromRgb(blend(primary, alpha, tokenRgb(theme, surface, accent)))
      expect(contrast(hover, tinted), `text-primary-hover on bg-primary/${alpha * 100} over ${surface}`).toBeGreaterThanOrEqual(4.5)
    }
  })

  it("primary-foreground on primary-hover ≥ 4.5:1", () => {
    expect(contrast(lum("primary-foreground"), lum("primary-hover"))).toBeGreaterThanOrEqual(4.5)
  })
})

// What both Harbor themes must hold, for every accent.
describe.each(ACCENT_IDS.flatMap((accent) => (["day", "night"] as const).map((theme) => [accent, theme] as const)))(
  "accent %s — %s theme Harbor contrast",
  (accent, theme) => {
    const lum = (name: string) => luminanceFromRgb(tokenRgb(theme, name, accent))

    it("white on primary-strong and primary-strong-hover (filled button) ≥ 4.5:1", () => {
      expect(contrast(WHITE, lum("primary-strong"))).toBeGreaterThanOrEqual(4.5)
      expect(contrast(WHITE, lum("primary-strong-hover"))).toBeGreaterThanOrEqual(4.5)
    })

    it("links (primary-hover) and primary text on background and card ≥ 4.5:1", () => {
      for (const name of ["primary-hover", "primary"]) {
        expect(contrast(lum(name), lum("background")), `${name} on background`).toBeGreaterThanOrEqual(4.5)
        expect(contrast(lum(name), lum("card")), `${name} on card`).toBeGreaterThanOrEqual(4.5)
      }
    })

    it("blue (brand) chip text on its fill ≥ 4.5:1", () => {
      expect(contrast(lum("chip-info-fg"), lum("chip-info-bg"))).toBeGreaterThanOrEqual(4.5)
    })
  },
)

// Accent-independent tokens.
describe.each(["day", "night"] as const)("%s theme neutral and status contrast", (theme) => {
  const lum = (name: string) => luminanceFromRgb(tokenRgb(theme, name))

  it("muted-foreground on background and card ≥ 4.5:1", () => {
    expect(contrast(lum("muted-foreground"), lum("background"))).toBeGreaterThanOrEqual(4.5)
    expect(contrast(lum("muted-foreground"), lum("card"))).toBeGreaterThanOrEqual(4.5)
  })

  // The dim metadata tier. Replaces text-muted-foreground/40–/70, which
  // composited to 1.74–3.21:1 on the dark background.
  it("muted-foreground-soft on card ≥ 4.5:1", () => {
    expect(contrast(lum("muted-foreground-soft"), lum("card"))).toBeGreaterThanOrEqual(4.5)
  })

  it.each(["ok", "warn", "danger", "neutral", "violet"])("chip %s text on its fill ≥ 4.5:1", (tone) => {
    expect(contrast(lum(`chip-${tone}-fg`), lum(`chip-${tone}-bg`))).toBeGreaterThanOrEqual(4.5)
  })
})

// Light is the first light palette the app shows. The semantic tokens double as
// text colours (text-success, text-destructive …) and were tuned for a dark
// ground, where lightness ≥ 0.72 reads; on white those fell to ~2–3:1.
describe("day theme semantic text contrast", () => {
  it.each(["success", "warn", "destructive", "info", "notice", "gold"])("text-%s on card ≥ 4.5:1", (name) => {
    expect(contrast(luminanceFromRgb(tokenRgb("day", name)), luminanceFromRgb(tokenRgb("day", "card")))).toBeGreaterThanOrEqual(4.5)
  })
})

// The rail's concept hues (lib/concept-accents.ts) beyond the semantic set:
// each is a glyph colour, so it has to read as text on the card in both themes.
describe("concept hue text contrast", () => {
  const HUES = ["indigo", "rose", "lime", "orange", "azure"]
  it.each(HUES)("text-%s on card ≥ 4.5:1 in the day theme", (name) => {
    expect(contrast(luminanceFromRgb(tokenRgb("day", name)), luminanceFromRgb(tokenRgb("day", "card")))).toBeGreaterThanOrEqual(4.5)
  })
  it.each(HUES)("text-%s on card ≥ 4.5:1 in the night theme", (name) => {
    expect(contrast(luminanceFromRgb(tokenRgb("night", name)), luminanceFromRgb(tokenRgb("night", "card")))).toBeGreaterThanOrEqual(4.5)
  })
})

// ── Harbor 2 ─────────────────────────────────────────────────────────────
//
// The Harbor 2 revision keeps every token name and moves the values to the
// design's (pinned mockup v67), with two contrast fixes the design itself
// misses: the tertiary text grey (--muted-foreground-soft) is darker in day
// and lighter in night than the design's #757D8C / #767D8B, which measured
// 3.4–4.2:1 on several surfaces. --icon-faint is the design's unnamed fourth
// grey, for icons and dividers only (3:1, WCAG 1.4.11).

const HARBOR2: Record<Theme, Record<string, string>> = {
  day: {
    background: "#FFFFFF",
    sidebar: "#F3F4F6",
    card: "#FFFFFF",
    popover: "#FFFFFF",
    muted: "#F8F9FA",
    "surface-subtle": "#F8F9FA",
    "surface-raised": "#FFFFFF",
    foreground: "#0C111B",
    "muted-foreground": "#4B5363",
    "muted-foreground-soft": "#616875",
    "icon-faint": "#7E8797",
    border: "#E4E6EA",
    "sidebar-border": "#E4E6EA",
    "line-strong": "#D2D6DC",
    success: "#157A45",
    warn: "#8A5A00",
    destructive: "#C0261B",
    "chip-ok-fg": "#157A45",
    "chip-ok-bg": "#E5F4EC",
    "chip-warn-fg": "#8A5A00",
    "chip-warn-bg": "#FCF1DC",
    "chip-danger-fg": "#C0261B",
    "chip-danger-bg": "#FCEAE8",
    "chip-violet-fg": "#6544D6",
    "chip-violet-bg": "#F0EBFD",
  },
  night: {
    background: "#0F1013",
    sidebar: "#0A0B0D",
    card: "#181A1F",
    popover: "#181A1F",
    muted: "#1E2127",
    "surface-subtle": "#0F1013",
    "surface-raised": "#1E2127",
    foreground: "#ECEEF2",
    "muted-foreground": "#A4AAB6",
    "muted-foreground-soft": "#9096A1",
    "icon-faint": "#6F7683",
    border: "#282B33",
    "sidebar-border": "#282B33",
    "line-strong": "#373B45",
    success: "#4CC38A",
    warn: "#E5A83B",
    destructive: "#F2695C",
    "chip-ok-fg": "#4CC38A",
    "chip-ok-bg": "#10261C",
    "chip-warn-fg": "#E5A83B",
    "chip-warn-bg": "#2B2010",
    "chip-danger-fg": "#F2695C",
    "chip-danger-bg": "#2E1513",
    "chip-violet-fg": "#A28BFA",
    "chip-violet-bg": "#1E1835",
  },
}

describe.each(["day", "night"] as const)("Harbor 2 %s token values", (theme) => {
  it.each(Object.entries(HARBOR2[theme]))("--%s is %s", (name, value) => {
    expect(token(theme, name).toUpperCase()).toBe(value)
  })
})

describe.each(["day", "night"] as const)("Harbor 2 %s contrast matrix", (theme) => {
  const lum = (name: string) => luminanceFromRgb(tokenRgb(theme, name))
  const SURFACES = ["background", "sidebar", "card", "muted", "surface-subtle", "surface-raised"]
  // Hover is translucent foreground (--row-hover-bg, 4 %) over a surface;
  // selection is the opaque accent tint (--selection-bg), so it is checked
  // for every accent.
  const hovered = (surface: string) =>
    luminanceFromRgb(blend(tokenRgb(theme, "foreground"), 0.04, tokenRgb(theme, surface)))
  const TEXT = ["foreground", "muted-foreground", "muted-foreground-soft", "success", "warn", "destructive"]

  it("row hover is 4 % foreground and selection is the accent tint", () => {
    expect(token(theme, "row-hover-bg")).toBe("color-mix(in srgb, var(--foreground) 4%, transparent)")
    expect(block(theme)).toMatch(/--selection-bg:\s*var\(--brand-tint\);/)
  })

  it.each(TEXT)("text %s ≥ 4.5:1 on every surface and on its hover", (text) => {
    for (const s of SURFACES) {
      expect(contrast(lum(text), lum(s)), `${text} on ${s}`).toBeGreaterThanOrEqual(4.5)
      expect(contrast(lum(text), hovered(s)), `${text} on hovered ${s}`).toBeGreaterThanOrEqual(4.5)
    }
  })

  it.each(ACCENT_IDS)("every text token ≥ 4.5:1 on a selected row (accent %s)", (accent) => {
    const tint = luminanceFromRgb(tokenRgb(theme, "selection-bg", accent))
    for (const text of TEXT) {
      expect(contrast(lum(text), tint), `${text} on ${accent} selection`).toBeGreaterThanOrEqual(4.5)
    }
  })

  it("icon-faint ≥ 3:1 on every surface (icons and dividers only, never text)", () => {
    for (const s of SURFACES) expect(contrast(lum("icon-faint"), lum(s)), `icon-faint on ${s}`).toBeGreaterThanOrEqual(3)
  })

  it.each(["ok", "warn", "danger", "violet"])("chip %s text ≥ 4.5:1 on its fill and on card", (tone) => {
    expect(contrast(lum(`chip-${tone}-fg`), lum(`chip-${tone}-bg`))).toBeGreaterThanOrEqual(4.5)
    expect(contrast(lum(`chip-${tone}-fg`), lum("card"))).toBeGreaterThanOrEqual(4.5)
  })
})

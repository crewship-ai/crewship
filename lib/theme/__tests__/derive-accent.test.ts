import { describe, it, expect, beforeEach, afterEach, vi } from "vitest"

import { BASES } from "../bases"
import { ACCENT_BOOT_SCRIPT, ACCENT_STORAGE_KEY, WORKSPACE_ACCENT_CSS_KEY, WORKSPACE_ACCENT_CSS_PATTERN, WORKSPACE_ACCENT_STYLE_ID } from "../accents"
import { blend, contrast, deriveAccent, hexToRgb, rgbToOklch, workspaceAccentCss } from "../derive-accent"

// An admin's colour is never used as-is: each token keeps its hue and has its
// lightness moved until it meets AA on every ground any base can put under it.
// These are the floors theme-contrast.test.ts holds the presets to.

const INPUTS = [
  "#9FE5BD", // the Pages default mint — far too light for white text
  "#1E7BFE", "#FF0000", "#FFFF00", "#00FF00", "#00FFFF", "#FF00FF",
  "#FFFFFF", "#000000", "#808080", "#F97316", "#7C3AED", "#0EA5C4",
  "#14532D", "#FDE68A", "#E11D48", "#1F2937", "#C0FFEE", "#123456",
]
const WHITE = hexToRgb("#FFFFFF")
const grounds = (mode: "light" | "dark") => BASES.flatMap((b) => b[mode].map(hexToRgb))

describe.each(INPUTS)("deriveAccent(%s)", (hex) => {
  const { light, dark } = deriveAccent(hex)
  const c = (a: string, b: string) => contrast(hexToRgb(a), hexToRgb(b))

  it("light: white on the filled button and its hover ≥ 4.5:1", () => {
    expect(c("#FFFFFF", light["brand-strong"])).toBeGreaterThanOrEqual(4.5)
    expect(c("#FFFFFF", light["brand-strong-hover"])).toBeGreaterThanOrEqual(4.5)
    expect(c(light["brand-foreground"], light.brand)).toBeGreaterThanOrEqual(4.5)
  })

  it("light: brand and links on every base's ground and card ≥ 4.5:1, chip ink on tint ≥ 4.5:1", () => {
    for (const g of grounds("light")) {
      expect(contrast(hexToRgb(light.brand), g)).toBeGreaterThanOrEqual(4.5)
      expect(contrast(hexToRgb(light["brand-ink"]), g)).toBeGreaterThanOrEqual(4.5)
    }
    expect(c(light["brand-ink"], light["brand-tint"])).toBeGreaterThanOrEqual(4.5)
  })

  it("dark: near-black text on brand and on links ≥ 4.5:1; white on the filled button ≥ 4.5:1", () => {
    expect(c(dark["brand-foreground"], dark.brand)).toBeGreaterThanOrEqual(4.5)
    expect(c(dark["brand-foreground"], dark["brand-ink"])).toBeGreaterThanOrEqual(4.5)
    expect(contrast(WHITE, hexToRgb(dark["brand-strong"]))).toBeGreaterThanOrEqual(4.5)
    expect(contrast(WHITE, hexToRgb(dark["brand-strong-hover"]))).toBeGreaterThanOrEqual(4.5)
  })

  it("dark: brand and links on every ground ≥ 4.5:1, links on brand/15–25 tints ≥ 4.5:1, chip ink on tint ≥ 4.5:1", () => {
    for (const g of grounds("dark")) {
      expect(contrast(hexToRgb(dark.brand), g)).toBeGreaterThanOrEqual(4.5)
      expect(contrast(hexToRgb(dark["brand-ink"]), g)).toBeGreaterThanOrEqual(4.5)
      for (const a of [0.15, 0.2, 0.25]) {
        expect(contrast(hexToRgb(dark["brand-ink"]), blend(hexToRgb(dark.brand), a, g))).toBeGreaterThanOrEqual(4.5)
      }
    }
    expect(c(dark["brand-ink"], dark["brand-tint"])).toBeGreaterThanOrEqual(4.5)
  })

  it("produces a stylesheet the boot script will accept", () => {
    expect(WORKSPACE_ACCENT_CSS_PATTERN.test(workspaceAccentCss(hex))).toBe(true)
  })
})

describe("deriveAccent keeps the colour's identity", () => {
  it("keeps the hue of a saturated input", () => {
    const [, , h] = rgbToOklch(hexToRgb("#7C3AED"))
    for (const token of Object.values(deriveAccent("#7C3AED").light).filter((v) => v.startsWith("#"))) {
      const [, C, th] = rgbToOklch(hexToRgb(token))
      if (C > 0.03) expect(Math.abs(th - h)).toBeLessThan(8)
    }
  })

  it("keeps a grey grey", () => {
    for (const token of Object.values(deriveAccent("#808080").dark).filter((v) => v.startsWith("#"))) {
      expect(rgbToOklch(hexToRgb(token))[1]).toBeLessThan(0.01)
    }
  })
})

describe("workspace accent boot script", () => {
  // vitest.setup.ts stubs localStorage with no-op mocks; the boot script needs
  // a store that keeps what is written.
  beforeEach(() => {
    const store = new Map<string, string>()
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
      clear: () => store.clear(),
    })
  })
  afterEach(() => vi.unstubAllGlobals())

  function boot(stored: string | null, css: string | null) {
    localStorage.clear()
    document.getElementById(WORKSPACE_ACCENT_STYLE_ID)?.remove()
    delete document.documentElement.dataset.accent
    if (stored) localStorage.setItem(ACCENT_STORAGE_KEY, stored)
    if (css) localStorage.setItem(WORKSPACE_ACCENT_CSS_KEY, css)
    new Function(ACCENT_BOOT_SCRIPT)()
  }

  it("re-injects the cached stylesheet before first paint", () => {
    const css = workspaceAccentCss("#7C3AED")
    boot("workspace", css)
    expect(document.documentElement.dataset.accent).toBe("workspace")
    expect(document.getElementById(WORKSPACE_ACCENT_STYLE_ID)?.textContent).toBe(css)
  })

  it("refuses a tampered cache — anything but our own declarations", () => {
    boot("workspace", `:root[data-accent="workspace"]{--brand:url(https://x.example/a);}`)
    expect(document.getElementById(WORKSPACE_ACCENT_STYLE_ID)).toBeNull()
    // The choice itself still applies; with no rules it shows Blue.
    expect(document.documentElement.dataset.accent).toBe("workspace")
  })

  it("still paints the presets", () => {
    boot("teal", null)
    expect(document.documentElement.dataset.accent).toBe("teal")
    boot("nonsense", null)
    expect(document.documentElement.dataset.accent).toBeUndefined()
  })
})

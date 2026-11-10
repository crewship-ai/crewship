import { readFileSync } from "node:fs"
import { join } from "node:path"
import { describe, expect, it } from "vitest"
import { documentSchema } from "@/lib/documents/schema"

/**
 * The Docs schema 5 palette stores names; app/globals.css resolves each name
 * per theme. These checks keep every shade readable (WCAG 2 contrast) in both
 * themes, on every surface a document is read on and on every highlight.
 */
const CSS = readFileSync(join(process.cwd(), "app", "globals.css"), "utf8")

/** The theme block that defines the page colours (globals.css has several small `:root` blocks). */
function block(selector: string): string {
  for (let start = CSS.indexOf(`\n${selector} {\n`); start >= 0; start = CSS.indexOf(`\n${selector} {\n`, start + 1)) {
    const body = CSS.slice(start, CSS.indexOf("\n}\n", start))
    if (body.includes("--background:")) return body
  }
  throw new Error(`${selector} theme block missing`)
}
function token(source: string, name: string): string {
  const match = new RegExp(`--${name}:\\s*(#[0-9A-Fa-f]{6});`).exec(source)
  if (!match) throw new Error(`--${name} missing or not a 6-digit hex value`)
  return match[1]
}
function luminance(hex: string): number {
  const [r, g, b] = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255).map(c => c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4)
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}
function contrast(a: string, b: string): number {
  const [x, y] = [luminance(a), luminance(b)].sort((m, n) => n - m)
  return (x + 0.05) / (y + 0.05)
}

const themes = [["light", block(":root")], ["dark", block(".dark")]] as const

/** Every accent's --brand (= --primary and --ring) per theme, from app/styles/accents.css. */
const ACCENTS = [...readFileSync(join(process.cwd(), "app", "styles", "accents.css"), "utf8")
  .matchAll(/:root(\.dark)?\[data-accent="([\w-]+)"\] \{\n {2}--brand: (#[0-9A-Fa-f]{6});/g)]
  .map(([, dark, name, brand]) => ({ theme: dark ? "dark" : "light", name, brand }))
/** `color-mix(in srgb, fg a%, bg)`: an alpha tint over an opaque surface. */
function mix(fg: string, bg: string, alpha: number): string {
  return "#" + [1, 3, 5].map(i => Math.round(parseInt(fg.slice(i, i + 2), 16) * alpha + parseInt(bg.slice(i, i + 2), 16) * (1 - alpha)).toString(16).padStart(2, "0")).join("")
}

describe("Docs text colour and highlight palette", () => {
  it("is the engine's closed list of names", () => {
    expect([...documentSchema.PALETTE]).toEqual(["gray", "red", "orange", "yellow", "green", "blue", "purple", "pink"])
  })

  it.each(themes)("every %s text shade is at least 4.5:1 on each reading surface and on every highlight", (_theme, source) => {
    const surfaces = ["background", "card", "muted"].map(name => token(source, name))
    const marks = documentSchema.PALETTE.map(name => token(source, `doc-mark-${name}`))
    for (const name of documentSchema.PALETTE) {
      const text = token(source, `doc-text-${name}`)
      for (const surface of [...surfaces, ...marks]) expect(contrast(text, surface), `${name} on ${surface}`).toBeGreaterThanOrEqual(4.5)
    }
  })

  it.each(themes)("default %s text is at least 7:1 on every highlight, and each highlight is visible on the page", (_theme, source) => {
    const foreground = token(source, "foreground"), background = token(source, "background")
    for (const name of documentSchema.PALETTE) {
      const mark = token(source, `doc-mark-${name}`)
      expect(contrast(foreground, mark), name).toBeGreaterThanOrEqual(7)
      expect(mark.toLowerCase(), name).not.toBe(background.toLowerCase())
    }
  })

  it("maps every name to its token in CSS and nothing else", () => {
    for (const name of documentSchema.PALETTE) {
      expect(CSS).toContain(`[data-text-color="${name}"] { color: var(--doc-text-${name}); }`)
      expect(CSS).toContain(`mark[data-highlight="${name}"] { background-color: var(--doc-mark-${name}); }`)
    }
    expect(CSS.match(/\[data-text-color="/g)?.length).toBe(documentSchema.PALETTE.length)
  })

  it("Word export uses the light theme's shades", () => {
    const source = readFileSync(join(process.cwd(), "tools", "docs-service", "docx-export.mjs"), "utf8")
    const light = block(":root")
    for (const name of documentSchema.PALETTE) {
      expect(source).toMatch(new RegExp(`TEXT_HEX = \\{[^}]*${name}: '${token(light, `doc-text-${name}`).slice(1)}'`, "i"))
      expect(source).toMatch(new RegExp(`MARK_HEX = \\{[^}]*${name}: '${token(light, `doc-mark-${name}`).slice(1)}'`, "i"))
    }
  })
  it("keeps the selection readable in both themes and every accent: tinted default text, untinted palette text with a visible bar (D17)", () => {
    // The rules this measures, exactly as shipped.
    expect(CSS).toContain(".document-selection-kept { background-color: color-mix(in srgb, var(--primary) 20%, transparent);")
    expect(CSS).toContain(":is([data-text-color] .document-selection-kept, .document-selection-kept[data-text-color], .document-selection-kept:has([data-text-color])) { background-color: transparent; box-shadow: inset 0 -2px 0 var(--ring); }")
    expect(CSS).toContain(".ProseMirror :is([data-text-color], [data-text-color] *)::selection { color: var(--foreground); }")
    const editor = readFileSync(join(process.cwd(), "components", "features", "artifacts-library", "kinds", "docs", "docs-view.tsx"), "utf8")
    expect(editor).toContain("selection:bg-primary/25")
    expect(ACCENTS.filter(accent => accent.theme === "light").length).toBeGreaterThanOrEqual(10)
    expect(ACCENTS.filter(accent => accent.theme === "dark").length).toBe(ACCENTS.filter(accent => accent.theme === "light").length)
    const measured: string[] = []
    for (const { theme, name, brand } of ACCENTS) {
      const source = theme === "dark" ? block(".dark") : block(":root")
      const foreground = token(source, "foreground")
      const surfaces = [token(source, "background"), token(source, "card"), token(source, "muted"), ...documentSchema.PALETTE.map(mark => token(source, `doc-mark-${mark}`))]
      let worstKept = 99, worstNative = 99, worstBar = 99
      for (const surface of surfaces) {
        // Kept selection over default text, and the focused browser selection (25%) where palette text turns --foreground.
        worstKept = Math.min(worstKept, contrast(foreground, mix(brand, surface, 0.2)))
        worstNative = Math.min(worstNative, contrast(foreground, mix(brand, surface, 0.25)))
        // Palette text is not tinted, so its own 4.5:1 (checked above) stands; the bar must be visible (non-text 3:1).
        worstBar = Math.min(worstBar, contrast(brand, surface))
      }
      expect(worstKept, `${theme} ${name} kept`).toBeGreaterThanOrEqual(4.5)
      expect(worstNative, `${theme} ${name} native`).toBeGreaterThanOrEqual(4.5)
      expect(worstBar, `${theme} ${name} bar`).toBeGreaterThanOrEqual(3)
      measured.push(`${theme}/${name}: kept ${worstKept.toFixed(2)} native ${worstNative.toFixed(2)} bar ${worstBar.toFixed(2)}`)
    }
    // A tint under palette text would fail: the reason the bar exists (worst case well under 4.5:1).
    const dark = block(".dark"), ink = ACCENTS.find(accent => accent.theme === "dark" && accent.name === "ink")!.brand
    expect(contrast(token(dark, "doc-text-red"), mix(ink, token(dark, "doc-mark-yellow"), 0.2))).toBeLessThan(4.5)
    console.info(`D17 selection contrast (worst per accent):\n${measured.join("\n")}`)
  })
})

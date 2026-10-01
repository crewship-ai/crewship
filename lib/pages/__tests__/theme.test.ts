import { describe, expect, it } from "vitest"
import { DEFAULT_PAGE_THEME, normalizePageTheme, colorContrast } from "../theme"
import { previewSnapshot } from "../preview-runtime"

it("only forwards palette tokens and rejects executable CSS/unknown workspace fields", () => {
 const theme = normalizePageTheme({ accent: "#123abc", background: "url(https://example.com)", token: "secret", extra: "#123456" })
 expect(theme).toEqual({ ...DEFAULT_PAGE_THEME, accent: "#123abc" })
 const snapshot = previewSnapshot({ slug: "ops", name: "Ops", panels: [] }, { ...theme, credentials: "secret" })
 expect(snapshot.theme).toEqual(theme)
 expect(JSON.stringify(snapshot)).not.toContain("secret")
})
it("provides usable defaults for old workspaces and readable body text", () => {
 expect(normalizePageTheme(null)).toEqual(DEFAULT_PAGE_THEME)
 expect(colorContrast(DEFAULT_PAGE_THEME.text, DEFAULT_PAGE_THEME.background)).toBeGreaterThan(4.5)
 expect(colorContrast(DEFAULT_PAGE_THEME.text, DEFAULT_PAGE_THEME.surface)).toBeGreaterThan(4.5)
 expect(colorContrast("#ffffff", "#000000")).toBeCloseTo(21)
})

import { pageThemeVars } from "../theme"

// The settings preview and the Pages SDK must paint a palette the same way:
// the same --crewship-page-* variables, and the same black-or-white text on
// the accent (tools/pages-build/sdk.ts applyTheme, threshold 0.179).
describe("pageThemeVars", () => {
  it("maps every colour to its --crewship-page-* variable", () => {
    const vars = pageThemeVars({ accent: "#9fe5bd", background: "#0c1410", surface: "#121c16", text: "#edf5ef", muted: "#a3b6aa", border: "#2a3b30" })
    expect(vars["--crewship-page-background"]).toBe("#0c1410")
    expect(vars["--crewship-page-border"]).toBe("#2a3b30")
    expect(Object.keys(vars).filter((k) => k.startsWith("--crewship-page-"))).toHaveLength(7)
  })

  it.each([
    ["#9fe5bd", "#000000"],
    ["#0e6be8", "#ffffff"],
    ["#ffffff", "#000000"],
    ["#1b1f2c", "#ffffff"],
  ])("text on accent %s is %s", (accent, onAccent) => {
    expect(pageThemeVars({ accent, background: "#000000", surface: "#000000", text: "#ffffff", muted: "#999999", border: "#333333" })["--crewship-page-on-accent"]).toBe(onAccent)
  })

  it("says whether the page reads as light or dark", () => {
    expect(pageThemeVars({ accent: "#000000", background: "#f4f7fb", surface: "#ffffff", text: "#0f1733", muted: "#566079", border: "#d8e0eb" }).colorScheme).toBe("light")
    expect(pageThemeVars({ accent: "#000000", background: "#0c1410", surface: "#121c16", text: "#edf5ef", muted: "#a3b6aa", border: "#2a3b30" }).colorScheme).toBe("dark")
  })
})

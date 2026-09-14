import { expect, it } from "vitest"
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

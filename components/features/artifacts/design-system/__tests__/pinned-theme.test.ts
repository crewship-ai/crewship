import { describe, expect, it } from "vitest"
import { pinnedThemeCss, pinnedSlideTheme, withPinnedTheme, slideFontFaces, parsePinnedFiles, type PinnedFile } from "../pinned-theme"

const tokens = JSON.stringify({
  schema_version: 2, name: "Harbor", themes: [{ id: "light", name: "Light" }, { id: "dark", name: "Dark" }],
  colors: { primary: { value: { light: "#1d4ed8", dark: "#93c5fd" }, usage: "Buttons" }, ink: "#111111" },
  typography: { body: { size: 16, weight: 400, font_family: "Inter, sans-serif" } },
  spacing: { md: 16 }, radii: { sm: 4 },
})
const inter: PinnedFile = { path: "fonts/Inter.woff2", media_type: "font/woff2", status: "copied", family: "Inter", weight: 400, style: "normal", content: "d09GMgABAAA=" }
const licence: PinnedFile = { path: "fonts/OFL.txt", media_type: "text/plain", status: "copied", family: "", weight: 0, style: "", content: "U0lM" }
const secret: PinnedFile = { path: "fonts/Secret.woff2", media_type: "font/woff2", status: "licence", family: "Secret", weight: 0, style: "", content: "" }

describe("pinned theme", () => {
  it("derives the default theme's tokens as custom properties and a face per copied font", () => {
    const css = pinnedThemeCss(tokens, [inter, licence, secret])!
    expect(css).toContain("--ds-color-primary:#1d4ed8;")
    expect(css).not.toContain("#93c5fd")
    expect(css).toContain("--ds-color-ink:#111111;")
    expect(css).toContain("--ds-space-md:16px;")
    expect(css).toContain("--ds-radius-sm:4px;")
    expect(css).toContain("--ds-font-body:Inter, sans-serif;")
    expect(css).toContain("--ds-font-size-body:16px;")
    expect(css).toContain(`@font-face{font-family:"Inter";src:url(data:font/woff2;base64,d09GMgABAAA=) format("woff2");font-weight:400;font-style:normal;font-display:swap}`)
    // A font left behind for its licence, and a licence file, get no face.
    expect(css).not.toContain("Secret")
    expect(css).not.toContain("OFL")
  })

  it("renders nothing from a copy it cannot read, and never lets a value close the style", () => {
    expect(pinnedThemeCss("{not json", [])).toBeNull()
    expect(pinnedThemeCss(tokens, [{ ...inter, content: "AAAA</style><script>alert(1)</script>" }])).not.toContain("<")
    expect(pinnedThemeCss(tokens, [{ ...inter, family: "x;}body{color:red" }])).not.toContain("body{")
    expect(pinnedThemeCss(tokens, [{ ...inter, media_type: "text/html" }])).not.toContain("@font-face")
    // A quote would end the family string inside @font-face; such a face is dropped.
    expect(pinnedThemeCss(tokens, [{ ...inter, family: 'Inter" ;src:url(x)' }])).not.toContain("@font-face")
    expect(slideFontFaces([{ ...inter, family: 'In"ter' }], "deck")).toEqual([])
  })

  it("puts the pinned theme first in the head so the project's own styles still win", () => {
    const html = "<!doctype html><html><head><title>x</title><style>h1{color:red}</style></head><body><h1>Hi</h1></body></html>"
    const out = withPinnedTheme(html, ":root{--ds-color-ink:#111111;}")
    expect(out.indexOf("data-library-pin")).toBeLessThan(out.indexOf("h1{color:red}"))
    expect(withPinnedTheme("<h1>No head</h1>", ":root{}")).toMatch(/^<style data-library-pin>:root\{\}<\/style><h1>/)
    expect(withPinnedTheme(html, null)).toBe(html)
  })

  it("names slide fonts apart from the app's own families", () => {
    const faces = slideFontFaces([inter, secret], "deck-1")
    expect(faces).toHaveLength(1)
    expect(faces[0]).toMatchObject({ family: "Inter", source: "url(data:font/woff2;base64,d09GMgABAAA=)", weight: "400", style: "normal" })
    const families = slideFontFaces([{ ...inter, family: "A B" }, { ...inter, family: "A-B" }], "deck-1")
    expect(new Set(families.map(face => face.alias)).size).toBe(2)
    expect(slideFontFaces([inter], "deck/1")[0].alias).not.toBe(faces[0].alias)
  })

  it("scans repeated comments without backtracking or mistaking a commented doctype for a real one", () => {
    const css = ":root{--ds-color-ink:#101010;}"
    const style = `<style data-library-pin>${css}</style>`
    const comments = "<!-- <!doctype fake> -->".repeat(4096)
    const document = "<html><body>Text</body></html>"
    expect(withPinnedTheme(comments + document, css)).toBe(style + comments + document)
    expect(withPinnedTheme(comments + "<!DOCTYPE html>" + document, css)).toBe(comments + "<!DOCTYPE html>" + style + document)
    expect(withPinnedTheme(comments + "<!-- <!doctype html>", css)).toBe(style + comments + "<!-- <!doctype html>")
  })

  it("keeps a leading doctype and never writes into a commented-out head", () => {
    const html = '<!-- <head> -->\n<!doctype html><html><head><title>Preview</title></head><body>Text</body></html>'
    const out = withPinnedTheme(html, ":root{--ds-color-ink:#111111;}")
    expect(out).toMatch(/^<!-- <head> -->\s*<!doctype html><style data-library-pin>/i)
  })

  it("resolves only requested Slides preview bindings from the pinned tokens", () => {
    const binding = { path: "slides-library.json", content: JSON.stringify({ schema_version: 1, background: "primary", text_color: "ink", typography: "body" }) }
    expect(pinnedSlideTheme(tokens, [binding])).toEqual({ background: "#1d4ed8", textColor: "#111111", typography: { size: 16, weight: 400, family: "Inter" } })
    expect(pinnedSlideTheme(undefined, [binding])).toBeUndefined()
    expect(pinnedSlideTheme(tokens, [binding, binding])).toBeUndefined()
    expect(pinnedSlideTheme(tokens, [{ ...binding, content: '{"schema_version":1,"background":"url(x)"}' }])).toBeUndefined()
  })

  it("admits only well-formed file lists", () => {
    expect(parsePinnedFiles({ files: [inter, secret] })).toHaveLength(2)
    expect(parsePinnedFiles({ files: [{ ...inter, status: "maybe" }] })).toBeNull()
    expect(parsePinnedFiles({ nope: [] })).toBeNull()
  })
})

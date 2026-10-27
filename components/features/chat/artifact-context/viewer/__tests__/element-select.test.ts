import { describe, expect, it } from "vitest"
import { anchorSelectorForId, buildPreviewDocument, isValidAnchorSelector, normalizeQuote, screenLabel, screensOf } from "../element-select"

const page = "<!doctype html><html><head><title>x</title></head><body><h1 id=\"hero\">Hi</h1></body></html>"

describe("buildPreviewDocument", () => {
  it("injects the select script only in select mode", () => {
    const off = buildPreviewDocument(page, { selectMode: false, screens: ["index.html"] })
    const on = buildPreviewDocument(page, { selectMode: true, screens: ["index.html"] })
    expect(off).not.toContain("crewship-artifact-select")
    expect(on).toContain("crewship-artifact-select")
  })
  it("intercepts screen links in both modes and keeps the page before the injected script", () => {
    for (const selectMode of [false, true]) {
      const doc = buildPreviewDocument(page, { selectMode, screens: ["index.html", "pricing.html"] })
      expect(doc).toContain("crewship-artifact-navigate")
      expect(doc.indexOf("<h1 id=\"hero\">")).toBeLessThan(doc.indexOf("<script"))
      expect(doc.trim().endsWith("</html>")).toBe(true)
    }
  })
  it("appends the script when the document has no body tag", () => {
    expect(buildPreviewDocument("<p id=a>x</p>", { selectMode: true, screens: [] })).toMatch(/^<p id=a>x<\/p><script/)
  })
  it("cannot be broken out of through a screen name", () => {
    const doc = buildPreviewDocument(page, { selectMode: false, screens: ["</script><script>alert(1)</script>.html"] })
    expect(doc).not.toContain("</script><script>alert(1)")
  })
})

describe("anchors", () => {
  it.each([
    ["hero", "#hero"],
    ["123", "#\\31 23"],
    ["a.b", "#a\\.b"],
    ["-1x", "#-\\31 x"],
    ["a b", "#a\\ b"],
  ])("escapes id %j as %j", (id, selector) => {
    expect(anchorSelectorForId(id)).toBe(selector)
    expect(isValidAnchorSelector(selector)).toBe(true)
  })
  it.each(["", "#", "hero", ".hero", "#a #b", "#a,#b", "#a#b", "#a.b", "#a>b", "#a[x]", "div#a", "#a:hover"])("rejects %j", selector => {
    expect(isValidAnchorSelector(selector)).toBe(false)
  })
  it("normalizes quotes like the server", () => {
    expect(normalizeQuote("  Hello\n\t  world  ")).toBe("Hello world")
    expect(normalizeQuote("x".repeat(600))).toHaveLength(500)
  })
})

describe("screens", () => {
  it("lists html files with the entrypoint first", () => {
    expect(screensOf({ entrypoint: "home.html", files: [
      { path: "about.html", content: "", media_type: "text/html" },
      { path: "style.css", content: "", media_type: "text/css" },
      { path: "home.html", content: "", media_type: "text/html" },
      { path: "pages/pricing.HTML", content: "", media_type: "text/html" },
    ] })).toEqual(["home.html", "about.html", "pages/pricing.HTML"])
    expect(screenLabel("pages/pricing-plans.html")).toBe("Pricing-plans")
  })
})

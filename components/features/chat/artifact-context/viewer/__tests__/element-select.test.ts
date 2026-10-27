import { describe, expect, it } from "vitest"
import { ANCHOR_QUOTE_MAX, ANCHOR_QUOTE_SOURCE, anchorSelectorForId, buildPreviewDocument, inlineProjectAssets, isValidAnchorSelector, normalizeQuote, screenLabel, screenLabels, screensOf } from "../element-select"

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

describe("inlineProjectAssets", () => {
  const files = [
    { path: "index.html", content: "", media_type: "text/html" },
    { path: "styles.css", content: "h1{color:red}", media_type: "text/css" },
    { path: "script.js", content: "window.ran=1", media_type: "text/javascript" },
    { path: "pages/sub.css", content: ".sub{}", media_type: "text/css" },
  ]
  const page = '<!doctype html><html><head><link rel="stylesheet" href="styles.css"><link rel="stylesheet" href="https://cdn.example/x.css"></head><body><h1 id="hero">Hi</h1><script src="script.js" defer></script></body></html>'

  it("puts the project's own stylesheets and scripts into the document, as a sandboxed frame cannot fetch them", () => {
    const out = inlineProjectAssets(page, "index.html", files)
    expect(out).toMatch(/^<!DOCTYPE html>/i)
    expect(out).toContain("<style>h1{color:red}</style>")
    expect(out).toContain("window.ran=1</script>")
    expect(out).not.toContain('src="script.js"')
    expect(out).not.toContain('href="styles.css"')
    // Not part of the project: left for the browser to handle.
    expect(out).toContain('href="https://cdn.example/x.css"')
    expect(out).toContain('<h1 id="hero">Hi</h1>')
  })
  it("resolves paths relative to the screen", () => {
    expect(inlineProjectAssets('<link rel="stylesheet" href="sub.css">', "pages/a.html", files)).toContain("<style>.sub{}</style>")
    expect(inlineProjectAssets('<link rel="stylesheet" href="../styles.css">', "pages/a.html", files)).toContain("<style>h1{color:red}</style>")
  })
  it("leaves a document without project assets untouched", () => {
    const plain = "<p id=a>x</p>"
    expect(inlineProjectAssets(plain, "index.html", files)).toBe(plain)
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
  it("normalizes quotes like the server (Go strings.Fields), without truncating", () => {
    expect(normalizeQuote("  Hello\n\t  world  ")).toBe("Hello world")
    expect(normalizeQuote("x".repeat(600))).toHaveLength(600)
    // Go's unicode.IsSpace: NEL and NBSP are spaces, U+FEFF is not (JS \\s disagrees on both).
    expect(normalizeQuote("\u00a0a\u0085b\u3000c\ufeff")).toBe("a b c\ufeff")
  })
})

describe("anchor quote inside the frame", () => {
  const quoteOf = new Function(`${ANCHOR_QUOTE_SOURCE};return quoteOf`)() as (el: Element) => string
  const el = (markup: string) => {
    document.body.innerHTML = markup
    return document.getElementById("t")!
  }
  it("joins text nodes with no separator between tags, like the server", () => {
    expect(quoteOf(el(`<div id="t"><h3>Team</h3><p>$24</p></div>`))).toBe("Team$24")
    expect(quoteOf(el(`<div id="t">\n  <h3>Team</h3>\n  <p>$24</p>\n</div>`))).toBe("Team $24")
  })
  it("skips script and style subtrees", () => {
    expect(quoteOf(el(`<div id="t">A<script>var x = 1</script><style>p{}</style><b>B</b></div>`))).toBe("AB")
  })
  it("never truncates", () => {
    expect(quoteOf(el(`<p id="t">${"y".repeat(800)}</p>`))).toHaveLength(800)
  })
  it("sets a limit the select script enforces", () => {
    expect(ANCHOR_QUOTE_MAX).toBe(5000)
    expect(buildPreviewDocument(page, { selectMode: true, screens: [] })).toContain(String(ANCHOR_QUOTE_MAX))
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
  it("uppercases short names and prefers a short, unique <title>", () => {
    expect(screenLabel("faq.html")).toBe("FAQ")
    expect(screenLabel("pricing.html")).toBe("Pricing")
    expect(screenLabel("faq.html", "<html><head><title>\n Questions &amp; answers </title></head></html>")).toBe("Questions & answers")
    expect(screenLabel("faq.html", "<title>A title that is much too long for a tab</title>")).toBe("FAQ")
    expect(screenLabel("faq.html", "<title>  </title>")).toBe("FAQ")
    const file = (path: string, title: string) => ({ path, content: `<title>${title}</title>`, media_type: "text/html" })
    expect(screenLabels({ entrypoint: "a.html", files: [file("a.html", "Plans"), file("b.html", "Compare")] }))
      .toEqual(new Map([["a.html", "Plans"], ["b.html", "Compare"]]))
    // Every page titled "Acme" would make the switcher useless: fall back to file names.
    expect(screenLabels({ entrypoint: "a.html", files: [file("a.html", "Acme"), file("faq.html", "Acme")] }))
      .toEqual(new Map([["a.html", "A"], ["faq.html", "FAQ"]]))
  })
})

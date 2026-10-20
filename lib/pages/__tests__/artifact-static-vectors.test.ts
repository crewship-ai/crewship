import { describe, expect, it } from "vitest"
import { staticArtifactDocument } from "../artifact-static"
import { staticCSS } from "../artifact-raster"

// The DOM-level vectors (script, on* handlers, svg/mathml, srcdoc, meta
// refresh, base, form, data: URLs) need a spec-faithful HTML parser: the
// unit environment (happy-dom) mis-parses fragments for DOMPurify, so those run
// in real browsers in e2e/static-app-preview.browser.mjs. This file covers the
// rules that do not depend on the DOM implementation.
describe("staticCSS rejects loading and legacy-execution constructs", () => {
  const sink = "https://sink.test"
  it.each([
    [`background:url(${sink})`], [`background:URL( "${sink}" )`], [`background-image:image-set("${sink}" 1x)`],
    [`content:src(${sink})`], [`background:cross-fade(url(a),url(b))`], [`background:paint(x)`],
    [`background:\\75rl(${sink})`], [`width:expression(alert(1))`], [`width:EXPRESSION (alert(1))`],
    [`behavior:url(x.htc)`], [`-moz-binding:url(${sink})`], [`background:/*c*/url(${sink})`],
    [`color:red;background:u/**/rl(${sink})`],
    [`@import url(${sink});p{color:red}`], [`@import "${sink}";`], [`@font-face{font-family:x;src:url(${sink})}`],
    [`@namespace url(${sink});`], [`@media print{@import "${sink}";}`], [`@charset "x";`], [`@\\69mport "${sink}";`],
    [`a{background:javascript:alert(1)}`],
  ])("refuses %s", css => expect(staticCSS(css)).toBe(false))
  it.each([
    ["@media (min-width:1px){p{color:rgb(1,2,3)}}"], ["@supports (display:grid){p{display:grid}}"],
    ["@keyframes a{from{opacity:0}to{opacity:1}}"], ["p{margin:0 auto;border:1px solid #ddd}"],
  ])("keeps static rule %s", css => expect(staticCSS(css)).toBe(true))
})

describe("staticArtifactDocument envelope", () => {
  it("always carries the restrictive CSP and no-referrer policy", async () => {
    const html = await staticArtifactDocument("<p>ok</p>")
    for (const part of ["default-src 'none'", "script-src 'none'", "connect-src 'none'", "frame-src 'none'", "form-action 'none'", "base-uri 'none'", "object-src 'none'", 'name="referrer" content="no-referrer"']) expect(html).toContain(part)
  })
  it("bounds oversized source instead of parsing it, counting bytes not characters", async () => {
    expect(await staticArtifactDocument("a".repeat(10 * 1024 * 1024 + 1))).toContain("exceeds the 10 MiB source limit")
    expect(await staticArtifactDocument("é".repeat(5 * 1024 * 1024 + 1))).toContain("exceeds the 10 MiB source limit")
  })
  it("rejects an already aborted preparation", async () => {
    const controller = new AbortController(); controller.abort()
    await expect(staticArtifactDocument("<p>x</p>", controller.signal)).rejects.toThrow()
  })
})

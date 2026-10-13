import DOMPurify from "dompurify"
import { afterEach, beforeEach, describe, expect, it } from "vitest"
import { staticArtifactDocument } from "../artifact-static"

const supported = DOMPurify.isSupported
beforeEach(() => { DOMPurify.isSupported = false })
afterEach(() => { DOMPurify.isSupported = supported })

describe("artifact static unsupported-browser boundary", () => {
  it("escapes executable and navigation content before constructing a fallback frame document", () => {
    const result = staticArtifactDocument(`<meta http-equiv="refresh" content="0;url=https://sink.test"><base href="https://sink.test"><link rel="preconnect" href="https://sink.test"><script>alert(1)</script><iframe src="https://sink.test"></iframe><svg><a href="https://sink.test">SVG</a></svg><form action="https://sink.test"><input name="secret"></form><a href="javascript:alert(1)" ping="https://sink.test" target="_blank" onclick="alert(1)">Report</a><img src="https://sink.test" srcset="https://sink.test 2x" onerror="alert(1)">`)
    const doc = new DOMParser().parseFromString(result, "text/html")
    expect(doc.querySelectorAll("script,iframe,svg,math,form,input,base,link")).toHaveLength(0)
    expect(doc.querySelectorAll("[href],[src],[srcset],[ping],[target],[onclick],[onerror]")).toHaveLength(0)
    expect(doc.querySelectorAll('meta[http-equiv]')).toHaveLength(1)
    expect(doc.querySelector('meta[http-equiv]')?.getAttribute("content")).toContain("default-src 'none'")
    expect(doc.body.textContent).toContain("Report")
  })
  it("fails closed to escaped source when DOMPurify cannot sanitize this DOM", () => {
    const result = staticArtifactDocument('<h1>Summary</h1><script>alert(1)</script>')
    expect(result).toContain('&lt;h1&gt;Summary&lt;/h1&gt;')
    expect(result).not.toContain('<script>')
    expect(result).toContain("script-src 'none'")
  })
})

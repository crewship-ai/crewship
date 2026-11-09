import { describe, expect, it } from "vitest"
import { MAX_MATH_CHARS, renderMath } from "../code-preview-math"

const ok = (source: string) => {
  const result = renderMath(source)
  if (!result.ok) throw new Error(result.error)
  return result.node
}

describe("renderMath", () => {
  it("typesets block math with MathML that keeps the source", () => {
    const node = ok(String.raw`E = mc^2 + \frac{\sqrt{a}}{\sum_{i=1}^{n} x_i}`)
    expect(node.matches(".katex-display") || node.querySelector(".katex-display")).toBeTruthy()
    expect(node.querySelector("math")).not.toBeNull()
    expect(node.querySelector("annotation")!.textContent).toBe(String.raw`E = mc^2 + \frac{\sqrt{a}}{\sum_{i=1}^{n} x_i}`)
  })

  it.each([
    ["an unclosed group", String.raw`\frac{a}{b`],
    ["an unknown command", String.raw`\notacommand{x}`],
    ["a stray close", "a}"],
  ])("reports %s as an error for this block only", (_name, source) => {
    const result = renderMath(source)
    expect(result.ok).toBe(false)
    if (!result.ok) expect(result.error).toMatch(/^Cannot show this formula: /)
  })

  it.each([
    ["\\href", String.raw`\href{javascript:alert(1)}{x}`],
    ["\\url", String.raw`\url{https://example.test/a}`],
    ["\\includegraphics", String.raw`\includegraphics{https://example.test/a.png}`],
    ["\\htmlClass", String.raw`\htmlClass{evil}{x}`],
    ["\\htmlData", String.raw`\htmlData{onclick=alert(1)}{x}`],
    ["\\htmlStyle", String.raw`\htmlStyle{background:url(https://example.test/a.png)}{x}`],
    ["\\htmlId", String.raw`\htmlId{x}{y}`],
  ])("never links, loads or adds attributes through %s", (_name, source) => {
    const result = renderMath(source)
    if (!result.ok) return
    const node = result.node
    expect(node.querySelector("a, img, image, iframe, object, embed, use, [href], [src], [xlink\\:href], [onclick], [id], .evil")).toBeNull()
    for (const element of node.querySelectorAll("*")) {
      for (const attribute of element.attributes) {
        expect(attribute.name).not.toMatch(/^(on|data-)/)
        expect(attribute.value).not.toMatch(/javascript:|url\(|example\.test/)
      }
    }
  })

  it("strips anything active even if the renderer produced it", () => {
    const node = ok(String.raw`\text{<img src=x onerror=alert(1)>}`)
    expect(node.querySelector("img, script, [onerror]")).toBeNull()
    expect(node.querySelector("annotation-xml")).toBeNull()
  })

  it("refuses empty and oversized sources without rendering", () => {
    expect(renderMath("  \n")).toEqual({ ok: false, error: "Nothing to show yet. Write a formula in Source." })
    const long = renderMath("x".repeat(MAX_MATH_CHARS + 1))
    expect(long.ok).toBe(false)
  })

  it("bounds macro expansion", () => {
    const bomb = String.raw`\def\a{\b\b\b\b\b\b\b\b\b\b}\def\b{\c\c\c\c\c\c\c\c\c\c}\def\c{x x x x x x x x x x}\a\a\a\a\a\a\a\a\a\a`
    const result = renderMath(bomb)
    expect(result.ok).toBe(false)
  })
})

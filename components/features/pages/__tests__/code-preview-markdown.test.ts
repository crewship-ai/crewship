import { describe, expect, it } from "vitest"
import { MARKDOWN_LIMITS, renderMarkdown, safeLinkHref } from "../code-preview-markdown"

const ok = (source: string) => {
  const result = renderMarkdown(source)
  if (!result.ok) throw new Error(result.error)
  return result.node
}

describe("renderMarkdown", () => {
  it("renders headings, emphasis, lists, checklists, quotes, code and tables", () => {
    const node = ok([
      "# Title", "", "Some **bold**, *italic*, ~~gone~~ and `code`.", "",
      "1. one", "2. two", "", "- [x] done", "- [ ] open", "", "> quoted", "",
      "```go", "func main() {}", "```", "", "| a | b |", "|---|:-:|", "| 1 | 2 |",
    ].join("\n"))
    expect(node.querySelector("h1")!.textContent).toBe("Title")
    expect(node.querySelector("strong")!.textContent).toBe("bold")
    expect(node.querySelector("em")!.textContent).toBe("italic")
    expect(node.querySelector("del")!.textContent).toBe("gone")
    expect(node.querySelector("p code")!.textContent).toBe("code")
    expect([...node.querySelectorAll("ol li")].map(li => li.textContent)).toEqual(["one", "two"])
    const boxes = [...node.querySelectorAll<HTMLInputElement>("ul li input[type=checkbox]")]
    expect(boxes.map(box => [box.checked, box.disabled])).toEqual([[true, true], [false, true]])
    expect(node.querySelector("blockquote")!.textContent).toBe("quoted")
    expect(node.querySelector("pre code")!.textContent).toBe("func main() {}")
    expect([...node.querySelectorAll("th")].map(th => th.textContent)).toEqual(["a", "b"])
    expect(node.querySelectorAll("td")[1].style.textAlign).toBe("center")
  })

  it("shows raw HTML as the typed characters and never creates its elements", () => {
    const node = ok("Hi <img src=x onerror=alert(1)> there\n\n<script>alert(2)</script>\n\n<iframe src=\"https://example.test\"></iframe>")
    expect(node.querySelector("img, script, iframe, [onerror]")).toBeNull()
    expect(node.textContent).toContain("<img src=x onerror=alert(1)>")
    expect(node.textContent).toContain("<script>alert(2)</script>")
  })

  it("never fetches images and keeps only safe links", () => {
    const node = ok("![chart](https://example.test/a.png) [ok](https://example.com/x) [mail](mailto:a@example.com) [bad](javascript:alert(1)) [data](data:text/html,x) [creds](https://u:p@example.com)")
    expect(node.querySelector("img")).toBeNull()
    expect(node.querySelector("[data-markdown-image]")!.textContent).toBe("[Image: chart]")
    const links = [...node.querySelectorAll("a")]
    expect(links.map(a => a.getAttribute("href"))).toEqual(["https://example.com/x", "mailto:a@example.com"])
    expect(links.every(a => a.rel === "noopener noreferrer nofollow" && a.target === "_blank")).toBe(true)
    expect(node.textContent).toContain("bad")
    expect(node.innerHTML).not.toMatch(/javascript:|data:text/)
  })

  it.each([
    ["javascript:alert(1)", null], ["JAVASCRIPT:alert(1)", null], ["vbscript:x", null], ["data:text/html,x", null],
    ["https://u:p@example.com", null], ["/relative", null], ["https://example.com/a?b#c", "https://example.com/a?b#c"],
  ])("safeLinkHref(%s)", (href, expected) => {
    expect(safeLinkHref(href)).toBe(expected)
  })

  it("refuses empty, oversized and too deeply nested sources", () => {
    expect(renderMarkdown(" ").ok).toBe(false)
    expect(renderMarkdown("x".repeat(MARKDOWN_LIMITS.chars + 1)).ok).toBe(false)
    const deep = renderMarkdown(">".repeat(MARKDOWN_LIMITS.depth + 5) + " deep")
    expect(deep.ok).toBe(false)
    if (!deep.ok) expect(deep.error).toMatch(/nested deeper than/)
    const many = renderMarkdown(Array.from({ length: MARKDOWN_LIMITS.tokens }, (_, i) => `- ${i}`).join("\n"))
    expect(many.ok).toBe(false)
  })
})

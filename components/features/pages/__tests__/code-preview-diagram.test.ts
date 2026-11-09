import { afterEach, describe, expect, it, vi } from "vitest"

const mermaid = vi.hoisted(() => ({ initialize: vi.fn(), render: vi.fn() }))
vi.mock("mermaid", () => ({ default: mermaid }))

import { DIAGRAM_LIMITS, MERMAID_OPTIONS, renderDiagram, sanitizeDiagramSvg } from "../code-preview-diagram"

afterEach(() => { mermaid.render.mockReset(); mermaid.initialize.mockClear(); vi.restoreAllMocks(); delete (window as unknown as { __ran?: boolean }).__ran })

const svgOf = (inner: string, id = "d") => `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" id="${id}" viewBox="0 0 100 50">${inner}</svg>`
const shadow = (node: HTMLElement) => node.shadowRoot ?? node

describe("sanitizeDiagramSvg", () => {
  it("keeps shapes, text, markers and plain styles", () => {
    const svg = sanitizeDiagramSvg(svgOf(`<style>#d .node rect{fill:#eee;stroke:#333}</style><defs><marker id="m"><path d="M0 0L5 5"/></marker></defs><g class="node"><rect width="10" height="10"/><text>Start</text></g><path d="M0 0L10 10" marker-end="url(#m)"/>`))!
    expect(svg.tagName.toLowerCase()).toBe("svg")
    expect(svg.querySelector("rect")).not.toBeNull()
    expect(svg.querySelector("text")!.textContent).toBe("Start")
    expect(svg.querySelector("path[marker-end]")!.getAttribute("marker-end")).toBe("url(#m)")
    expect(svg.querySelector("style")!.textContent).toContain("fill:#eee")
  })

  it("removes scripts, links, foreign content, images, use and event handlers", () => {
    const svg = sanitizeDiagramSvg(svgOf([
      `<script>window.__ran = true</script>`,
      `<a href="javascript:window.__ran=true" xlink:href="javascript:window.__ran=true"><text>Click me</text></a>`,
      `<foreignObject><div xmlns="http://www.w3.org/1999/xhtml"><img src="x" onerror="window.__ran=true"/></div></foreignObject>`,
      `<image href="https://example.test/a.png"/>`, `<use xlink:href="https://example.test/s.svg#x"/>`,
      `<rect onclick="window.__ran=true" onmouseover="window.__ran=true" width="1" height="1"/>`,
      `<animate attributeName="href" to="javascript:alert(1)"/><set attributeName="onclick" to="alert(1)"/>`,
    ].join("")))!
    expect(svg.querySelector("script, a, foreignObject, foreignobject, image, img, use, animate, set")).toBeNull()
    expect(svg.querySelector("[onclick], [onmouseover], [onerror], [href]")).toBeNull()
    expect(svg.outerHTML).not.toMatch(/javascript:|xlink:href|example\.test/)
    // A link's label stays visible as plain text.
    expect(svg.querySelector("text")!.textContent).toBe("Click me")
  })

  it("drops styles and attributes that would load anything", () => {
    const svg = sanitizeDiagramSvg(svgOf([
      `<style>@import url(https://example.test/x.css); #d{fill:red}</style>`,
      `<style>#d .label{background:url("https://example.test/t.png")}</style>`,
      `<style>@font-face{font-family:x;src:url(https://example.test/f.woff)}</style>`,
      `<style>#d{fill:\\75 rl(https://example.test/e)}</style>`,
      `<rect style="fill:url(https://example.test/p.svg#g)" width="1" height="1"/>`,
      `<rect fill="url(https://example.test/p.svg#g)" filter="url(//example.test/f)" width="1" height="1"/>`,
    ].join("")))!
    expect(svg.querySelector("style")).toBeNull()
    expect(svg.outerHTML).not.toContain("example.test")
    expect(svg.querySelectorAll("rect")).toHaveLength(2)
  })

  it.each(["fill", "filter", "mask", "cursor", "stroke", "marker-end", "clip-path"])("drops a CSS escape or external url in a plain %s attribute", attr => {
    const svg = sanitizeDiagramSvg(svgOf([
      `<rect id="a" width="1" height="1" ${attr}="\\75 rl(https://evil.test/esc)"/>`,
      `<rect id="b" width="1" height="1" ${attr}="u\\72 l(https://evil.test/esc2)"/>`,
      `<rect id="c" width="1" height="1" ${attr}="URL( 'https://evil.test/up')"/>`,
      `<rect id="d" width="1" height="1" ${attr}="url(#m) url(https://evil.test/two)"/>`,
      `<rect id="e" width="1" height="1" ${attr}="url(#m)"/>`,
    ].join("")))!
    for (const id of ["a", "b", "c", "d"]) expect(svg.querySelector(`#${id}`)!.hasAttribute(attr)).toBe(false)
    expect(svg.querySelector("#e")!.getAttribute(attr)).toBe("url(#m)")
    expect(svg.outerHTML).not.toContain("evil.test")
  })

  it("drops a style sheet that targets the host and rules that position anything", () => {
    const host = sanitizeDiagramSvg(svgOf(`<style>:host{position:fixed;inset:0;z-index:99999;background:#fff}</style><text>x</text>`))!
    expect(host.querySelector("style")).toBeNull()
    const positioned = sanitizeDiagramSvg(svgOf(`<style>#d .node{fill:#eee} #d .tip{position:fixed;top:0} #d .edge{stroke:#333}</style>`))!
    const text = positioned.querySelector("style")!.textContent!
    expect(text).toContain("fill:#eee")
    expect(text).toContain("stroke:#333")
    expect(text).not.toMatch(/position/)
  })

  it("reads output with HTML-only markup such as <br> instead of dropping it", () => {
    const svg = sanitizeDiagramSvg(svgOf(`<g><text>one<br>two</text></g>`))
    expect(svg).not.toBeNull()
    expect(svg!.querySelector("br")).toBeNull()
    expect(svg!.querySelector("text")!.textContent).toBe("onetwo")
  })

  it("returns null for anything that is not a single SVG", () => {
    expect(sanitizeDiagramSvg("<div>no</div>")).toBeNull()
    expect(sanitizeDiagramSvg("")).toBeNull()
  })
})

describe("renderDiagram", () => {
  it("configures mermaid strictly and renders each diagram with its own id", async () => {
    mermaid.render.mockImplementation(async (id: string) => ({ svg: svgOf(`<g><text>A</text></g>`, id) }))
    const first = await renderDiagram("flowchart LR\n  A --> B")
    const second = await renderDiagram("flowchart LR\n  B --> C")
    expect(first.ok && second.ok).toBe(true)
    // Mermaid's settings are global and shared with chat's renderer: they are set again before every diagram.
    expect(mermaid.initialize).toHaveBeenCalledTimes(2)
    expect(mermaid.initialize).toHaveBeenCalledWith(expect.objectContaining(MERMAID_OPTIONS))
    expect(MERMAID_OPTIONS).toMatchObject({ startOnLoad: false, securityLevel: "strict", htmlLabels: false, flowchart: { htmlLabels: false }, maxTextSize: 5_000, maxEdges: 150, suppressErrorRendering: true })
    // Directives in a diagram cannot loosen these settings or restyle the page.
    expect(MERMAID_OPTIONS.secure).toEqual(expect.arrayContaining(["securityLevel", "htmlLabels", "flowchart", "maxTextSize", "maxEdges", "themeCSS", "fontFamily"]))
    const [[idA, textA], [idB]] = mermaid.render.mock.calls
    expect(textA).toBe("flowchart LR\n  A --> B")
    expect(idA).toMatch(/^docs-diagram-/)
    expect(idA).not.toBe(idB)
    if (!first.ok) return
    const svg = shadow(first.node).querySelector("svg")!
    expect(svg.getAttribute("role")).toBe("img")
    expect(svg.getAttribute("aria-label")).toBe("Diagram")
    expect(first.node.dataset.diagramPreview).toBe("")
  })

  it("never lets a click callback, javascript: link or HTML label through", async () => {
    mermaid.render.mockImplementation(async (id: string) => ({ svg: svgOf(`<g class="node" onclick="window.__ran=true"><a href="javascript:window.__ran=true"><text>A</text></a></g><foreignObject><div xmlns="http://www.w3.org/1999/xhtml"><b>html</b><img src="x" onerror="window.__ran=true"/></div></foreignObject><script>window.__ran=true</script>`, id) }))
    const result = await renderDiagram("flowchart LR\n  A-->B\n  click A \"javascript:window.__ran=true\"\n  click B callback")
    expect(result.ok).toBe(true)
    if (!result.ok) return
    document.body.append(result.node)
    const root = shadow(result.node)
    expect(root.querySelector("a, script, foreignObject, foreignobject, img, b, [onclick], [href]")).toBeNull()
    expect(root.querySelector("text")!.textContent).toBe("A")
    expect((window as unknown as { __ran?: boolean }).__ran).toBeUndefined()
    result.node.remove()
  })

  it("explains a diagram mermaid cannot read, in that block only", async () => {
    mermaid.render.mockRejectedValue(new Error("Parse error on line 2:\n...A -->\n-----^\nExpecting 'NODE_STRING', got 'EOF'"))
    const result = await renderDiagram("flowchart LR\n  A -->")
    expect(result).toEqual({ ok: false, error: "Cannot show this diagram: Parse error on line 2: ...A --> -----^ Expecting 'NODE_STRING', got 'EOF'" })
  })

  it("refuses output that sanitizes to nothing", async () => {
    mermaid.render.mockResolvedValue({ svg: "<div>not svg</div>" })
    expect(await renderDiagram("flowchart LR\n A")).toEqual({ ok: false, error: "Cannot show this diagram: it could not be drawn safely." })
  })

  it("refuses empty and oversized sources without running mermaid", async () => {
    expect(await renderDiagram(" \n")).toEqual({ ok: false, error: "Nothing to show yet. Write a Mermaid diagram in Source." })
    expect(await renderDiagram("a".repeat(DIAGRAM_LIMITS.chars + 1))).toEqual({ ok: false, error: "This diagram is longer than 5,000 characters, so only its source is shown." })
    expect(mermaid.render).not.toHaveBeenCalled()
  })

  // Mermaid draws into the live page before its output can be cleaned, so anything that would make it load
  // an image, icon or styled URL is refused before Mermaid sees it.
  it.each([
    ["an image shape", 'flowchart LR\n  A@{ img: "https://evil.test/shape.png", label: "x" }\n  A-->B'],
    ["a quoted image key", 'flowchart LR\n  A@{ "img": "/api/v1/me" }'],
    ["an icon shape", 'flowchart LR\n  A@{ icon: "fa:user" }'],
    ["sequence properties", 'sequenceDiagram\n  participant A\n  properties A: {"icon":"https://evil.test/seqicon.png"}'],
    ["a classDef url", "stateDiagram-v2\n  s1-->s2\n  classDef c fill:url(https://evil.test/sc)\n  class s1 c"],
    ["a style url", "flowchart LR\n  A-->B\n  style A fill:URL( https://evil.test/u )"],
    ["a CSS escape", "stateDiagram-v2\n  s1-->s2\n  classDef c fill:\\75 rl(https://evil.test/esc)"],
  ])("refuses %s without running mermaid", async (_name, source) => {
    const result = await renderDiagram(source)
    expect(result).toEqual({ ok: false, error: "Cannot show this diagram: it refers to an image, icon, URL or escaped text, which previews never load." })
    expect(mermaid.render).not.toHaveBeenCalled()
  })

  it("refuses too many or too deeply nested subgraphs without running mermaid", async () => {
    const deep = "flowchart LR\n" + Array.from({ length: DIAGRAM_LIMITS.depth + 1 }, (_, i) => `subgraph s${i}`).join("\n") + "\n  A\n" + "end\n".repeat(DIAGRAM_LIMITS.depth + 1)
    expect(await renderDiagram(deep)).toEqual({ ok: false, error: `Cannot show this diagram: subgraphs are nested deeper than ${DIAGRAM_LIMITS.depth} levels.` })
    const many = "flowchart LR\n" + Array.from({ length: DIAGRAM_LIMITS.subgraphs + 1 }, (_, i) => `subgraph s${i}\n  n${i}\nend`).join("\n")
    expect(await renderDiagram(many)).toEqual({ ok: false, error: `Cannot show this diagram: it has more than ${DIAGRAM_LIMITS.subgraphs} subgraphs.` })
    expect(mermaid.render).not.toHaveBeenCalled()
  })

  it("marks a diagram that took long to draw as slow", async () => {
    mermaid.render.mockImplementation(async (id: string) => ({ svg: svgOf("<g/>", id) }))
    const clock = vi.spyOn(performance, "now")
    clock.mockReturnValueOnce(1_000).mockReturnValueOnce(1_000 + DIAGRAM_LIMITS.slowMs + 1)
    const slow = await renderDiagram("flowchart LR\n A-->B")
    expect(slow.ok && slow.slow).toBe(true)
    clock.mockReturnValueOnce(1_000).mockReturnValueOnce(1_010)
    const quick = await renderDiagram("flowchart LR\n A-->B")
    expect(quick.ok && quick.slow).toBe(false)
  })
})

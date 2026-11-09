import { afterEach, describe, expect, it, vi } from "vitest"

const mermaid = vi.hoisted(() => {
  // Like mermaid: initialize merges into the site configuration that every later render uses.
  let site: Record<string, unknown> = { securityLevel: "strict", theme: "default" }
  return {
    initialize: vi.fn((config: Record<string, unknown>) => { site = { ...site, ...config } }),
    render: vi.fn(),
    mermaidAPI: { getSiteConfig: () => site },
    reset: (config: Record<string, unknown>) => { site = config },
  }
})
vi.mock("mermaid", () => ({ default: mermaid }))

import { DIAGRAM_LIMITS, MERMAID_OPTIONS, SURFACE_CSP, renderDiagram, sanitizeDiagramSvg, svgFromSandboxed } from "../code-preview-diagram"

afterEach(() => { mermaid.render.mockReset(); mermaid.initialize.mockClear(); vi.restoreAllMocks(); delete (window as unknown as { __ran?: boolean }).__ran })

const svgOf = (inner: string, id = "d") => `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" id="${id}" viewBox="0 0 100 50">${inner}</svg>`
const shadow = (node: HTMLElement) => node.shadowRoot ?? node
/** What mermaid's sandbox mode returns: a frame holding the SVG as a base64 data: document. */
const sandboxed = (svg: string) => {
  const bytes = new TextEncoder().encode(`<body style="margin:0">${svg}</body>`)
  return `<iframe style="width:100%;height:100px;" src="data:text/html;charset=UTF-8;base64,${btoa(Array.from(bytes, byte => String.fromCodePoint(byte)).join(""))}" sandbox="allow-top-navigation-by-user-activation allow-popups">
  The "iframe" tag is not supported by your browser.
</iframe>`
}
/** A mermaid render that, like the real one, draws in its sandbox frame and returns that frame. */
const drawing = (inner: string) => async (id: string, _text: string, container: HTMLElement) => {
  const frame = container.ownerDocument.createElement("iframe")
  frame.id = `i${id}`; frame.setAttribute("sandbox", "")
  // Attached without the surface's appendChild lock: happy-dom gives a frame no document until it loads one, and the
  // lock refuses a frame without a document. The lock on attach is exercised in the Chromium probe.
  container.insertBefore(frame, null)
  return { svg: sandboxed(svgOf(inner, id)) }
}

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
    mermaid.render.mockImplementation(drawing(`<g><text>A</text></g>`))
    const first = await renderDiagram("flowchart LR\n  A --> B")
    const second = await renderDiagram("flowchart LR\n  B --> C")
    expect(first.ok && second.ok).toBe(true)
    // Mermaid's settings are global and shared with chat's renderer: they are set again before every diagram.
    expect(mermaid.initialize.mock.calls.filter(([config]) => config === MERMAID_OPTIONS)).toHaveLength(2)
    expect(mermaid.initialize).toHaveBeenCalledWith(expect.objectContaining(MERMAID_OPTIONS))
    expect(MERMAID_OPTIONS).toMatchObject({ startOnLoad: false, securityLevel: "sandbox", htmlLabels: false, flowchart: { htmlLabels: false }, maxTextSize: 5_000, maxEdges: 150, suppressErrorRendering: true })
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
    mermaid.render.mockImplementation(drawing(`<g class="node" onclick="window.__ran=true"><a href="javascript:window.__ran=true"><text>A</text></a></g><foreignObject><div xmlns="http://www.w3.org/1999/xhtml"><b>html</b><img src="x" onerror="window.__ran=true"/></div></foreignObject><script>window.__ran=true</script>`))
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
    expect(result).toEqual({ ok: false, error: "Cannot show this diagram: Parse error on line 2: ...A --> -----^ Expecting 'NODE_STRING', got 'EOF'", ms: expect.any(Number) })
  })

  it("refuses output that sanitizes to nothing", async () => {
    mermaid.render.mockResolvedValue({ svg: sandboxed("<div>not svg</div>") })
    expect(await renderDiagram("flowchart LR\n A")).toMatchObject({ ok: false, error: "Cannot show this diagram: it could not be drawn safely." })
    // Only mermaid's sandbox frame is accepted: a bare SVG means it did not render in isolation.
    mermaid.render.mockResolvedValue({ svg: svgOf("<g/>") })
    expect(await renderDiagram("flowchart LR\n A")).toMatchObject({ ok: false, error: "Cannot show this diagram: it could not be drawn safely." })
  })

  it("refuses empty and oversized sources without running mermaid", async () => {
    expect(await renderDiagram(" \n")).toEqual({ ok: false, error: "Nothing to show yet. Write a Mermaid diagram in Source." })
    expect(await renderDiagram("a".repeat(DIAGRAM_LIMITS.chars + 1))).toEqual({ ok: false, error: "This diagram is longer than 5,000 characters, so only its source is shown." })
    expect(mermaid.render).not.toHaveBeenCalled()
  })

  // Mermaid draws in a network-free frame; sources that would load anything are also refused before it runs.
  it.each([
    ["an image shape", 'flowchart LR\n  A@{ img: "https://evil.test/shape.png", label: "x" }\n  A-->B'],
    ["a quoted image key", 'flowchart LR\n  A@{ "img": "/api/v1/me" }'],
    ["an icon shape", 'flowchart LR\n  A@{ icon: "fa:user" }'],
    ["sequence properties", 'sequenceDiagram\n  participant A\n  properties A: {"icon":"https://evil.test/seqicon.png"}'],
    ["a classDef url", "stateDiagram-v2\n  s1-->s2\n  classDef c fill:url(https://evil.test/sc)\n  class s1 c"],
    ["a style url", "flowchart LR\n  A-->B\n  style A fill:URL( https://evil.test/u )"],
    ["a CSS escape", "stateDiagram-v2\n  s1-->s2\n  classDef c fill:\\75 rl(https://evil.test/esc)"],
    ["a single-quoted image key", "flowchart LR\n  A@{ 'img': 'https://evil.test/sqimg', label: x }\n  A-->B"],
    ["a single-quoted icon key", "flowchart LR\n  A@{ 'icon': 'fa:user', label: x }"],
    ["an image-set mask", 'stateDiagram-v2\n  s1-->s2\n  classDef c mask-image:image-set("https://evil.test/is1" 1x)\n  class s1 c'],
    ["a webkit image-set", 'stateDiagram-v2\n  s1-->s2\n  classDef c -webkit-mask-image:-webkit-image-set("https://evil.test/wis" 1x)'],
    ["a cross-fade", 'flowchart LR\n  A-->B\n  style A background:cross-fade("https://evil.test/cf" 50%, red)'],
    ["an image()", 'flowchart LR\n  A-->B\n  style A background:image("https://evil.test/im")'],
    ["an entity-encoded url", "stateDiagram-v2\n  s1-->s2\n  classDef c fill:url#40;https://evil.test/ent#41;"],
  ])("refuses %s without running mermaid", async (_name, source) => {
    const result = await renderDiagram(source)
    expect(result).toEqual({ ok: false, error: "Cannot show this diagram: it refers to an image, icon, URL or escaped text, which previews never load." })
    expect(mermaid.render).not.toHaveBeenCalled()
  })

  it("refuses too many or too deeply nested subgraphs without running mermaid", async () => {
    const deep = "flowchart LR\n" + Array.from({ length: DIAGRAM_LIMITS.depth + 1 }, (_, i) => `subgraph s${i}`).join("\n") + "\n  A\n" + "end\n".repeat(DIAGRAM_LIMITS.depth + 1)
    expect(await renderDiagram(deep)).toEqual({ ok: false, error: `Cannot show this diagram: it is nested deeper than ${DIAGRAM_LIMITS.depth} levels.` })
    // On one line, separated by semicolons.
    const inline = "flowchart TD\n" + Array.from({ length: 280 }, (_, i) => `subgraph s${i};`).join("") + "a-->b;" + "end;".repeat(280)
    expect((await renderDiagram(inline)).ok).toBe(false)
    const states = "stateDiagram-v2\n" + Array.from({ length: DIAGRAM_LIMITS.depth + 1 }, (_, i) => `state S${i} {\n`).join("") + "a-->b\n" + "}\n".repeat(DIAGRAM_LIMITS.depth + 1)
    expect(await renderDiagram(states)).toEqual({ ok: false, error: `Cannot show this diagram: it is nested deeper than ${DIAGRAM_LIMITS.depth} levels.` })
    const many = "flowchart LR\n" + Array.from({ length: DIAGRAM_LIMITS.subgraphs + 1 }, (_, i) => `subgraph s${i}\n  n${i}\nend`).join("\n")
    expect(await renderDiagram(many)).toEqual({ ok: false, error: `Cannot show this diagram: it has more than ${DIAGRAM_LIMITS.subgraphs} subgraphs.` })
    expect(mermaid.render).not.toHaveBeenCalled()
  })

  it.each([
    ["state transitions", "stateDiagram-v2\n" + Array.from({ length: 151 }, (_, i) => `s${i}-->s${i + 1}`).join("\n")],
    ["sequence messages", "sequenceDiagram\n" + Array.from({ length: 151 }, (_, i) => `A${i % 5}->>A${(i + 1) % 5}: m`).join("\n")],
    ["class relations", "classDiagram\n" + Array.from({ length: 151 }, (_, i) => `C${i} <|-- C${i + 1}`).join("\n")],
    ["ER relations", "erDiagram\n" + Array.from({ length: 151 }, (_, i) => `E${i} ||--o{ E${i + 1} : r`).join("\n")],
  ])("refuses more than the connection limit in %s without running mermaid", async (_name, source) => {
    expect(await renderDiagram(source)).toEqual({ ok: false, error: `Cannot show this diagram: it has more than ${DIAGRAM_LIMITS.edges} connections.` })
    expect(mermaid.render).not.toHaveBeenCalled()
  })

  it("refuses more statements than the limit, and accepts an ER diagram with many relations under it", async () => {
    const tasks = "gantt\n  dateFormat YYYY-MM-DD\n  section S\n" + Array.from({ length: DIAGRAM_LIMITS.statements }, () => "  T :1d").join("\n")
    expect(await renderDiagram(tasks)).toEqual({ ok: false, error: `Cannot show this diagram: it has more than ${DIAGRAM_LIMITS.statements} statements.` })
    mermaid.render.mockImplementation(drawing("<g/>"))
    expect((await renderDiagram("erDiagram\n" + Array.from({ length: 20 }, (_, i) => `E${i} ||--o{ E${i + 1} : r`).join("\n"))).ok).toBe(true)
  })

  it("refuses to draw when mermaid's frame has no document to lock", async () => {
    mermaid.render.mockImplementation(async (id: string, _text: string, container: HTMLElement) => {
      const frame = container.ownerDocument.createElement("iframe"); frame.id = `i${id}`
      container.appendChild(frame)
      return { svg: sandboxed(svgOf("<g/>", id)) }
    })
    expect(await renderDiagram("flowchart LR\n A-->B")).toMatchObject({ ok: false, error: "Cannot show this diagram: this browser cannot draw it in isolation." })
  })

  it("draws inside a frame whose policy allows no requests, and locks mermaid's own frame too", async () => {
    let seen: { policy: string | null | undefined; sandbox: string | null; inPage: boolean; inner: string | null | undefined } | undefined
    mermaid.render.mockImplementation(async (id: string, text: string, container: HTMLElement) => {
      const doc = container.ownerDocument
      const frame = [...document.querySelectorAll("iframe")].find(candidate => candidate.contentDocument === doc)
      const inner = doc.createElement("iframe"); inner.id = `i${id}`; inner.setAttribute("sandbox", "")
      // happy-dom gives a frame a document only once it loads one.
      const loaded = new Promise(done => inner.addEventListener("load", done, { once: true }))
      inner.srcdoc = "<!doctype html><html><head></head><body></body></html>"
      container.insertBefore(inner, null)
      await loaded
      // Mermaid looks its frame up by id in the page and draws in its document.
      const found = document.querySelector(`#i${id}`) as HTMLIFrameElement
      seen = {
        policy: doc.querySelector('meta[http-equiv="Content-Security-Policy"]')?.getAttribute("content"),
        sandbox: frame?.getAttribute("sandbox") ?? null, inPage: doc === document,
        inner: found.contentDocument?.querySelector('meta[http-equiv="Content-Security-Policy"]')?.getAttribute("content"),
      }
      return drawing("<g/>")(id, text, container)
    })
    expect((await renderDiagram("flowchart LR\n A-->B")).ok).toBe(true)
    expect(seen).toEqual({ policy: SURFACE_CSP, sandbox: "allow-same-origin", inPage: false, inner: SURFACE_CSP })
    expect(SURFACE_CSP).toBe("default-src 'none'; style-src 'unsafe-inline'")
    // Nothing is left behind in the page.
    expect(document.querySelector("iframe, [id^=idocs-diagram-]")).toBeNull()
  })

  it("reads only the SVG out of mermaid's sandbox frame", () => {
    expect(svgFromSandboxed(sandboxed(svgOf("<g><text>é ✓</text></g>")))).toBe(svgOf("<g><text>é ✓</text></g>"))
    expect(svgFromSandboxed(svgOf("<g/>"))).toBeNull()
    expect(svgFromSandboxed('<iframe src="data:text/html;charset=UTF-8;base64,@@@"></iframe>')).toBeNull()
  })

  it("leaves mermaid's settings as it found them, so chat's diagrams are not drawn in Docs' sandbox mode", async () => {
    const chat = { securityLevel: "strict", theme: "dark", htmlLabels: true, startOnLoad: false }
    mermaid.reset({ ...chat })
    mermaid.render.mockImplementation(drawing("<g/>"))
    expect((await renderDiagram("flowchart LR\n A-->B")).ok).toBe(true)
    expect(mermaid.mermaidAPI.getSiteConfig()).toMatchObject(chat)
    mermaid.render.mockRejectedValue(new Error("Parse error"))
    expect((await renderDiagram("flowchart LR\n A-->")).ok).toBe(false)
    expect(mermaid.mermaidAPI.getSiteConfig()).toMatchObject(chat)
    // Docs' settings were in force while it drew.
    expect(mermaid.initialize).toHaveBeenCalledWith(MERMAID_OPTIONS)
  })

  it("reports how long the render took", async () => {
    mermaid.render.mockImplementation(async (id: string, text: string, container: HTMLElement) => {
      await new Promise(done => setTimeout(done, 60))
      return drawing("<g/>")(id, text, container)
    })
    const result = await renderDiagram("flowchart LR\n A-->B")
    expect(result.ms).toBeGreaterThanOrEqual(50)
  })
})

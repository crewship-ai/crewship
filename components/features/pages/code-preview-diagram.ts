import type { MermaidConfig } from "mermaid"
import type { PreviewResult } from "./document-code-blocks"

/**
 * Diagram preview for Docs code blocks whose language is `mermaid`.
 *
 * Mermaid is pinned (package.json) and loaded only when a diagram is first
 * shown. It never draws in the page itself: it runs in its `sandbox` mode,
 * and the document it draws in sits inside a hidden frame whose own Content
 * Security Policy allows no requests at all (`default-src 'none'`, inline
 * styles only) and which runs no script of its own. Whatever a diagram's
 * styles or shapes name (images, icons, masks, fonts), the browser refuses to
 * fetch it. Sources that name images, icons, URLs or CSS escapes are also
 * refused before Mermaid runs, as a second layer.
 *
 * Mermaid runs with SVG text labels (no HTML labels) and directives inside a
 * diagram cannot change its settings. The SVG it returns is cleaned again: no
 * scripts, links, foreign (HTML) content, images, `use` references, event
 * handlers, or styles and attributes that could load a URL. It is shown in
 * its own shadow root so its style sheet can only style the diagram. A
 * diagram Mermaid cannot read shows the reason in that block, above its
 * source; the source is never changed.
 */

/**
 * Bounds that keep one diagram from freezing the tab, for every diagram type:
 * source length, connections (arrows and links; `edges`), statements,
 * subgraph count and nesting. They are checked before Mermaid runs.
 */
export const DIAGRAM_LIMITS = { chars: 5_000, edges: 150, subgraphs: 50, depth: 6, statements: 300 }

/**
 * A second layer behind the network-free render surface: sources that name an
 * image, an icon, a URL or use escapes are refused before Mermaid sees them.
 * Image and icon shapes (any key quoting), sequence `properties` (participant
 * icons), `url(` (also entity-encoded as `url#40;`), `image-set`,
 * `cross-fade`, `image()`, `element()`, `src()` and any backslash.
 */
const LOADING_SOURCE = /url\s*(?:\(|#)|\\|image-set|cross-fade|\b(?:image|element|src)\s*\(|\b(?:img|icon)['"]?\s*:|\bproperties\b/i

/** Connections in any diagram type: flowchart, state, class, ER and sequence arrows, and links. */
const CONNECTION = /<?[-=.~]{2,}[>x)o|*]*|<\|--|\|\|--|}o--|-[>x)]+/g

/** Why this source is refused before Mermaid runs, or null when it may be drawn. */
export function refuseDiagram(source: string): string | null {
  if (LOADING_SOURCE.test(source)) return "it refers to an image, icon, URL or escaped text, which previews never load"
  let depth = 0, deepest = 0, subgraphs = 0
  // Subgraphs can be opened and closed anywhere on a line (`subgraph a; subgraph b; … end; end`). The total
  // count is the hard bound; depth is approximate, since a label may also contain the word "end".
  for (const [token] of source.matchAll(/\b(?:subgraph|end)\b/g)) {
    if (token === "subgraph") { subgraphs++; deepest = Math.max(deepest, ++depth) } else depth = Math.max(0, depth - 1)
  }
  // State diagrams nest composite states with braces (other types use braces for shapes and relations).
  if (/^\s*stateDiagram/m.test(source)) {
    let braces = 0
    for (const char of source) { if (char === "{") deepest = Math.max(deepest, ++braces); else if (char === "}") braces = Math.max(0, braces - 1) }
  }
  if (deepest > DIAGRAM_LIMITS.depth) return `it is nested deeper than ${DIAGRAM_LIMITS.depth} levels`
  if (subgraphs > DIAGRAM_LIMITS.subgraphs) return `it has more than ${DIAGRAM_LIMITS.subgraphs} subgraphs`
  const connections = source.match(CONNECTION)?.length ?? 0
  if (connections > DIAGRAM_LIMITS.edges) return `it has more than ${DIAGRAM_LIMITS.edges} connections`
  const statements = source.split(/[\n;]/).filter(part => part.trim()).length
  if (statements > DIAGRAM_LIMITS.statements) return `it has more than ${DIAGRAM_LIMITS.statements} statements`
  return null
}

/** Settings a diagram's own `%%{init}%%` directive may not override. */
const SECURE_KEYS = ["secure", "securityLevel", "startOnLoad", "maxTextSize", "maxEdges", "suppressErrorRendering", "htmlLabels", "flowchart", "themeCSS", "themeVariables", "fontFamily", "altFontFamily", "dompurifyConfig", "theme", "darkMode", "look", "layout", "elk", "icons"]

export const MERMAID_OPTIONS = {
  startOnLoad: false,
  // Mermaid draws in a frame it creates inside the network-free surface below; it returns that frame as markup.
  securityLevel: "sandbox",
  htmlLabels: false,
  flowchart: { htmlLabels: false },
  maxTextSize: DIAGRAM_LIMITS.chars,
  maxEdges: DIAGRAM_LIMITS.edges,
  suppressErrorRendering: true,
  secure: SECURE_KEYS,
} satisfies MermaidConfig

/** Style text that could load or escape: url(...) to other than `#id`, imports, fonts, CSS escapes. */
const LOADS = /@import|@font-face|\\|expression\s*\(|image-set|url\s*\(\s*(?!['"]?#)/i
/** Style text that could reach outside the diagram's own box. */
const ESCAPES_BOX = /:host|::?slotted|::part/i
/** The only form of url(...) an attribute may hold: a reference to an element of this SVG. */
const LOCAL_URL = /^url\(#[\w-]+\)$/

/** True when an attribute value could load, run or escape anything: a backslash, javascript:/data:, or url(...) other than exactly `url(#id)`. */
function unsafeValue(value: string): boolean {
  const trimmed = value.trim()
  if (value.includes("\\") || /javascript:|data:/i.test(value)) return true
  return /url\s*\(/i.test(value) && !LOCAL_URL.test(trimmed)
}

/**
 * The SVG elements a diagram may keep (lower-case local names). Everything
 * else is removed with its content: scripts, `foreignObject`
 * (HTML labels), `image`, `use`, animation elements and `feImage`.
 * A link (`a`) becomes a plain group, so its label stays visible.
 */
const ALLOWED = new Set(["svg", "g", "path", "rect", "circle", "ellipse", "line", "polyline", "polygon", "text", "tspan", "title", "desc", "defs", "marker", "style", "lineargradient", "radialgradient", "stop", "clippath", "mask", "pattern", "filter",
  "feblend", "fecolormatrix", "fecomponenttransfer", "fecomposite", "feconvolvematrix", "fediffuselighting", "fedisplacementmap", "fedistantlight", "fedropshadow", "feflood", "fefunca", "fefuncb", "fefuncg", "fefuncr", "fegaussianblur", "femerge", "femergenode", "femorphology", "feoffset", "fepointlight", "fespecularlighting", "fespotlight", "fetile", "feturbulence"])
/** Elements replaced by a plain group instead of being removed. */
const UNWRAP = new Set(["a"])
const SVG_NS = "http://www.w3.org/2000/svg"
const FORBID_ATTR = ["src", "srcset", "action", "formaction", "xml:base", "attributename"]

function cleanAttributes(element: Element) {
  for (const { name, value } of [...element.attributes]) {
    const lower = name.toLowerCase()
    if (lower.startsWith("on") || lower.includes("href") || FORBID_ATTR.includes(lower) || unsafeValue(value) || (lower === "style" && (LOADS.test(value) || /position/i.test(value)))) element.removeAttribute(name)
  }
}

/** One root element parsed where nothing runs or loads: as XML first, as HTML in a detached document if that fails. */
function parseInert(markup: string): Element | null {
  // HTML serialization may hold void elements such as <br>; closed, they parse as XML too.
  for (const candidate of [markup, markup.replace(/<br\s*>/gi, "<br/>")]) {
    const xml = new DOMParser().parseFromString(candidate, "image/svg+xml")
    if (!xml.getElementsByTagName("parsererror").length && xml.documentElement) return xml.documentElement
  }
  const inert = document.implementation.createHTMLDocument("")
  inert.body.innerHTML = markup
  return inert.body.childElementCount === 1 ? inert.body.firstElementChild : null
}

/**
 * Mermaid's SVG with everything that could run or load removed, or null when
 * it is not one SVG element. The markup is parsed in an inert document
 * (XML, or a detached HTML document; nothing in it runs or loads), walked
 * against an element allowlist and cleaned of event handlers, link attributes and styles or values that
 * reference a URL, and only then adopted into the page.
 */
export function sanitizeDiagramSvg(svg: string): SVGSVGElement | null {
  if (!svg) return null
  const parsed = parseInert(svg)
  if (!parsed || parsed.namespaceURI !== SVG_NS || parsed.localName !== "svg") return null
  const clean = (element: Element) => {
    for (const child of [...element.children]) {
      const name = child.localName.toLowerCase()
      if (child.namespaceURI !== SVG_NS || !ALLOWED.has(name)) {
        if (UNWRAP.has(name) && child.namespaceURI === SVG_NS) {
          // Mermaid wraps a node with a link in `a` and positions the `a`: keep it as a plain group, without the link.
          const group = child.ownerDocument.createElementNS(SVG_NS, "g")
          for (const { name: attr, value } of [...child.attributes]) if (!attr.includes(":")) group.setAttribute(attr, value)
          group.append(...[...child.childNodes])
          child.replaceWith(group)
          clean(group); cleanAttributes(group)
          continue
        }
        child.remove(); continue
      }
      clean(child)
    }
    if (element.localName.toLowerCase() === "style") {
      // A style sheet holds text only, and only text that loads nothing.
      if (element.childElementCount || LOADS.test(element.textContent ?? "") || ESCAPES_BOX.test(element.textContent ?? "")) { element.remove(); return }
      // Nothing in a diagram may be positioned out of its box (an overlay over the page): drop such rules.
      element.textContent = (element.textContent ?? "").replace(/[^{}]*\{[^{}]*\bposition\s*:[^{}]*\}/gi, "")
    }
    cleanAttributes(element)
  }
  clean(parsed)
  return document.importNode(parsed, true) as SVGSVGElement
}

type Mermaid = typeof import("mermaid").default
let ready: Promise<Mermaid> | undefined
const load = () => ready ??= import("mermaid").then(module => module.default)

let sequence = 0
const nextId = () => `docs-diagram-${++sequence}-${Math.random().toString(36).slice(2, 8)}`

/** The policy of the document Mermaid draws in: no request of any kind, inline styles only, no script. */
export const SURFACE_CSP = "default-src 'none'; style-src 'unsafe-inline'"

class Unavailable extends Error {}

/**
 * A hidden frame whose document allows no requests (its own CSP) and runs no
 * script (sandboxed without `allow-scripts`). It shares this page's origin
 * only so this page can lay the diagram out inside it. Mermaid's own sandbox
 * frame is created inside it and inherits its policy; the policy is also set
 * on that frame's document before anything is drawn.
 */
async function openSurface() {
  const frame = document.createElement("iframe")
  frame.setAttribute("sandbox", "allow-same-origin")
  frame.setAttribute("aria-hidden", "true")
  frame.tabIndex = -1
  frame.style.cssText = "position:fixed;left:-20000px;top:0;width:1600px;height:1200px;border:0;visibility:hidden;pointer-events:none"
  frame.srcdoc = `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="${SURFACE_CSP}"></head><body></body></html>`
  const loaded = new Promise<void>((resolve, reject) => {
    frame.addEventListener("load", () => resolve(), { once: true })
    setTimeout(() => reject(new Unavailable()), 5_000)
  })
  document.body.append(frame)
  try { await loaded } catch (cause) { frame.remove(); throw cause }
  const doc = frame.contentDocument
  if (!doc?.body || !locked(doc)) { frame.remove(); throw new Unavailable() }
  const container = doc.createElement("div")
  // Mermaid appends its sandbox frame here and draws in that frame's document at once: lock that document
  // as it is attached, before anything is drawn in it.
  const append = container.appendChild.bind(container)
  container.appendChild = <T extends Node>(child: T): T => {
    const added = append(child)
    if ((child as unknown as Element).localName === "iframe") {
      const inner = (child as unknown as HTMLIFrameElement).contentDocument
      if (!inner) throw new Unavailable()
      lock(inner)
    }
    return added
  }
  doc.body.append(container)
  return { doc, container, close: () => frame.remove() }
}

const locked = (doc: Document) => [...doc.querySelectorAll('head > meta[http-equiv="Content-Security-Policy"]')].some(meta => meta.getAttribute("content") === SURFACE_CSP)

function lock(doc: Document) {
  if (locked(doc)) return
  const meta = doc.createElement("meta")
  meta.httpEquiv = "Content-Security-Policy"; meta.content = SURFACE_CSP
  ;(doc.head ?? doc.documentElement).prepend(meta)
}

/**
 * Mermaid finds its sandbox frame by id in the page's document. The frame
 * lives inside the surface instead, so the page gets a hidden stand-in with
 * that id that hands Mermaid the frame's (locked) document.
 */
function standIn(id: string, surface: Document) {
  const node = document.createElement("div")
  node.id = `i${id}`; node.hidden = true
  Object.defineProperty(node, "contentDocument", {
    get: () => {
      const doc = (surface.getElementById(`i${id}`) as HTMLIFrameElement | null)?.contentDocument ?? null
      if (doc) lock(doc)
      return doc
    },
  })
  document.body.append(node)
  return node
}

/** The SVG markup inside Mermaid's sandbox result (a frame with a base64 data: document), or null. */
export function svgFromSandboxed(markup: string): string | null {
  const match = /src="data:text\/html;charset=UTF-8;base64,([A-Za-z0-9+/=]+)"/.exec(markup)
  if (!match) return null
  let html: string
  try { html = new TextDecoder().decode(Uint8Array.from(atob(match[1]), char => char.charCodeAt(0))) } catch { return null }
  const body = /^<body[^>]*>([\s\S]*)<\/body>$/.exec(html.trim())
  return body ? body[1] : null
}

const PageImage = typeof window === "undefined" ? undefined as unknown as typeof Image : window.Image

// One diagram at a time: Mermaid's settings are global (shared with chat's renderer) and set for each render.
let queue: Promise<unknown> = Promise.resolve()

export function renderDiagram(source: string): Promise<PreviewResult> {
  const run = queue.then(() => draw(source), () => draw(source))
  queue = run
  return run
}

async function draw(source: string): Promise<PreviewResult> {
  if (!source.trim()) return { ok: false, error: "Nothing to show yet. Write a Mermaid diagram in Source." }
  if (source.length > DIAGRAM_LIMITS.chars) return { ok: false, error: `This diagram is longer than ${DIAGRAM_LIMITS.chars.toLocaleString("en-US")} characters, so only its source is shown.` }
  const refused = refuseDiagram(source)
  if (refused) return { ok: false, error: `Cannot show this diagram: ${refused}.` }
  const mermaid = await load().catch(() => null)
  if (!mermaid) return { ok: false, error: "The preview could not be loaded. The source is unchanged." }
  const started = performance.now()
  const ms = () => performance.now() - started
  let markup: string
  let surface: Awaited<ReturnType<typeof openSurface>> | undefined
  let stand: HTMLElement | undefined
  // Mermaid's settings are global and chat's renderer initializes them only once: put back what was there,
  // whatever happens, so chat never draws with Docs' sandbox settings.
  let previous: MermaidConfig | undefined
  try { previous = structuredClone(mermaid.mermaidAPI.getSiteConfig()) } catch { previous = undefined }
  try {
    surface = await openSurface()
    const id = nextId()
    stand = standIn(id, surface.doc)
    mermaid.initialize(MERMAID_OPTIONS)
    // Image shapes measure their picture with `new Image()`, which belongs to this page. While Mermaid draws,
    // such images are created in the locked surface instead, so the browser refuses to fetch them there too.
    const surfaceDoc = surface.doc
    window.Image = function Image() { return surfaceDoc.createElement("img") } as unknown as typeof window.Image
    try { markup = (await mermaid.render(id, source, surface.container)).svg } finally { window.Image = PageImage }
  } catch (cause) {
    if (cause instanceof Unavailable) return { ok: false, error: "Cannot show this diagram: this browser cannot draw it in isolation.", ms: ms() }
    const message = cause instanceof Error && cause.message ? cause.message.replace(/\s+/g, " ").trim() : "it could not be read"
    return { ok: false, error: `Cannot show this diagram: ${message.slice(0, 300)}`, ms: ms() }
  } finally {
    stand?.remove(); surface?.close()
    if (previous) mermaid.initialize(previous)
  }
  const inner = svgFromSandboxed(markup)
  const svg = inner === null ? null : sanitizeDiagramSvg(inner)
  if (!svg) return { ok: false, error: "Cannot show this diagram: it could not be drawn safely.", ms: ms() }
  svg.setAttribute("role", "img")
  if (!svg.getAttribute("aria-label")) svg.setAttribute("aria-label", "Diagram")
  svg.style.maxWidth = svg.style.maxWidth || "100%"
  const host = document.createElement("div")
  host.dataset.diagramPreview = ""
  const shadow = host.attachShadow({ mode: "open" })
  shadow.append(svg)
  return { ok: true, node: host, ms: ms() }
}

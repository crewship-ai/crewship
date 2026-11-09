import type { MermaidConfig } from "mermaid"
import type { PreviewResult } from "./document-code-blocks"

/**
 * Diagram preview for Docs code blocks whose language is `mermaid`.
 *
 * Mermaid is pinned (package.json) and loaded only when a diagram is first
 * shown. It runs with `securityLevel: "strict"` and SVG text labels (no HTML
 * labels), so click callbacks and links are not bound; directives inside a
 * diagram cannot change those settings or inject page CSS. Sources that
 * would make Mermaid load an image, icon or URL are refused before it runs,
 * because it draws into the live page before its output is cleaned. Its SVG is then
 * sanitized again: no scripts, links, foreign (HTML) content, images, `use`
 * references, event handlers, or styles and attributes that could load a
 * URL. The SVG is placed in its own shadow root so its style sheet can only
 * style the diagram. Nothing is fetched. A diagram Mermaid cannot read shows
 * the reason in that block, above its source; the source is never changed.
 */

/**
 * Bounds that keep one diagram from freezing the tab: source length, edges,
 * subgraph count and nesting. A render slower than `slowMs` is reported as
 * slow, and the block then waits for the reader before drawing again.
 */
export const DIAGRAM_LIMITS = { chars: 5_000, edges: 150, subgraphs: 50, depth: 6, slowMs: 500 }

/**
 * Mermaid draws into a temporary element in the live page before its output
 * can be cleaned, so a source that names an image, an icon, a URL or uses CSS
 * escapes is refused before Mermaid sees it: image and icon shapes, sequence
 * `properties` (participant icons), `url(` in styles and class definitions,
 * and any backslash.
 */
const LOADING_SOURCE = /url\s*\(|\\|\b(?:img|icon)\s*:|"(?:img|icon)"\s*:|\bproperties\b/i

/** Settings a diagram's own `%%{init}%%` directive may not override. */
const SECURE_KEYS = ["secure", "securityLevel", "startOnLoad", "maxTextSize", "maxEdges", "suppressErrorRendering", "htmlLabels", "flowchart", "themeCSS", "themeVariables", "fontFamily", "altFontFamily", "dompurifyConfig", "theme", "darkMode", "look", "layout", "elk", "icons"]

export const MERMAID_OPTIONS = {
  startOnLoad: false,
  securityLevel: "strict",
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

export async function renderDiagram(source: string): Promise<PreviewResult> {
  if (!source.trim()) return { ok: false, error: "Nothing to show yet. Write a Mermaid diagram in Source." }
  if (source.length > DIAGRAM_LIMITS.chars) return { ok: false, error: `This diagram is longer than ${DIAGRAM_LIMITS.chars.toLocaleString("en-US")} characters, so only its source is shown.` }
  if (LOADING_SOURCE.test(source)) return { ok: false, error: "Cannot show this diagram: it refers to an image, icon, URL or escaped text, which previews never load." }
  let depth = 0, deepest = 0, subgraphs = 0
  for (const line of source.split("\n")) {
    if (/^\s*subgraph\b/.test(line)) { subgraphs++; deepest = Math.max(deepest, ++depth) } else if (/^\s*end\s*$/.test(line)) depth = Math.max(0, depth - 1)
  }
  if (deepest > DIAGRAM_LIMITS.depth) return { ok: false, error: `Cannot show this diagram: subgraphs are nested deeper than ${DIAGRAM_LIMITS.depth} levels.` }
  if (subgraphs > DIAGRAM_LIMITS.subgraphs) return { ok: false, error: `Cannot show this diagram: it has more than ${DIAGRAM_LIMITS.subgraphs} subgraphs.` }
  let markup: string
  const started = performance.now()
  try {
    const mermaid = await load()
    // Mermaid's settings are global and shared with the chat renderer, so they are set again for every diagram.
    mermaid.initialize(MERMAID_OPTIONS)
    markup = (await mermaid.render(nextId(), source)).svg
  } catch (cause) {
    const message = cause instanceof Error && cause.message ? cause.message.replace(/\s+/g, " ").trim() : "it could not be read"
    return { ok: false, error: `Cannot show this diagram: ${message.slice(0, 300)}` }
  }
  const svg = sanitizeDiagramSvg(markup)
  if (!svg) return { ok: false, error: "Cannot show this diagram: it could not be drawn safely." }
  svg.setAttribute("role", "img")
  if (!svg.getAttribute("aria-label")) svg.setAttribute("aria-label", "Diagram")
  svg.style.maxWidth = svg.style.maxWidth || "100%"
  const host = document.createElement("div")
  host.dataset.diagramPreview = ""
  const shadow = host.attachShadow({ mode: "open" })
  shadow.append(svg)
  return { ok: true, node: host, slow: performance.now() - started > DIAGRAM_LIMITS.slowMs }
}

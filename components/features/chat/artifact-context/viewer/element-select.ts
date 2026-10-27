import type { ArtifactFile, ArtifactManifest } from "../projects/types"

/**
 * The preview runs the artifact's own HTML in an opaque-origin sandbox
 * (`allow-scripts` only). The parent cannot reach into it, so selection,
 * screen links and highlighting travel as postMessage. Messages go out with
 * targetOrigin "*" because an opaque origin has no name; the receiver checks
 * `event.source` instead.
 */
export const SELECT_MESSAGE = "crewship-artifact-select"
export const NAVIGATE_MESSAGE = "crewship-artifact-navigate"
export const HIGHLIGHT_MESSAGE = "crewship-artifact-highlight"

/**
 * Longest anchor quote the frame sends. The quote is never truncated (the
 * server compares it with the element's full text), so a larger element is
 * shown as not commentable instead; a smaller part inside it is.
 */
export const ANCHOR_QUOTE_MAX = 5000

// Go's unicode.IsSpace, which strings.Fields splits on. JS `\s` differs: it
// includes U+FEFF and lacks U+0085.
const SPACE = "[\\t\\n\\v\\f\\r \\u0085\\u00a0\\u1680\\u2000-\\u200a\\u2028\\u2029\\u202f\\u205f\\u3000]+"
const SPACES = new RegExp(SPACE, "g")
const EDGES = new RegExp(`^${SPACE}|${SPACE}$`, "g")

/** strings.Join(strings.Fields(text), " "), as the server normalizes an anchor's text. */
export function normalizeQuote(text: string): string {
  return text.replace(SPACES, " ").replace(EDGES, "")
}

/**
 * The server's anchor text, as source for the frame: every descendant text
 * node in document order, skipping <script> and <style> subtrees, joined with
 * nothing (no space between adjacent tags), then normalized. Not textContent,
 * which includes script and style text. Exported so tests run the same code.
 */
export const ANCHOR_QUOTE_SOURCE = `function quoteOf(root){
var parts=[],sp=${JSON.stringify(SPACE)};
(function walk(n){for(var c=n.firstChild;c;c=c.nextSibling){
  if(c.nodeType===3)parts.push(c.nodeValue);
  else if(c.nodeType===1&&c.localName!=="script"&&c.localName!=="style")walk(c)}})(root);
return parts.join("").replace(new RegExp(sp,"g")," ").replace(new RegExp("^"+sp+"|"+sp+"$","g"),"")}`

/** `CSS.escape` per CSSOM, inlined: jsdom and older engines lack it, and the selector must match what the frame posts. */
function cssEscape(value: string): string {
  let out = ""
  for (let i = 0; i < value.length; i++) {
    const ch = value.charAt(i)
    const code = value.charCodeAt(i)
    if (code === 0) out += "\uFFFD"
    else if ((code >= 0x1 && code <= 0x1f) || code === 0x7f || (i === 0 && code >= 0x30 && code <= 0x39) || (i === 1 && code >= 0x30 && code <= 0x39 && value.charCodeAt(0) === 0x2d)) out += `\\${code.toString(16)} `
    else if (i === 0 && value.length === 1 && code === 0x2d) out += `\\${ch}`
    else if (code >= 0x80 || code === 0x2d || code === 0x5f || /[0-9A-Za-z]/.test(ch)) out += ch
    else out += `\\${ch}`
  }
  return out
}

export function anchorSelectorForId(id: string): string { return `#${cssEscape(id)}` }

// One id selector: `#` and an escaped identifier, no combinators, classes or pseudos.
const ANCHOR = /^#(?:[A-Za-z0-9_\u0080-\uFFFF-]|\\[0-9a-fA-F]{1,6} ?|\\[^0-9a-fA-F\n\r\f])+$/

/** The server accepts only a single `#id` anchor. */
export function isValidAnchorSelector(selector: string): boolean { return ANCHOR.test(selector) }

/** HTML files of a manifest, entrypoint first, the rest in manifest order. */
export function screensOf(manifest: Pick<ArtifactManifest, "entrypoint" | "files">): string[] {
  const html = manifest.files.map(file => file.path).filter(path => /\.html?$/i.test(path))
  return html.includes(manifest.entrypoint) ? [manifest.entrypoint, ...html.filter(path => path !== manifest.entrypoint)] : html
}

const TITLE_MAX = 24
const ENTITIES: Record<string, string> = { amp: "&", lt: "<", gt: ">", quot: "\"", apos: "'", nbsp: "\u00a0" }
const decode = (text: string) => text.replace(/&(#x[0-9a-f]+|#\d+|[a-z]+);/gi, (whole, name: string) => {
  if (name[0] !== "#") return ENTITIES[name.toLowerCase()] ?? whole
  const code = name[1] === "x" || name[1] === "X" ? parseInt(name.slice(2), 16) : parseInt(name.slice(1), 10)
  return code > 0 && code <= 0x10ffff ? String.fromCodePoint(code) : whole
})

/** The document's <title>, when it is short enough for a tab. */
function titleOf(html: string): string | null {
  const raw = /<title\b[^>]*>([\s\S]*?)<\/title\s*>/i.exec(html)?.[1]
  const title = raw === undefined ? "" : normalizeQuote(decode(raw))
  return title && title.length <= TITLE_MAX ? title : null
}

/** "pages/pricing.html" → "Pricing", "faq.html" → "FAQ". */
function fileLabel(path: string): string {
  const name = (path.split("/").pop() ?? path).replace(/\.[^.]+$/, "")
  if (!name) return path
  return name.length <= 3 && /^[a-z]+$/i.test(name) ? name.toUpperCase() : name.charAt(0).toUpperCase() + name.slice(1)
}

/** A screen's name: its short <title> when the HTML is given and has one, otherwise the file name. */
export function screenLabel(path: string, html?: string): string {
  return (html !== undefined ? titleOf(html) : null) ?? fileLabel(path)
}

/** Labels for every screen. Titles count only when they tell the screens apart; one shared site title would not. */
export function screenLabels(manifest: Pick<ArtifactManifest, "entrypoint" | "files">): Map<string, string> {
  const paths = screensOf(manifest)
  const titles = paths.map(path => titleOf(manifest.files.find(file => file.path === path)?.content ?? ""))
  const distinct = titles.every(Boolean) && new Set(titles).size === titles.length
  return new Map(paths.map((path, i) => [path, distinct ? titles[i]! : fileLabel(path)]))
}

/** JSON safe inside a <script>: no "</script", no HTML comment openers, no line separators. */
function scriptJson(value: unknown): string {
  return JSON.stringify(value).replace(/</g, "\\u003c").replace(/>/g, "\\u003e").replace(/\u2028/g, "\\u2028").replace(/\u2029/g, "\\u2029")
}

// Runs inside the frame in both modes: screen links become navigate messages
// (a sandboxed frame must never leave for another URL), and the parent can
// ask for an element to be outlined.
const BASE_SCRIPT = `(function(){
var screens=__SCREENS__,current=__PATH__;
function post(m){parent.postMessage(m,"*")}
document.addEventListener("click",function(e){
  var a=e.target&&e.target.closest?e.target.closest("a[href]"):null;if(!a)return;
  var href=a.getAttribute("href")||"";if(href.charAt(0)==="#")return;
  e.preventDefault();
  try{var u=new URL(href,"https://artifact.invalid/"+current);
    if(u.origin==="https://artifact.invalid"){var p=decodeURIComponent(u.pathname.slice(1));if(screens.indexOf(p)>=0)post({type:"${NAVIGATE_MESSAGE}",path:p})}}catch(_){}
},true);
var st=document.createElement("style");
st.textContent="[data-crewship-highlight]{outline:2px solid #f59e0b!important;outline-offset:2px;transition:outline-color .3s}";
(document.head||document.documentElement).appendChild(st);
window.addEventListener("message",function(e){
  if(e.source!==parent||!e.data||e.data.type!=="${HIGHLIGHT_MESSAGE}")return;
  var prev=document.querySelectorAll("[data-crewship-highlight]");for(var i=0;i<prev.length;i++)prev[i].removeAttribute("data-crewship-highlight");
  var el=null;try{el=document.querySelector(e.data.selector)}catch(_){}
  if(!el)return;el.setAttribute("data-crewship-highlight","");el.scrollIntoView({block:"center"});
  setTimeout(function(){el.removeAttribute("data-crewship-highlight")},2400);
});
})();`

// Only in select mode: elements with an id (or inside one) are pickable and
// dashed; the rest show not-allowed, because the server anchors by #id only.
// An element whose text exceeds the quote limit is not pickable either.
// The picked element stays solid while the pin card is open.
const SELECT_SCRIPT = `(function(){
${ANCHOR_QUOTE_SOURCE}
var quotes=new WeakMap();
function quote(el){if(!quotes.has(el))quotes.set(el,quoteOf(el));return quotes.get(el)}
function pickable(el){return quote(el).length<=${ANCHOR_QUOTE_MAX}}
var st=document.createElement("style");
st.textContent="*{cursor:not-allowed!important}[id],[id] *{cursor:crosshair!important}body [id]{outline:1px dashed rgba(59,130,246,.55);outline-offset:2px}[data-crewship-hover]{outline:2px solid #3b82f6!important;background-color:rgba(59,130,246,.06)}[data-crewship-picked]{outline:2px solid #3b82f6!important}";
(document.head||document.documentElement).appendChild(st);
var hovered=null;
function target(n){return n&&n.closest?n.closest("[id]"):null}
document.addEventListener("mouseover",function(e){
  var el=target(e.target);if(el&&!pickable(el)){if(!el.hasAttribute("title"))el.setAttribute("title","Too much text to comment on; select a smaller part");el.style.setProperty("cursor","not-allowed","important");el=null}
  if(hovered&&hovered!==el)hovered.removeAttribute("data-crewship-hover");
  hovered=el;if(el)el.setAttribute("data-crewship-hover","");
  else if(!target(e.target)&&e.target&&e.target.setAttribute&&!e.target.hasAttribute("title"))e.target.setAttribute("title","This element cannot be commented on");
},true);
document.addEventListener("click",function(e){
  e.preventDefault();e.stopPropagation();
  var el=target(e.target);if(!el||!pickable(el))return;
  var was=document.querySelectorAll("[data-crewship-picked]");for(var i=0;i<was.length;i++)was[i].removeAttribute("data-crewship-picked");
  el.setAttribute("data-crewship-picked","");
  parent.postMessage({type:"${SELECT_MESSAGE}",selector:"#"+CSS.escape(el.id),quote:quote(el)},"*");
},true);
})();`

/** A relative reference from the screen at `from`, as a manifest path; null when it leaves the project. */
function projectPath(from: string, ref: string): string | null {
  if (/^[a-z][a-z0-9+.-]*:|^\/\//i.test(ref) || ref.startsWith("/")) return null
  const parts = from.split("/").slice(0, -1)
  for (const segment of ref.split(/[?#]/)[0].split("/")) {
    if (segment === "..") { if (!parts.length) return null; parts.pop() }
    else if (segment && segment !== ".") parts.push(segment)
  }
  return parts.join("/")
}

/**
 * The screen with the project's own stylesheets and scripts written into it.
 * The frame has an opaque origin and no URL of its own, so a relative
 * `styles.css` or `script.js` could never load. References outside the
 * project stay as they are.
 */
export function inlineProjectAssets(html: string, path: string, files: Pick<ArtifactFile, "path" | "content" | "media_type">[]): string {
  if (typeof DOMParser === "undefined") return html
  const byPath = new Map(files.map(file => [file.path, file]))
  const fileFor = (ref: string | null, kind: RegExp) => {
    const target = ref == null ? null : projectPath(path, ref)
    const file = target == null ? undefined : byPath.get(target)
    return file && (kind.test(file.media_type) || kind.test(file.path)) ? file : undefined
  }
  const doc = new DOMParser().parseFromString(html, "text/html")
  let changed = false
  for (const link of doc.querySelectorAll("link[href]")) {
    if (!/(^|\s)stylesheet(\s|$)/i.test(link.getAttribute("rel") ?? "")) continue
    const file = fileFor(link.getAttribute("href"), /css/i)
    if (!file) continue
    const style = doc.createElement("style")
    style.textContent = file.content
    link.replaceWith(style)
    changed = true
  }
  for (const script of doc.querySelectorAll("script[src]")) {
    const file = fileFor(script.getAttribute("src"), /javascript|ecmascript|\.m?js$/i)
    if (!file) continue
    const inline = doc.createElement("script")
    for (const attr of script.attributes) if (attr.name !== "src" && attr.name !== "defer" && attr.name !== "async") inline.setAttribute(attr.name, attr.value)
    inline.textContent = file.content
    // An inline script ignores defer; running it after the parsed body keeps the order it expects.
    if (script.hasAttribute("defer") && !script.hasAttribute("async")) { script.remove(); doc.body.append(inline) }
    else script.replaceWith(inline)
    changed = true
  }
  if (!changed) return html
  return (/^\s*<!doctype/i.test(html) ? "<!DOCTYPE html>\n" : "") + doc.documentElement.outerHTML
}

export interface PreviewDocumentOptions {
  selectMode: boolean
  /** Screen paths a link may switch to. */
  screens: string[]
  /** Path of the screen being shown, so relative links resolve as on the server. Defaults to the first screen. */
  path?: string
}

/** The artifact HTML with the frame bridge appended before </body> (or at the end). */
export function buildPreviewDocument(html: string, opts: PreviewDocumentOptions): string {
  const base = BASE_SCRIPT.replace("__SCREENS__", () => scriptJson(opts.screens)).replace("__PATH__", () => scriptJson(opts.path ?? opts.screens[0] ?? ""))
  const injected = `<script>${base}</script>${opts.selectMode ? `<script>${SELECT_SCRIPT}</script>` : ""}`
  const close = html.search(/<\/body\s*>(?![\s\S]*<\/body\s*>)/i)
  return close >= 0 ? html.slice(0, close) + injected + html.slice(close) : html + injected
}

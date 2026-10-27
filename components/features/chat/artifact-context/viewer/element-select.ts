import type { ArtifactManifest } from "../projects/types"

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

const QUOTE_LIMIT = 500

/** Whitespace collapsed and trimmed, as the server normalizes an anchor's quote. */
export function normalizeQuote(text: string): string {
  const collapsed = text.replace(/\s+/g, " ").trim()
  // Code points, not UTF-16 units, so the cap never splits a surrogate pair.
  const chars = Array.from(collapsed)
  return chars.length > QUOTE_LIMIT ? chars.slice(0, QUOTE_LIMIT).join("") : collapsed
}

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

/** "pages/pricing.html" → "Pricing". */
export function screenLabel(path: string): string {
  const name = (path.split("/").pop() ?? path).replace(/\.[^.]+$/, "")
  return name ? name.charAt(0).toUpperCase() + name.slice(1) : path
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
// The picked element stays solid while the pin card is open.
const SELECT_SCRIPT = `(function(){
var st=document.createElement("style");
st.textContent="*{cursor:not-allowed!important}[id],[id] *{cursor:crosshair!important}body [id]{outline:1px dashed rgba(59,130,246,.55);outline-offset:2px}[data-crewship-hover]{outline:2px solid #3b82f6!important;background-color:rgba(59,130,246,.06)}[data-crewship-picked]{outline:2px solid #3b82f6!important}";
(document.head||document.documentElement).appendChild(st);
var hovered=null;
function target(n){return n&&n.closest?n.closest("[id]"):null}
document.addEventListener("mouseover",function(e){
  var el=target(e.target);if(hovered&&hovered!==el)hovered.removeAttribute("data-crewship-hover");
  hovered=el;if(el)el.setAttribute("data-crewship-hover","");
  else if(e.target&&e.target.setAttribute&&!e.target.hasAttribute("title"))e.target.setAttribute("title","This element cannot be commented on");
},true);
document.addEventListener("click",function(e){
  e.preventDefault();e.stopPropagation();
  var el=target(e.target);if(!el)return;
  var was=document.querySelectorAll("[data-crewship-picked]");for(var i=0;i<was.length;i++)was[i].removeAttribute("data-crewship-picked");
  el.setAttribute("data-crewship-picked","");
  var q=(el.textContent||"").replace(/\\s+/g," ").trim();if(q.length>${QUOTE_LIMIT})q=Array.from(q).slice(0,${QUOTE_LIMIT}).join("");
  parent.postMessage({type:"${SELECT_MESSAGE}",selector:"#"+CSS.escape(el.id),quote:q},"*");
},true);
})();`

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

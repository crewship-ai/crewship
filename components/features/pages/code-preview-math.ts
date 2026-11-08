import katex from "katex"
import DOMPurify from "dompurify"
import "katex/dist/katex.min.css"
import type { PreviewResult } from "./document-code-blocks"

/**
 * Block math for Docs code blocks whose language is `latex` or `math`.
 *
 * KaTeX is pinned (package.json) and bundled with its fonts, so rendering
 * fetches nothing. `trust: false` refuses every command that could link,
 * load or embed (\href, \url, \includegraphics, \htmlClass, \htmlData …);
 * the output is sanitized again (HTML, SVG paths and MathML; no links,
 * images or style sheets) before it reaches the page. A syntax error is reported for this block only; the
 * source is never changed.
 */

/** Longest source rendered; longer blocks stay source-only. */
export const MAX_MATH_CHARS = 10_000


const sanitize = (html: string) => DOMPurify.sanitize(html, {
  // KaTeX draws roots and stretchy arrows as inline SVG paths.
  USE_PROFILES: { html: true, svg: true, mathMl: true },
  FORBID_TAGS: ["a", "img", "image", "iframe", "object", "embed", "form", "input", "style", "link", "use", "foreignObject", "script"],
  FORBID_ATTR: ["href", "xlink:href", "src", "srcset", "action", "formaction"],
  // The TeX source travels with the MathML (copy and screen readers). Plain `annotation` holds text only;
  // `annotation-xml`, which can carry markup, stays forbidden.
  ADD_TAGS: ["semantics", "annotation"],
  ADD_ATTR: ["encoding"],
})

export function renderMath(source: string): PreviewResult {
  if (!source.trim()) return { ok: false, error: "Nothing to show yet. Write a formula in Source." }
  if (source.length > MAX_MATH_CHARS) return { ok: false, error: `This formula is longer than ${MAX_MATH_CHARS.toLocaleString("en-US")} characters, so only its source is shown.` }
  let html: string
  try {
    html = katex.renderToString(source, { displayMode: true, throwOnError: true, trust: false, strict: "ignore", output: "htmlAndMathml", maxSize: 50, maxExpand: 500, globalGroup: false })
  } catch (cause) {
    const message = cause instanceof Error ? cause.message.replace(/^KaTeX parse error:\s*/, "") : "the formula could not be read"
    return { ok: false, error: `Cannot show this formula: ${message.slice(0, 300)}` }
  }
  const node = document.createElement("div")
  node.dataset.mathPreview = ""
  node.innerHTML = sanitize(html)
  // The sanitizer may unwrap KaTeX's outer display span; the wrapper carries its centring instead.
  if (!node.querySelector(":scope > .katex-display")) node.classList.add("katex-display")
  return { ok: true, node }
}

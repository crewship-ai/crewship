import { Lexer, type Token, type Tokens } from "marked"
import type { PreviewResult } from "./document-code-blocks"

/**
 * Markdown preview for Docs code blocks whose language is `markdown` or `md`.
 *
 * The source is tokenized (GitHub-flavoured Markdown) and the page is built
 * from the tokens with DOM calls and text nodes: no HTML string from the
 * source is ever parsed. Raw HTML in the source shows as literal text,
 * images show as their description and are never fetched, and links keep
 * only HTTP, HTTPS and mailto addresses (opened in a new tab without a
 * referrer). Size, nesting and token count are limited.
 */

export const MARKDOWN_LIMITS = { chars: 20_000, depth: 12, tokens: 5_000 }

const SAFE_LINK = /^(https?:|mailto:)/i

class Limit extends Error {}

export function safeLinkHref(href: string | undefined): string | null {
  if (!href) return null
  try {
    const url = new URL(href)
    if (!SAFE_LINK.test(url.protocol) || url.username || url.password) return null
    return url.href
  } catch { return null }
}

export function renderMarkdown(source: string): PreviewResult {
  if (source.length > MARKDOWN_LIMITS.chars) return { ok: false, error: `This Markdown is longer than ${MARKDOWN_LIMITS.chars.toLocaleString("en-US")} characters, so only its source is shown.` }
  if (!source.trim()) return { ok: false, error: "Nothing to show yet. Write Markdown in Source." }
  let tokens: Token[]
  try { tokens = new Lexer({ gfm: true, breaks: false }).lex(source) } catch { return { ok: false, error: "Cannot show this Markdown: it could not be read." } }
  const root = document.createElement("div")
  root.dataset.markdownPreview = ""
  let count = 0
  const el = <K extends keyof HTMLElementTagNameMap>(tag: K, depth: number) => {
    if (++count > MARKDOWN_LIMITS.tokens) throw new Limit(`more than ${MARKDOWN_LIMITS.tokens.toLocaleString("en-US")} elements`)
    if (depth > MARKDOWN_LIMITS.depth) throw new Limit(`nested deeper than ${MARKDOWN_LIMITS.depth} levels`)
    return document.createElement(tag)
  }
  const text = (value: string) => document.createTextNode(value)

  const inline = (parent: HTMLElement, list: Token[] | undefined, depth: number) => {
    for (const token of list ?? []) parent.append(inlineToken(token, depth))
  }
  const inlineToken = (token: Token, depth: number): Node => {
    switch (token.type) {
      case "strong": case "em": case "del": {
        const node = el(token.type === "strong" ? "strong" : token.type === "em" ? "em" : "del", depth)
        inline(node, (token as Tokens.Strong).tokens, depth + 1); return node
      }
      case "codespan": { const node = el("code", depth); node.textContent = (token as Tokens.Codespan).text; return node }
      case "br": return el("br", depth)
      case "link": {
        const link = token as Tokens.Link, href = safeLinkHref(link.href)
        if (!href) { const span = el("span", depth); inline(span, link.tokens, depth + 1); return span }
        const node = el("a", depth)
        node.href = href; node.target = "_blank"; node.rel = "noopener noreferrer nofollow"
        inline(node, link.tokens, depth + 1); return node
      }
      // Never fetched: an image shows only what it describes.
      case "image": { const span = el("span", depth); span.dataset.markdownImage = ""; span.textContent = `[Image: ${(token as Tokens.Image).text || "no description"}]`; return span }
      case "text": {
        const t = token as Tokens.Text
        if (t.tokens?.length) { const span = document.createDocumentFragment(); for (const child of t.tokens) span.append(inlineToken(child, depth)); return span }
        return text(t.text)
      }
      case "escape": return text((token as Tokens.Escape).text)
      // Raw HTML stays visible as the characters that were typed.
      case "html": return text(token.raw)
      case "checkbox": return text("")
      default: return text(token.raw ?? "")
    }
  }

  const block = (parent: HTMLElement, list: Token[] | undefined, depth: number) => {
    for (const token of list ?? []) {
      switch (token.type) {
        case "space": break
        case "heading": { const t = token as Tokens.Heading; const node = el(`h${Math.min(6, Math.max(1, t.depth))}` as "h1", depth); inline(node, t.tokens, depth + 1); parent.append(node); break }
        case "paragraph": { const node = el("p", depth); inline(node, (token as Tokens.Paragraph).tokens, depth + 1); parent.append(node); break }
        case "text": { const t = token as Tokens.Text; const node = el("p", depth); if (t.tokens?.length) inline(node, t.tokens, depth + 1); else node.textContent = t.text; parent.append(node); break }
        case "blockquote": { const node = el("blockquote", depth); block(node, (token as Tokens.Blockquote).tokens, depth + 1); parent.append(node); break }
        case "code": { const pre = el("pre", depth), code = el("code", depth + 1); code.textContent = (token as Tokens.Code).text; pre.append(code); parent.append(pre); break }
        case "hr": parent.append(el("hr", depth)); break
        case "list": {
          const t = token as Tokens.List
          const node = el(t.ordered ? "ol" : "ul", depth)
          if (t.ordered && typeof t.start === "number" && t.start !== 1) (node as HTMLOListElement).start = t.start
          for (const item of t.items) {
            const li = el("li", depth + 1)
            if (item.task) {
              const box = el("input", depth + 2); box.type = "checkbox"; box.checked = item.checked === true; box.disabled = true
              box.setAttribute("aria-label", item.checked ? "Done" : "Not done"); li.append(box, text(" "))
              li.dataset.task = ""
            }
            // Tight list items hold inline text; loose ones hold paragraphs.
            const children = item.tokens.filter(child => child.type !== "checkbox")
            if (children.every(child => child.type === "text")) for (const child of children) li.append(inlineToken(child, depth + 2))
            else block(li, children, depth + 2)
            node.append(li)
          }
          parent.append(node); break
        }
        case "table": {
          const t = token as Tokens.Table
          const table = el("table", depth), head = el("thead", depth + 1), headRow = el("tr", depth + 2), body = el("tbody", depth + 1)
          const align = (cell: HTMLTableCellElement, value: Tokens.TableCell["align"]) => { if (value) cell.style.textAlign = value }
          for (const cell of t.header) { const th = el("th", depth + 3); th.scope = "col"; align(th, cell.align); inline(th, cell.tokens, depth + 4); headRow.append(th) }
          head.append(headRow)
          for (const row of t.rows) {
            const tr = el("tr", depth + 2)
            for (const cell of row) { const td = el("td", depth + 3); align(td, cell.align); inline(td, cell.tokens, depth + 4); tr.append(td) }
            body.append(tr)
          }
          table.append(head, body); parent.append(table); break
        }
        // Raw HTML blocks stay visible as text.
        case "html": { const node = el("pre", depth); node.dataset.markdownHtml = ""; node.textContent = token.raw.replace(/\n+$/, ""); parent.append(node); break }
        case "def": break
        default: { const node = el("p", depth); node.textContent = token.raw ?? ""; parent.append(node) }
      }
    }
  }

  try { block(root, tokens, 0) } catch (cause) {
    if (cause instanceof Limit) return { ok: false, error: `Cannot show this Markdown: ${cause.message}.` }
    return { ok: false, error: "Cannot show this Markdown: it could not be read." }
  }
  return { ok: true, node: root }
}

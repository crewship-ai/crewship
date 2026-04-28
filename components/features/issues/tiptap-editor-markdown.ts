import { marked } from "marked"
import DOMPurify from "dompurify"
import TurndownService from "turndown"
import { all, createLowlight } from "lowlight"
import "highlight.js/styles/github-dark.css"

// Lowlight + turndown are package-singletons — instantiated once per
// process so every editor instance shares the same configured pipeline.
// Importing this module is what guarantees the syntax CSS is bundled.

const lowlight = createLowlight(all)

// ---------------------------------------------------------------------------
// Turndown setup (HTML -> Markdown)
// ---------------------------------------------------------------------------
const turndown = new TurndownService({
  headingStyle: "atx",
  codeBlockStyle: "fenced",
  bulletListMarker: "-",
  emDelimiter: "*",
  strongDelimiter: "**",
})

// Task list rules
turndown.addRule("taskListChecked", {
  filter: (node) =>
    node.nodeName === "LI" &&
    node.getAttribute("data-checked") === "true",
  replacement: (content) => `- [x] ${content.trim()}\n`,
})

turndown.addRule("taskListUnchecked", {
  filter: (node) =>
    node.nodeName === "LI" &&
    node.getAttribute("data-checked") === "false",
  replacement: (content) => `- [ ] ${content.trim()}\n`,
})

// Table rule
turndown.addRule("table", {
  filter: "table",
  replacement: (_content, node) => {
    const el = node as HTMLTableElement
    const rows = Array.from(el.querySelectorAll("tr"))
    if (rows.length === 0) return ""

    const headerRow = rows[0]
    const headerCells = Array.from(
      headerRow.querySelectorAll("th, td"),
    ).map((c) => c.textContent?.trim() || "")

    let result = "| " + headerCells.join(" | ") + " |\n"
    result += "| " + headerCells.map(() => "---").join(" | ") + " |\n"

    for (const row of rows.slice(1)) {
      const cells = Array.from(row.querySelectorAll("td, th")).map(
        (c) => c.textContent?.trim() || "",
      )
      result += "| " + cells.join(" | ") + " |\n"
    }

    return "\n" + result + "\n"
  },
})

// Table sub-elements (prevent default processing)
turndown.addRule("tableCell", {
  filter: ["th", "td"],
  replacement: (content) => content.trim(),
})

turndown.addRule("tableRow", {
  filter: "tr",
  replacement: () => "",
})

turndown.addRule("tableSection", {
  filter: ["thead", "tbody", "tfoot"],
  replacement: () => "",
})

// Strikethrough
turndown.addRule("strikethrough", {
  filter: ["s", "del"],
  replacement: (content) => `~~${content}~~`,
})

// Underline (preserve as HTML in markdown)
turndown.addRule("underline", {
  filter: "u",
  replacement: (content) => `<u>${content}</u>`,
})

// Highlight / mark
turndown.addRule("highlight", {
  filter: "mark",
  replacement: (content) => `==${content}==`,
})

function htmlToMarkdown(html: string): string {
  if (!html) return ""
  return turndown.turndown(html).trim()
}

// ---------------------------------------------------------------------------
// Markdown -> HTML
// ---------------------------------------------------------------------------
function markdownToHtml(md: string): string {
  if (!md) return ""
  return DOMPurify.sanitize(
    marked.parse(md, { async: false, breaks: true, gfm: true }) as string,
  )
}

export { lowlight, htmlToMarkdown, markdownToHtml }

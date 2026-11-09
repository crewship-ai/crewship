import type { PreviewResult } from "./document-code-blocks"

/**
 * CSV preview for Docs code blocks whose language is `csv`: a strict
 * RFC 4180 reading (comma separator, double-quoted fields with "" escapes,
 * LF or CRLF rows) shown as a table whose first row is the header. Anything
 * the format does not allow is an error for this block, not a guess: a
 * stray quote, an unterminated quoted field, or rows of different lengths.
 * Cells are text nodes only; nothing in a cell is interpreted as markup.
 */

export const CSV_LIMITS = { chars: 100_000, rows: 500, columns: 50, cell: 2_000 }

export type CsvResult = { ok: true; rows: string[][] } | { ok: false; error: string }

export function parseCsv(source: string): CsvResult {
  if (source.length > CSV_LIMITS.chars) return { ok: false, error: `This CSV is longer than ${CSV_LIMITS.chars.toLocaleString("en-US")} characters, so only its source is shown.` }
  // One final line break ends the last row; it does not start an empty one.
  const text = source.endsWith("\r\n") ? source.slice(0, -2) : source.endsWith("\n") ? source.slice(0, -1) : source
  if (!text) return { ok: false, error: "Nothing to show yet. Write comma-separated rows in Source." }
  const rows: string[][] = []
  let row: string[] = [], field = "", quoted = false, afterQuote = false, line = 1
  const fail = (why: string): CsvResult => ({ ok: false, error: `Cannot show this CSV: ${why} (line ${line}).` })
  const endField = (): CsvResult | null => {
    if (field.length > CSV_LIMITS.cell) return fail(`a cell is longer than ${CSV_LIMITS.cell.toLocaleString("en-US")} characters`)
    row.push(field); field = ""; afterQuote = false
    return row.length > CSV_LIMITS.columns ? fail(`more than ${CSV_LIMITS.columns} columns`) : null
  }
  const endRow = (): CsvResult | null => {
    const failed = endField(); if (failed) return failed
    if (rows.length && row.length !== rows[0].length) return fail(`this row has ${row.length} ${row.length === 1 ? "cell" : "cells"}, the header has ${rows[0].length}`)
    rows.push(row); row = []
    return rows.length > CSV_LIMITS.rows ? fail(`more than ${CSV_LIMITS.rows} rows`) : null
  }
  for (let i = 0; i < text.length; i++) {
    const char = text[i]
    if (quoted) {
      if (char === "\"") {
        if (text[i + 1] === "\"") { field += "\""; i++ } else { quoted = false; afterQuote = true }
      } else {
        if (char === "\n") line++
        field += char
      }
      continue
    }
    if (char === ",") { const failed = endField(); if (failed) return failed; continue }
    if (char === "\r" && text[i + 1] === "\n") continue
    if (char === "\n") { const failed = endRow(); if (failed) return failed; line++; continue }
    if (afterQuote) return fail("text after a closing quote")
    if (char === "\"") {
      if (field.length) return fail("a quote inside an unquoted cell")
      quoted = true; continue
    }
    if (char === "\r") return fail("a carriage return outside a quoted cell")
    field += char
  }
  if (quoted) return fail("a quoted cell is not closed")
  const failed = endRow(); if (failed) return failed
  return { ok: true, rows }
}

export function renderCsv(source: string): PreviewResult {
  const parsed = parseCsv(source)
  if (!parsed.ok) return parsed
  const [header, ...body] = parsed.rows
  const wrap = document.createElement("div"), table = document.createElement("table"), caption = document.createElement("caption")
  wrap.dataset.csvPreview = ""
  caption.className = "sr-only"
  caption.textContent = `CSV table, ${body.length} ${body.length === 1 ? "row" : "rows"} and ${header.length} ${header.length === 1 ? "column" : "columns"}`
  const head = document.createElement("thead"), headRow = document.createElement("tr")
  for (const name of header) { const th = document.createElement("th"); th.scope = "col"; th.textContent = name; headRow.append(th) }
  head.append(headRow)
  const tbody = document.createElement("tbody")
  for (const cells of body) {
    const tr = document.createElement("tr")
    for (const value of cells) { const td = document.createElement("td"); td.textContent = value; tr.append(td) }
    tbody.append(tr)
  }
  table.append(caption, head, tbody); wrap.append(table)
  return { ok: true, node: wrap }
}

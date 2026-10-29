/**
 * Read-only projection of a GridCraft native workbook snapshot.
 *
 * Cells live in `sheets[].cells.bands["<band>"].rows["<row>"]["<col>"]`. The row key is the
 * absolute 0-based sheet row, not an offset inside the band, and a band holds 64 rows, so a
 * row belongs to band `floor(row / 64)`. Column keys are 0-based (0 = A). The server's effects
 * diff uses the same arithmetic and refuses a row filed under the wrong band; so does this.
 */

export const GRIDCRAFT_BAND_ROWS = 64
export const GRIDCRAFT_MAX_ROWS = 1_048_576
export const GRIDCRAFT_MAX_COLUMNS = 16_384

export type WorkbookCellKind = "text" | "number" | "bool" | "error" | "empty"
export interface WorkbookCell {
  row: number
  col: number
  text: string
  kind: WorkbookCellKind
  /** Formula source without the leading "=", when the cell holds one. */
  formula?: string
}
export interface WorkbookSheet {
  name: string
  /** Cells inside the render window only, keyed by `cellKey(row, col)`. */
  cells: Map<string, WorkbookCell>
  /** Used extent of the whole sheet: highest occupied row/column + 1 (0 when empty). */
  rows: number
  cols: number
  count: number
}
export interface WorkbookView {
  sheets: WorkbookSheet[]
  activeSheet: number
}
export interface RenderWindow {
  rows: number
  cols: number
}

const INDEX = /^(0|[1-9]\d*)$/
const object = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value)

export const cellKey = (row: number, col: number) => `${row}:${col}`

/** 0 → A, 25 → Z, 26 → AA. */
export function columnName(col: number): string {
  let out = ""
  for (let n = col + 1; n > 0; n = Math.floor((n - 1) / 26)) out = String.fromCharCode(65 + ((n - 1) % 26)) + out
  return out
}
export const cellAddress = (row: number, col: number) => `${columnName(col)}${row + 1}`

const ERRORS: Record<string, string> = {
  Null: "#NULL!", Div0: "#DIV/0!", Value: "#VALUE!", Ref: "#REF!", Name: "#NAME?", Num: "#NUM!",
  NA: "#N/A", GettingData: "#GETTING_DATA", Spill: "#SPILL!", Calc: "#CALC!", Circ: "#CIRC!",
}

/** Numbers as a spreadsheet shows them in General format: 10 not 10.0, 0.3 not 0.30000000000000004. */
export function formatNumber(value: number): string {
  if (!Number.isFinite(value)) return "#NUM!"
  if (Number.isInteger(value)) return String(value)
  return String(Number(value.toPrecision(15)))
}

/** A GridCraft `{t, v}` value (or a bare JSON primitive) as display text. */
export function formatCellValue(value: unknown): { text: string; kind: WorkbookCellKind } {
  if (value === null || value === undefined) return { text: "", kind: "empty" }
  if (typeof value === "number") return { text: formatNumber(value), kind: "number" }
  if (typeof value === "boolean") return { text: value ? "TRUE" : "FALSE", kind: "bool" }
  if (typeof value === "string") return { text: value, kind: "text" }
  if (!object(value)) return { text: "", kind: "empty" }
  const v = value.v
  switch (value.t) {
    case "Empty": return { text: "", kind: "empty" }
    case "Number": return typeof v === "number" ? { text: formatNumber(v), kind: "number" } : { text: "", kind: "empty" }
    case "Bool": return typeof v === "boolean" ? { text: v ? "TRUE" : "FALSE", kind: "bool" } : { text: "", kind: "empty" }
    case "Error": return { text: typeof v === "string" ? (ERRORS[v] ?? (v.startsWith("#") ? v : `#${v.toUpperCase()}`)) : "#VALUE!", kind: "error" }
    case "Text": return { text: typeof v === "string" ? v : "", kind: "text" }
    default:
      // A value type this preview does not know yet: show a primitive as text, never an object dump.
      if (typeof v === "number") return { text: formatNumber(v), kind: "number" }
      if (typeof v === "string" || typeof v === "boolean") return { text: String(v), kind: "text" }
      return { text: "", kind: "empty" }
  }
}

function formulaOf(value: unknown): string | undefined {
  if (!object(value) || typeof value.text !== "string") return undefined
  return value.text.startsWith("=") ? value.text.slice(1) : value.text
}

/**
 * Projects a native snapshot. Returns null when the structure is not a GridCraft workbook
 * (bad keys, a row filed under the wrong band, out-of-range coordinates): fail closed rather
 * than show cells at guessed addresses. Only cells inside `window` are kept.
 */
export function parseWorkbookSnapshot(snapshot: unknown, window: RenderWindow = { rows: GRIDCRAFT_MAX_ROWS, cols: GRIDCRAFT_MAX_COLUMNS }): WorkbookView | null {
  if (!object(snapshot) || !Array.isArray(snapshot.sheets)) return null
  const sheets: WorkbookSheet[] = []
  for (const rawSheet of snapshot.sheets) {
    if (!object(rawSheet) || typeof rawSheet.name !== "string" || !object(rawSheet.cells)) return null
    const bands = rawSheet.cells.bands
    if (!object(bands)) return null
    const sheet: WorkbookSheet = { name: rawSheet.name, cells: new Map(), rows: 0, cols: 0, count: 0 }
    for (const [bandKey, rawBand] of Object.entries(bands)) {
      if (!INDEX.test(bandKey) || !object(rawBand) || !object(rawBand.rows)) return null
      const band = Number(bandKey)
      for (const [rowKey, rawRow] of Object.entries(rawBand.rows)) {
        if (!INDEX.test(rowKey) || !object(rawRow)) return null
        const row = Number(rowKey)
        if (row >= GRIDCRAFT_MAX_ROWS || Math.floor(row / GRIDCRAFT_BAND_ROWS) !== band) return null
        for (const [colKey, rawCell] of Object.entries(rawRow)) {
          if (!INDEX.test(colKey)) return null
          const col = Number(colKey)
          if (col >= GRIDCRAFT_MAX_COLUMNS) return null
          sheet.count++
          sheet.rows = Math.max(sheet.rows, row + 1)
          sheet.cols = Math.max(sheet.cols, col + 1)
          if (row >= window.rows || col >= window.cols) continue
          const cell = object(rawCell) ? rawCell : { value: rawCell }
          const formula = formulaOf(cell.formula)
          sheet.cells.set(cellKey(row, col), { row, col, ...formatCellValue(cell.value), ...(formula !== undefined ? { formula } : {}) })
        }
      }
    }
    sheets.push(sheet)
  }
  const active = snapshot.active_sheet
  const activeSheet = typeof active === "number" && Number.isInteger(active) && active >= 0 && active < sheets.length ? active : 0
  return { sheets, activeSheet }
}

/** Grid size to draw: the used range, at least `minimum` for readability, at most `limit`. */
export function renderExtent(sheet: Pick<WorkbookSheet, "rows" | "cols">, minimum: RenderWindow, limit: RenderWindow) {
  return {
    rows: Math.min(Math.max(sheet.rows, minimum.rows), limit.rows),
    cols: Math.min(Math.max(sheet.cols, minimum.cols), limit.cols),
    truncated: sheet.rows > limit.rows || sheet.cols > limit.cols,
  }
}

// A small GridCraft workbook that exercises the data classes a spreadsheet
// must not lose: formulas (also across sheets), number formats and more than
// one sheet. Built from the same blank workbook the New Doc dialog uses, in the
// engine's own JSON model (see internal/appengine/testdata/recalc-honest.json).
import { emptyDocument } from "./native"

type Workbook = {
  sheets: Array<{ name: string; cells: unknown }>
  styles: { styles: Array<{ num_fmt: string } & Record<string, unknown>> }
} & Record<string, unknown>

export const PERCENT_FORMAT = "0.00%"
export const MONEY_FORMAT = "#,##0.00"

function cell(v: number, formula: string | null = null, style = 0) {
  return { value: { t: "Number", v }, formula: formula === null ? null : { text: formula, array: null }, style }
}

export function richWorkbook(): Buffer {
  const wb = JSON.parse(emptyDocument("gridcraft").toString("utf8")) as Workbook
  const base = wb.styles.styles[0]
  wb.styles.styles.push({ ...structuredClone(base), num_fmt: PERCENT_FORMAT }, { ...structuredClone(base), num_fmt: MONEY_FORMAT })
  const first = wb.sheets[0]
  const second = structuredClone(first)
  second.name = "Totals"
  first.cells = { bands: { "0": { rows: { "0": { "0": cell(5), "1": cell(10, "A1*2"), "2": cell(0.256, null, 1), "3": cell(1234.5, null, 2) } } } }, len: 4 }
  second.cells = { bands: { "0": { rows: { "0": { "0": cell(11, "Sheet1!B1+1"), "1": cell(21, "Sheet1!B1+Sheet1!A1*2+1") } } } }, len: 2 }
  wb.sheets = [first, second]
  return Buffer.from(JSON.stringify(wb))
}

export interface CellFacts {
  value: unknown
  formula: string | null
  numFmt: string
}

/** Sheet names in order and, per "Sheet!A1", what a user would see: value, formula, number format. */
export function readFacts(bytes: Buffer): { sheets: string[]; cells: Record<string, CellFacts> } {
  const wb = JSON.parse(bytes.toString("utf8")) as Workbook
  const cells: Record<string, CellFacts> = {}
  for (const sheet of wb.sheets) {
    const bands = (sheet.cells as { bands?: Record<string, { rows: Record<string, Record<string, { value: { v?: unknown }; formula: { text: string } | null; style: number }>> }> }).bands ?? {}
    for (const band of Object.values(bands)) {
      for (const [row, cols] of Object.entries(band.rows)) {
        for (const [col, c] of Object.entries(cols)) {
          const addr = `${String.fromCharCode(65 + Number(col))}${Number(row) + 1}`
          cells[`${sheet.name}!${addr}`] = { value: c.value?.v, formula: c.formula?.text ?? null, numFmt: wb.styles.styles[c.style]?.num_fmt ?? "?" }
        }
      }
    }
  }
  return { sheets: wb.sheets.map((s) => s.name), cells }
}

export const EXPECTED_SHEETS = ["Sheet1", "Totals"]
export const EXPECTED_CELLS: Record<string, CellFacts> = {
  "Sheet1!A1": { value: 5, formula: null, numFmt: "General" },
  "Sheet1!B1": { value: 10, formula: "A1*2", numFmt: "General" },
  "Sheet1!C1": { value: 0.256, formula: null, numFmt: PERCENT_FORMAT },
  "Sheet1!D1": { value: 1234.5, formula: null, numFmt: MONEY_FORMAT },
  "Totals!A1": { value: 11, formula: "Sheet1!B1+1", numFmt: "General" },
  "Totals!B1": { value: 21, formula: "Sheet1!B1+Sheet1!A1*2+1", numFmt: "General" },
}

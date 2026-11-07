// A small GridCraft workbook that exercises the data classes a spreadsheet
// must not lose: typed values, dates, formulas (also across sheets) and more than
// one sheet. Built from the same blank workbook the New Doc dialog uses, in the
// engine's own JSON model (see internal/appengine/testdata/recalc-honest.json).
import { emptyDocument } from "./native"

type Workbook = {
  sheets: Array<{ name: string; cells: unknown }>
  date_system: "D1900" | "D1904"
  styles: { styles: Array<{ num_fmt: string } & Record<string, unknown>> }
} & Record<string, unknown>

export const PERCENT_FORMAT = "0.00%"
export const MONEY_FORMAT = "#,##0.00"
export const DATE_FORMAT = "yyyy-mm-dd"
export const EXPECTED_DATE_SYSTEM = "D1900"

function cell(v: number, formula: string | null = null, style = 0) {
  return { value: { t: "Number", v }, formula: formula === null ? null : { text: formula, array: null }, style }
}

export function richWorkbook(): Buffer {
  const wb = JSON.parse(emptyDocument("gridcraft").toString("utf8")) as Workbook
  // Grid0.12.1 dates are numeric serials formatted under the workbook date system.
  // D1900 + 45292 + yyyy-mm-dd denotes 2024-01-01. Preserve the canonical blank.
  const base = wb.styles.styles[0]
  wb.styles.styles.push({ ...structuredClone(base), num_fmt: PERCENT_FORMAT }, { ...structuredClone(base), num_fmt: MONEY_FORMAT }, { ...structuredClone(base), num_fmt: DATE_FORMAT })
  const first = wb.sheets[0]
  const second = structuredClone(first)
  second.name = "Totals"
  first.cells = { bands: { "0": { rows: { "0": { "0": cell(5), "1": cell(10, "A1*2"), "2": cell(0.256, null, 1), "3": cell(1234.5, null, 2), "4": { value: { t: "Bool", v: true }, formula: null, style: 0 }, "5": { value: { t: "Bool", v: false }, formula: null, style: 0 }, "6": { value: { t: "Text", v: "00123" }, formula: null, style: 0 }, "7": cell(45292, null, 3) } } } }, len: 8 }
  second.cells = { bands: { "0": { rows: { "0": { "0": cell(11, "Sheet1!B1+1"), "1": cell(21, "Sheet1!B1+Sheet1!A1*2+1") } } } }, len: 2 }
  wb.sheets = [first, second]
  return Buffer.from(JSON.stringify(wb))
}

export interface CellFacts {
  valueType: string | undefined
  value: unknown
  formula: string | null
  numFmt: string
}

/** Sheet names in order and, per "Sheet!A1", the native value type, value, formula and number format. */
export function readFacts(bytes: Buffer): { sheets: string[]; dateSystem: Workbook["date_system"]; cells: Record<string, CellFacts> } {
  const wb = JSON.parse(bytes.toString("utf8")) as Workbook
  const cells: Record<string, CellFacts> = {}
  for (const sheet of wb.sheets) {
    const bands = (sheet.cells as { bands?: Record<string, { rows: Record<string, Record<string, { value: { t?: string; v?: unknown }; formula: { text: string } | null; style: number }>> }> }).bands ?? {}
    for (const band of Object.values(bands)) {
      for (const [row, cols] of Object.entries(band.rows)) {
        for (const [col, c] of Object.entries(cols)) {
          const addr = `${String.fromCharCode(65 + Number(col))}${Number(row) + 1}`
          cells[`${sheet.name}!${addr}`] = { valueType: c.value?.t, value: c.value?.v, formula: c.formula?.text ?? null, numFmt: wb.styles.styles[c.style]?.num_fmt ?? "?" }
        }
      }
    }
  }
  return { sheets: wb.sheets.map((s) => s.name), dateSystem: wb.date_system, cells }
}

export const EXPECTED_SHEETS = ["Sheet1", "Totals"]
export const EXPECTED_CELLS: Record<string, CellFacts> = {
  "Sheet1!A1": { valueType: "Number", value: 5, formula: null, numFmt: "General" },
  "Sheet1!B1": { valueType: "Number", value: 10, formula: "A1*2", numFmt: "General" },
  "Sheet1!C1": { valueType: "Number", value: 0.256, formula: null, numFmt: PERCENT_FORMAT },
  "Sheet1!D1": { valueType: "Number", value: 1234.5, formula: null, numFmt: MONEY_FORMAT },
  "Sheet1!E1": { valueType: "Bool", value: true, formula: null, numFmt: "General" },
  "Sheet1!F1": { valueType: "Bool", value: false, formula: null, numFmt: "General" },
  "Sheet1!G1": { valueType: "Text", value: "00123", formula: null, numFmt: "General" },
  "Sheet1!H1": { valueType: "Number", value: 45292, formula: null, numFmt: DATE_FORMAT },
  "Totals!A1": { valueType: "Number", value: 11, formula: "Sheet1!B1+1", numFmt: "General" },
  "Totals!B1": { valueType: "Number", value: 21, formula: "Sheet1!B1+Sheet1!A1*2+1", numFmt: "General" },
}

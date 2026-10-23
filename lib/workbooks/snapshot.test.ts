import { describe, expect, it } from "vitest"
import { cellAddress, cellKey, columnName, formatCellValue, formatNumber, GRIDCRAFT_BAND_ROWS, parseWorkbookSnapshot, renderExtent } from "./snapshot"

// Cells copied from GridCraft 0.10.0 native output (other sheet fields omitted).
const text = (v: string) => ({ value: { t: "Text", v }, formula: null, style: 0 })
const number = (v: number) => ({ value: { t: "Number", v }, formula: null, style: 0 })
// A1:B3 = Item/Qty, Apples 10, Pears 5.
const valuesV1 = { sheets: [{ name: "Sheet1", cells: { bands: { "0": { rows: {
  "0": { "0": text("Item"), "1": text("Qty") },
  "1": { "0": text("Apples"), "1": number(10.0) },
  "2": { "0": text("Pears"), "1": number(5.0) },
} } }, len: 6 } }], active_sheet: 0 }
// B2 = 1, C9 = B2*2 (cached 2).
const formulaC9 = { sheets: [{ name: "Sheet1", cells: { bands: { "0": { rows: {
  "1": { "0": text("Input"), "1": number(1.0) },
  "8": { "0": text("Derived"), "2": { value: { t: "Number", v: 2.0 }, formula: { text: "B2*2", array: null }, style: 0 } },
} } }, len: 4 } }], active_sheet: 0 }
// cell.set A70 = "x": the row key stays absolute (69) and is filed under band 1.
const secondBand = { sheets: [{ name: "Sheet1", cells: { bands: { "1": { rows: { "69": { "0": text("x") } } } }, len: 1 } }], active_sheet: 0 }

const at = (snapshot: unknown, address: [number, number], sheet = 0) => parseWorkbookSnapshot(snapshot)!.sheets[sheet].cells.get(cellKey(...address))

describe("GridCraft cell addressing", () => {
  it("uses 64-row bands with absolute 0-based row keys", () => {
    expect(GRIDCRAFT_BAND_ROWS).toBe(64)
    expect(at(valuesV1, [1, 0])?.text).toBe("Apples")
    expect(cellAddress(1, 0)).toBe("A2")
    expect(at(valuesV1, [2, 1])?.text).toBe("5")
    expect(cellAddress(2, 1)).toBe("B3")
    const c9 = at(formulaC9, [8, 2])!
    expect(cellAddress(c9.row, c9.col)).toBe("C9")
    expect(at(secondBand, [69, 0])?.text).toBe("x")
    expect(cellAddress(69, 0)).toBe("A70")
  })

  it("names columns like a spreadsheet", () => {
    expect([0, 1, 25, 26, 27, 51, 52, 701, 702, 16383].map(columnName)).toEqual(["A", "B", "Z", "AA", "AB", "AZ", "BA", "ZZ", "AAA", "XFD"])
  })

  it("reports the used extent and the active sheet", () => {
    const view = parseWorkbookSnapshot(formulaC9)!
    expect(view.sheets[0]).toMatchObject({ name: "Sheet1", rows: 9, cols: 3, count: 4 })
    expect(parseWorkbookSnapshot({ sheets: [valuesV1.sheets[0], { ...valuesV1.sheets[0], name: "Two" }], active_sheet: 1 })!.activeSheet).toBe(1)
    expect(parseWorkbookSnapshot({ ...valuesV1, active_sheet: 9 })!.activeSheet).toBe(0)
  })

  it("keeps a formula beside its cached value", () => {
    expect(at(formulaC9, [8, 2])).toMatchObject({ text: "2", kind: "number", formula: "B2*2" })
    expect(at(formulaC9, [1, 1])?.formula).toBeUndefined()
  })

  it("keeps only cells inside the render window but counts the whole sheet", () => {
    const view = parseWorkbookSnapshot(secondBand, { rows: 10, cols: 5 })!
    expect(view.sheets[0].cells.size).toBe(0)
    expect(view.sheets[0]).toMatchObject({ rows: 70, cols: 1, count: 1 })
  })

  it.each([
    ["a row under the wrong band", { sheets: [{ name: "S", cells: { bands: { "0": { rows: { "69": { "0": text("x") } } } }, len: 1 } }] }],
    ["a row-in-band offset key", { sheets: [{ name: "S", cells: { bands: { "1": { rows: { "5": { "0": text("x") } } } }, len: 1 } }] }],
    ["a non-decimal key", { sheets: [{ name: "S", cells: { bands: { "0": { rows: { "01": { "0": text("x") } } } }, len: 1 } }] }],
    ["a column past XFD", { sheets: [{ name: "S", cells: { bands: { "0": { rows: { "0": { "16384": text("x") } } } }, len: 1 } }] }],
    ["no sheets array", { sheets: {} }],
    ["not an object", "workbook"],
  ])("fails closed on %s", (_name, snapshot) => expect(parseWorkbookSnapshot(snapshot)).toBeNull())
})

describe("GridCraft value formatting", () => {
  it.each([
    [{ t: "Number", v: 10.0 }, "10", "number"],
    [{ t: "Number", v: 1.5 }, "1.5", "number"],
    [{ t: "Number", v: 0.30000000000000004 }, "0.3", "number"],
    [{ t: "Number", v: -42 }, "-42", "number"],
    [{ t: "Bool", v: true }, "TRUE", "bool"],
    [{ t: "Bool", v: false }, "FALSE", "bool"],
    [{ t: "Error", v: "Div0" }, "#DIV/0!", "error"],
    [{ t: "Error", v: "NA" }, "#N/A", "error"],
    [{ t: "Error", v: "Name" }, "#NAME?", "error"],
    [{ t: "Text", v: "" }, "", "text"],
    [{ t: "Text", v: "Apples" }, "Apples", "text"],
    [{ t: "Empty", v: null }, "", "empty"],
    [{ t: "Future", v: { nested: true } }, "", "empty"],
    [42, "42", "number"],
    [null, "", "empty"],
  ])("formats %j", (value, text, kind) => expect(formatCellValue(value)).toEqual({ text, kind }))

  it("never prints a non-finite number", () => expect(formatNumber(Number.POSITIVE_INFINITY)).toBe("#NUM!"))

  it("sizes the grid to the used range within bounds", () => {
    const minimum = { rows: 10, cols: 5 }, limit = { rows: 500, cols: 52 }
    expect(renderExtent({ rows: 3, cols: 2 }, minimum, limit)).toEqual({ rows: 10, cols: 5, truncated: false })
    expect(renderExtent({ rows: 70, cols: 8 }, minimum, limit)).toEqual({ rows: 70, cols: 8, truncated: false })
    expect(renderExtent({ rows: 800, cols: 60 }, minimum, limit)).toEqual({ rows: 500, cols: 52, truncated: true })
  })
})

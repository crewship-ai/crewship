import { describe, expect, it } from "vitest"
import { readFacts, richWorkbook } from "../../e2e/acceptance/prd/craft-lease/sheet-workbook"

describe("Grid A6 typed roundtrip fixture", () => {
  it("retains the existing numbers, formulas, formats and multiple sheets", () => {
    const facts = readFacts(richWorkbook())
    expect(facts.sheets).toEqual(["Sheet1", "Totals"])
    expect(facts.cells).toMatchObject({
      "Sheet1!A1": { value: 5, formula: null, numFmt: "General" },
      "Sheet1!B1": { value: 10, formula: "A1*2", numFmt: "General" },
      "Sheet1!C1": { value: 0.256, formula: null, numFmt: "0.00%" },
      "Sheet1!D1": { value: 1234.5, formula: null, numFmt: "#,##0.00" },
      "Totals!A1": { value: 11, formula: "Sheet1!B1+1", numFmt: "General" },
      "Totals!B1": { value: 21, formula: "Sheet1!B1+Sheet1!A1*2+1", numFmt: "General" },
    })
  })

  it.each([
    ["Sheet1!E1", "Bool", true, "General"],
    ["Sheet1!F1", "Bool", false, "General"],
    ["Sheet1!G1", "Text", "00123", "General"],
    ["Sheet1!H1", "Number", 45292, "yyyy-mm-dd"],
  ] as const)("retains the native type and value at %s", (address, valueType, value, numFmt) => {
    expect(readFacts(richWorkbook()).cells[address]).toEqual({ valueType, value, formula: null, numFmt })
  })

  it("retains D1900 so the date serial means 2024-01-01", () => {
    // Pinned Grid0.12.1 represents dates as Number + num_fmt + date_system,
    // rather than a separate Date value tag.
    expect(readFacts(richWorkbook())).toHaveProperty("dateSystem", "D1900")
  })

  it("does not erase a changed native value tag from the facts", () => {
    const workbook = JSON.parse(richWorkbook().toString("utf8"))
    workbook.sheets[0].cells.bands["0"].rows["0"]["0"].value.t = "Text"
    workbook.sheets[0].cells.bands["0"].rows["0"]["0"].value.v = "5"
    expect(readFacts(Buffer.from(JSON.stringify(workbook))).cells["Sheet1!A1"]).toMatchObject({ valueType: "Text", value: "5" })
  })

  it("does not hide a changed workbook date system", () => {
    const workbook = JSON.parse(richWorkbook().toString("utf8"))
    workbook.date_system = "D1904"
    expect(readFacts(Buffer.from(JSON.stringify(workbook)))).toHaveProperty("dateSystem", "D1904")
  })
})

import { describe, expect, it } from "vitest"
import { WORKBOOK_ACCEPT, isWorkbookName, workbookBaseName } from "../workbook-file"

describe("which file names are workbooks a Sheet keeps as they are", () => {
  it("takes .xlsx and .xlsm in any case and nothing else", () => {
    expect(["a.xlsx", "B.XLSM", "c.Xlsx"].map(isWorkbookName)).toEqual([true, true, true])
    expect(["a.xlsb", "a.xls", "a.ods", "a.csv", "xlsx", ".xlsx"].map(isWorkbookName)).toEqual([false, false, false, false, false, false])
  })
  it("names the Sheet without the extension", () => {
    expect(workbookBaseName("Q3 plan.xlsm")).toBe("Q3 plan")
    expect(workbookBaseName("Budget.XLSX")).toBe("Budget")
    expect(workbookBaseName(".xlsx")).toBe("Sheet")
  })
  it("lets the file picker offer both formats by extension and type", () => {
    expect(WORKBOOK_ACCEPT.split(",")).toEqual([".xlsx", ".xlsm", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "application/vnd.ms-excel.sheet.macroEnabled.12"])
  })
})

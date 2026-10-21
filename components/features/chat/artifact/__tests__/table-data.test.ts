import { describe, expect, it } from "vitest"
import { columnLabel, parseDelimited, selectionContext, TABLE_LIMITS, visibleRows } from "../table-data"

describe("bounded delimited tables", () => {
  it("preserves quoted Unicode separators, escaped quotes, embedded newlines and empty cells", () => {
    expect(parseDelimited('\uFEFFname,note,empty\r\nŽofie,"first, second\n""quoted""",\r\n', ",")).toEqual({ columns: 3, rows: [["name", "note", "empty"], ["Žofie", 'first, second\n"quoted"', ""]] })
    expect(parseDelimited('a\t"b\tc"\t\n', "\t").rows).toEqual([["a", "b\tc", ""]])
    expect(parseDelimited('""', ",").rows).toEqual([[""]])
    expect(parseDelimited("", ",").rows).toEqual([])
  })
  it.each(['"unclosed', 'field"quote', '"closed"x'])('refuses malformed CSV %s', input => {
    expect(() => parseDelimited(input, ",")).toThrow()
  })
  it("refuses oversized dimensions instead of silently truncating", () => {
    expect(() => parseDelimited("x".repeat(TABLE_LIMITS.bytes + 1), ",")).toThrow("5 MiB")
    expect(() => parseDelimited(Array(102).fill("x").join(","), ",")).toThrow("column or cell")
    expect(() => parseDelimited("x\n".repeat(TABLE_LIMITS.rows + 1), ",")).toThrow("20,000-row")
    expect(() => parseDelimited((Array(100).fill("x").join(",") + "\n").repeat(2001), ",")).toThrow("column or cell")
  })
  it("sorts and filters without changing original coordinates or source data", () => {
    const table = parseDelimited("Alpha,10\nBeta,2\nGamma,30", ",")
    expect(visibleRows(table, "a", { column: 1, descending: false }).map(row => row.index)).toEqual([1, 0, 2])
    expect(visibleRows(table, "beta", null).map(row => row.index)).toEqual([1])
    expect(table.rows[0]).toEqual(["Alpha", "10"])
    const context = selectionContext("reports/a.csv", "sha", table, [2, 1, 1])
    expect(context).toContain('"range": "A2:B2"')
    expect(context).toContain('"range": "A3:B3"')
    expect(context).toContain('"sha256": "sha"')
    expect(context).toContain("untrusted reference data")
    expect(columnLabel(0)).toBe("A")
    expect(columnLabel(26)).toBe("AA")
    expect(columnLabel(99)).toBe("CV")
  })
  it("does not execute formulas and rejects stale or excessively large selection", () => {
    const table = parseDelimited("=SUM(A2:A3),https://example.org", ",")
    expect(table.rows[0][0]).toBe("=SUM(A2:A3)")
    expect(() => selectionContext("a.csv", "v1", table, [1])).toThrow("no longer")
    expect(() => selectionContext("a.csv", "v1", table, [])).toThrow("at most")
    const many = parseDelimited("a,b,c\n".repeat(100), ",")
    expect(() => selectionContext("a.csv", "v1", many, Array.from({ length: 100 }, (_, i) => i))).toThrow("200 cells")
    expect(() => selectionContext("a.csv", "v1", parseDelimited('x'.repeat(13000), ","), [0])).toThrow("context limit")
  })
})

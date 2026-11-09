import { describe, expect, it } from "vitest"
import { CSV_LIMITS, parseCsv, renderCsv } from "../code-preview-csv"

describe("parseCsv", () => {
  it.each([
    ["plain rows", "a,b\n1,2", [["a", "b"], ["1", "2"]]],
    ["CRLF rows and a final line break", "a,b\r\n1,2\r\n", [["a", "b"], ["1", "2"]]],
    ["quoted commas, quotes and line breaks", "name,note\n\"Doe, Jane\",\"said \"\"hi\"\"\nthen left\"", [["name", "note"], ["Doe, Jane", "said \"hi\"\nthen left"]]],
    ["empty cells", "a,b,c\n,,", [["a", "b", "c"], ["", "", ""]]],
    ["one column", "a\n1\n2", [["a"], ["1"], ["2"]]],
    ["unicode", "město,počet\nBrno,3", [["město", "počet"], ["Brno", "3"]]],
  ])("reads %s", (_name, source, rows) => {
    expect(parseCsv(source)).toEqual({ ok: true, rows })
  })

  it.each([
    ["a stray quote", "a,b\n1,x\"y", /a quote inside an unquoted cell \(line 2\)/],
    ["text after a closing quote", "a,b\n\"1\"x,2", /text after a closing quote \(line 2\)/],
    ["an unterminated quote", "a,b\n\"1,2", /a quoted cell is not closed/],
    ["ragged rows", "a,b\n1,2,3", /this row has 3 cells, the header has 2 \(line 2\)/],
    ["a bare carriage return", "a,b\r1,2", /carriage return/],
    ["nothing", "", /Nothing to show yet/],
    ["too many columns", Array.from({ length: CSV_LIMITS.columns + 1 }, (_, i) => `c${i}`).join(","), /more than 50 columns/],
    ["too many rows", ["h", ...Array.from({ length: CSV_LIMITS.rows + 1 }, (_, i) => String(i))].join("\n"), /more than 500 rows/],
    ["a huge cell", "a\n" + "x".repeat(CSV_LIMITS.cell + 1), /a cell is longer than/],
    ["a huge source", "a\n" + "x,".repeat(CSV_LIMITS.chars), /longer than 100,000 characters/],
  ])("refuses %s with a reason", (_name, source, reason) => {
    const result = parseCsv(source)
    expect(result.ok).toBe(false)
    if (!result.ok) expect(result.error).toMatch(reason)
  })
})

describe("renderCsv", () => {
  it("builds an accessible table whose cells are text only", () => {
    const result = renderCsv("name,html\nx,\"<img src=x onerror=alert(1)><script>alert(2)</script>\"")
    expect(result.ok).toBe(true)
    if (!result.ok) return
    const table = result.node.querySelector("table")!
    expect(table.querySelector("caption")!.textContent).toBe("CSV table, 1 row and 2 columns")
    expect([...table.querySelectorAll("th")].map(th => [th.textContent, th.getAttribute("scope")])).toEqual([["name", "col"], ["html", "col"]])
    expect(table.querySelectorAll("td")[1].textContent).toBe("<img src=x onerror=alert(1)><script>alert(2)</script>")
    expect(result.node.querySelector("img, script")).toBeNull()
  })
})

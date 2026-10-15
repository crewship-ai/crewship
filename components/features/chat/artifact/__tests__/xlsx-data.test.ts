import { describe, expect, it } from "vitest"
import { zipSync, strToU8 } from "fflate"
import { parseXlsxWorkbook } from "../xlsx-data"
import { unpackBoundedWorkbook, XLSX_LIMITS } from "../xlsx-archive"

const NS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
const REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
function workbook(sheets: string[], extras: Record<string, string> = {}, options: { styles?: string; shared?: string; epoch1904?: boolean } = {}) {
  const files: Record<string, Uint8Array> = {
    "[Content_Types].xml": strToU8('<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/></Types>'),
    "xl/workbook.xml": strToU8(`<workbook xmlns="${NS}" xmlns:r="${REL}"><workbookPr date1904="${Number(!!options.epoch1904)}"/><sheets>${sheets.map((_, i) => `<sheet name="Sheet ${i + 1}" sheetId="${i + 1}" r:id="r${i}"/>`).join("")}</sheets></workbook>`),
    "xl/_rels/workbook.xml.rels": strToU8(`<Relationships>${sheets.map((_, i) => `<Relationship Id="r${i}" Type="${REL}/worksheet" Target="worksheets/sheet${i}.xml"/>`).join("")}</Relationships>`),
  }
  sheets.forEach((data, i) => { files[`xl/worksheets/sheet${i}.xml`] = strToU8(`<worksheet xmlns="${NS}"><sheetData>${data}</sheetData></worksheet>`) })
  if (options.shared) files["xl/sharedStrings.xml"] = strToU8(options.shared)
  if (options.styles) files["xl/styles.xml"] = strToU8(options.styles)
  for (const [path, value] of Object.entries(extras)) files[path] = strToU8(value)
  return zipSync(files)
}

describe("bounded local XLSX data", () => {
  it("preserves Unicode, leading gaps, empty cells and sheet coordinates", async () => {
    const bytes = workbook(['<row r="3"><c r="B3" t="inlineStr"><is><t> Žofie &amp; 文 </t></is></c><c r="D3" t="b"><v>1</v></c></row>', '<row r="1"><c r="A1"><v>12345678901234567890</v></c></row>'])
    const result = await parseXlsxWorkbook(bytes)
    expect(result.sheets).toHaveLength(2)
    expect(result.sheets[0].table.rows).toEqual([["", "", "", ""], ["", "", "", ""], ["", " Žofie & 文 ", "", "TRUE"]])
    expect(result.sheets[1].table.rows).toEqual([["12345678901234567890"]])
  })
  it("renders only cached formula values and labels missing caches explicitly", async () => {
    const result = await parseXlsxWorkbook(workbook(['<row r="1"><c r="A1"><f>HYPERLINK("https://untrusted.test")</f><v>42</v></c><c r="B1" t="str"><f>CONCAT(A1)</f><v>saved result</v></c><c r="C1" t="str"><f>UNCACHED()</f></c><c r="D1"><f>UNCACHED()</f><v/></c><c r="E1" t="str"><f>EMPTY()</f><v/></c></row>']))
    expect(result.formulas).toBe(5)
    expect(result.missingFormulaResults).toBe(2)
    expect(result.sheets[0].table.rows[0]).toEqual(["42", "saved result", "[Formula not evaluated: no cached result]", "[Formula not evaluated: no cached result]", ""])
    expect(JSON.stringify(result)).not.toContain("HYPERLINK")
  })
  it("resolves shared and rich strings and saved date styles without trimming", async () => {
    const result = await parseXlsxWorkbook(workbook(['<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" s="0"><v>1</v></c></row>'], {}, {
      shared: '<sst><si><r><t>one </t></r><r><t>two</t></r><rPh><t>ignored</t></rPh></si></sst>',
      styles: '<styleSheet><cellXfs><xf numFmtId="14"/></cellXfs></styleSheet>', epoch1904: true,
    }))
    expect(result.sheets[0].table.rows).toEqual([["one two", "1904-01-02T00:00:00.000Z"]])
  })
  it.each([
    ['<row r="1"><c r="XFD1"><v>1</v></c></row>', "100-column"],
    ['<row r="999999"><c r="A999999"><v>1</v></c></row>', "coordinate"],
    ['<row r="20000"><c r="CV20000"><v>1</v></c></row>', "sparse coordinates"],
    ['<row r="1"><c r="A1"><v>1</v></c><c r="A1"><v>2</v></c></row>', "duplicated"],
  ])("refuses dangerous sparse/out-of-order dimensions", async (xml, reason) => {
    await expect(parseXlsxWorkbook(workbook([xml]))).rejects.toThrow(reason)
  })
  it("bounds worksheets and strings before the library allocates a grid", async () => {
    await expect(parseXlsxWorkbook(workbook(Array(11).fill("")))).rejects.toThrow("10-sheet")
    await expect(parseXlsxWorkbook(workbook([`<row r="1"><c r="A1" t="inlineStr"><is><t>${"x".repeat(XLSX_LIMITS.cellCharacters + 1)}</t></is></c></row>`]))).rejects.toThrow("32,768-character")
    const repeated = Array.from({ length: 321 }, (_, i) => `<row r="${i + 1}"><c r="A${i + 1}" t="s"><v>0</v></c></row>`).join("")
    await expect(parseXlsxWorkbook(workbook([repeated], {}, { shared: `<sst><si><t>${"x".repeat(32768)}</t></si></sst>` }))).rejects.toThrow("10 MiB text")
  })
  it("refuses DTDs, macros and external relationships", async () => {
    await expect(parseXlsxWorkbook(workbook([""] , { "xl/workbook.xml": '<!DOCTYPE workbook [<!ENTITY payload "unsafe">]><workbook/>' }))).rejects.toThrow("declarations")
    await expect(parseXlsxWorkbook(workbook([""], { "xl/vbaProject.bin": "macro" }))).rejects.toThrow("Macros")
    await expect(parseXlsxWorkbook(workbook([""], { "xl/worksheets/_rels/sheet0.xml.rels": '<Relationships><Relationship Id="link" TargetMode="External" Target="https://untrusted.test"/></Relationships>' }))).rejects.toThrow("External")
  })
})

describe("actual ZIP expansion budget", () => {
  it("counts actual inflated output even with a falsified tiny originalSize", () => {
    const bytes = zipSync({ "bomb.xml": strToU8("x".repeat(100_000)) })
    const view = new DataView(bytes.buffer)
    view.setUint32(22, 1, true)
    for (let at = 0; at < bytes.length - 46; at++) if (view.getUint32(at, true) === 0x02014b50) view.setUint32(at + 24, 1, true)
    expect(() => unpackBoundedWorkbook(bytes, 4096)).toThrow("expanded ZIP")
  })
  it("rejects duplicate entries and traversal paths", () => {
    expect(() => unpackBoundedWorkbook(zipSync({ "../bad.xml": strToU8("x") }))).toThrow("unsafe path")
    const original = zipSync({ "one.xml": strToU8("x"), "two.xml": strToU8("x") })
    const duplicate = original.slice(), from = strToU8("two.xml"), to = strToU8("one.xml")
    for (let at = 0; at <= duplicate.length - from.length; at++) {
      if (from.every((value, i) => duplicate[at + i] === value)) duplicate.set(to, at)
    }
    expect(() => unpackBoundedWorkbook(duplicate)).toThrow("duplicate")
  })
  it("rejects truncated, encrypted/legacy and oversized inputs", () => {
    const valid = workbook([""])
    expect(() => unpackBoundedWorkbook(valid.slice(0, -2))).toThrow("incomplete")
    const corrupt = valid.slice(), view = new DataView(corrupt.buffer)
    for (let at = 0; at < corrupt.length - 46; at++) if (view.getUint32(at, true) === 0x02014b50) { view.setUint32(at + 16, 1234, true); break }
    expect(() => unpackBoundedWorkbook(corrupt)).toThrow("checksum")
    const encrypted = valid.slice()
    new DataView(encrypted.buffer).setUint16(6, 1, true)
    expect(() => unpackBoundedWorkbook(encrypted)).toThrow("Encrypted workbooks")
    expect(() => unpackBoundedWorkbook(Uint8Array.of(0xd0, 0xcf, 0x11, 0xe0))).toThrow("Encrypted or legacy")
    expect(() => unpackBoundedWorkbook(new Uint8Array(XLSX_LIMITS.compressedBytes + 1))).toThrow("5 MiB input")
  })
})

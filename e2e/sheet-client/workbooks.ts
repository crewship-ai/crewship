// Synthetic workbooks for the client's path through a Sheet. Every value is
// generated here from a fixed seed: no client data, nothing copied from a real
// file. The package is written by hand (raw SpreadsheetML), so each part a
// client workbook carries is there on purpose and its saved values are known.
//
// clientWorkbook(): about 5 MB, five sheets.
//   Vstupy     A1:D1 merged title; B3 region (list validation), B4 month (whole
//              number 1–12 validation), B5 factor 2, B6 threshold.
//   Data       Region, Produkt, Měsíc, Kusy, Cena, Tržba (=D*E), `rows` rows.
//   Souhrn     A1:C1 merged; B4:B7 =SUMIFS(Tržba by region), C4:C7 =B*Vstupy!B5,
//              B9 SUMIFS by region and month from Vstupy, B10 =SUM(B4:B7);
//              conditional formats on B4:B7 (colour scale) and C4:C7 (> threshold).
//   PivotZdroj a small source range, and
//   Pivot      a PivotTable over it (rows Region, Sum of Tržba) with its cache.
// lossyWorkbook(): one sheet and an Excel 2016 chart (chartEx) the editor does
// not keep, so a save from the editor would drop it (fidelity_loss).
import { zip } from "./zip"

export const REGIONS = ["Sever", "Jih", "Východ", "Západ"] as const
export const XLSX = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

const NS = 'xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"'
const REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
const XML = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n'
const esc = (s: string) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;")

/** mulberry32: a small fixed-seed generator, so the same call gives the same workbook. */
function prng(seed: number) {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

class Strings {
  private index = new Map<string, number>()
  private list: string[] = []
  id(s: string): number {
    let i = this.index.get(s)
    if (i === undefined) { i = this.list.length; this.list.push(s); this.index.set(s, i) }
    return i
  }
  xml(): string {
    return `${XML}<sst ${NS} count="${this.list.length}" uniqueCount="${this.list.length}">${this.list.map(s => `<si><t xml:space="preserve">${esc(s)}</t></si>`).join("")}</sst>`
  }
}

type Cell = { s: string } | { n: number } | { f: string; v: number } | { f: string; str: string }
function cellXml(ref: string, c: Cell, sst: Strings, style = 0): string {
  const st = style ? ` s="${style}"` : ""
  if ("s" in c) return `<c r="${ref}" t="s"${st}><v>${sst.id(c.s)}</v></c>`
  if ("n" in c) return `<c r="${ref}"${st}><v>${c.n}</v></c>`
  if ("str" in c) return `<c r="${ref}" t="str"${st}><f>${esc(c.f)}</f><v>${esc(c.str)}</v></c>`
  return `<c r="${ref}"${st}><f>${esc(c.f)}</f><v>${c.v}</v></c>`
}
function rowXml(r: number, cells: Array<[string, Cell, number?]>, sst: Strings): string {
  return `<row r="${r}">${cells.map(([col, c, style]) => cellXml(`${col}${r}`, c, sst, style)).join("")}</row>`
}

const STYLES = `${XML}<styleSheet ${NS}>
<fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="14"/><name val="Calibri"/></font></fonts>
<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>
<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>
<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
<cellXfs count="3"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/><xf numFmtId="3" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/></cellXfs>
<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>
<dxfs count="1"><dxf><font><b/><color rgb="FF9C0006"/></font><fill><patternFill><bgColor rgb="FFFFC7CE"/></patternFill></fill></dxf></dxfs>
</styleSheet>`

function sheetXml(rows: string[], extra: { before?: string; after?: string } = {}): string {
  return `${XML}<worksheet ${NS}>${extra.before ?? ""}<sheetData>${rows.join("")}</sheetData>${extra.after ?? ""}</worksheet>`
}

export interface ClientWorkbook {
  name: string
  bytes: Buffer
  rows: number
  /** Saved values of Souhrn as generated (factor 2). */
  expected: { byRegion: number[]; adjusted: number[]; selected: number; total: number; pivot: number[] }
  /** Souhrn C4:C7 for another factor in Vstupy!B5. */
  adjustedFor(factor: number): number[]
}

/** About 5 MB with the default rows (the size reported by the runbook's run). */
export function clientWorkbook(rows = Number(process.env.SHEET_E2E_ROWS) || 145_000, name = "klient-syntetika.xlsx"): ClientWorkbook {
  const sst = new Strings()
  const rnd = prng(20261126)
  const last = rows + 1
  const factor = 2
  const threshold = 1_000_000_000
  const selected = { region: "Sever", month: 3 }

  // Data
  const byRegion = [0, 0, 0, 0]
  let selectedSum = 0
  const dataRows: string[] = [rowXml(1, [["A", { s: "Region" }], ["B", { s: "Produkt" }], ["C", { s: "Měsíc" }], ["D", { s: "Kusy" }], ["E", { s: "Cena" }], ["F", { s: "Tržba" }]], sst)]
  for (let i = 0; i < rows; i++) {
    const r = i + 2
    const ri = i % 4
    const month = (Math.floor(i / 4) % 12) + 1
    const units = 1 + Math.floor(rnd() * 500)
    const price = 10 + Math.floor(rnd() * 990)
    const sales = units * price
    byRegion[ri] += sales
    if (REGIONS[ri] === selected.region && month === selected.month) selectedSum += sales
    dataRows.push(rowXml(r, [["A", { s: REGIONS[ri] }], ["B", { s: `P${String((i % 20) + 1).padStart(2, "0")}` }], ["C", { n: month }], ["D", { n: units }], ["E", { n: price }], ["F", { f: `D${r}*E${r}`, v: sales }]], sst))
  }
  const range = (col: string) => `Data!$${col}$2:$${col}$${last}`

  // Vstupy (first sheet: the editor helpers start at the first sheet)
  const vstupy = sheetXml([
    rowXml(1, [["A", { s: "Syntetický plán – testovací data" }, 1]], sst),
    rowXml(3, [["A", { s: "Region" }], ["B", { s: selected.region }]], sst),
    rowXml(4, [["A", { s: "Měsíc" }], ["B", { n: selected.month }]], sst),
    rowXml(5, [["A", { s: "Koeficient" }], ["B", { n: factor }]], sst),
    rowXml(6, [["A", { s: "Prah" }], ["B", { n: threshold }]], sst),
  ], {
    before: '<cols><col min="1" max="1" width="18" customWidth="1"/><col min="2" max="4" width="14" customWidth="1"/></cols>',
    after: '<mergeCells count="1"><mergeCell ref="A1:D1"/></mergeCells>'
      + '<dataValidations count="2">'
      + `<dataValidation type="list" allowBlank="1" showErrorMessage="1" sqref="B3"><formula1>"${REGIONS.join(",")}"</formula1></dataValidation>`
      + '<dataValidation type="whole" allowBlank="1" showErrorMessage="1" sqref="B4"><formula1>1</formula1><formula2>12</formula2></dataValidation>'
      + "</dataValidations>",
  })

  // Souhrn
  const total = byRegion.reduce((a, b) => a + b, 0)
  const souhrnRows = [
    rowXml(1, [["A", { s: "Souhrn podle regionu" }, 1]], sst),
    rowXml(3, [["A", { s: "Region" }], ["B", { s: "Tržba" }], ["C", { s: "Upraveno" }]], sst),
    ...REGIONS.map((reg, i) => rowXml(4 + i, [["A", { s: reg }], ["B", { f: `SUMIFS(${range("F")},${range("A")},A${4 + i})`, v: byRegion[i] }, 2], ["C", { f: `B${4 + i}*Vstupy!$B$5`, v: byRegion[i] * factor }, 2]], sst)),
    rowXml(9, [["A", { s: "Vybraný region a měsíc" }], ["B", { f: `SUMIFS(${range("F")},${range("A")},Vstupy!$B$3,${range("C")},Vstupy!$B$4)`, v: selectedSum }, 2]], sst),
    rowXml(10, [["A", { s: "Celkem" }], ["B", { f: "SUM(B4:B7)", v: total }, 2]], sst),
  ]
  const souhrn = sheetXml(souhrnRows, {
    before: '<cols><col min="1" max="1" width="24" customWidth="1"/><col min="2" max="3" width="18" customWidth="1"/></cols>',
    after: '<mergeCells count="1"><mergeCell ref="A1:C1"/></mergeCells>'
      + '<conditionalFormatting sqref="B4:B7"><cfRule type="colorScale" priority="2"><colorScale><cfvo type="min"/><cfvo type="max"/><color rgb="FFF8696B"/><color rgb="FF63BE7B"/></colorScale></cfRule></conditionalFormatting>'
      + '<conditionalFormatting sqref="C4:C7"><cfRule type="cellIs" dxfId="0" priority="1" operator="greaterThan"><formula>Vstupy!$B$6</formula></cfRule></conditionalFormatting>',
  })

  // PivotZdroj and Pivot
  const pivotSource: Array<[string, number]> = REGIONS.flatMap((reg, i) => [[reg, 100 + i * 10], [reg, 5 + i]] as Array<[string, number]>)
  const pivotSums = REGIONS.map(reg => pivotSource.filter(([r]) => r === reg).reduce((a, [, v]) => a + v, 0))
  const zdroj = sheetXml([
    rowXml(1, [["A", { s: "Region" }], ["B", { s: "Tržba" }]], sst),
    ...pivotSource.map(([reg, v], i) => rowXml(2 + i, [["A", { s: reg }], ["B", { n: v }]], sst)),
  ])
  const pivotRows = [
    rowXml(3, [["A", { s: "Popisky řádků" }], ["B", { s: "Součet z Tržba" }]], sst),
    ...REGIONS.map((reg, i) => rowXml(4 + i, [["A", { s: reg }], ["B", { n: pivotSums[i] }]], sst)),
    rowXml(8, [["A", { s: "Celkový součet" }], ["B", { n: pivotSums.reduce((a, b) => a + b, 0) }]], sst),
  ]
  const pivotSheet = sheetXml(pivotRows)
  const cacheDef = `${XML}<pivotCacheDefinition ${NS} r:id="rId1" refreshOnLoad="0" recordCount="${pivotSource.length}">`
    + `<cacheSource type="worksheet"><worksheetSource ref="A1:B${pivotSource.length + 1}" sheet="PivotZdroj"/></cacheSource>`
    + `<cacheFields count="2"><cacheField name="Region" numFmtId="0"><sharedItems count="4">${REGIONS.map(r => `<s v="${esc(r)}"/>`).join("")}</sharedItems></cacheField>`
    + `<cacheField name="Tržba" numFmtId="0"><sharedItems containsSemiMixedTypes="0" containsString="0" containsNumber="1" containsInteger="1" minValue="5" maxValue="130"/></cacheField></cacheFields></pivotCacheDefinition>`
  const cacheRecords = `${XML}<pivotCacheRecords ${NS} count="${pivotSource.length}">${pivotSource.map(([reg, v]) => `<r><x v="${REGIONS.indexOf(reg as typeof REGIONS[number])}"/><n v="${v}"/></r>`).join("")}</pivotCacheRecords>`
  const pivotTable = `${XML}<pivotTableDefinition ${NS.replace(' xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"', "")} name="KontingencniTabulka1" cacheId="1" applyNumberFormats="0" applyBorderFormats="0" applyFontFormats="0" applyPatternFormats="0" applyAlignmentFormats="0" applyWidthHeightFormats="1" dataCaption="Hodnoty" updatedVersion="6" minRefreshableVersion="3" useAutoFormatting="1" itemPrintTitles="1" createdVersion="6" indent="0" outline="1" outlineData="1" multipleFieldFilters="0">`
    + `<location ref="A3:B8" firstHeaderRow="1" firstDataRow="1" firstDataCol="1"/>`
    + `<pivotFields count="2"><pivotField axis="axisRow" showAll="0"><items count="5">${REGIONS.map((_, i) => `<item x="${i}"/>`).join("")}<item t="default"/></items></pivotField><pivotField dataField="1" showAll="0"/></pivotFields>`
    + `<rowFields count="1"><field x="0"/></rowFields><rowItems count="5">${REGIONS.map((_, i) => `<i><x v="${i}"/></i>`).join("")}<i t="grand"><x/></i></rowItems><colItems count="1"><i/></colItems>`
    + `<dataFields count="1"><dataField name="Součet z Tržba" fld="1" baseField="0" baseItem="0"/></dataFields>`
    + `<pivotTableStyleInfo name="PivotStyleLight16" showRowHeaders="1" showColHeaders="1" showRowStripes="0" showColStripes="0" showLastColumn="1"/></pivotTableDefinition>`

  const sheets = ["Vstupy", "Data", "Souhrn", "PivotZdroj", "Pivot"]
  const workbook = `${XML}<workbook ${NS}><bookViews><workbookView activeTab="0"/></bookViews><sheets>${sheets.map((s, i) => `<sheet name="${s}" sheetId="${i + 1}" r:id="rId${i + 1}"/>`).join("")}</sheets>`
    + `<definedNames><definedName name="Koeficient">Vstupy!$B$5</definedName></definedNames>`
    + `<calcPr calcId="191029" fullCalcOnLoad="0"/><pivotCaches><pivotCache cacheId="1" r:id="rId${sheets.length + 3}"/></pivotCaches></workbook>`
  const workbookRels = `${XML}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`
    + sheets.map((_, i) => `<Relationship Id="rId${i + 1}" Type="${REL}/worksheet" Target="worksheets/sheet${i + 1}.xml"/>`).join("")
    + `<Relationship Id="rId${sheets.length + 1}" Type="${REL}/styles" Target="styles.xml"/>`
    + `<Relationship Id="rId${sheets.length + 2}" Type="${REL}/sharedStrings" Target="sharedStrings.xml"/>`
    + `<Relationship Id="rId${sheets.length + 3}" Type="${REL}/pivotCacheDefinition" Target="pivotCache/pivotCacheDefinition1.xml"/>`
    + "</Relationships>"
  const contentTypes = `${XML}<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/>`
    + `<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`
    + sheets.map((_, i) => `<Override PartName="/xl/worksheets/sheet${i + 1}.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`).join("")
    + `<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`
    + `<Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>`
    + `<Override PartName="/xl/pivotCache/pivotCacheDefinition1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.pivotCacheDefinition+xml"/>`
    + `<Override PartName="/xl/pivotCache/pivotCacheRecords1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.pivotCacheRecords+xml"/>`
    + `<Override PartName="/xl/pivotTables/pivotTable1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.pivotTable+xml"/>`
    + `<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/></Types>`
  const rootRels = `${XML}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${REL}/officeDocument" Target="xl/workbook.xml"/><Relationship Id="rId2" Type="${REL}/extended-properties" Target="docProps/app.xml"/></Relationships>`
  const app = `${XML}<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"><Application>Crewship e2e synthetic workbook</Application></Properties>`
  const pivotSheetRels = `${XML}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${REL}/pivotTable" Target="../pivotTables/pivotTable1.xml"/></Relationships>`
  const pivotTableRels = `${XML}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${REL}/pivotCacheDefinition" Target="../pivotCache/pivotCacheDefinition1.xml"/></Relationships>`
  const cacheRels = `${XML}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${REL}/pivotCacheRecords" Target="pivotCacheRecords1.xml"/></Relationships>`

  // Worksheets first, so the shared strings are complete when their part is written.
  const data = sheetXml(dataRows, { before: `<dimension ref="A1:F${last}"/>` })
  const bytes = zip([
    ["[Content_Types].xml", contentTypes],
    ["_rels/.rels", rootRels],
    ["docProps/app.xml", app],
    ["xl/workbook.xml", workbook],
    ["xl/_rels/workbook.xml.rels", workbookRels],
    ["xl/styles.xml", STYLES],
    ["xl/worksheets/sheet1.xml", vstupy],
    ["xl/worksheets/sheet2.xml", data],
    ["xl/worksheets/sheet3.xml", souhrn],
    ["xl/worksheets/sheet4.xml", zdroj],
    ["xl/worksheets/sheet5.xml", pivotSheet],
    ["xl/worksheets/_rels/sheet5.xml.rels", pivotSheetRels],
    ["xl/pivotTables/pivotTable1.xml", pivotTable],
    ["xl/pivotTables/_rels/pivotTable1.xml.rels", pivotTableRels],
    ["xl/pivotCache/pivotCacheDefinition1.xml", cacheDef],
    ["xl/pivotCache/_rels/pivotCacheDefinition1.xml.rels", cacheRels],
    ["xl/pivotCache/pivotCacheRecords1.xml", cacheRecords],
    ["xl/sharedStrings.xml", sst.xml()],
  ])
  return {
    name, bytes, rows,
    expected: { byRegion, adjusted: byRegion.map(v => v * factor), selected: selectedSum, total, pivot: pivotSums },
    adjustedFor: (f: number) => byRegion.map(v => v * f),
  }
}

/**
 * Two sheets that one formula reads: ListA!B2 = a, ListB!B2 = b, and
 * Vypocet!B2 =ListA!B2+ListB!B2 with its saved value a + b. The acceptance of two
 * changes on two sheets uses it: the person changes ListA, "the agent" ListB.
 */
export function twoSheetWorkbook(a: number, b: number, name = "dva-listy-syntetika.xlsx"): { name: string; bytes: Buffer } {
  const sst = new Strings()
  const listA = sheetXml([rowXml(1, [["A", { s: "Člověk" }]], sst), rowXml(2, [["A", { s: "Hodnota" }], ["B", { n: a }]], sst)])
  const listB = sheetXml([rowXml(1, [["A", { s: "Agent" }]], sst), rowXml(2, [["A", { s: "Hodnota" }], ["B", { n: b }]], sst)])
  const vypocet = sheetXml([rowXml(2, [["A", { s: "Součet" }], ["B", { f: "ListA!B2+ListB!B2", v: a + b }]], sst)])
  const sheets = ["ListA", "ListB", "Vypocet"]
  const bytes = zip([
    ["[Content_Types].xml", `${XML}<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/>`
      + `<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`
      + sheets.map((_, i) => `<Override PartName="/xl/worksheets/sheet${i + 1}.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`).join("")
      + `<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`
      + `<Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/></Types>`],
    ["_rels/.rels", `${XML}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${REL}/officeDocument" Target="xl/workbook.xml"/></Relationships>`],
    ["xl/workbook.xml", `${XML}<workbook ${NS}><sheets>${sheets.map((s, i) => `<sheet name="${s}" sheetId="${i + 1}" r:id="rId${i + 1}"/>`).join("")}</sheets><calcPr calcId="191029"/></workbook>`],
    ["xl/_rels/workbook.xml.rels", `${XML}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">${sheets.map((_, i) => `<Relationship Id="rId${i + 1}" Type="${REL}/worksheet" Target="worksheets/sheet${i + 1}.xml"/>`).join("")}<Relationship Id="rId4" Type="${REL}/styles" Target="styles.xml"/><Relationship Id="rId5" Type="${REL}/sharedStrings" Target="sharedStrings.xml"/></Relationships>`],
    ["xl/styles.xml", STYLES],
    ["xl/worksheets/sheet1.xml", listA],
    ["xl/worksheets/sheet2.xml", listB],
    ["xl/worksheets/sheet3.xml", vypocet],
    ["xl/sharedStrings.xml", sst.xml()],
  ])
  return { name, bytes }
}

/**
 * One sheet (Vstupy: A1 "Hodnota", B1 5, B2 =B1*2) and an Excel 2016 waterfall
 * chart (chartEx), drawn from a drawing like Excel writes it. The editor keeps no
 * chartEx, so a save from the editor would drop one chart: fidelity_loss "charts".
 */
export function lossyWorkbook(name = "ztratovy-syntetika.xlsx"): { name: string; bytes: Buffer } {
  const sst = new Strings()
  const sheet = sheetXml([
    rowXml(1, [["A", { s: "Hodnota" }], ["B", { n: 5 }]], sst),
    rowXml(2, [["A", { s: "Dvojnásobek" }], ["B", { f: "B1*2", v: 10 }]], sst),
  ], { after: '<drawing r:id="rId1"/>' })
  const drawing = `${XML}<xdr:wsDr xmlns:xdr="http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006">`
    + `<xdr:twoCellAnchor><xdr:from><xdr:col>3</xdr:col><xdr:colOff>0</xdr:colOff><xdr:row>1</xdr:row><xdr:rowOff>0</xdr:rowOff></xdr:from><xdr:to><xdr:col>9</xdr:col><xdr:colOff>0</xdr:colOff><xdr:row>15</xdr:row><xdr:rowOff>0</xdr:rowOff></xdr:to>`
    + `<mc:AlternateContent><mc:Choice xmlns:cx1="http://schemas.microsoft.com/office/drawing/2015/9/8/chartex" Requires="cx1"><xdr:graphicFrame macro=""><xdr:nvGraphicFramePr><xdr:cNvPr id="2" name="Vodopád 1"/><xdr:cNvGraphicFramePr/></xdr:nvGraphicFramePr><xdr:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/></xdr:xfrm>`
    + `<a:graphic><a:graphicData uri="http://schemas.microsoft.com/office/drawing/2014/chartex"><cx:chart xmlns:cx="http://schemas.microsoft.com/office/drawing/2014/chartex" xmlns:r="${REL}" r:id="rId1"/></a:graphicData></a:graphic></xdr:graphicFrame></mc:Choice>`
    + `<mc:Fallback><xdr:sp macro="" textlink=""><xdr:nvSpPr><xdr:cNvPr id="0" name=""/><xdr:cNvSpPr><a:spLocks noTextEdit="1"/></xdr:cNvSpPr></xdr:nvSpPr><xdr:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="4000000" cy="2500000"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></xdr:spPr><xdr:txBody><a:bodyPr/><a:p><a:r><a:t>This chart is not available in your version of Excel.</a:t></a:r></a:p></xdr:txBody></xdr:sp></mc:Fallback></mc:AlternateContent>`
    + `<xdr:clientData/></xdr:twoCellAnchor></xdr:wsDr>`
  const chartEx = `${XML}<cx:chartSpace xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="${REL}" xmlns:cx="http://schemas.microsoft.com/office/drawing/2014/chartex">`
    + `<cx:chartData><cx:data id="0"><cx:strDim type="cat"><cx:f>Vstupy!$A$1:$A$2</cx:f></cx:strDim><cx:numDim type="val"><cx:f>Vstupy!$B$1:$B$2</cx:f></cx:numDim></cx:data></cx:chartData>`
    + `<cx:chart><cx:plotArea><cx:plotAreaRegion><cx:series layoutId="waterfall" uniqueId="{00000000-0000-0000-0000-000000000001}"><cx:dataId val="0"/></cx:series></cx:plotAreaRegion><cx:axis id="0"><cx:catScaling/></cx:axis><cx:axis id="1"><cx:valScaling/></cx:axis></cx:plotArea></cx:chart></cx:chartSpace>`
  const contentTypes = `${XML}<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/>`
    + `<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`
    + `<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`
    + `<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`
    + `<Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>`
    + `<Override PartName="/xl/drawings/drawing1.xml" ContentType="application/vnd.openxmlformats-officedocument.drawing+xml"/>`
    + `<Override PartName="/xl/charts/chartEx1.xml" ContentType="application/vnd.ms-office.chartex+xml"/></Types>`
  const bytes = zip([
    ["[Content_Types].xml", contentTypes],
    ["_rels/.rels", `${XML}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${REL}/officeDocument" Target="xl/workbook.xml"/></Relationships>`],
    ["xl/workbook.xml", `${XML}<workbook ${NS}><sheets><sheet name="Vstupy" sheetId="1" r:id="rId1"/></sheets></workbook>`],
    ["xl/_rels/workbook.xml.rels", `${XML}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${REL}/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="${REL}/styles" Target="styles.xml"/><Relationship Id="rId3" Type="${REL}/sharedStrings" Target="sharedStrings.xml"/></Relationships>`],
    ["xl/styles.xml", STYLES],
    ["xl/worksheets/sheet1.xml", sheet],
    ["xl/worksheets/_rels/sheet1.xml.rels", `${XML}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${REL}/drawing" Target="../drawings/drawing1.xml"/></Relationships>`],
    ["xl/drawings/drawing1.xml", drawing],
    ["xl/drawings/_rels/drawing1.xml.rels", `${XML}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.microsoft.com/office/2014/relationships/chartEx" Target="../charts/chartEx1.xml"/></Relationships>`],
    ["xl/charts/chartEx1.xml", chartEx],
    ["xl/sharedStrings.xml", sst.xml()],
  ])
  return { name, bytes }
}
